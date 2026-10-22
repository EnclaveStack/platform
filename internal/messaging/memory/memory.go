// Package memory is an in-memory message broker with the semantics of natsbroker (ADR-029): visibility
// timeouts, receive counts, maximum receives with dead-letter notices, deduplication, retention, and
// topics whose subscriptions start at the messages published after they were added. It backs the fake
// engine; the conformance suite in messagingtest holds both implementations to the same behaviour.
package memory

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/messaging"
)

const (
	duplicateWindow = 2 * time.Minute
	topicAckWait    = time.Minute
	poll            = 20 * time.Millisecond
)

type stored struct {
	seq     uint64
	body    string
	attrs   map[string]string
	sentAt  time.Time
	headers map[string]string
}

type queue struct {
	cfg      messaging.Queue
	msgs     map[uint64]*stored
	lastSeq  uint64
	dedup    map[string]dedupEntry
	inflight map[uint64]time.Time // seq → visible again at
	receives map[uint64]int
	dead     map[uint64]bool // reached maxReceives: not delivered again
}

type dedupEntry struct {
	seq uint64
	at  time.Time
}

type subscription struct {
	next     uint64               // next new sequence to deliver
	waiting  map[uint64]time.Time // seq → deliverable again at (nak'd or ack wait over)
	attempts map[uint64]int
	inflight map[uint64]bool
}

type topic struct {
	cfg     messaging.Topic
	msgs    []*stored
	lastSeq uint64
	dedup   map[string]dedupEntry
	subs    map[string]*subscription
}

type deadLetter struct {
	queue      string
	seq        uint64
	deliveries int
	hiddenTill time.Time
}

// Broker is the in-memory broker. The zero value isn't usable; use New.
type Broker struct {
	mu     sync.Mutex
	queues map[string]*queue
	topics map[string]*topic
	dead   []*deadLetter
	down   bool
	now    func() time.Time
}

var _ messaging.Broker = (*Broker)(nil)

// New returns an empty broker.
func New() *Broker {
	return &Broker{queues: map[string]*queue{}, topics: map[string]*topic{}, now: time.Now}
}

// Open returns b for any endpoint, so a messaging.Pool can hand it out.
func (b *Broker) Open(context.Context, messaging.Endpoint) (messaging.Broker, error) { return b, nil }

// SetDown makes every call fail with messaging.ErrUnavailable, as a stopped broker does.
func (b *Broker) SetDown(down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = down
}

// Lose forgets a queue or topic, as when its stream is removed outside EnclaveStack (drift).
func (b *Broker) Lose(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.queues, name)
	delete(b.topics, name)
}

// Close implements messaging.Broker; the broker stays usable through Open.
func (b *Broker) Close() error { return nil }

func (b *Broker) lock() error {
	b.mu.Lock()
	if b.down {
		b.mu.Unlock()
		return messaging.ErrUnavailable
	}
	return nil
}

func notFound(what, name string) error {
	return fmt.Errorf("%w: %s %s", messaging.ErrNotFound, what, name)
}

// EnsureQueue implements messaging.Broker.
func (b *Broker) EnsureQueue(_ context.Context, cfg messaging.Queue) error {
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	if q, ok := b.queues[cfg.Name]; ok {
		q.cfg = cfg
		return nil
	}
	b.queues[cfg.Name] = &queue{cfg: cfg, msgs: map[uint64]*stored{}, dedup: map[string]dedupEntry{}, inflight: map[uint64]time.Time{},
		receives: map[uint64]int{}, dead: map[uint64]bool{}}
	return nil
}

// DeleteQueue implements messaging.Broker.
func (b *Broker) DeleteQueue(_ context.Context, name string) error {
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	delete(b.queues, name)
	return nil
}

// PurgeQueue implements messaging.Broker.
func (b *Broker) PurgeQueue(_ context.Context, name string) error {
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	q, ok := b.queues[name]
	if !ok {
		return notFound("queue", name)
	}
	q.msgs, q.inflight, q.receives, q.dead = map[uint64]*stored{}, map[uint64]time.Time{}, map[uint64]int{}, map[uint64]bool{}
	return nil
}

// EnsureTopic implements messaging.Broker.
func (b *Broker) EnsureTopic(_ context.Context, cfg messaging.Topic) error {
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	t, ok := b.topics[cfg.Name]
	if !ok {
		t = &topic{dedup: map[string]dedupEntry{}, subs: map[string]*subscription{}}
		b.topics[cfg.Name] = t
	}
	t.cfg = cfg
	for _, name := range cfg.Subscriptions {
		if _, ok := t.subs[name]; !ok {
			t.subs[name] = &subscription{next: t.lastSeq + 1, waiting: map[uint64]time.Time{}, attempts: map[uint64]int{}, inflight: map[uint64]bool{}}
		}
	}
	for name := range t.subs {
		if !slices.Contains(cfg.Subscriptions, name) {
			delete(t.subs, name)
		}
	}
	return nil
}

// DeleteTopic implements messaging.Broker.
func (b *Broker) DeleteTopic(_ context.Context, name string) error {
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	delete(b.topics, name)
	return nil
}

func newStored(seq uint64, m messaging.Message, at time.Time, headers map[string]string) *stored {
	attrs := map[string]string{}
	for k, v := range m.Attributes {
		attrs[k] = v
	}
	return &stored{seq: seq, body: m.Body, attrs: attrs, sentAt: at, headers: headers}
}

func duplicate(d map[string]dedupEntry, id string, now time.Time) (uint64, bool) {
	if id == "" {
		return 0, false
	}
	if e, ok := d[id]; ok && now.Sub(e.at) < duplicateWindow {
		return e.seq, true
	}
	return 0, false
}

// expire drops messages past retention (and stale deduplication IDs).
func (q *queue) expire(now time.Time) {
	for seq, m := range q.msgs {
		if q.cfg.Retention > 0 && now.Sub(m.sentAt) > q.cfg.Retention {
			q.remove(seq)
		}
	}
	for id, e := range q.dedup {
		if now.Sub(e.at) >= duplicateWindow {
			delete(q.dedup, id)
		}
	}
}

func (q *queue) remove(seq uint64) {
	delete(q.msgs, seq)
	delete(q.inflight, seq)
	delete(q.receives, seq)
	delete(q.dead, seq)
}

func (b *Broker) send(name string, m messaging.Message, headers map[string]string) (messaging.Sent, error) {
	q, ok := b.queues[name]
	if !ok {
		return messaging.Sent{}, notFound("queue", name)
	}
	now := b.now()
	q.expire(now)
	if len(m.Body) > q.cfg.MaxBytes && q.cfg.MaxBytes > 0 {
		return messaging.Sent{}, fmt.Errorf("message size exceeds maximum allowed")
	}
	if seq, dup := duplicate(q.dedup, m.DedupID, now); dup {
		return messaging.Sent{ID: strconv.FormatUint(seq, 10), Sequence: seq, Duplicate: true}, nil
	}
	q.lastSeq++
	q.msgs[q.lastSeq] = newStored(q.lastSeq, m, now, headers)
	if m.DedupID != "" {
		q.dedup[m.DedupID] = dedupEntry{seq: q.lastSeq, at: now}
	}
	return messaging.Sent{ID: strconv.FormatUint(q.lastSeq, 10), Sequence: q.lastSeq}, nil
}

// Send implements messaging.Broker.
func (b *Broker) Send(_ context.Context, name string, m messaging.Message) (messaging.Sent, error) {
	if err := b.lock(); err != nil {
		return messaging.Sent{}, err
	}
	defer b.mu.Unlock()
	return b.send(name, m, nil)
}

// Publish implements messaging.Broker.
func (b *Broker) Publish(_ context.Context, name string, m messaging.Message) (messaging.Sent, error) {
	if err := b.lock(); err != nil {
		return messaging.Sent{}, err
	}
	defer b.mu.Unlock()
	t, ok := b.topics[name]
	if !ok {
		return messaging.Sent{}, notFound("topic", name)
	}
	now := b.now()
	t.expire(now)
	if seq, dup := duplicate(t.dedup, m.DedupID, now); dup {
		return messaging.Sent{ID: strconv.FormatUint(seq, 10), Sequence: seq, Duplicate: true}, nil
	}
	t.lastSeq++
	t.msgs = append(t.msgs, newStored(t.lastSeq, m, now, nil))
	if m.DedupID != "" {
		t.dedup[m.DedupID] = dedupEntry{seq: t.lastSeq, at: now}
	}
	return messaging.Sent{ID: strconv.FormatUint(t.lastSeq, 10), Sequence: t.lastSeq}, nil
}

// settle returns expired in-flight messages to the queue, and turns those at their maximum receives into
// dead letters.
func (b *Broker) settle(name string, q *queue, now time.Time) {
	for seq, visible := range q.inflight {
		if now.Before(visible) {
			continue
		}
		delete(q.inflight, seq)
		if q.cfg.MaxReceives > 0 && q.receives[seq] >= q.cfg.MaxReceives && !q.dead[seq] {
			q.dead[seq] = true
			b.dead = append(b.dead, &deadLetter{queue: name, seq: seq, deliveries: q.receives[seq]})
		}
	}
}

func receipt(queue string, seq uint64, delivery int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("mem.%s.%s.%d.%d", messaging.QueueStream(queue), messaging.QueueConsumer, seq, delivery)))
}

func (b *Broker) take(name string, max int) ([]messaging.Received, error) {
	q, ok := b.queues[name]
	if !ok {
		return nil, notFound("queue", name)
	}
	now := b.now()
	q.expire(now)
	b.settle(name, q, now)
	var seqs []uint64
	for seq := range q.msgs {
		if _, busy := q.inflight[seq]; !busy && !q.dead[seq] {
			seqs = append(seqs, seq)
		}
	}
	slices.Sort(seqs)
	out := []messaging.Received{}
	for _, seq := range seqs {
		if len(out) == max {
			break
		}
		m := q.msgs[seq]
		q.receives[seq]++
		q.inflight[seq] = now.Add(q.cfg.VisibilityTimeout)
		out = append(out, messaging.Received{ID: strconv.FormatUint(seq, 10), Body: m.body, Attributes: copyAttrs(m.attrs), SentAt: m.sentAt,
			Receipt: receipt(name, seq, q.receives[seq]), ReceiveCount: q.receives[seq]})
	}
	return out, nil
}

// Receive implements messaging.Broker, waiting up to wait for at least one message.
func (b *Broker) Receive(ctx context.Context, name string, max int, wait time.Duration) ([]messaging.Received, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := b.lock(); err != nil {
			return nil, err
		}
		out, err := b.take(name, max)
		b.mu.Unlock()
		if err != nil || len(out) > 0 || !time.Now().Before(deadline) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Delete implements messaging.Broker.
func (b *Broker) Delete(_ context.Context, name, rcpt string) error {
	raw, err := base64.RawURLEncoding.DecodeString(rcpt)
	prefix := "mem." + messaging.QueueStream(name) + "." + messaging.QueueConsumer + "."
	rest, ok := strings.CutPrefix(string(raw), prefix)
	if err != nil || !ok {
		return messaging.ErrBadReceipt
	}
	seqText, _, _ := strings.Cut(rest, ".")
	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil {
		return messaging.ErrBadReceipt
	}
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	q, ok := b.queues[name]
	if !ok {
		return notFound("queue", name)
	}
	if _, ok := q.msgs[seq]; !ok {
		return messaging.ErrBadReceipt
	}
	q.remove(seq)
	return nil
}

// Peek implements messaging.Broker.
func (b *Broker) Peek(_ context.Context, name string, limit int) ([]messaging.Received, error) {
	if err := b.lock(); err != nil {
		return nil, err
	}
	defer b.mu.Unlock()
	q, ok := b.queues[name]
	if !ok {
		return nil, notFound("queue", name)
	}
	q.expire(b.now())
	var seqs []uint64
	for seq := range q.msgs {
		seqs = append(seqs, seq)
	}
	slices.Sort(seqs)
	out := []messaging.Received{}
	for _, seq := range seqs[:min(limit, len(seqs))] {
		m := q.msgs[seq]
		out = append(out, messaging.Received{ID: strconv.FormatUint(seq, 10), Body: m.body, Attributes: copyAttrs(m.attrs), SentAt: m.sentAt})
	}
	return out, nil
}

// QueueStats implements messaging.Broker.
func (b *Broker) QueueStats(_ context.Context, name string) (messaging.QueueStats, error) {
	if err := b.lock(); err != nil {
		return messaging.QueueStats{}, err
	}
	defer b.mu.Unlock()
	q, ok := b.queues[name]
	if !ok {
		return messaging.QueueStats{}, notFound("queue", name)
	}
	now := b.now()
	q.expire(now)
	b.settle(name, q, now)
	st := messaging.QueueStats{InFlight: int64(len(q.inflight)), Messages: int64(len(q.msgs) - len(q.inflight))}
	for _, m := range q.msgs {
		if st.Oldest == nil || m.sentAt.Before(*st.Oldest) {
			at := m.sentAt
			st.Oldest = &at
		}
	}
	return st, nil
}

func (t *topic) expire(now time.Time) {
	keep := t.msgs[:0]
	for _, m := range t.msgs {
		if t.cfg.Retention <= 0 || now.Sub(m.sentAt) <= t.cfg.Retention {
			keep = append(keep, m)
		}
	}
	t.msgs = keep
	for id, e := range t.dedup {
		if now.Sub(e.at) >= duplicateWindow {
			delete(t.dedup, id)
		}
	}
}

func (t *topic) message(seq uint64) *stored {
	for _, m := range t.msgs {
		if m.seq == seq {
			return m
		}
	}
	return nil
}

// TopicStats implements messaging.Broker.
func (b *Broker) TopicStats(_ context.Context, name string) (messaging.TopicStats, error) {
	if err := b.lock(); err != nil {
		return messaging.TopicStats{}, err
	}
	defer b.mu.Unlock()
	t, ok := b.topics[name]
	if !ok {
		return messaging.TopicStats{}, notFound("topic", name)
	}
	st := messaging.TopicStats{Published: t.lastSeq, Pending: map[string]int64{}}
	for n, s := range t.subs {
		st.Pending[n] = int64(t.lastSeq+1-s.next) + int64(len(s.waiting)+len(s.inflight))
	}
	return st, nil
}

// Fetch implements messaging.Broker: redeliveries that are due first, then new messages.
func (b *Broker) Fetch(ctx context.Context, name, subName string, max int, wait time.Duration) ([]messaging.Delivery, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := b.lock(); err != nil {
			return nil, err
		}
		out, err := b.fetch(name, subName, max)
		b.mu.Unlock()
		if err != nil || len(out) > 0 || !time.Now().Before(deadline) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (b *Broker) fetch(name, subName string, max int) ([]messaging.Delivery, error) {
	t, ok := b.topics[name]
	if !ok {
		return nil, notFound("topic", name)
	}
	s, ok := t.subs[subName]
	if !ok {
		return nil, notFound("subscription", subName)
	}
	now := b.now()
	t.expire(now)
	var due []uint64
	for seq, at := range s.waiting {
		if !now.Before(at) {
			due = append(due, seq)
		}
	}
	slices.Sort(due)
	for ; s.next <= t.lastSeq; s.next++ {
		due = append(due, s.next)
	}
	var out []messaging.Delivery
	for _, seq := range due {
		if len(out) == max {
			s.waiting[seq] = now // not taken this time
			continue
		}
		m := t.message(seq)
		delete(s.waiting, seq)
		if m == nil { // past retention
			delete(s.attempts, seq)
			continue
		}
		s.attempts[seq]++
		s.inflight[seq] = true
		seq := seq
		settle := func(redeliverAt *time.Time) func(context.Context) error {
			return func(context.Context) error {
				if err := b.lock(); err != nil {
					return err
				}
				defer b.mu.Unlock()
				delete(s.inflight, seq)
				if redeliverAt == nil {
					delete(s.attempts, seq)
					return nil
				}
				s.waiting[seq] = *redeliverAt
				return nil
			}
		}
		out = append(out, messaging.Delivery{Sequence: seq, Body: m.body, Attributes: copyAttrs(m.attrs), Published: m.sentAt, Attempt: s.attempts[seq],
			Ack: settle(nil), Term: settle(nil),
			Nak: func(ctx context.Context, d time.Duration) error { at := b.now().Add(d); return settle(&at)(ctx) }})
	}
	return out, nil
}

// DeadLetters implements messaging.Broker; a notice not marked done within a minute is handed out again.
func (b *Broker) DeadLetters(ctx context.Context, max int, wait time.Duration) ([]messaging.DeadLetter, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := b.lock(); err != nil {
			return nil, err
		}
		now := b.now()
		for name, q := range b.queues {
			b.settle(name, q, now)
		}
		var out []messaging.DeadLetter
		for _, d := range b.dead {
			if len(out) == max || now.Before(d.hiddenTill) {
				continue
			}
			d.hiddenTill = now.Add(topicAckWait)
			notice := d
			out = append(out, messaging.DeadLetter{Queue: d.queue, Sequence: d.seq, Deliveries: d.deliveries, Done: func(context.Context) error {
				b.mu.Lock()
				defer b.mu.Unlock()
				b.dead = slices.DeleteFunc(b.dead, func(x *deadLetter) bool { return x == notice })
				return nil
			}})
		}
		b.mu.Unlock()
		if len(out) > 0 || !time.Now().Before(deadline) {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// MoveToDeadLetter implements messaging.Broker.
func (b *Broker) MoveToDeadLetter(_ context.Context, from string, seq uint64, to string) error {
	if err := b.lock(); err != nil {
		return err
	}
	defer b.mu.Unlock()
	src, ok := b.queues[from]
	if !ok {
		return notFound("queue", from)
	}
	m, ok := src.msgs[seq]
	if !ok {
		return nil
	}
	headers := map[string]string{messaging.DeadLetterFromHeader: from}
	if _, err := b.send(to, messaging.Message{Body: m.body, Attributes: m.attrs, DedupID: fmt.Sprintf("dl:%s:%d", from, seq)}, headers); err != nil {
		return err
	}
	src.remove(seq)
	return nil
}

// DeadLetterFrom reports which queue a message was moved from ("" when it wasn't), for tests.
func (b *Broker) DeadLetterFrom(queue string, seq uint64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if q, ok := b.queues[queue]; ok {
		if m, ok := q.msgs[seq]; ok {
			return m.headers[messaging.DeadLetterFromHeader]
		}
	}
	return ""
}

func copyAttrs(a map[string]string) map[string]string {
	out := make(map[string]string, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}
