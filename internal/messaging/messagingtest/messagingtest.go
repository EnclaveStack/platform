// Package messagingtest is the conformance suite every messaging.Broker passes (L5-002 §9): the real
// NATS broker and the in-memory one behave the same way for everything EnclaveStack relies on.
package messagingtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/messaging"
)

// Run runs the suite; fresh returns an empty broker for each case.
func Run(t *testing.T, fresh func(t *testing.T) messaging.Broker) {
	cases := map[string]func(t *testing.T, b messaging.Broker){
		"send receive delete":       sendReceiveDelete,
		"visibility timeout":        visibilityTimeout,
		"dead letters":              deadLetters,
		"deduplication":             deduplication,
		"peek and stats":            peekAndStats,
		"purge":                     purge,
		"receipts belong to queues": receiptsBelongToQueues,
		"long poll":                 longPoll,
		"topic fan-out":             topicFanOut,
		"topic retries":             topicRetries,
		"missing":                   missing,
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { c(t, fresh(t)) })
	}
}

func queue(name string, visibility time.Duration, maxReceives int) messaging.Queue {
	return messaging.Queue{Name: name, VisibilityTimeout: visibility, Retention: time.Hour, MaxBytes: 256 << 10, MaxReceives: maxReceives}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func receiveOne(t *testing.T, b messaging.Broker, q string, wait time.Duration) messaging.Received {
	t.Helper()
	got, err := b.Receive(context.Background(), q, 1, wait)
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("received %d messages from %s, want 1", len(got), q)
	}
	return got[0]
}

func sendReceiveDelete(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("emails", 30*time.Second, 0)))
	sent, err := b.Send(ctx, "emails", messaging.Message{Body: "hello", Attributes: map[string]string{"kind": "receipt"}})
	must(t, err)
	if sent.ID == "" || sent.Duplicate {
		t.Errorf("sent: %+v", sent)
	}
	m := receiveOne(t, b, "emails", time.Second)
	if m.Body != "hello" || m.Attributes["kind"] != "receipt" || m.ReceiveCount != 1 || m.Receipt == "" || m.ID != sent.ID || m.SentAt.IsZero() {
		t.Errorf("received: %+v", m)
	}
	if got, _ := b.Receive(ctx, "emails", 1, 0); len(got) != 0 {
		t.Errorf("an in-flight message was received again: %+v", got)
	}
	must(t, b.Delete(ctx, "emails", m.Receipt))
	if err := b.Delete(ctx, "emails", m.Receipt); !errors.Is(err, messaging.ErrBadReceipt) {
		t.Errorf("a used receipt deleted again: %v", err)
	}
	st, err := b.QueueStats(ctx, "emails")
	must(t, err)
	if st.Messages != 0 || st.InFlight != 0 {
		t.Errorf("stats after delete: %+v", st)
	}
}

func visibilityTimeout(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("jobs", time.Second, 0)))
	_, err := b.Send(ctx, "jobs", messaging.Message{Body: "work"})
	must(t, err)
	first := receiveOne(t, b, "jobs", time.Second)
	time.Sleep(1300 * time.Millisecond)
	again := receiveOne(t, b, "jobs", 2*time.Second)
	if again.ID != first.ID || again.ReceiveCount != 2 {
		t.Errorf("redelivery: %+v after %+v", again, first)
	}
}

func deadLetters(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("failed", 30*time.Second, 0)))
	must(t, b.EnsureQueue(ctx, queue("emails", time.Second, 2)))
	sent, err := b.Send(ctx, "emails", messaging.Message{Body: "poison", Attributes: map[string]string{"order": "7"}})
	must(t, err)
	receiveOne(t, b, "emails", time.Second)
	time.Sleep(1200 * time.Millisecond)
	receiveOne(t, b, "emails", 2*time.Second)
	time.Sleep(1200 * time.Millisecond)
	if got, _ := b.Receive(ctx, "emails", 1, 500*time.Millisecond); len(got) != 0 {
		t.Fatalf("a message past its maximum receives was delivered: %+v", got)
	}
	var notices []messaging.DeadLetter
	deadline := time.Now().Add(5 * time.Second)
	for len(notices) == 0 && time.Now().Before(deadline) {
		notices, err = b.DeadLetters(ctx, 10, 500*time.Millisecond)
		must(t, err)
	}
	if len(notices) != 1 || notices[0].Queue != "emails" || notices[0].Sequence != sent.Sequence || notices[0].Deliveries != 2 {
		t.Fatalf("dead letters: %+v", notices)
	}
	must(t, b.MoveToDeadLetter(ctx, "emails", notices[0].Sequence, "failed"))
	must(t, b.MoveToDeadLetter(ctx, "emails", notices[0].Sequence, "failed")) // a repeat is harmless
	must(t, notices[0].Done(ctx))
	moved := receiveOne(t, b, "failed", time.Second)
	if moved.Body != "poison" || moved.Attributes["order"] != "7" || moved.ReceiveCount != 1 {
		t.Errorf("moved: %+v", moved)
	}
	if got, _ := b.Receive(ctx, "failed", 1, 300*time.Millisecond); len(got) != 0 {
		t.Errorf("the move was repeated: %+v", got)
	}
	if st, _ := b.QueueStats(ctx, "emails"); st.Messages != 0 || st.InFlight != 0 {
		t.Errorf("source after the move: %+v", st)
	}
	if again, _ := b.DeadLetters(ctx, 10, 300*time.Millisecond); len(again) != 0 {
		t.Errorf("a handled notice came back: %+v", again)
	}
}

func deduplication(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("orders", 30*time.Second, 0)))
	a, err := b.Send(ctx, "orders", messaging.Message{Body: "x", DedupID: "order-1"})
	must(t, err)
	c, err := b.Send(ctx, "orders", messaging.Message{Body: "x", DedupID: "order-1"})
	must(t, err)
	if !c.Duplicate || c.Sequence != a.Sequence {
		t.Errorf("duplicate: %+v then %+v", a, c)
	}
	if st, _ := b.QueueStats(ctx, "orders"); st.Messages != 1 {
		t.Errorf("messages: %+v", st)
	}
}

func peekAndStats(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("audit", 30*time.Second, 0)))
	for _, body := range []string{"one", "two", "three"} {
		_, err := b.Send(ctx, "audit", messaging.Message{Body: body})
		must(t, err)
	}
	peeked, err := b.Peek(ctx, "audit", 2)
	must(t, err)
	if len(peeked) != 2 || peeked[0].Body != "one" || peeked[1].Body != "two" || peeked[0].Receipt != "" {
		t.Errorf("peek: %+v", peeked)
	}
	m := receiveOne(t, b, "audit", time.Second)
	if m.Body != "one" || m.ReceiveCount != 1 {
		t.Errorf("peek changed delivery: %+v", m)
	}
	st, err := b.QueueStats(ctx, "audit")
	must(t, err)
	if st.Messages != 2 || st.InFlight != 1 || st.Oldest == nil {
		t.Errorf("stats: %+v", st)
	}
}

func purge(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("logs", 30*time.Second, 0)))
	for range 5 {
		_, err := b.Send(ctx, "logs", messaging.Message{Body: "line"})
		must(t, err)
	}
	must(t, b.PurgeQueue(ctx, "logs"))
	if st, _ := b.QueueStats(ctx, "logs"); st.Messages != 0 {
		t.Errorf("after purge: %+v", st)
	}
	_, err := b.Send(ctx, "logs", messaging.Message{Body: "after"})
	must(t, err)
	if m := receiveOne(t, b, "logs", time.Second); m.Body != "after" {
		t.Errorf("after purge: %+v", m)
	}
}

func receiptsBelongToQueues(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("aaa", 30*time.Second, 0)))
	must(t, b.EnsureQueue(ctx, queue("bbb", 30*time.Second, 0)))
	_, err := b.Send(ctx, "aaa", messaging.Message{Body: "mine"})
	must(t, err)
	m := receiveOne(t, b, "aaa", time.Second)
	if err := b.Delete(ctx, "bbb", m.Receipt); !errors.Is(err, messaging.ErrBadReceipt) {
		t.Errorf("another queue's receipt: %v", err)
	}
	if err := b.Delete(ctx, "aaa", "not-a-receipt"); !errors.Is(err, messaging.ErrBadReceipt) {
		t.Errorf("garbage receipt: %v", err)
	}
	must(t, b.Delete(ctx, "aaa", m.Receipt))
}

func longPoll(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	must(t, b.EnsureQueue(ctx, queue("later", 30*time.Second, 0)))
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = b.Send(ctx, "later", messaging.Message{Body: "arrived"})
	}()
	start := time.Now()
	m := receiveOne(t, b, "later", 5*time.Second)
	if m.Body != "arrived" || time.Since(start) > 4*time.Second {
		t.Errorf("long poll: %+v after %s", m, time.Since(start))
	}
}

func topicFanOut(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	s1, s2 := messaging.SubscriptionName("queue/emails"), messaging.SubscriptionName("https://hooks.example.com/a")
	must(t, b.EnsureTopic(ctx, messaging.Topic{Name: "orders", Retention: time.Hour, MaxBytes: 1 << 20, Subscriptions: []string{s1}}))
	_, err := b.Publish(ctx, "orders", messaging.Message{Body: "first"})
	must(t, err)
	must(t, b.EnsureTopic(ctx, messaging.Topic{Name: "orders", Retention: time.Hour, MaxBytes: 1 << 20, Subscriptions: []string{s1, s2}}))
	_, err = b.Publish(ctx, "orders", messaging.Message{Body: "second", Attributes: map[string]string{"id": "2"}})
	must(t, err)
	got1, err := b.Fetch(ctx, "orders", s1, 10, time.Second)
	must(t, err)
	got2, err := b.Fetch(ctx, "orders", s2, 10, time.Second)
	must(t, err)
	if len(got1) != 2 || got1[0].Body != "first" || len(got2) != 1 || got2[0].Body != "second" || got2[0].Attributes["id"] != "2" || got2[0].Attempt != 1 {
		t.Fatalf("fan-out: %+v / %+v", got1, got2)
	}
	for _, d := range append(got1, got2...) {
		must(t, d.Ack(ctx))
	}
	st, err := b.TopicStats(ctx, "orders")
	must(t, err)
	if st.Published != 2 || st.Pending[s1] != 0 || st.Pending[s2] != 0 {
		t.Errorf("topic stats: %+v", st)
	}
	must(t, b.EnsureTopic(ctx, messaging.Topic{Name: "orders", Retention: time.Hour, MaxBytes: 1 << 20, Subscriptions: []string{s1}}))
	if _, err := b.Fetch(ctx, "orders", s2, 1, 100*time.Millisecond); !errors.Is(err, messaging.ErrNotFound) {
		t.Errorf("a removed subscription: %v", err)
	}
}

func topicRetries(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	s := messaging.SubscriptionName("https://hooks.example.com/b")
	must(t, b.EnsureTopic(ctx, messaging.Topic{Name: "events", Retention: time.Hour, MaxBytes: 1 << 20, Subscriptions: []string{s}}))
	_, err := b.Publish(ctx, "events", messaging.Message{Body: "retry me"})
	must(t, err)
	_, err = b.Publish(ctx, "events", messaging.Message{Body: "give up"})
	must(t, err)
	got, err := b.Fetch(ctx, "events", s, 10, time.Second)
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("fetched %d", len(got))
	}
	must(t, got[0].Nak(ctx, 500*time.Millisecond))
	must(t, got[1].Term(ctx))
	if again, _ := b.Fetch(ctx, "events", s, 10, 100*time.Millisecond); len(again) != 0 {
		t.Errorf("redelivered before its delay: %+v", again)
	}
	again, err := b.Fetch(ctx, "events", s, 10, 3*time.Second)
	must(t, err)
	if len(again) != 1 || again[0].Body != "retry me" || again[0].Attempt != 2 {
		t.Fatalf("retry: %+v", again)
	}
	must(t, again[0].Ack(ctx))
	if more, _ := b.Fetch(ctx, "events", s, 10, 700*time.Millisecond); len(more) != 0 {
		t.Errorf("an acked or terminated message came back: %+v", more)
	}
}

func missing(t *testing.T, b messaging.Broker) {
	ctx := context.Background()
	if _, err := b.Send(ctx, "nope", messaging.Message{Body: "x"}); !errors.Is(err, messaging.ErrNotFound) {
		t.Errorf("send to a missing queue: %v", err)
	}
	if _, err := b.QueueStats(ctx, "nope"); !errors.Is(err, messaging.ErrNotFound) {
		t.Errorf("stats of a missing queue: %v", err)
	}
	if _, err := b.Publish(ctx, "nope", messaging.Message{Body: "x"}); !errors.Is(err, messaging.ErrNotFound) {
		t.Errorf("publish to a missing topic: %v", err)
	}
	must(t, b.DeleteQueue(ctx, "nope"))
	must(t, b.DeleteTopic(ctx, "nope"))
}
