package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/webhook"
	"github.com/enclavestack/platform/pkg/api"
)

// The delivery loop (ADR-029, L2-008 TOP-01 – TOP-07, QUE-03): one goroutine per subscription of every
// ready topic, and one that moves queue messages past their maximum receives to dead-letter queues.

const (
	// DefaultDeliveryTick is how often the set of subscriptions is compared with the running deliverers.
	DefaultDeliveryTick = time.Second
	deliveryBatch       = 32
	deliveryWait        = time.Second
	maxRetryDelay       = 5 * time.Minute
)

// deliverer is one running subscription.
type deliverer struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// deliveryLoop keeps a deliverer per subscription of every ready topic, and the dead-letter mover.
func (r *Reconciler) deliveryLoop(ctx context.Context) {
	tick := r.DeliveryTick
	if tick <= 0 {
		tick = DefaultDeliveryTick
	}
	running := map[string]*deliverer{}
	defer func() {
		for _, d := range running {
			d.cancel()
			<-d.done
		}
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.deadLetterLoop(ctx)
	}()
	defer wg.Wait()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		want := r.wantedSubscriptions(ctx)
		for key, d := range running {
			if _, ok := want[key]; !ok {
				d.cancel()
				<-d.done
				delete(running, key)
			}
		}
		for key, w := range want {
			if _, ok := running[key]; ok {
				continue
			}
			dctx, cancel := context.WithCancel(ctx)
			d := &deliverer{cancel: cancel, done: make(chan struct{})}
			running[key] = d
			go func() {
				defer close(d.done)
				r.deliver(dctx, w)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// subscriptionWork is one subscription to serve: a topic, by ID, broker key and name, and one target in
// its project.
type subscriptionWork struct {
	topicID, topic, name, project string
	sub                           api.Subscription
}

// wantedSubscriptions lists the subscriptions of ready topics, keyed so a changed target restarts its
// deliverer. Topics being reconciled keep theirs, so an update doesn't pause deliveries.
func (r *Reconciler) wantedSubscriptions(ctx context.Context) map[string]subscriptionWork {
	topics, err := r.Store.ResourcesOfKind(ctx, api.KindTopic)
	if err != nil {
		return nil
	}
	want := map[string]subscriptionWork{}
	for _, t := range topics {
		if t.Phase != api.PhaseReady && t.Phase != api.PhaseUpdating {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindTopic, t.Spec)
		if err != nil {
			continue
		}
		for _, sub := range raw.(api.TopicSpec).Subscriptions {
			want[t.ID+"|"+sub.Target()+"|"+sub.Secret] = subscriptionWork{topicID: t.ID, topic: domain.BrokerKey(t.Project, t.Name), name: t.Name, project: t.ProjectName(), sub: sub}
		}
	}
	return want
}

// deliver serves one subscription until ctx ends.
func (r *Reconciler) deliver(ctx context.Context, w subscriptionWork) {
	name := messaging.SubscriptionName(w.sub.Target())
	for ctx.Err() == nil {
		b, err := r.broker(ctx)
		if err != nil {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		batch, err := b.Fetch(ctx, w.topic, name, deliveryBatch, deliveryWait)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, messaging.ErrNotFound) {
				r.Log.Debug("fetch for a subscription failed", "topic", w.topic, "target", w.sub.Target(), "error", err)
			}
			sleepCtx(ctx, time.Second) // the topic's reconcile creates a missing consumer
			continue
		}
		for _, d := range batch {
			r.deliverOne(ctx, w, d)
		}
	}
}

func targetType(s api.Subscription) string {
	if s.Queue != "" {
		return "queue"
	}
	return "webhook"
}

// deliverOne delivers a message and settles it: ack on success, a delayed retry on failure, and after
// the last attempt, terminate with an event (TOP-02).
func (r *Reconciler) deliverOne(ctx context.Context, w subscriptionWork, d messaging.Delivery) {
	err := r.send(ctx, w, d)
	if ctx.Err() != nil {
		return // redelivered after the ack wait
	}
	now := r.Now()
	if err == nil {
		if aerr := d.Ack(ctx); aerr != nil {
			return // delivered again later; deliveries are at least once
		}
		_ = r.Store.CountDelivery(ctx, w.topicID, w.sub.Target(), true, "", now)
		r.Metrics.Deliveries.WithLabelValues(targetType(w.sub), "delivered").Inc()
		return
	}
	msg := truncate(err.Error())
	if d.Attempt >= api.WebhookAttempts {
		_ = d.Term(ctx)
		_ = r.Store.CountDelivery(ctx, w.topicID, w.sub.Target(), false, msg, now)
		r.Metrics.Deliveries.WithLabelValues(targetType(w.sub), "gave_up").Inc()
		if t, gerr := r.Store.GetResource(ctx, w.topicID); gerr == nil {
			r.event(ctx, t, "delivery_failed", fmt.Sprintf("Gave up delivering message %s:%d to %s after %d attempts: %s", w.name, d.Sequence, w.sub.Target(), d.Attempt, msg))
		}
		return
	}
	_ = d.Nak(ctx, retryDelay(d.Attempt))
	_ = r.Store.NoteDeliveryError(ctx, w.topicID, w.sub.Target(), msg, now)
	r.Metrics.Deliveries.WithLabelValues(targetType(w.sub), "retry").Inc()
}

// retryDelay is min(2^(attempt-1) s, 5 min).
func retryDelay(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < maxRetryDelay; i++ {
		d *= 2
	}
	return min(d, maxRetryDelay)
}

// send hands one message to a queue or a webhook.
func (r *Reconciler) send(ctx context.Context, w subscriptionWork, d messaging.Delivery) error {
	id := w.name + ":" + strconv.FormatUint(d.Sequence, 10)
	if w.sub.Queue != "" {
		b, err := r.broker(ctx)
		if err != nil {
			return err
		}
		// The message ID makes a redelivery to the same queue a duplicate, not a second message.
		_, err = b.Send(ctx, domain.BrokerKey(w.project, w.sub.Queue), messaging.Message{Body: d.Body, Attributes: d.Attributes, DedupID: id + ":" + messaging.SubscriptionName(w.sub.Target())})
		if errors.Is(err, messaging.ErrNotFound) {
			return fmt.Errorf("the queue %s doesn't exist on the broker yet", w.sub.Queue)
		}
		return err
	}
	return r.postWebhook(ctx, w, d, id)
}

// postWebhook POSTs the body with the topic, message ID, attempt and attributes as headers, signed when
// the subscription names a secret (internal/webhook: no redirects, only a 2xx is a delivery).
func (r *Reconciler) postWebhook(ctx context.Context, w subscriptionWork, d messaging.Delivery, id string) error {
	body := []byte(d.Body)
	headers := map[string]string{"Content-Type": "text/plain; charset=utf-8", "X-Enclave-Topic": w.name,
		"X-Enclave-Message-Id": id, "X-Enclave-Attempt": strconv.Itoa(d.Attempt)}
	for k, v := range d.Attributes {
		headers["X-Enclave-Attr-"+k] = v
	}
	if w.sub.Secret != "" {
		key, err := r.secretValue(ctx, w.project, w.sub.Secret)
		if err != nil {
			return err
		}
		headers[webhook.SignatureHeader] = webhook.Signature(key, body)
	}
	if err := webhook.Post(ctx, r.HTTP, w.sub.URL, body, headers); err != nil {
		return fmt.Errorf("the webhook %w", err)
	}
	return nil
}

// deadLetterLoop moves queue messages that reached their maximum receives to their dead-letter queue,
// or records that they stay until their retention ends (QUE-03).
func (r *Reconciler) deadLetterLoop(ctx context.Context) {
	for ctx.Err() == nil {
		b, err := r.broker(ctx)
		if err != nil {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		notices, err := b.DeadLetters(ctx, deliveryBatch, deliveryWait)
		if err != nil {
			sleepCtx(ctx, time.Second)
			continue
		}
		for _, n := range notices {
			if err := r.deadLetter(ctx, b, n); err == nil {
				_ = n.Done(ctx)
			}
		}
		if len(notices) == 0 {
			sleepCtx(ctx, 100*time.Millisecond)
		}
	}
}

func (r *Reconciler) deadLetter(ctx context.Context, b messaging.Broker, n messaging.DeadLetter) error {
	project, name := domain.SplitBrokerKey(n.Queue)
	q, err := r.Store.GetResourceByName(ctx, project, api.KindQueue, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil // the queue went; nothing to do
	}
	if err != nil {
		return err
	}
	raw, _, err := api.NormalizeSpec(api.KindQueue, q.Spec)
	if err != nil {
		return nil
	}
	s := raw.(api.QueueSpec)
	r.Metrics.DeadLetters.Inc()
	_ = r.Store.CountDelivery(ctx, q.ID, store.DeadLetterTarget, true, "", r.Now())
	if s.DeadLetterQueue == "" {
		r.event(ctx, q, "max_receives", fmt.Sprintf("Message %d was received %d times without being deleted. It isn't delivered again and stays until its retention ends.", n.Sequence, n.Deliveries))
		return nil
	}
	if err := b.MoveToDeadLetter(ctx, n.Queue, n.Sequence, domain.BrokerKey(project, s.DeadLetterQueue)); err != nil {
		r.Log.Warn("move to the dead-letter queue failed", "queue", n.Queue, "sequence", n.Sequence, "error", err)
		return err
	}
	r.event(ctx, q, "dead_lettered", fmt.Sprintf("Message %d was received %d times without being deleted; moved to %s.", n.Sequence, n.Deliveries, s.DeadLetterQueue))
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
