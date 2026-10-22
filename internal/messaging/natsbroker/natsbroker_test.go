package natsbroker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/crypto/bcrypt"

	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/messaging/messagingtest"
)

// embedded starts a JetStream server from the configuration the broker container gets (ADR-029), with
// the given users besides the system user (password "secret").
func embedded(t *testing.T, users ...messaging.User) messaging.Endpoint {
	t.Helper()
	dir := t.TempDir()
	conf := filepath.Join(dir, "nats.conf")
	all := append([]messaging.User{{Name: messaging.SystemUserName, PasswordHash: hash(t, "secret")}}, users...)
	body := strings.Replace(string(messaging.RenderConfig(all)), `"/data"`, fmt.Sprintf("%q", dir), 1)
	body = strings.Replace(body, `"0.0.0.0:4222"`, `"127.0.0.1:-1"`, 1)
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := server.ProcessConfigFile(conf)
	if err != nil {
		t.Fatalf("the rendered configuration doesn't load: %v\n%s", err, body)
	}
	opts.NoLog, opts.NoSigs = true, true
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS didn't start")
	}
	t.Cleanup(s.Shutdown)
	return messaging.Endpoint{URL: s.ClientURL(), User: messaging.SystemUserName, Password: "secret"}
}

func hash(t *testing.T, password string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// Each queue's and topic's user reaches only its own subjects (L3-006 §8).
func TestUsersReachOnlyTheirOwnQueueOrTopic(t *testing.T) {
	ctx := context.Background()
	ep := embedded(t, messaging.QueueUser("emails", hash(t, "q-pass")), messaging.TopicUser("orders", hash(t, "t-pass")))
	b, err := Open(ctx, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, q := range []string{"emails", "other"} {
		if err := b.EnsureQueue(ctx, messaging.Queue{Name: q, VisibilityTimeout: 30 * time.Second, Retention: time.Hour, MaxBytes: 1024}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.EnsureTopic(ctx, messaging.Topic{Name: "orders", Retention: time.Hour, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}

	qc, err := nats.Connect(ep.URL, nats.UserInfo("q-emails", "q-pass"))
	if err != nil {
		t.Fatal(err)
	}
	defer qc.Close()
	js, _ := jetstream.New(qc)
	if _, err := js.Publish(ctx, messaging.QueueSubject("emails"), []byte("hi")); err != nil {
		t.Fatalf("own queue: %v", err)
	}
	c, err := js.Consumer(ctx, messaging.QueueStream("emails"), messaging.QueueConsumer)
	if err != nil {
		t.Fatalf("own consumer: %v", err)
	}
	batch, err := c.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for m := range batch.Messages() {
		n++
		if err := m.DoubleAck(ctx); err != nil {
			t.Errorf("ack: %v", err)
		}
	}
	if n != 1 {
		t.Errorf("received %d", n)
	}
	if _, err := js.Publish(ctx, messaging.QueueSubject("other"), []byte("x")); err == nil {
		t.Error("published to another queue")
	}
	if _, err := js.Consumer(ctx, messaging.QueueStream("other"), messaging.QueueConsumer); err == nil {
		t.Error("read another queue's consumer")
	}
	if err := js.DeleteStream(ctx, messaging.QueueStream("emails")); err == nil {
		t.Error("deleted its own stream")
	}

	tc, err := nats.Connect(ep.URL, nats.UserInfo("t-orders", "t-pass"))
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	live, err := tc.SubscribeSync(messaging.TopicSubject("orders"))
	if err != nil {
		t.Fatal(err)
	}
	tjs, _ := jetstream.New(tc)
	if _, err := tjs.Publish(ctx, messaging.TopicSubject("orders"), []byte("order 1")); err != nil {
		t.Fatalf("own topic: %v", err)
	}
	if m, err := live.NextMsg(2 * time.Second); err != nil || string(m.Data) != "order 1" {
		t.Errorf("live listener: %v %v", m, err)
	}
	if _, err := tjs.Publish(ctx, messaging.QueueSubject("emails"), []byte("x")); err == nil {
		t.Error("a topic user published to a queue")
	}
	if _, err := nats.Connect(ep.URL, nats.UserInfo("q-emails", "wrong")); err == nil {
		t.Error("a wrong password connected")
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	messagingtest.Run(t, func(t *testing.T) messaging.Broker {
		b, err := Open(context.Background(), embedded(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		return b
	})
}

func TestUnreachableBrokerIsUnavailable(t *testing.T) {
	if _, err := Open(context.Background(), messaging.Endpoint{URL: "nats://127.0.0.1:1", User: "system", Password: "x"}); err == nil {
		t.Fatal("connected to nothing")
	}
}

// A topic keeps messages for its retention (TOP-05) and a queue for its own (QUE-02), as the streams'
// maximum age; a change applies in place.
func TestRetentionIsTheStreamsMaximumAge(t *testing.T) {
	ctx := context.Background()
	b, err := Open(ctx, embedded(t))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	maxAge := func(stream string) time.Duration {
		s, err := b.(*Broker).js.Stream(ctx, stream)
		if err != nil {
			t.Fatal(err)
		}
		return s.CachedInfo().Config.MaxAge
	}
	if err := b.EnsureTopic(ctx, messaging.Topic{Name: "orders", Retention: 24 * time.Hour, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureQueue(ctx, messaging.Queue{Name: "emails", VisibilityTimeout: 30 * time.Second, Retention: 96 * time.Hour, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if got := maxAge(messaging.TopicStream("orders")); got != 24*time.Hour {
		t.Errorf("topic retention %s", got)
	}
	if got := maxAge(messaging.QueueStream("emails")); got != 96*time.Hour {
		t.Errorf("queue retention %s", got)
	}
	if err := b.EnsureTopic(ctx, messaging.Topic{Name: "orders", Retention: 336 * time.Hour, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if got := maxAge(messaging.TopicStream("orders")); got != 336*time.Hour {
		t.Errorf("topic retention after a change %s", got)
	}
}
