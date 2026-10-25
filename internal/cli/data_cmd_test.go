package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.6 commands (L2-002 CLI-29 – CLI-36) against a real control plane on the fake engine, the in-memory
// broker and in-memory email.

func TestDatabaseEnginesFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("db", "create", "analytics", "--engine", "clickhouse", "--wait")
	var ds api.DatabaseSpec
	h.spec("db", "analytics", &ds)
	if ds.Engine != api.EngineClickHouse || ds.Version != "26.3" {
		t.Errorf("spec %+v", ds)
	}
	if r := h.mustRun("db", "get", "analytics"); !strings.Contains(r.out, "http://analytics:<password>@127.0.0.1:") || !strings.Contains(r.out, "nativePort") {
		t.Errorf("db get: %s", r.out)
	}
	if r := h.run("db", "create", "bad", "--engine", "mysql", "--version", "17"); r.code != cli.ExitError {
		t.Errorf("mysql 17: %d %s", r.code, r.err)
	}
}

func TestQueuesAndTopicsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("queue", "create", "emails-failed", "--wait")
	h.mustRun("queue", "create", "emails", "--visibility-timeout", "45s", "--retention", "48h", "--max-message-size", "64KiB", "--max-receives", "5", "--dead-letter-queue", "emails-failed", "--wait")
	var qs api.QueueSpec
	h.spec("queue", "emails", &qs)
	if qs != (api.QueueSpec{VisibilityTimeoutSeconds: 45, RetentionHours: 48, MaxMessageBytes: 64 << 10, MaxReceives: 5, DeadLetterQueue: "emails-failed"}) {
		t.Errorf("queue spec %+v", qs)
	}
	if r := h.run("queue", "create", "x-q", "--retention", "90m"); r.code != cli.ExitUsage {
		t.Errorf("fractional hours: %d %s", r.code, r.err)
	}
	h.mustRun("queue", "send", "emails", "hello", "--attr", "kind=receipt", "--dedup-id", "m-1")
	if r := h.mustRun("queue", "send", "emails", "hello", "--dedup-id", "m-1"); !strings.Contains(r.out, "not sent again") {
		t.Errorf("duplicate: %s", r.out)
	}
	h.stdin = "from stdin"
	h.mustRun("queue", "send", "emails", "-")
	if r := h.mustRun("queue", "peek", "emails"); !strings.Contains(r.out, "2 waiting") || !strings.Contains(r.out, "from stdin") {
		t.Errorf("peek: %s", r.out)
	}
	r := h.mustRun("queue", "receive", "emails", "--max", "10")
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	var first api.ReceivedMessage
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first.Body != "hello" || first.Attributes["kind"] != "receipt" {
		t.Fatalf("receive: %s", r.out)
	}
	h.mustRun("queue", "delete-message", "emails", first.Receipt)
	if r := h.run("queue", "delete-message", "emails", first.Receipt); r.code == 0 {
		t.Errorf("a used receipt: %s", r.out)
	}
	if r := h.run("queue", "purge", "emails"); r.code != cli.ExitUsage {
		t.Errorf("purge without --yes off a terminal: %d", r.code)
	}
	h.mustRun("queue", "purge", "emails", "--yes", "--wait")
}

func TestTopicsAndEmailFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("queue", "create", "inbox", "--wait")
	if r := h.run("topic", "create", "orders", "--queue", "inbox", "--webhook", "https://hooks.example.com/a,secret=hook-key"); r.code != cli.ExitError {
		t.Errorf("a webhook secret that doesn't exist: %d %s", r.code, r.err)
	}
	h.stdin = "s3cret\n"
	h.mustRun("secret", "create", "hook-key", "--from-stdin", "--wait")
	h.mustRun("topic", "create", "orders", "--queue", "inbox", "--webhook", "https://hooks.example.com/a,secret=hook-key", "--retention", "12h", "--wait")
	var ts api.TopicSpec
	h.spec("topic", "orders", &ts)
	if ts.RetentionHours != 12 || len(ts.Subscriptions) != 2 || ts.Subscriptions[1] != (api.Subscription{URL: "https://hooks.example.com/a", Secret: "hook-key"}) {
		t.Errorf("topic spec %+v", ts)
	}
	h.mustRun("topic", "publish", "orders", `{"id":1}`)
	if r := h.mustRun("topic", "deliveries", "orders"); !strings.Contains(r.out, "1 published") || !strings.Contains(r.out, "queue/inbox") {
		t.Errorf("deliveries: %s", r.out)
	}
	h.mustRun("topic", "update", "orders", "--queue", "inbox", "--wait")
	var updated api.TopicSpec
	h.spec("topic", "orders", &updated)
	if len(updated.Subscriptions) != 1 {
		t.Errorf("update replaces subscriptions: %+v", updated.Subscriptions)
	}

	h.mustRun("email", "create", "shop-mail", "--domain", "example.com", "--wait")
	h.mustRun("email", "send", "shop-mail", "--from", "shop@example.com", "--to", "a@b.test", "--subject", "Receipt", "--text", "Thanks")
	r := h.mustRun("-o", "json", "email", "messages", "shop-mail")
	var list api.EmailList
	if json.Unmarshal([]byte(r.out), &list) != nil || list.Total != 1 {
		t.Fatalf("messages: %s", r.out)
	}
	if r := h.mustRun("email", "message", "shop-mail", list.Items[0].ID); !strings.Contains(r.out, "Subject: Receipt") || !strings.Contains(r.out, "Thanks") {
		t.Errorf("message: %s", r.out)
	}
	if r := h.run("email", "send", "shop-mail", "--from", "x@other.test", "--to", "a@b.test", "--subject", "x", "--text", "x"); r.code != cli.ExitError {
		t.Errorf("sender outside the domain: %d %s", r.code, r.err)
	}
	h.stdin = `{"username":"u","password":"p"}` + "\n"
	h.mustRun("secret", "create", "smtp-login", "--from-stdin", "--wait")
	h.mustRun("email", "update", "shop-mail", "--relay-host", "smtp.example.com", "--relay-credentials", "smtp-login", "--wait")
	var es api.EmailSpec
	h.spec("email", "shop-mail", &es)
	if es.Mode != api.EmailRelaying || es.Relay == nil || es.Relay.Port != 587 {
		t.Errorf("relay spec %+v", es)
	}
	h.mustRun("email", "update", "shop-mail", "--capture", "--wait")
	var captured api.EmailSpec
	h.spec("email", "shop-mail", &captured)
	if captured.Mode != api.EmailCapture || captured.Relay != nil {
		t.Errorf("capture spec %+v", captured)
	}
}

func TestSearchCDNAndTranscodersFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustRun("search", "create", "catalogue", "--memory", "3072", "--wait")
	var ss api.SearchSpec
	h.spec("search", "catalogue", &ss)
	if ss.MemoryMiB != 3072 || ss.Version != "3" {
		t.Errorf("search spec %+v", ss)
	}
	h.mustRun("search", "restart", "catalogue", "--wait")

	h.mustRun("app", "create", "web", "--image", "nginx:1.27", "--port", "80", "--wait")
	h.mustRun("cdn", "create", "assets", "--hostname", "assets.localhost", "--origin", "app/web:8080", "--default-ttl", "10m", "--wait")
	var cs api.CDNSpec
	h.spec("cdn", "assets", &cs)
	if cs.Origin != "app/web" || cs.OriginPort != 8080 || cs.TTL() != 600 || cs.TLS != api.TLSInternal {
		t.Errorf("cdn spec %+v", cs)
	}
	h.mustRun("cdn", "purge", "assets", "--yes", "--wait")
	if r := h.run("cdn", "create", "dup", "--hostname", "assets.localhost", "--origin", "app/web"); r.code != cli.ExitConflict {
		t.Errorf("a taken hostname: %d %s", r.code, r.err)
	}

	h.mustRun("bucket", "create", "uploads", "--wait")
	h.mustRun("transcoder", "create", "videos", "--input", "uploads/videos/", "--output", "uploads/media/", "--preset", "mp4-720p", "--preset", "thumbnail", "--no-watch", "--concurrency", "2", "--wait")
	var tc api.TranscoderSpec
	h.spec("transcoder", "videos", &tc)
	if tc.Watching() || tc.Concurrency != 2 || len(tc.Presets) != 2 || tc.Input != (api.BucketFolder{Bucket: "uploads", Prefix: "videos/"}) {
		t.Errorf("transcoder spec %+v", tc)
	}
	if r := h.run("transcoder", "run", "videos", "elsewhere/a.mov"); r.code != cli.ExitError {
		t.Errorf("a key outside the folder: %d %s", r.code, r.err)
	}
	if r := h.run("transcoder", "run", "videos", "videos/missing.mov", "--wait"); r.code == 0 || !strings.Contains(r.err, "no longer exists") {
		t.Errorf("a missing file: %d %s", r.code, r.err)
	}
	if r := h.mustRun("transcoder", "runs", "videos"); !strings.Contains(r.out, "failed") {
		t.Errorf("runs: %s", r.out)
	}
	if r := h.mustRun("ls"); !strings.Contains(r.out, "transcoder") || !strings.Contains(r.out, "cdn") || !strings.Contains(r.out, "search") {
		t.Errorf("ls: %s", r.out)
	}
}
