package reconciler_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// v0.6 (L2-008) through the real API on the fake engine, the in-memory broker and in-memory email.

func create(t *testing.T, c *client.Client, kind api.Kind, name string, spec any) api.Resource {
	t.Helper()
	res, err := c.CreateResource(context.Background(), kind, name, spec)
	if err != nil {
		t.Fatalf("create %s %s: %v", kind, name, err)
	}
	return waitReadyWithin(t, c, res.ID, 10*time.Second)
}

func TestDatabaseEnginesReportTheirOwnConnectionFacts(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	for _, e := range []struct{ engine, version string }{{api.EngineMySQL, "8.4"}, {api.EngineMongoDB, "7.0"}, {api.EngineClickHouse, "26.3"}} {
		db := create(t, c, api.KindDatabase, e.engine+"-db", api.DatabaseSpec{Engine: e.engine})
		if db.Outputs["engine"] != e.engine || db.Outputs["passwordSecret"] != e.engine+"-db-password" || db.Outputs["database"] != e.engine+"_db" {
			t.Errorf("%s outputs: %v", e.engine, db.Outputs)
		}
		var spec api.DatabaseSpec
		_ = json.Unmarshal(db.Spec, &spec)
		if spec.Version != e.version {
			t.Errorf("%s default version %s", e.engine, spec.Version)
		}
		_, hasNative := db.Outputs["nativePort"]
		if hasNative != (e.engine == api.EngineClickHouse) {
			t.Errorf("%s nativePort: %v", e.engine, db.Outputs)
		}
		if _, err := c.UpdateResource(ctx, db.ID, map[string]any{"engine": api.EnginePostgres, "version": "16"}, nil); apiCode(err) != api.CodeImmutableField {
			t.Errorf("%s engine change: %v", e.engine, err)
		}
	}
	m, _ := c.Machine(ctx)
	if m.MemoryReservedBytes < int64(1024+1024+2048)<<20 {
		t.Errorf("reserved memory %d", m.MemoryReservedBytes)
	}
}

func TestDumpsRestoreOnlyIntoTheirOwnEngine(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	create(t, c, api.KindBucket, "backups", api.BucketSpec{})
	db := create(t, c, api.KindDatabase, "legacy", api.DatabaseSpec{Engine: api.EngineMySQL})
	env.Fake.SetData(db.ID, "row 1")
	b := create(t, c, api.KindBackup, "legacy-nightly", api.BackupSpec{Source: api.BackupSource{Database: "legacy"}, Destination: api.BackupDestination{Bucket: "backups"}})
	run, err := c.RunNow(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, c, b.ID, run.ID)
	if done.Status != api.RunSucceeded || !strings.HasSuffix(done.Artifact.Key, ".sql") {
		t.Fatalf("mysql backup run: %+v", done.Run)
	}
	restored, err := c.Restore(ctx, b.ID, api.RestoreRequest{RunID: run.ID, Mode: api.RestoreNew, Name: "legacy-copy"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, restored.ID, 10*time.Second)
	if env.Fake.Data(restored.ID) != "row 1" {
		t.Errorf("restored data %q", env.Fake.Data(restored.ID))
	}
}

func TestQueuesDeliverRedeliverAndDeadLetter(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	failed := create(t, c, api.KindQueue, "emails-failed", api.QueueSpec{})
	q := create(t, c, api.KindQueue, "emails", api.QueueSpec{VisibilityTimeoutSeconds: 1, MaxReceives: 2, DeadLetterQueue: "emails-failed"})
	if q.Outputs["subject"] != "es.q.emails" || q.Outputs["username"] != "q-emails" || q.Outputs["passwordSecret"] != "emails-queue-password" ||
		q.Outputs["internalUrl"] != "nats://es-system-broker:4222" || !strings.HasPrefix(q.Outputs["url"].(string), "nats://127.0.0.1:") {
		t.Errorf("queue outputs: %v", q.Outputs)
	}
	conf, _ := env.Fake.BrokerConfig()
	if s := string(conf); !strings.Contains(s, `"q-emails"`) || !strings.Contains(s, `"q-emails-failed"`) || !strings.Contains(s, `"system"`) || strings.Contains(s, "emails-queue-password") {
		t.Errorf("broker config:\n%s", conf)
	}
	if m, _ := c.Machine(ctx); !m.Broker.Available {
		t.Errorf("machine broker: %+v", m.Broker)
	}

	sent, err := c.SendMessage(ctx, q.ID, api.SendMessage{Body: "receipt for order 7", Attributes: map[string]string{"order": "7"}})
	if err != nil || sent.ID == "" {
		t.Fatalf("send: %+v %v", sent, err)
	}
	list, err := c.Messages(ctx, q.ID, 10)
	if err != nil || len(list.Items) != 1 || list.Items[0].Body != "receipt for order 7" || list.Stats.Messages != 1 {
		t.Fatalf("peek: %+v %v", list, err)
	}
	got, err := c.ReceiveMessages(ctx, q.ID, api.ReceiveMessages{Max: 10, WaitSeconds: 1})
	if err != nil || len(got) != 1 || got[0].ReceiveCount != 1 || got[0].Attributes["order"] != "7" {
		t.Fatalf("receive: %+v %v", got, err)
	}
	time.Sleep(1200 * time.Millisecond)
	got, _ = c.ReceiveMessages(ctx, q.ID, api.ReceiveMessages{WaitSeconds: 2})
	if len(got) != 1 || got[0].ReceiveCount != 2 {
		t.Fatalf("redelivery: %+v", got)
	}
	// Past maximum receives it moves to the dead-letter queue (QUE-03).
	waitFor(t, 10*time.Second, func() bool {
		l, err := c.Messages(ctx, failed.ID, 10)
		return err == nil && len(l.Items) == 1 && l.Items[0].Body == "receipt for order 7"
	})
	if l, _ := c.Messages(ctx, q.ID, 10); l.Stats.Messages != 0 || l.Stats.DeadLettered != 1 {
		t.Errorf("source after dead-lettering: %+v", l.Stats)
	}
	events, _ := c.Events(ctx, q.ID, 50)
	if !slices.ContainsFunc(events, func(e api.Event) bool { return e.Type == "dead_lettered" }) {
		t.Errorf("no dead_lettered event: %+v", events)
	}

	// Receive and delete; a used receipt is reported.
	moved, _ := c.ReceiveMessages(ctx, failed.ID, api.ReceiveMessages{})
	del, err := c.DeleteMessages(ctx, failed.ID, []string{moved[0].Receipt, moved[0].Receipt, "garbage"})
	if err != nil || del.Deleted != 1 || len(del.Failed) != 2 {
		t.Errorf("delete: %+v %v", del, err)
	}

	// Refusals: a body over the limit, a dead-letter queue in use, the queue as its own dead-letter queue.
	small := create(t, c, api.KindQueue, "small", api.QueueSpec{MaxMessageBytes: 1024})
	if _, err := c.SendMessage(ctx, small.ID, api.SendMessage{Body: strings.Repeat("x", 1025)}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("oversized body: %v", err)
	}
	if _, err := c.DeleteResource(ctx, failed.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a dead-letter queue in use: %v", err)
	}
	if _, err := c.UpdateResource(ctx, small.ID, map[string]any{"maxReceives": 3, "deadLetterQueue": "small"}, nil); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("own dead-letter queue: %v", err)
	}

	// Purge (QUE-07).
	for range 3 {
		_, _ = c.SendMessage(ctx, small.ID, api.SendMessage{Body: "x"})
	}
	if _, err := c.Action(ctx, small.ID, api.ActionPurge); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		l, err := c.Messages(ctx, small.ID, 10)
		return err == nil && l.Stats.Messages == 0
	})

	// Deleting the last queue removes the broker.
	for _, id := range []string{small.ID, q.ID, failed.ID} {
		if _, err := c.DeleteResource(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitGone(ctx, id, 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if m, _ := c.Machine(ctx); m.Broker.Available {
		t.Error("the broker outlived the last queue")
	}
	if _, err := c.FindResource(ctx, api.KindSecret, "emails-queue-password"); err == nil {
		t.Error("the queue's password secret outlived it")
	}
}

func TestTopicsFanOutToQueuesAndSignedWebhooks(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	type hit struct {
		body, signature, attempt, topic string
	}
	var mu sync.Mutex
	var hits []hit
	failing := true
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		hits = append(hits, hit{string(b), r.Header.Get("X-Enclave-Signature"), r.Header.Get("X-Enclave-Attempt"), r.Header.Get("X-Enclave-Topic")})
		if failing && len(hits) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer hook.Close()
	create(t, c, api.KindSecret, "hook-key", api.SecretSpec{Value: "s3cret"})
	inbox := create(t, c, api.KindQueue, "inbox", api.QueueSpec{})
	topic := create(t, c, api.KindTopic, "orders", api.TopicSpec{Subscriptions: []api.Subscription{{Queue: "inbox"}, {URL: hook.URL + "/hook", Secret: "hook-key"}}})
	if topic.Outputs["subject"] != "es.t.orders" || topic.Outputs["subscriptions"] != float64(2) {
		t.Errorf("topic outputs: %v", topic.Outputs)
	}
	time.Sleep(200 * time.Millisecond) // the deliverers start within a tick
	if _, err := c.SendMessage(ctx, topic.ID, api.SendMessage{Body: `{"id":1}`}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		l, err := c.Messages(ctx, inbox.ID, 10)
		return err == nil && len(l.Items) == 1 && l.Items[0].Body == `{"id":1}`
	})
	// The webhook failed once, then got it again with a valid signature (TOP-02).
	waitFor(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(hits) == 2
	})
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(`{"id":1}`))
	if h := hits[1]; h.signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) || h.attempt != "2" || h.topic != "orders" {
		t.Errorf("second delivery: %+v", h)
	}
	waitFor(t, 5*time.Second, func() bool {
		d, err := c.Deliveries(ctx, topic.ID)
		return err == nil && d.Published == 1 && len(d.Items) == 2 && d.Items[0].Delivered == 1 && d.Items[1].Delivered == 1
	})
	// A queue subscribed by a topic can't be deleted; deleting the topic keeps the queue (TOP-08).
	if _, err := c.DeleteResource(ctx, inbox.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a subscribed queue: %v", err)
	}
	if _, err := c.DeleteResource(ctx, topic.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, topic.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if l, err := c.Messages(ctx, inbox.ID, 10); err != nil || len(l.Items) != 1 {
		t.Errorf("the queue after its topic went: %+v %v", l, err)
	}
	// http:// is for localhost only (TOP-03).
	if _, err := c.CreateResource(ctx, api.KindTopic, "bad", api.TopicSpec{Subscriptions: []api.Subscription{{URL: "http://example.com/h"}}}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("plain-http webhook: %v", err)
	}
}

func TestBrokerDriftIsRepaired(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(cfg *server.Config) { cfg.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	q := create(t, c, api.KindQueue, "jobs", api.QueueSpec{})
	env.Broker.Lose("jobs")
	waitFor(t, 10*time.Second, func() bool {
		events, _ := c.Events(ctx, q.ID, 20)
		return slices.ContainsFunc(events, func(e api.Event) bool { return e.Type == "drift_detected" })
	})
	waitReadyWithin(t, c, q.ID, 10*time.Second)
	if _, err := c.SendMessage(ctx, q.ID, api.SendMessage{Body: "back"}); err != nil {
		t.Errorf("send after repair: %v", err)
	}
	env.Fake.StopBroker()
	waitFor(t, 10*time.Second, func() bool {
		m, _ := c.Machine(ctx)
		return m.Broker.Available
	})
}

func TestSearchAndEmailServices(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	s := create(t, c, api.KindSearch, "catalogue", api.SearchSpec{})
	if s.Outputs["username"] != "admin" || s.Outputs["version"] != "3.3.2" || s.Outputs["passwordSecret"] != "catalogue-search-password" {
		t.Errorf("search outputs: %v", s.Outputs)
	}
	sec, _ := c.FindResource(ctx, api.KindSecret, "catalogue-search-password")
	pw, _ := c.RevealSecret(ctx, sec.ID)
	if !strings.ContainsAny(pw, "-_") || !strings.ContainsAny(pw, "0123456789") || strings.ToLower(pw) == pw {
		t.Errorf("the search password lacks a class OpenSearch requires: %q", pw)
	}
	if _, err := c.Action(ctx, s.ID, api.ActionRestart); err != nil {
		t.Errorf("restart search: %v", err)
	}

	mail := create(t, c, api.KindEmail, "shop-mail", api.EmailSpec{Domain: "example.com"})
	if mail.Outputs["mode"] != api.EmailCapture || mail.Outputs["smtpPort"] != float64(1025) || mail.Outputs["webUrl"] == nil {
		t.Errorf("email outputs: %v", mail.Outputs)
	}
	sent, err := c.SendEmail(ctx, mail.ID, api.SendEmail{From: "shop@example.com", To: []string{"a@b.test"}, Bcc: []string{"audit@b.test"}, Subject: "Receipt", Text: "Thanks", HTML: "<p>Thanks</p>"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.Emails(ctx, mail.ID, 10)
	if err != nil || list.Total != 1 || list.Items[0].Relay != api.RelayCaptured || list.Items[0].Subject != "Receipt" {
		t.Fatalf("emails: %+v %v", list, err)
	}
	full, err := c.Email(ctx, mail.ID, sent.ID)
	if err != nil || full.HTML != "<p>Thanks</p>" || full.Bcc[0] != "audit@b.test" {
		t.Errorf("email: %+v %v", full, err)
	}
	if _, err := c.SendEmail(ctx, mail.ID, api.SendEmail{From: "x@other.test", To: []string{"a@b.test"}, Subject: "x", Text: "x"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("sender outside the domain: %v", err)
	}
	if _, err := c.Email(ctx, mail.ID, "nope"); apiCode(err) != api.CodeNotFound {
		t.Errorf("missing email: %v", err)
	}
	// Relay mode needs a credentials secret holding a username and password (EML-02).
	create(t, c, api.KindSecret, "smtp-login", api.SecretSpec{Value: `{"username":"u","password":"p"}`})
	if _, err := c.UpdateResource(ctx, mail.ID, map[string]any{"mode": "relay", "relay": map[string]any{"host": "smtp.example.com", "credentialsSecret": "smtp-login"}}, nil); err != nil {
		t.Fatal(err)
	}
	relayed := waitReadyWithin(t, c, mail.ID, 10*time.Second)
	if relayed.Outputs["mode"] != api.EmailRelaying {
		t.Errorf("relay outputs: %v", relayed.Outputs)
	}
	if _, err := c.SendEmail(ctx, mail.ID, api.SendEmail{From: "shop@example.com", To: []string{"a@b.test"}, Subject: "Second", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := c.Emails(ctx, mail.ID, 1); l.Total != 2 || l.Items[0].Relay != api.RelayRelayed {
		t.Errorf("after relay: %+v", l)
	}
	if _, err := c.DeleteResource(ctx, func() string { s, _ := c.FindResource(ctx, api.KindSecret, "smtp-login"); return s.ID }(), nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting the relay's secret: %v", err)
	}
}

func TestCDNsRouteThroughTheEdgeAndOpenBucketsForReading(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	create(t, c, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	cdn := create(t, c, api.KindCDN, "assets", api.CDNSpec{Hostname: "assets.localhost", Origin: "app/web"})
	if cdn.Outputs["url"] != "https://assets.localhost" || cdn.Outputs["origin"] != "es-app-web:80" || cdn.Outputs["certificateState"] != "issued" {
		t.Errorf("cdn outputs: %v", cdn.Outputs)
	}
	if !slices.ContainsFunc(env.Fake.EdgeRoutes(), func(r engine.Route) bool {
		return r.Hostname == "assets.localhost" && r.Upstream == "es-cdn-assets:8080"
	}) {
		t.Errorf("edge routes: %+v", env.Fake.EdgeRoutes())
	}
	if _, err := c.CreateResource(ctx, api.KindDomain, "clash", api.DomainSpec{Hostname: "assets.localhost", App: "web"}); apiCode(err) != api.CodeHostnameTaken {
		t.Errorf("a domain on a CDN's hostname: %v", err)
	}
	env.Fake.SetCached(cdn.ID, 4096)
	if _, err := c.Action(ctx, cdn.ID, api.ActionPurge); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return env.Fake.Purges(cdn.ID) == 1 })
	if _, err := c.Action(ctx, cdn.ID, api.ActionRedeploy); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("redeploy on a CDN: %v", err)
	}

	create(t, c, api.KindBucket, "uploads", api.BucketSpec{})
	bc := create(t, c, api.KindCDN, "files", api.CDNSpec{Hostname: "files.localhost", Origin: "bucket/uploads"})
	if !env.Fake.BucketPublic("uploads") || bc.Outputs["origin"] != "es-system-objectstore:9000" {
		t.Errorf("bucket CDN: public %v outputs %v", env.Fake.BucketPublic("uploads"), bc.Outputs)
	}
	if _, err := c.DeleteResource(ctx, bc.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, bc.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if env.Fake.BucketPublic("uploads") {
		t.Error("the bucket stayed public after its CDN went")
	}
	if slices.ContainsFunc(env.Fake.EdgeRoutes(), func(r engine.Route) bool { return r.Hostname == "files.localhost" }) {
		t.Error("the deleted CDN's route stayed")
	}
}

func TestTranscodersConvertNewObjectsAndFilesAskedFor(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	create(t, c, api.KindBucket, "uploads", api.BucketSpec{})
	uploads := backup.Target{Endpoint: "http://127.0.0.1:41999", Bucket: "uploads"}
	env.Backups.Put(uploads, "videos/old.mov", []byte("old video"))
	tc := create(t, c, api.KindTranscoder, "videos", api.TranscoderSpec{Input: api.BucketFolder{Bucket: "uploads", Prefix: "videos/"},
		Output: api.BucketFolder{Bucket: "uploads", Prefix: "media/"}, Presets: []string{api.PresetMP4720p, api.PresetHLS720p, api.PresetThumbnail}})
	time.Sleep(300 * time.Millisecond) // the first listing is the baseline: old.mov isn't converted (TRN-03)
	env.Backups.Put(uploads, "videos/cat.mov", []byte("cat video"))
	var run api.Run
	waitFor(t, 10*time.Second, func() bool {
		runs, _ := c.ListRuns(ctx, tc.ID, 10, "")
		if len(runs.Items) == 1 && runs.Items[0].Status != api.RunRunning {
			run = runs.Items[0]
			return true
		}
		return false
	})
	if run.Status != api.RunSucceeded || run.Key != "videos/cat.mov" || run.Trigger != api.TriggerWatch || len(run.Outputs) != 4 {
		t.Fatalf("watch run: %+v", run)
	}
	want := []string{"media/cat/hls-720p/index.m3u8", "media/cat/hls-720p/segment-000.ts", "media/cat/mp4-720p.mp4", "media/cat/thumbnail.jpg"}
	for _, k := range want {
		if !slices.Contains(env.Backups.Objects(uploads), k) {
			t.Errorf("missing output %s in %v", k, env.Backups.Objects(uploads))
		}
	}
	// By hand, a file seen before (TRN-04); one that isn't media fails its run, not the transcoder (TRN-05).
	manual, err := c.Transcode(ctx, tc.ID, "videos/old.mov")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitRun(t, c, tc.ID, manual.ID); done.Status != api.RunSucceeded || done.Trigger != api.TriggerManual {
		t.Errorf("manual run: %+v", done.Run)
	}
	env.Backups.Put(uploads, "videos/notes.txt", []byte(fake.NotMedia+" just text"))
	waitFor(t, 10*time.Second, func() bool {
		runs, _ := c.ListRuns(ctx, tc.ID, 10, "")
		return slices.ContainsFunc(runs.Items, func(r api.Run) bool { return r.Key == "videos/notes.txt" && r.Status == api.RunFailed })
	})
	if got, _ := c.GetResource(ctx, tc.ID); got.Status.Phase != api.PhaseReady {
		t.Errorf("the transcoder after a failed run: %s", got.Status.Phase)
	}
	if _, err := c.Transcode(ctx, tc.ID, "other/x.mov"); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a key outside the input folder: %v", err)
	}
	if _, err := c.RunNow(ctx, tc.ID); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a run without a key: %v", err)
	}
	b, _ := c.FindResource(ctx, api.KindBucket, "uploads")
	if _, err := c.DeleteResource(ctx, b.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting the transcoder's bucket: %v", err)
	}
}

// Networks, private DNS and firewalls apply to the new engines and services as to v0.4's kinds (DBE-06,
// SRC-05, EML-07).
func TestDataServicesJoinNetworksAndFirewalls(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	create(t, c, api.KindNetwork, "backend", api.NetworkSpec{CIDR: "10.41.0.0/16", Subnets: []api.Subnet{{Name: "data", CIDR: "10.41.1.0/24"}}})
	db := create(t, c, api.KindDatabase, "legacy", api.DatabaseSpec{Engine: api.EngineMySQL, Network: "backend"})
	search := create(t, c, api.KindSearch, "finder", api.SearchSpec{Network: "backend"})
	mail := create(t, c, api.KindEmail, "shop-mail", api.EmailSpec{Domain: "example.com", Network: "backend"})
	for _, r := range []struct {
		res api.Resource
		dns string
	}{{db, "legacy.database.internal"}, {search, "finder.search.internal"}, {mail, "shop-mail.email.internal"}} {
		if !inRange(t, r.res.Outputs[engine.OutputPrivateIP], "10.41.1.0/24") || r.res.Outputs[engine.OutputPrivateDNS] != r.dns {
			t.Errorf("%s/%s placement: %v", r.res.Kind, r.res.Name, r.res.Outputs)
		}
	}
	create(t, c, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 8080, Network: "backend"})
	create(t, c, api.KindFirewall, "data-fw", api.FirewallSpec{Targets: []string{"database/legacy", "search/finder", "email/shop-mail"},
		Ingress: []api.FirewallRule{{From: []string{"app/web"}, Ports: []string{"3306", "9200", "1025"}}}})
	for _, r := range []api.Resource{db, search, mail} {
		if p, _ := env.Fake.FirewallOf(r.ID); p == nil || len(p.Ingress) != 1 {
			t.Errorf("%s/%s firewall: %+v", r.Kind, r.Name, p)
		}
	}
}

// A transcoder runs at most `concurrency` files at once; a file asked for beyond that is refused, not
// queued, so the caller knows to try again (TRN-07).
func TestTranscoderConcurrencyBoundsManualRuns(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(cfg *server.Config) { cfg.FakeStepDelay = 300 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	create(t, c, api.KindBucket, "uploads", api.BucketSpec{})
	uploads := backup.Target{Endpoint: "http://127.0.0.1:41999", Bucket: "uploads"}
	env.Backups.Put(uploads, "videos/a.mov", []byte("a"))
	env.Backups.Put(uploads, "videos/b.mov", []byte("b"))
	no := false
	tc := create(t, c, api.KindTranscoder, "videos", api.TranscoderSpec{Input: api.BucketFolder{Bucket: "uploads", Prefix: "videos/"},
		Output: api.BucketFolder{Bucket: "uploads", Prefix: "media/"}, Presets: []string{api.PresetThumbnail}, Watch: &no, Concurrency: 1})
	first, err := c.Transcode(ctx, tc.ID, "videos/a.mov")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Transcode(ctx, tc.ID, "videos/b.mov"); apiCode(err) != api.CodeRunInProgress {
		t.Errorf("a second file at concurrency 1: %v", err)
	}
	if done := waitRun(t, c, tc.ID, first.ID); done.Status != api.RunSucceeded {
		t.Fatalf("first run: %+v", done.Run)
	}
	if _, err := c.Transcode(ctx, tc.ID, "videos/b.mov"); err != nil {
		t.Errorf("after the first finished: %v", err)
	}
}
