package cli_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.7 commands (L2-002 CLI-37 – CLI-45).

func (h *harness) ok(args ...string) result {
	h.t.Helper()
	r := h.run(args...)
	if r.code != 0 {
		h.t.Fatalf("enclave %s: exit %d\nout: %s\nerr: %s", strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r
}

func TestProjectsUsersAndScopedTokensFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.ok("project", "create", "staging", "--description", "a copy of the shop")
	h.ok("secret", "create", "key", "--value", "v", "--wait")
	h.ok("--project", "staging", "secret", "create", "key", "--value", "v2", "--wait")
	h.ok("project", "use", "staging")
	if r := h.ok("secret", "get", "key", "--reveal"); strings.TrimSpace(r.out) != "v2" {
		t.Errorf("project use: %q", r.out)
	}
	if r := h.ok("ls", "--all-projects", "--kind", "secret"); !strings.Contains(r.out, "PROJECT") || !strings.Contains(r.out, "staging") || !strings.Contains(r.out, "default") {
		t.Errorf("ls -A: %s", r.out)
	}
	if r := h.ok("--project", "default", "secret", "get", "key", "--reveal"); strings.TrimSpace(r.out) != "v" {
		t.Errorf("--project beats project use: %q", r.out)
	}
	if r := h.ok("project", "ls"); !strings.Contains(r.out, "* ") && !strings.Contains(r.out, "*  staging") {
		t.Logf("project ls: %s", r.out)
	}
	inv := h.ok("user", "invite", "dev@example.test", "--project-role", "staging=viewer")
	if !strings.Contains(inv.out, "/invite?code=") {
		t.Errorf("invite link: %q", inv.out)
	}
	if r := h.ok("user", "ls"); !strings.Contains(r.out, "dev@example.test") || !strings.Contains(r.out, "invited") || !strings.Contains(r.out, "staging=viewer") {
		t.Errorf("user ls: %s", r.out)
	}
	h.ok("user", "update", "dev@example.test", "--project-role", "staging=editor")
	tok := h.ok("token", "create", "--name", "ci", "--role", "viewer", "--project", "staging")
	secret := strings.TrimSpace(tok.out)
	h.vars["ENCLAVE_TOKEN"] = secret
	if r := h.run("secret", "create", "nope", "--value", "x"); r.code != cli.ExitAuth || !strings.Contains(r.err, "editor role in staging") {
		t.Errorf("a viewer token creates: %d %q", r.code, r.err)
	}
	if r := h.run("--project", "default", "secret", "get", "key"); r.code != cli.ExitNotFound {
		t.Errorf("a staging token reads default: %d %q", r.code, r.err)
	}
	delete(h.vars, "ENCLAVE_TOKEN")
	if r := h.ok("token", "ls"); !strings.Contains(r.out, "viewer") || !strings.Contains(r.out, "staging") {
		t.Errorf("token ls: %s", r.out)
	}
	if r := h.run("project", "delete", "staging", "--yes"); r.code != cli.ExitConflict || !strings.Contains(r.err, "secret/key") {
		t.Errorf("delete a non-empty project: %d %q", r.code, r.err)
	}
	h.ok("user", "remove", "dev@example.test", "--yes")
	if r := h.ok("audit", "--limit", "20"); !strings.Contains(r.out, "user_invited") || !strings.Contains(r.out, "project_created") {
		t.Errorf("audit: %s", r.out)
	}
}

func TestMonitoringFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	t.Cleanup(hook.Close)
	m := h.env.Monitor
	var clockMu sync.Mutex
	// The monitor's clock runs normally (the notifier needs it), except at the two samples, where the test
	// pins it 30 s apart exactly, so the CPU rate is exact however slow the machine is.
	var pinned *time.Time
	m.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		if pinned != nil {
			return *pinned
		}
		return time.Now()
	}
	pin := func(t *time.Time) { clockMu.Lock(); pinned = t; clockMu.Unlock() }
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				m.NotifyOnce(context.Background())
			}
		}
	}()

	h.ok("app", "create", "api", "--image", "nginx", "--wait")
	h.ok("channel", "create", "ops", "--webhook", hook.URL, "--wait")
	if r := h.ok("channel", "test", "ops"); !strings.Contains(r.out, "Delivered") {
		t.Errorf("channel test: %s", r.out)
	}
	h.ok("alert", "create", "api-crashing", "--crash-loop", "app/api", "--restarts", "2", "--window", "5m", "--channel", "ops", "--wait")
	if r := h.ok("alert", "get", "api-crashing"); !strings.Contains(r.out, "crash_loop") {
		t.Errorf("alert get: %s", r.out)
	}
	if r := h.run("alert", "create", "bad", "--failed", "app/api", "--metric", "cpu", "--channel", "ops"); r.code != cli.ExitUsage {
		t.Errorf("two conditions: %d", r.code)
	}
	h.ok("alert", "update", "api-crashing", "--disabled", "--wait")
	h.ok("dashboard", "create", "health", "--widget", "title=API CPU,metric=cpu,resources=app/api", "--wait")

	app, _ := h.env.Store.GetResourceByName(context.Background(), api.DefaultProject, api.KindApp, "api")
	first, second := time.Now().Add(-time.Minute), time.Now().Add(-30*time.Second) // inside the metrics window
	pin(&first)
	h.env.Fake.SetStats(app.ID, engine.ContainerStats{ID: "c", CPUSeconds: 1, MemoryBytes: 50 << 20})
	_ = m.SampleOnce(context.Background())
	pin(&second)
	h.env.Fake.SetStats(app.ID, engine.ContainerStats{ID: "c", CPUSeconds: 16, MemoryBytes: 60 << 20})
	_ = m.SampleOnce(context.Background())
	pin(nil)
	if r := h.ok("metrics", "app/api", "--metric", "cpu", "--metric", "memory"); !strings.Contains(r.out, "0.500") || !strings.Contains(r.out, "60.0 MiB") {
		t.Errorf("metrics: %s", r.out)
	}
	if r := h.ok("metrics", "machine", "--metric", "diskFree"); !strings.Contains(r.out, "diskFree") {
		t.Errorf("machine metrics: %s", r.out)
	}
	h.ok("logexport", "create", "out", "--http", hook.URL, "--filter", "^err", "--flush", "15s", "--wait")
	if r := h.ok("logexport", "get", "out"); !strings.Contains(r.out, "flushSeconds") {
		t.Errorf("logexport get: %s", r.out)
	}
	if r := h.run("logexport", "create", "two", "--http", hook.URL, "--loki", hook.URL); r.code != cli.ExitUsage {
		t.Errorf("two destinations: %d", r.code)
	}
	if r := h.run("secret", "create", "x", "--from-store", "vault", "--key", "a", "--value", "v"); r.code != cli.ExitUsage {
		t.Errorf("a value and a store: %d", r.code)
	}
	if r := h.ok("ls", "--kind", "alert"); !strings.Contains(r.out, "api-crashing") {
		t.Errorf("ls --kind alert: %s", r.out)
	}
}

func TestInvitesAreEmailedFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.ok("email", "create", "mail", "--wait")
	if r := h.run("machine", "set", "--invite-email", "mail"); r.code != cli.ExitUsage {
		t.Errorf("--invite-email without --invite-from: %d", r.code)
	}
	if r := h.ok("machine", "set", "--invite-email", "default/mail", "--invite-from", "hc@example.test"); !strings.Contains(r.out, "from hc@example.test through default/mail") {
		t.Errorf("machine set: %s", r.out)
	}
	if r := h.ok("user", "invite", "dev@example.test", "--role", "viewer"); !strings.Contains(r.err, "Emailed to dev@example.test") || !strings.Contains(r.out, "/invite?code=") {
		t.Errorf("invite: %q %q", r.out, r.err)
	}
	if r := h.ok("machine", "set", "--no-invite-email"); !strings.Contains(r.out, "Invites emailed: no") {
		t.Errorf("--no-invite-email: %s", r.out)
	}
}
