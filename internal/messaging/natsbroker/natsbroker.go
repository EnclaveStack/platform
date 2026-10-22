// Package natsbroker implements the messaging port on NATS JetStream (ADR-029): a queue is a work-queue
// stream with one durable pull consumer, a topic a limits stream with a durable consumer per subscription,
// and dead-letter advisories are captured in a stream so they survive the control plane being down.
package natsbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/enclavestack/platform/internal/messaging"
)

const (
	// DeadLetterStream keeps max-deliveries advisories until the delivery loop handles them.
	DeadLetterStream   = "ES_DEAD_LETTERS"
	deadLetterConsumer = "mover"
	advisorySubjects   = "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.>"
	// DuplicateWindow is how long a deduplication ID is remembered (QUE-04).
	DuplicateWindow = 2 * time.Minute
	requestTimeout  = 10 * time.Second
	headerSentAt    = "Hc-Sent-At"
)

// Broker is a connection to the broker as its system user.
type Broker struct {
	nc *nats.Conn
	js jetstream.JetStream
}

var _ messaging.Broker = (*Broker)(nil)

// Open connects as the endpoint's user. An unreachable broker is messaging.ErrUnavailable.
func Open(_ context.Context, ep messaging.Endpoint) (messaging.Broker, error) {
	nc, err := nats.Connect(ep.URL, nats.UserInfo(ep.User, ep.Password), nats.Name("enclave control plane"),
		nats.Timeout(5*time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(time.Second))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", messaging.ErrUnavailable, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &Broker{nc: nc, js: js}, nil
}

// Close implements messaging.Broker.
func (b *Broker) Close() error {
	b.nc.Close()
	return nil
}

func (b *Broker) check(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jetstream.ErrStreamNotFound), errors.Is(err, jetstream.ErrConsumerNotFound):
		return fmt.Errorf("%w: %v", messaging.ErrNotFound, err)
	case !b.nc.IsConnected(), errors.Is(err, nats.ErrNoResponders), errors.Is(err, nats.ErrTimeout), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, jetstream.ErrNoHeartbeat), errors.Is(err, nats.ErrConnectionClosed):
		return fmt.Errorf("%w: %v", messaging.ErrUnavailable, err)
	}
	return err
}

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, requestTimeout)
}

// EnsureQueue implements messaging.Broker: the stream and its consumer, changed in place.
func (b *Broker) EnsureQueue(ctx context.Context, q messaging.Queue) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if err := b.ensureDeadLetters(ctx); err != nil {
		return err
	}
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: messaging.QueueStream(q.Name), Subjects: []string{messaging.QueueSubject(q.Name)},
		Retention: jetstream.WorkQueuePolicy, MaxAge: q.Retention, MaxMsgSize: int32(q.MaxBytes + messaging.HeaderAllowance), Duplicates: DuplicateWindow, Storage: jetstream.FileStorage})
	if err != nil {
		return b.check(fmt.Errorf("queue stream: %w", err))
	}
	maxDeliver := -1
	if q.MaxReceives > 0 {
		maxDeliver = q.MaxReceives
	}
	_, err = b.js.CreateOrUpdateConsumer(ctx, messaging.QueueStream(q.Name), jetstream.ConsumerConfig{Durable: messaging.QueueConsumer,
		AckPolicy: jetstream.AckExplicitPolicy, AckWait: q.VisibilityTimeout, MaxDeliver: maxDeliver, MaxAckPending: -1, MaxWaiting: 512})
	return b.check(err)
}

// ensureDeadLetters creates the advisory stream and its consumer once.
func (b *Broker) ensureDeadLetters(ctx context.Context) error {
	if _, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: DeadLetterStream, Subjects: []string{advisorySubjects},
		MaxAge: 14 * 24 * time.Hour, Storage: jetstream.FileStorage}); err != nil {
		return b.check(fmt.Errorf("dead-letter stream: %w", err))
	}
	_, err := b.js.CreateOrUpdateConsumer(ctx, DeadLetterStream, jetstream.ConsumerConfig{Durable: deadLetterConsumer, AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: time.Minute})
	return b.check(err)
}

func (b *Broker) deleteStream(ctx context.Context, name string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	err := b.js.DeleteStream(ctx, name)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil
	}
	return b.check(err)
}

// DeleteQueue implements messaging.Broker.
func (b *Broker) DeleteQueue(ctx context.Context, name string) error {
	return b.deleteStream(ctx, messaging.QueueStream(name))
}

// PurgeQueue implements messaging.Broker.
func (b *Broker) PurgeQueue(ctx context.Context, name string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	s, err := b.js.Stream(ctx, messaging.QueueStream(name))
	if err != nil {
		return b.check(err)
	}
	return b.check(s.Purge(ctx))
}

// EnsureTopic implements messaging.Broker: the stream, a consumer per subscription (new ones start with
// messages published from now on, TOP-04), and none for removed subscriptions.
func (b *Broker) EnsureTopic(ctx context.Context, t messaging.Topic) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	stream := messaging.TopicStream(t.Name)
	s, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: stream, Subjects: []string{messaging.TopicSubject(t.Name)},
		Retention: jetstream.LimitsPolicy, MaxAge: t.Retention, MaxMsgSize: int32(t.MaxBytes + messaging.HeaderAllowance), Duplicates: DuplicateWindow, Storage: jetstream.FileStorage})
	if err != nil {
		return b.check(fmt.Errorf("topic stream: %w", err))
	}
	want := map[string]bool{}
	for _, sub := range t.Subscriptions {
		want[sub] = true
		if _, err := s.Consumer(ctx, sub); err == nil {
			continue
		} else if !errors.Is(err, jetstream.ErrConsumerNotFound) {
			return b.check(err)
		}
		if _, err := s.CreateConsumer(ctx, jetstream.ConsumerConfig{Durable: sub, AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
			DeliverPolicy: jetstream.DeliverNewPolicy, MaxDeliver: -1, MaxAckPending: 1024}); err != nil {
			return b.check(fmt.Errorf("subscription %s: %w", sub, err))
		}
	}
	names := s.ConsumerNames(ctx)
	var stale []string
	for name := range names.Name() {
		if !want[name] {
			stale = append(stale, name)
		}
	}
	if err := names.Err(); err != nil {
		return b.check(err)
	}
	for _, name := range stale {
		if err := s.DeleteConsumer(ctx, name); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
			return b.check(err)
		}
	}
	return nil
}

// DeleteTopic implements messaging.Broker.
func (b *Broker) DeleteTopic(ctx context.Context, name string) error {
	return b.deleteStream(ctx, messaging.TopicStream(name))
}

func toMsg(subject string, m messaging.Message, extra nats.Header) *nats.Msg {
	msg := nats.NewMsg(subject)
	msg.Data = []byte(m.Body)
	for k, v := range m.Attributes {
		msg.Header.Set(messaging.AttributeHeaderPrefix+k, v)
	}
	for k, v := range extra {
		msg.Header[k] = v
	}
	if msg.Header.Get(headerSentAt) == "" {
		msg.Header.Set(headerSentAt, time.Now().UTC().Format(time.RFC3339Nano))
	}
	return msg
}

func (b *Broker) publish(ctx context.Context, msg *nats.Msg, dedup string) (messaging.Sent, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var opts []jetstream.PublishOpt
	if dedup != "" {
		opts = append(opts, jetstream.WithMsgID(dedup))
	}
	ack, err := b.js.PublishMsg(ctx, msg, opts...)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, jetstream.ErrNoStreamResponse) {
			return messaging.Sent{}, fmt.Errorf("%w: no stream for %s", messaging.ErrNotFound, msg.Subject)
		}
		return messaging.Sent{}, b.check(err)
	}
	return messaging.Sent{ID: strconv.FormatUint(ack.Sequence, 10), Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// Send implements messaging.Broker.
func (b *Broker) Send(ctx context.Context, queue string, m messaging.Message) (messaging.Sent, error) {
	return b.publish(ctx, toMsg(messaging.QueueSubject(queue), m, nil), m.DedupID)
}

// Publish implements messaging.Broker.
func (b *Broker) Publish(ctx context.Context, topic string, m messaging.Message) (messaging.Sent, error) {
	return b.publish(ctx, toMsg(messaging.TopicSubject(topic), m, nil), m.DedupID)
}

func attributes(h nats.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if name, ok := strings.CutPrefix(k, messaging.AttributeHeaderPrefix); ok && len(v) > 0 {
			out[name] = v[0]
		}
	}
	return out
}

func sentAt(h nats.Header, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, h.Get(headerSentAt)); err == nil {
		return t
	}
	return fallback
}

// Receive implements messaging.Broker; the receipt is the delivery's ack subject, base64url-encoded.
func (b *Broker) Receive(ctx context.Context, queue string, max int, wait time.Duration) ([]messaging.Received, error) {
	cctx, cancel := context.WithTimeout(ctx, wait+requestTimeout)
	defer cancel()
	c, err := b.js.Consumer(cctx, messaging.QueueStream(queue), messaging.QueueConsumer)
	if err != nil {
		return nil, b.check(err)
	}
	var batch jetstream.MessageBatch
	if wait <= 0 {
		batch, err = c.FetchNoWait(max)
	} else {
		batch, err = c.Fetch(max, jetstream.FetchMaxWait(wait))
	}
	if err != nil {
		return nil, b.check(err)
	}
	out := []messaging.Received{}
	for m := range batch.Messages() {
		md, err := m.Metadata()
		if err != nil {
			continue
		}
		out = append(out, messaging.Received{ID: strconv.FormatUint(md.Sequence.Stream, 10), Body: string(m.Data()), Attributes: attributes(m.Headers()),
			SentAt: sentAt(m.Headers(), md.Timestamp), Receipt: base64.RawURLEncoding.EncodeToString([]byte(m.Reply())), ReceiveCount: int(md.NumDelivered)})
	}
	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		return out, b.check(err)
	}
	return out, nil
}

// Delete implements messaging.Broker: an acknowledgement on the receipt's subject, confirmed by the server.
func (b *Broker) Delete(ctx context.Context, queue, receipt string) error {
	raw, err := base64.RawURLEncoding.DecodeString(receipt)
	subject := string(raw)
	if err != nil || !strings.HasPrefix(subject, "$JS.ACK.") || !strings.Contains(subject, "."+messaging.QueueStream(queue)+"."+messaging.QueueConsumer+".") {
		return messaging.ErrBadReceipt
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	// The server accepts an acknowledgement twice silently; a work queue drops an acknowledged message, so
	// a receipt whose message is gone deleted nothing (L3-002 §9.3).
	seq, ok := receiptSequence(subject, messaging.QueueStream(queue))
	if !ok {
		return messaging.ErrBadReceipt
	}
	s, err := b.js.Stream(ctx, messaging.QueueStream(queue))
	if err != nil {
		return b.check(err)
	}
	if _, err := s.GetMsg(ctx, seq); errors.Is(err, jetstream.ErrMsgNotFound) {
		return messaging.ErrBadReceipt
	} else if err != nil {
		return b.check(err)
	}
	reply, err := b.nc.RequestWithContext(ctx, subject, []byte("+ACK"))
	if err != nil {
		return b.check(err)
	}
	if len(reply.Data) > 0 && strings.Contains(string(reply.Data), `"error"`) {
		return messaging.ErrBadReceipt
	}
	return nil
}

// Peek implements messaging.Broker: messages still in the queue, oldest first, read by sequence.
func (b *Broker) Peek(ctx context.Context, queue string, limit int) ([]messaging.Received, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	s, err := b.js.Stream(ctx, messaging.QueueStream(queue))
	if err != nil {
		return nil, b.check(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return nil, b.check(err)
	}
	out := []messaging.Received{}
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && len(out) < limit && info.State.Msgs > 0; seq++ {
		m, err := s.GetMsg(ctx, seq)
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			continue
		}
		if err != nil {
			return out, b.check(err)
		}
		out = append(out, messaging.Received{ID: strconv.FormatUint(m.Sequence, 10), Body: string(m.Data), Attributes: attributes(m.Header), SentAt: sentAt(m.Header, m.Time)})
	}
	return out, nil
}

// QueueStats implements messaging.Broker.
func (b *Broker) QueueStats(ctx context.Context, queue string) (messaging.QueueStats, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	s, err := b.js.Stream(ctx, messaging.QueueStream(queue))
	if err != nil {
		return messaging.QueueStats{}, b.check(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return messaging.QueueStats{}, b.check(err)
	}
	c, err := s.Consumer(ctx, messaging.QueueConsumer)
	if err != nil {
		return messaging.QueueStats{}, b.check(err)
	}
	ci, err := c.Info(ctx)
	if err != nil {
		return messaging.QueueStats{}, b.check(err)
	}
	st := messaging.QueueStats{InFlight: int64(ci.NumAckPending), Messages: max(int64(info.State.Msgs)-int64(ci.NumAckPending), 0)}
	if info.State.Msgs > 0 {
		first := info.State.FirstTime
		st.Oldest = &first
	}
	return st, nil
}

// TopicStats implements messaging.Broker.
func (b *Broker) TopicStats(ctx context.Context, topic string) (messaging.TopicStats, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	s, err := b.js.Stream(ctx, messaging.TopicStream(topic))
	if err != nil {
		return messaging.TopicStats{}, b.check(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return messaging.TopicStats{}, b.check(err)
	}
	st := messaging.TopicStats{Published: info.State.LastSeq, Pending: map[string]int64{}}
	list := s.ListConsumers(ctx)
	for ci := range list.Info() {
		st.Pending[ci.Name] = int64(ci.NumPending) + int64(ci.NumAckPending)
	}
	return st, b.check(list.Err())
}

// Fetch implements messaging.Broker.
func (b *Broker) Fetch(ctx context.Context, topic, subscription string, max int, wait time.Duration) ([]messaging.Delivery, error) {
	cctx, cancel := context.WithTimeout(ctx, wait+requestTimeout)
	defer cancel()
	c, err := b.js.Consumer(cctx, messaging.TopicStream(topic), subscription)
	if err != nil {
		return nil, b.check(err)
	}
	batch, err := c.Fetch(max, jetstream.FetchMaxWait(wait))
	if err != nil {
		return nil, b.check(err)
	}
	var out []messaging.Delivery
	for m := range batch.Messages() {
		md, err := m.Metadata()
		if err != nil {
			continue
		}
		msg := m
		out = append(out, messaging.Delivery{Sequence: md.Sequence.Stream, Body: string(m.Data()), Attributes: attributes(m.Headers()),
			Published: sentAt(m.Headers(), md.Timestamp), Attempt: int(md.NumDelivered),
			Ack:  func(ctx context.Context) error { return b.check(msg.DoubleAck(ctx)) },
			Nak:  func(_ context.Context, d time.Duration) error { return b.check(msg.NakWithDelay(d)) },
			Term: func(_ context.Context) error { return b.check(msg.Term()) }})
	}
	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		return out, b.check(err)
	}
	return out, nil
}

type advisory struct {
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	StreamSeq  uint64 `json:"stream_seq"`
	Deliveries int    `json:"deliveries"`
}

// DeadLetters implements messaging.Broker: queue messages that reached their maximum receives. Advisories
// of anything else are acknowledged and skipped.
func (b *Broker) DeadLetters(ctx context.Context, max int, wait time.Duration) ([]messaging.DeadLetter, error) {
	cctx, cancel := context.WithTimeout(ctx, wait+requestTimeout)
	defer cancel()
	c, err := b.js.Consumer(cctx, DeadLetterStream, deadLetterConsumer)
	if errors.Is(err, jetstream.ErrStreamNotFound) || errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil, nil // no queue has been created yet
	}
	if err != nil {
		return nil, b.check(err)
	}
	batch, err := c.Fetch(max, jetstream.FetchMaxWait(wait))
	if err != nil {
		return nil, b.check(err)
	}
	var out []messaging.DeadLetter
	for m := range batch.Messages() {
		var a advisory
		if json.Unmarshal(m.Data(), &a) != nil {
			_ = m.Ack()
			continue
		}
		queue, isQueue := strings.CutPrefix(a.Stream, "ESQ_")
		if !isQueue || a.Consumer != messaging.QueueConsumer {
			_ = m.Ack()
			continue
		}
		msg := m
		out = append(out, messaging.DeadLetter{Queue: queue, Sequence: a.StreamSeq, Deliveries: a.Deliveries,
			Done: func(ctx context.Context) error { return b.check(msg.DoubleAck(ctx)) }})
	}
	if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		return out, b.check(err)
	}
	return out, nil
}

// MoveToDeadLetter implements messaging.Broker: copy into the dead-letter queue (deduplicated, so a
// repeat after a crash is harmless), then delete from the source. A message already gone is no error.
func (b *Broker) MoveToDeadLetter(ctx context.Context, from string, seq uint64, to string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	s, err := b.js.Stream(ctx, messaging.QueueStream(from))
	if err != nil {
		return b.check(err)
	}
	m, err := s.GetMsg(ctx, seq)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return nil
	}
	if err != nil {
		return b.check(err)
	}
	h := nats.Header{}
	for k, v := range m.Header {
		h[k] = v
	}
	h.Set(messaging.DeadLetterFromHeader, from)
	msg := nats.NewMsg(messaging.QueueSubject(to))
	msg.Data, msg.Header = m.Data, h
	if _, err := b.publish(ctx, msg, fmt.Sprintf("dl:%s:%d", from, seq)); err != nil {
		return err
	}
	if err := s.DeleteMsg(ctx, seq); err != nil && !errors.Is(err, jetstream.ErrMsgNotFound) {
		return b.check(err)
	}
	return nil
}

// receiptSequence reads the stream sequence from an ack subject, in either of its forms:
// $JS.ACK.<stream>.<consumer>.<delivered>.<stream seq>.… or $JS.ACK.<domain>.<account>.<stream>.<consumer>.….
func receiptSequence(subject, stream string) (uint64, bool) {
	tokens := strings.Split(subject, ".")
	for i := 2; i+3 < len(tokens); i++ {
		if tokens[i] == stream && tokens[i+1] == messaging.QueueConsumer {
			n, err := strconv.ParseUint(tokens[i+3], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}
