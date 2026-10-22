// Package testenv boots a real control plane in-process for integration tests: real SQLite in a temp
// directory, real HTTP on a random loopback port, the fake engine driver, and fast reconcile timing.
// No mocks at this level (L5-002 §1).
package testenv

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Admin credentials used by Setup.
const (
	AdminEmail    = "admin@example.com"
	AdminPassword = "correct-horse-battery"
)

// Env is a running control plane.
type Env struct {
	*server.Running
	DataDir string
	Logs    *SyncBuffer
	cfg     server.Config
}

// SyncBuffer is a goroutine-safe log sink tests can search.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything logged so far.
func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Option adjusts the server config.
type Option func(*server.Config)

// WithConfig applies f to the config.
func WithConfig(f func(*server.Config)) Option { return Option(f) }

// Start boots a control plane that stops when the test ends.
func Start(t testing.TB, opts ...Option) *Env {
	t.Helper()
	dir := t.TempDir()
	cfg := server.Config{Listen: "127.0.0.1:0", DataDir: dir, Driver: "fake", LogLevel: "debug",
		PollInterval: 50 * time.Millisecond, DriftEvery: time.Hour, CertWait: 300 * time.Millisecond, SchedulerTick: 50 * time.Millisecond, TranscodeWatch: 100 * time.Millisecond, RateLimit: 10000, ShutdownGrace: 5 * time.Second,
		ManualMonitor: true, // tests drive the monitor (Env.Monitor) with their own clock
		MachineCheck:  100 * time.Millisecond, MachineOffline: 500 * time.Millisecond}
	for _, o := range opts {
		o(&cfg)
	}
	return start(t, cfg)
}

func start(t testing.TB, cfg server.Config) *Env {
	t.Helper()
	logs := &SyncBuffer{}
	var out io.Writer = logs
	if os.Getenv("ENCLAVE_TEST_LOGS") != "" {
		out = io.MultiWriter(logs, os.Stderr)
	}
	r, err := server.Start(context.Background(), cfg, out)
	if err != nil {
		t.Fatalf("start control plane: %v", err)
	}
	e := &Env{Running: r, DataDir: cfg.DataDir, Logs: logs, cfg: cfg}
	t.Cleanup(func() { _ = r.Stop() })
	return e
}

// Restart stops the server and starts a new one on the same data directory (recovery tests).
func (e *Env) Restart(t testing.TB) *Env {
	t.Helper()
	if err := e.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	return start(t, e.cfg)
}

// Setup completes first-run setup and returns a client signed in with a cookie session.
func (e *Env) Setup(t testing.TB) *client.Client {
	t.Helper()
	c := client.New(e.URL, "")
	if _, err := c.Setup(context.Background(), api.SetupRequest{SetupCode: e.SetupCode, MachineName: "Test machine", Email: AdminEmail, Password: AdminPassword}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return c
}

// Token completes setup if needed and returns a client authenticated with a new token of scope.
func (e *Env) Token(t testing.TB, scope string) (*client.Client, string) {
	t.Helper()
	ctx := context.Background()
	session := client.New(e.URL, "")
	if st, err := session.GetSetup(ctx); err != nil {
		t.Fatalf("get setup: %v", err)
	} else if st.Required {
		session = e.Setup(t)
	} else if _, err := session.SignIn(ctx, AdminEmail, AdminPassword); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	tok, err := session.CreateToken(ctx, api.CreateTokenRequest{Name: "test-" + scope, Scope: scope})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return client.New(e.URL, tok.Secret), tok.Secret
}

// WaitReady waits (bounded) for a resource to be ready.
func WaitReady(t testing.TB, c *client.Client, id string) api.Resource {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := c.WaitReady(ctx, id, 20*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("wait ready %s: %v (status %+v)", id, err, r.Status)
	}
	return r
}

// Eventually polls cond until true or the timeout passes.
func Eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// ExpireTokenForTest backdates a token's expiry so expiry handling can be tested without waiting.
func (e *Env) ExpireTokenForTest(id string) error {
	return e.Store.ExpireTokenForTest(context.Background(), id)
}
