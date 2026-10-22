//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// v0.6 on real Docker through the built binaries (L5-002 §9): OpenTofu applies the shop of L2-008 §1 —
// a topic fanning out to a queue and a webhook, a dead-letter queue, a MySQL database, an email service,
// a CDN in front of a bucket and a transcoder — and the CLI, an app speaking NATS and the engine check it.
func TestDataAndMessagingStackForRealWithOpenTofu(t *testing.T) {
	requireTools(t, "docker", "tofu", "curl")
	ctx := context.Background()
	var mu sync.Mutex
	var hooks []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hooks = append(hooks, string(b)+"|"+r.Header.Get("X-Enclave-Signature"))
		mu.Unlock()
	}))
	defer hook.Close()

	s := startServer(t, t.TempDir(), freePort(t))
	token, c := setupAndToken(t, s)
	bin := build(t)
	work := t.TempDir()
	rc := filepath.Join(work, "dev.tofurc")
	_ = os.WriteFile(rc, []byte(fmt.Sprintf("provider_installation {\n  dev_overrides { \"enclavestack/enclave\" = %q }\n  direct {}\n}\n", bin)), 0o600)
	id := suffix()
	main := fmt.Sprintf(`
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}
provider "enclave" {}

resource "enclave_secret" "hook" {
  name  = "e2e-hook-%[1]s"
  value = "s3cret"
}
resource "enclave_queue" "failed" {
  name = "e2e-failed-%[1]s"
}
resource "enclave_queue" "emails" {
  name                       = "e2e-emails-%[1]s"
  visibility_timeout_seconds = 2
  max_receives               = 2
  dead_letter_queue          = enclave_queue.failed.name
}
resource "enclave_topic" "orders" {
  name          = "e2e-orders-%[1]s"
  subscriptions = [
    { queue = enclave_queue.emails.name },
    { url = %[2]q, secret = enclave_secret.hook.name },
  ]
}
resource "enclave_database" "legacy" {
  name   = "e2e-legacy-%[1]s"
  engine = "mysql"
}
resource "enclave_email" "mail" {
  name   = "e2e-mail-%[1]s"
  domain = "example.com"
}
resource "enclave_bucket" "uploads" {
  name = "e2e-uploads-%[1]s"
}
resource "enclave_cdn" "files" {
  name     = "e2e-files-%[1]s"
  hostname = "e2e-files-%[1]s.localhost"
  origin   = "bucket/${enclave_bucket.uploads.name}"
}
resource "enclave_transcoder" "videos" {
  name    = "e2e-videos-%[1]s"
  input   = { bucket = enclave_bucket.uploads.name, prefix = "videos/" }
  output  = { bucket = enclave_bucket.uploads.name, prefix = "media/" }
  presets = ["mp4-480p", "thumbnail"]
}
`, id, hook.URL+"/orders")
	_ = os.WriteFile(filepath.Join(work, "main.tf"), []byte(main), 0o600)
	env := append(os.Environ(), "TF_CLI_CONFIG_FILE="+rc, "ENCLAVE_SERVER="+s.url, "ENCLAVE_TOKEN="+token, "TF_IN_AUTOMATION=1")
	tofu := func(args ...string) string {
		cmd := exec.Command("tofu", args...)
		cmd.Dir, cmd.Env = work, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tofu %s: %v\n%s\nserver log:\n%s", strings.Join(args, " "), err, out, tail(s.logs.String(), 40))
		}
		return string(out)
	}
	t.Cleanup(func() {
		cmd := exec.Command("tofu", "destroy", "-auto-approve", "-no-color")
		cmd.Dir, cmd.Env = work, env
		_ = cmd.Run()
		// The store keeps this server's credentials; the next test's server has its own (as in TestEveryKind…).
		_ = exec.Command("docker", "rm", "-f", "es-system-objectstore").Run()
		_ = exec.Command("docker", "volume", "rm", "-f", "es-system-objectstore-data").Run()
	})
	tofu("apply", "-auto-approve", "-no-color")

	// An app inside EnclaveStack publishes with the topic's own user over NATS (TOP-06), and the message
	// reaches the queue and the signed webhook (TOP-01, TOP-02).
	topic := getJSON(t, s, token, "topic", "e2e-orders-"+id)
	sec, _ := c.FindResource(ctx, api.KindSecret, topic.Outputs["passwordSecret"].(string))
	pw, err := c.RevealSecret(ctx, sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub := exec.Command("docker", "run", "--rm", "--network", "enclave", "natsio/nats-box:0.18.1", "nats", "--server", topic.Outputs["internalUrl"].(string),
		"--user", topic.Outputs["username"].(string), "--password", pw, "pub", topic.Outputs["subject"].(string), "order 1")
	if out, err := pub.CombinedOutput(); err != nil {
		t.Fatalf("publish from an app: %v %s", err, out)
	}
	deny := exec.Command("docker", "run", "--rm", "--network", "enclave", "natsio/nats-box:0.18.1", "nats", "--server", topic.Outputs["internalUrl"].(string),
		"--user", topic.Outputs["username"].(string), "--password", pw, "pub", "es.q.e2e-failed-"+id, "sneaky")
	if out, err := deny.CombinedOutput(); err == nil && !strings.Contains(string(out), "ermission") {
		t.Errorf("a topic user published to a queue: %s", out)
	}
	waitUntil(t, 30*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(hooks) == 1
	}, "the webhook didn't receive the message")
	if !strings.HasPrefix(hooks[0], "order 1|sha256=") {
		t.Errorf("webhook: %q", hooks[0])
	}
	emails := getJSON(t, s, token, "queue", "e2e-emails-"+id)
	r := mustCLI(t, s, token, "", "queue", "receive", emails.Name, "--wait", "10s")
	if !strings.Contains(r, `"body":"order 1"`) {
		t.Fatalf("receive: %s", r)
	}
	// Not deleted twice, it moves to the dead-letter queue at the next receive, as SQS does (QUE-03).
	time.Sleep(2500 * time.Millisecond)
	mustCLI(t, s, token, "", "queue", "receive", emails.Name, "--wait", "10s")
	time.Sleep(2500 * time.Millisecond)
	if out := mustCLI(t, s, token, "", "queue", "receive", emails.Name, "--wait", "2s"); strings.Contains(out, "order 1") {
		t.Errorf("a message past its maximum receives was delivered again: %s", out)
	}
	waitUntil(t, 30*time.Second, func() bool {
		return strings.Contains(mustCLI(t, s, token, "", "queue", "peek", "e2e-failed-"+id), "order 1")
	}, "the message never reached the dead-letter queue")

	// MySQL answers SQL with the managed password.
	db := getJSON(t, s, token, "db", "e2e-legacy-"+id)
	dbSec, _ := c.FindResource(ctx, api.KindSecret, db.Outputs["passwordSecret"].(string))
	dbPw, _ := c.RevealSecret(ctx, dbSec.ID)
	sql := exec.Command("docker", "exec", "-e", "MYSQL_PWD="+dbPw, "es-database-e2e-legacy-"+id, "mysql", "-h", "127.0.0.1", "-u", db.Outputs["username"].(string), "-N", "-e", "SELECT 41+1", db.Outputs["database"].(string))
	if out, err := sql.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "42" {
		t.Errorf("mysql: %v %s", err, out)
	}

	// Email: the CLI sends, the service keeps it (EML-03, EML-04).
	mustCLI(t, s, token, "", "email", "send", "e2e-mail-"+id, "--from", "shop@example.com", "--to", "a@b.test", "--subject", "Receipt", "--text", "Thanks")
	if out := mustCLI(t, s, token, "", "email", "messages", "e2e-mail-"+id); !strings.Contains(out, "Receipt") || !strings.Contains(out, "captured") {
		t.Errorf("email messages:\n%s", out)
	}

	// A video lands in the bucket: the transcoder converts it; the CDN serves the thumbnail (TRN-03, CDN-02, CDN-03).
	bucket := getJSON(t, s, token, "bucket", "e2e-uploads-"+id)
	credSec, _ := c.FindResource(ctx, api.KindSecret, "objectstore-credentials")
	raw, _ := c.RevealSecret(ctx, credSec.ID)
	var creds struct{ AccessKey, SecretKey string }
	_ = json.Unmarshal([]byte(raw), &creds)
	mc, err := minio.New(strings.TrimPrefix(bucket.Outputs["endpoint"].(string), "http://"), &minio.Options{Creds: credentials.NewStaticV4(creds.AccessKey, creds.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	clip, err := exec.Command("docker", "run", "--rm", "--entrypoint", "ffmpeg", "linuxserver/ffmpeg:8.0.1", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10", "-c:v", "libx264", "-f", "matroska", "-").Output()
	if err != nil {
		t.Fatalf("make a clip: %v", err)
	}
	time.Sleep(12 * time.Second) // after the transcoder's first listing: this object is new (TRN-03)
	if _, err := mc.PutObject(ctx, bucket.Name, "videos/cat.mkv", bytes.NewReader(clip), int64(len(clip)), minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 90*time.Second, func() bool {
		out := mustCLI(t, s, token, "", "transcoder", "runs", "e2e-videos-"+id)
		return strings.Contains(out, "succeeded") || strings.Contains(out, "failed")
	}, "no transcoder run")
	if out := mustCLI(t, s, token, "", "transcoder", "runs", "e2e-videos-"+id); !strings.Contains(out, "succeeded") {
		t.Fatalf("transcoder runs:\n%s\nserver log:\n%s", out, tail(s.logs.String(), 30))
	}
	host := "e2e-files-" + id + ".localhost"
	curl := func() (string, string) {
		out, _ := exec.Command("curl", "-sk", "-o", "/dev/null", "-D", "-", "--resolve", host+":443:127.0.0.1", "https://"+host+"/media/cat/thumbnail.jpg").Output()
		status, cache := "", ""
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "HTTP/") {
				status = strings.Fields(line)[1]
			}
			if strings.HasPrefix(strings.ToLower(line), "x-cache:") {
				cache = strings.TrimSpace(line[len("x-cache:"):])
			}
		}
		return status, cache
	}
	waitUntil(t, 60*time.Second, func() bool { st, _ := curl(); return st == "200" }, "the CDN never served the thumbnail")
	if _, cache := curl(); cache != "HIT" {
		t.Errorf("second request X-Cache %q, want HIT", cache)
	}

	if again := tofu("plan", "-no-color", "-detailed-exitcode"); !strings.Contains(again, "No changes") {
		t.Errorf("second plan is not empty:\n%s", again)
	}
	tofu("destroy", "-auto-approve", "-no-color")
	if _, err := c.FindResource(ctx, api.KindQueue, "e2e-emails-"+id); !client.IsNotFound(err) {
		t.Errorf("queue survived destroy: %v", err)
	}
	for _, name := range []string{"es-system-broker", "es-cdn-e2e-files-" + id, "es-email-e2e-mail-" + id} {
		if exec.Command("docker", "inspect", name).Run() == nil {
			t.Errorf("%s left behind", name)
		}
	}
	for _, line := range strings.Split(s.logs.String(), "\n") {
		if strings.Contains(line, `"level":"ERROR"`) {
			t.Errorf("server logged an error: %s", line)
		}
	}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
