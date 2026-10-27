// Package messaging is the port to the message broker behind queues and topics (ADR-029). natsbroker
// implements it on NATS JetStream; memory implements the same semantics in memory for the fake engine.
// Services use the data methods (send, receive, delete, peek, publish, stats); only the reconciler uses
// the control and delivery methods (internal/archtest).
package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrNotFound means the queue or topic doesn't exist on the broker.
var ErrNotFound = errors.New("not found on the broker")

// ErrUnavailable means the broker can't be reached. It is transient.
var ErrUnavailable = errors.New("the message broker isn't reachable")

// ErrBadReceipt means a receipt isn't one of this queue's, or deleted nothing.
var ErrBadReceipt = errors.New("this receipt doesn't match a message of this queue")

// Queue is a queue's broker configuration (QUE-01 – QUE-04).
type Queue struct {
	Name              string
	VisibilityTimeout time.Duration
	Retention         time.Duration
	MaxBytes          int
	// MaxReceives bounds deliveries; 0 means no limit.
	MaxReceives int
}

// Topic is a topic's broker configuration: its subscriptions are consumer names (SubscriptionName).
type Topic struct {
	Name          string
	Retention     time.Duration
	MaxBytes      int
	Subscriptions []string
}

// Message is what is sent or published.
type Message struct {
	Body       string
	Attributes map[string]string
	DedupID    string
}

// Sent answers a send or publish.
type Sent struct {
	ID        string
	Sequence  uint64
	Duplicate bool
}

// Received is a message read from a queue. Receipt is empty when peeked.
type Received struct {
	ID           string
	Body         string
	Attributes   map[string]string
	SentAt       time.Time
	Receipt      string
	ReceiveCount int
}

// QueueStats is a queue's live state.
type QueueStats struct {
	Messages int64
	InFlight int64
	Oldest   *time.Time
}

// TopicStats counts a topic's messages and each subscription's backlog.
type TopicStats struct {
	Published uint64
	Pending   map[string]int64 // by subscription name
}

// Delivery is one message fetched for a topic subscription. Exactly one of Ack, Nak or Term must follow.
type Delivery struct {
	Sequence   uint64
	Body       string
	Attributes map[string]string
	Published  time.Time
	Attempt    int
	Ack        func(context.Context) error
	Nak        func(ctx context.Context, delay time.Duration) error
	Term       func(context.Context) error
	// Progress tells the broker the delivery is still being handled, so it isn't handed out again (a
	// function's run can outlast the ack wait, ADR-055).
	Progress func(context.Context) error
}

// DeadLetter is a queue message that reached its maximum receives. Done acknowledges it once handled.
type DeadLetter struct {
	Queue      string
	Sequence   uint64
	Deliveries int
	Done       func(context.Context) error
}

// Broker is the message broker (ADR-029).
type Broker interface {
	// Control (reconciler only).
	EnsureQueue(ctx context.Context, q Queue) error
	DeleteQueue(ctx context.Context, name string) error
	PurgeQueue(ctx context.Context, name string) error
	EnsureTopic(ctx context.Context, t Topic) error
	DeleteTopic(ctx context.Context, name string) error

	// Data.
	Send(ctx context.Context, queue string, m Message) (Sent, error)
	Publish(ctx context.Context, topic string, m Message) (Sent, error)
	Receive(ctx context.Context, queue string, max int, wait time.Duration) ([]Received, error)
	Delete(ctx context.Context, queue, receipt string) error
	Peek(ctx context.Context, queue string, limit int) ([]Received, error)
	QueueStats(ctx context.Context, queue string) (QueueStats, error)
	TopicStats(ctx context.Context, topic string) (TopicStats, error)

	// Delivery (the reconciler's delivery loop).
	Fetch(ctx context.Context, topic, subscription string, max int, wait time.Duration) ([]Delivery, error)
	DeadLetters(ctx context.Context, max int, wait time.Duration) ([]DeadLetter, error)
	MoveToDeadLetter(ctx context.Context, from string, seq uint64, to string) error

	Close() error
}

// Header names carried with messages.
const (
	AttributeHeaderPrefix = "Hc-Attr-"
	DeadLetterFromHeader  = "Hc-Dead-Letter-From"
)

// Endpoint is where the broker is and how the control plane signs in.
type Endpoint struct {
	URL      string
	User     string
	Password string
}

// Opener connects to a broker.
type Opener func(ctx context.Context, ep Endpoint) (Broker, error)

// Pool keeps one broker connection, replaced when the endpoint changes (a new port or password).
type Pool struct {
	open    Opener
	resolve func(ctx context.Context) (Endpoint, error)

	mu  sync.Mutex
	ep  Endpoint
	cur Broker
}

// NewPool returns a pool that resolves the endpoint on each use.
func NewPool(open Opener, resolve func(ctx context.Context) (Endpoint, error)) *Pool {
	return &Pool{open: open, resolve: resolve}
}

// Broker returns a connected broker, or ErrUnavailable.
func (p *Pool) Broker(ctx context.Context) (Broker, error) {
	ep, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil && p.ep == ep {
		return p.cur, nil
	}
	if p.cur != nil {
		_ = p.cur.Close()
		p.cur = nil
	}
	b, err := p.open(ctx, ep)
	if err != nil {
		return nil, err
	}
	p.cur, p.ep = b, ep
	return b, nil
}

// Close closes the current connection.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		return nil
	}
	err := p.cur.Close()
	p.cur = nil
	return err
}

// SubscriptionName is a subscription's consumer name: "s_" and the first 12 hex digits of the SHA-256 of
// its target ("queue/<name>" or the URL).
func SubscriptionName(target string) string { return "s_" + hash12(target) }

// QueueSubject and TopicSubject are where apps publish (L3-002 §9.2).
func QueueSubject(name string) string { return "es.q." + name }

// TopicSubject is a topic's subject.
func TopicSubject(name string) string { return "es.t." + name }

// QueueStream and TopicStream name the streams.
func QueueStream(name string) string { return "ESQ_" + name }

// TopicStream names a topic's stream.
func TopicStream(name string) string { return "EST_" + name }

// QueueConsumer is the one consumer of every queue.
const QueueConsumer = "workers"

func hash12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
