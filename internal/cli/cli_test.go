package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// harness runs the CLI in-process against a real control plane, with an isolated config file.
type harness struct {
	t           *testing.T
	env         *testenv.Env
	cfg         string
	vars        map[string]string
	stdin       string
	interactive bool
	// exec captures programs the CLI runs (vm ssh).
	exec func(name string, args []string) error
}

type result struct {
	code     int
	out, err string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := testenv.Start(t)
	return &harness{t: t, env: env, cfg: filepath.Join(t.TempDir(), "config.json"), vars: map[string]string{}}
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	var out, errb bytes.Buffer
	vars := map[string]string{"ENCLAVE_CONFIG": h.cfg, "ENCLAVE_SERVER": h.env.URL}
	for k, v := range h.vars {
		vars[k] = v
	}
	io := cli.IO{In: strings.NewReader(h.stdin), Out: &out, Err: &errb, Interactive: h.interactive,
		Getenv: func(k string) string { return vars[k] }, Exec: h.exec}
	code := cli.Run(context.Background(), append([]string{"--timeout", "20s"}, args...), io) // before args: "--" ends flags
	h.stdin = ""
	return result{code: code, out: out.String(), err: errb.String()}
}

func (h *harness) login() {
	h.t.Helper()
	h.env.Setup(h.t)
	h.stdin = testenv.AdminEmail + "\n" + testenv.AdminPassword + "\n"
	if r := h.run("login"); r.code != 0 {
		h.t.Fatalf("login: %d %s %s", r.code, r.out, r.err)
	}
}

func TestLoginSavesAPrivateTokenAndWhoami(t *testing.T) {
	h := newHarness(t)
	h.login()
	st, err := os.Stat(h.cfg)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", st, err)
	}
	var cfg cli.Config
	b, _ := os.ReadFile(h.cfg)
	_ = json.Unmarshal(b, &cfg)
	if !strings.HasPrefix(cfg.Token, api.TokenPrefix) || cfg.Server != h.env.URL {
		t.Errorf("config %+v", cfg)
	}
	r := h.run("whoami")
	if r.code != 0 || !strings.Contains(r.out, testenv.AdminEmail) || !strings.Contains(r.out, "an admin token on all projects") {
		t.Errorf("whoami: %d %q", r.code, r.out)
	}
	h.stdin = testenv.AdminEmail + "\nwrong-password\n"
	if r := h.run("login"); r.code != cli.ExitAuth || !strings.Contains(r.err, "don't match") {
		t.Errorf("bad login: %d %q", r.code, r.err)
	}
}

func TestNotSignedInExitsWithAuthCode(t *testing.T) {
	h := newHarness(t)
	r := h.run("ls")
	if r.code != cli.ExitAuth || !strings.Contains(r.err, "enclave login") {
		t.Errorf("%d %q", r.code, r.err)
	}
}

func TestEnvironmentTokenBeatsTheConfigFile(t *testing.T) {
	h := newHarness(t)
	h.login()
	_, readTok := h.env.Token(t, api.ScopeRead)
	h.vars["ENCLAVE_TOKEN"] = readTok
	if r := h.run("cache", "create", "envtest"); r.code != cli.ExitAuth || !strings.Contains(r.err, "editor role") {
		t.Errorf("env token not used: %d %q", r.code, r.err)
	}
	if r := h.run("cache", "create", "flagtest", "--token", "hc_pat_bogus"); r.code != cli.ExitAuth {
		t.Errorf("flag token not used: %d", r.code)
	}
}

func TestFullLifecycleThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.stdin = "s3cret-value\n"
	if r := h.run("secret", "create", "stripe-key", "--from-stdin", "--wait"); r.code != 0 {
		t.Fatalf("secret create: %d %s", r.code, r.err)
	}
	if r := h.run("db", "create", "orders", "--wait"); r.code != 0 || !strings.Contains(r.out, "ready") || !strings.Contains(r.out, "passwordSecret") {
		t.Fatalf("db create: %d %q %q", r.code, r.out, r.err)
	}
	if r := h.run("cache", "create", "sessions", "--max-memory", "128", "--wait"); r.code != 0 {
		t.Fatalf("cache: %d %s", r.code, r.err)
	}
	if r := h.run("bucket", "create", "media", "--versioning", "--wait"); r.code != 0 {
		t.Fatalf("bucket: %d %s", r.code, r.err)
	}
	r := h.run("app", "create", "web", "--image", "nginx:1.27-alpine", "--port", "80", "--publish", "--env", "LOG_LEVEL=info", "--secret-env", "STRIPE_KEY=stripe-key", "--wait")
	if r.code != 0 || !strings.Contains(r.err, "app web: ready") {
		t.Fatalf("app create: %d %q %q", r.code, r.out, r.err)
	}
	r = h.run("ls")
	for _, name := range []string{"stripe-key", "orders", "orders-password", "sessions", "media", "web", "objectstore-credentials"} {
		if !strings.Contains(r.out, name) {
			t.Errorf("ls missing %s:\n%s", name, r.out)
		}
	}
	r = h.run("get", "app", "web", "-o", "json")
	var app api.Resource
	if err := json.Unmarshal([]byte(r.out), &app); err != nil || app.Name != "web" || app.Status.Phase != api.PhaseReady {
		t.Fatalf("get json: %v %q", err, r.out)
	}
	if r := h.run("app", "update", "web", "--image", "nginx:1.28-alpine", "--wait"); r.code != 0 {
		t.Fatalf("update: %d %s", r.code, r.err)
	}
	if r := h.run("app", "redeploy", "web", "--wait"); r.code != 0 {
		t.Fatalf("redeploy: %d %s", r.code, r.err)
	}
	if r := h.run("app", "restart", "web"); r.code != 0 || !strings.Contains(r.out, "Restart requested") {
		t.Fatalf("restart: %d %s", r.code, r.err)
	}
	if r := h.run("logs", "app", "web", "--tail", "2"); r.code != 0 || strings.Count(r.out, "\n") != 2 {
		t.Errorf("logs: %d %q", r.code, r.out)
	}
	if r := h.run("events", "app", "web"); r.code != 0 || !strings.Contains(r.out, "updated") {
		t.Errorf("events: %d %q", r.code, r.out)
	}
	if r := h.run("secret", "get", "stripe-key", "--reveal"); r.code != 0 || strings.TrimSpace(r.out) != "s3cret-value" {
		t.Errorf("reveal: %d %q", r.code, r.out)
	}
	if r := h.run("secret", "get", "stripe-key"); strings.Contains(r.out, "s3cret-value") {
		t.Error("value shown without --reveal")
	}
	if r := h.run("delete", "secret", "stripe-key", "--yes"); r.code != cli.ExitConflict || !strings.Contains(r.err, "app/web") {
		t.Errorf("delete used secret: %d %q", r.code, r.err)
	}
	if r := h.run("delete", "app", "web"); r.code != cli.ExitUsage || !strings.Contains(r.err, "--yes") {
		t.Errorf("non-interactive delete without --yes: %d %q", r.code, r.err)
	}
	if r := h.run("delete", "app", "web", "--yes", "--wait"); r.code != 0 {
		t.Fatalf("delete: %d %s", r.code, r.err)
	}
	if r := h.run("get", "app", "web"); r.code != cli.ExitNotFound {
		t.Errorf("get deleted: %d", r.code)
	}
	if r := h.run("machine"); r.code != 0 || !strings.Contains(r.out, "fake") {
		t.Errorf("machine: %d %q", r.code, r.out)
	}
}

func TestUsageErrorsAndValidation(t *testing.T) {
	h := newHarness(t)
	h.login()
	cases := map[string][]string{
		"unknown kind":    {"get", "lambda", "x"},
		"missing arg":     {"get", "app"},
		"missing --image": {"app", "create", "web"},
		"bad env pair":    {"app", "create", "web", "--image", "nginx", "--env", "NOEQUALS"},
		"bad output":      {"ls", "-o", "yaml"},
		"empty update":    {"cache", "update", "sessions"},
		"unknown command": {"frobnicate"},
	}
	for name, args := range cases {
		if r := h.run(args...); r.code != cli.ExitUsage {
			t.Errorf("%s: exit %d (%q)", name, r.code, r.err)
		}
	}
	r := h.run("db", "create", "Bad_Name")
	if r.code != cli.ExitError || !strings.Contains(r.err, "name:") || !strings.Contains(r.err, "Request ID") {
		t.Errorf("validation: %d %q", r.code, r.err)
	}
	if r := h.run("cache", "create", "dup"); r.code != 0 {
		t.Fatal(r.err)
	}
	if r := h.run("cache", "create", "dup"); r.code != cli.ExitConflict {
		t.Errorf("duplicate: %d", r.code)
	}
}

func TestTokensAndLogout(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.run("token", "create", "--name", "ci", "--scope", "read")
	if r.code != 0 || !strings.HasPrefix(strings.TrimSpace(r.out), api.TokenPrefix) || !strings.Contains(r.err, "won't be shown again") {
		t.Fatalf("token create: %d %q %q", r.code, r.out, r.err)
	}
	secret := strings.TrimSpace(r.out)
	if r := h.run("token", "list"); r.code != 0 || !strings.Contains(r.out, "ci") || strings.Contains(r.out, secret) {
		t.Errorf("token list (must not show the secret): %d %q", r.code, r.out)
	}
	if r := h.run("logout"); r.code != 0 {
		t.Fatalf("logout: %d %s", r.code, r.err)
	}
	if r := h.run("whoami"); r.code != cli.ExitAuth {
		t.Errorf("still signed in after logout: %d", r.code)
	}
}

func TestServerDownIsAClearError(t *testing.T) {
	h := newHarness(t)
	h.vars["ENCLAVE_SERVER"] = "http://127.0.0.1:1"
	h.vars["ENCLAVE_TOKEN"] = "hc_pat_x"
	r := h.run("ls")
	if r.code != cli.ExitError || !strings.Contains(r.err, "can't reach EnclaveStack at http://127.0.0.1:1") {
		t.Errorf("%d %q", r.code, r.err)
	}
}

func TestHealthCheckAndStrategyFlags(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.run("app", "create", "api", "--image", "nginx:1.27-alpine", "--port", "8080", "--health", "http:/healthz",
		"--health-interval", "2", "--health-retries", "5", "--strategy", "recreate", "--wait")
	if r.code != 0 {
		t.Fatalf("create: %d %q", r.code, r.err)
	}
	r = h.run("get", "app", "api", "-o", "json")
	var res api.Resource
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		t.Fatal(err)
	}
	var spec api.AppSpec
	if err := json.Unmarshal(res.Spec, &spec); err != nil || spec.Strategy != api.StrategyRecreate || spec.HealthCheck == nil ||
		spec.HealthCheck.Type != api.HealthHTTP || spec.HealthCheck.Path != "/healthz" || spec.HealthCheck.IntervalSeconds != 2 || spec.HealthCheck.Retries != 5 {
		t.Fatalf("spec %+v %+v (%v)", spec, spec.HealthCheck, err)
	}
	if res.Outputs["servingRevision"] != float64(1) {
		t.Errorf("outputs %v", res.Outputs)
	}
	if r := h.run("app", "update", "api", "--health", "cmd:pg_isready -U x", "--wait"); r.code != 0 {
		t.Fatalf("update health: %d %q", r.code, r.err)
	}
	for name, args := range map[string][]string{
		"interval without --health": {"app", "update", "api", "--health-interval", "3"},
		"unknown check":             {"app", "update", "api", "--health", "ping"},
		"tcp port not a number":     {"app", "update", "api", "--health", "tcp:eighty"},
	} {
		if r := h.run(args...); r.code != cli.ExitUsage {
			t.Errorf("%s: exit %d (%q)", name, r.code, r.err)
		}
	}
	h.env.Fake.SetFault("api", fake.Fault{Unhealthy: true})
	r = h.run("app", "update", "api", "--image", "nginx:1.28-alpine", "--wait")
	if r.code != cli.ExitError || !strings.Contains(r.err, "failed its health check") {
		t.Errorf("unhealthy deploy should fail --wait with the probe result: %d %q", r.code, r.err)
	}
}

func TestDomainsAndMachineSettingsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	if r := h.run("app", "create", "web", "--image", "nginx:1.27-alpine", "--port", "80", "--wait"); r.code != 0 {
		t.Fatalf("app: %d %q", r.code, r.err)
	}
	r := h.run("domain", "create", "web-local", "--hostname", "web.localhost", "--app", "web", "--wait")
	if r.code != 0 || !strings.Contains(r.out, "https://web.localhost") || !strings.Contains(r.out, "issued") {
		t.Fatalf("domain create: %d %q %q", r.code, r.out, r.err)
	}
	if r := h.run("domain", "ca"); r.code != 0 || !strings.Contains(r.out, "BEGIN CERTIFICATE") {
		t.Errorf("domain ca: %d %q", r.code, r.err)
	}
	if r := h.run("domain", "create", "dup", "--hostname", "web.localhost", "--app", "web"); r.code != cli.ExitConflict || !strings.Contains(r.err, "already serves") {
		t.Errorf("duplicate hostname: %d %q", r.code, r.err)
	}
	if r := h.run("domain", "update", "web-local", "--tls", "off", "--wait"); r.code != 0 {
		t.Fatalf("domain update: %d %q", r.code, r.err)
	}
	if r := h.run("get", "domain", "web-local"); !strings.Contains(r.out, "http://web.localhost") {
		t.Errorf("tls off url: %q", r.out)
	}
	if r := h.run("logs", "domain", "web-local"); r.code == 0 {
		t.Error("domains have no logs")
	}
	if r := h.run("machine", "set"); r.code != cli.ExitUsage {
		t.Errorf("machine set without flags: %d", r.code)
	}
	if r := h.run("machine", "set", "--public-address", "203.0.113.7"); r.code != 0 || !strings.Contains(r.out, "203.0.113.7") {
		t.Fatalf("machine set: %d %q %q", r.code, r.out, r.err)
	}
	if r := h.run("machine"); !strings.Contains(r.out, "203.0.113.7") || !strings.Contains(r.out, "Edge proxy") {
		t.Errorf("machine: %q", r.out)
	}
	if r := h.run("machine", "set", "--acme-email", "not-an-email"); r.code != cli.ExitError {
		t.Errorf("bad acme email: %d %q", r.code, r.err)
	}
	if r := h.run("delete", "app", "web", "--yes"); r.code != cli.ExitConflict || !strings.Contains(r.err, "domain/web-local") {
		t.Errorf("app used by a domain: %d %q", r.code, r.err)
	}
	if r := h.run("delete", "domain", "web-local", "--yes", "--wait"); r.code != 0 {
		t.Errorf("delete domain: %d %q", r.code, r.err)
	}
}

func TestJobsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.run("job", "create", "tick", "--schedule", "@hourly", "--time-zone", "Asia/Kolkata", "--image", "busybox:1.37", "--run-timeout", "30s", "--wait", "--", "sh", "-c", "echo hello")
	if r.code != 0 || !strings.Contains(r.out, "nextRunAt") {
		t.Fatalf("job create: %d %q %q", r.code, r.out, r.err)
	}
	var got api.Resource
	_ = json.Unmarshal([]byte(h.run("get", "job", "tick", "-o", "json").out), &got)
	var js api.JobSpec
	if err := json.Unmarshal(got.Spec, &js); err != nil || js.TimeoutSeconds != 30 || strings.Join(js.Command, " ") != "sh -c echo hello" || js.TimeZone != "Asia/Kolkata" {
		t.Errorf("spec %+v (%v)", js, err)
	}
	r = h.run("job", "run", "tick", "--wait")
	if r.code != 0 || !strings.Contains(r.out, "tick: sh -c echo hello") || !strings.Contains(r.err, "succeeded") {
		t.Fatalf("job run --wait: %d %q %q", r.code, r.out, r.err)
	}
	if r := h.run("job", "runs", "tick"); r.code != 0 || !strings.Contains(r.out, "manual") || !strings.Contains(r.out, "succeeded") {
		t.Errorf("runs: %d %q", r.code, r.out)
	}
	if r := h.run("job", "logs", "tick"); r.code != 0 || !strings.Contains(r.out, "echo hello") {
		t.Errorf("logs: %d %q %q", r.code, r.out, r.err)
	}
	if r := h.run("job", "update", "tick", "--wait", "--", "sh", "-c", "exit 4"); r.code != 0 {
		t.Fatalf("update: %d %q", r.code, r.err)
	}
	if r := h.run("job", "run", "tick", "--wait"); r.code != cli.ExitError || !strings.Contains(r.err, "failed: Exited with code 4.") {
		t.Errorf("failing run --wait: %d %q", r.code, r.err)
	}
	if r := h.run("job", "create", "bad", "--schedule", "@every 5m", "--image", "busybox"); r.code != cli.ExitError || !strings.Contains(r.err, "spec.schedule") {
		t.Errorf("bad schedule: %d %q", r.code, r.err)
	}
	if r := h.run("job", "create", "both", "--schedule", "@daily", "--image", "busybox", "--app", "x"); r.code != cli.ExitError || !strings.Contains(r.err, "spec.app") {
		t.Errorf("app and image: %d %q", r.code, r.err)
	}
	if r := h.run("logs", "job", "tick"); r.code == 0 {
		t.Error("logs for a job should point at its runs")
	}
}

func TestBackupsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	id := func(kind, name string) string {
		var r api.Resource
		if err := json.Unmarshal([]byte(h.run("get", kind, name, "-o", "json").out), &r); err != nil {
			t.Fatalf("get %s %s: %v", kind, name, err)
		}
		return r.ID
	}
	for _, args := range [][]string{{"db", "create", "shop", "--wait"}, {"bucket", "create", "vault", "--wait"}} {
		if r := h.run(args...); r.code != 0 {
			t.Fatalf("%v: %d %s", args, r.code, r.err)
		}
	}
	h.env.Fake.SetData(id("database", "shop"), "orders=3")
	r := h.run("backup", "create", "nightly", "--source", "database/shop", "--bucket", "vault", "--keep", "3", "--time-zone", "Asia/Kolkata", "--wait")
	if r.code != 0 || !strings.Contains(r.out, "nextRunAt") {
		t.Fatalf("backup create: %d %q %q", r.code, r.out, r.err)
	}
	if r := h.run("backup", "run", "nightly", "--wait"); r.code != 0 || !strings.Contains(r.err, "succeeded") {
		t.Fatalf("backup run --wait: %d %q %q", r.code, r.out, r.err)
	}
	var runs api.RunList
	if err := json.Unmarshal([]byte(h.run("backup", "runs", "nightly", "-o", "json").out), &runs); err != nil || len(runs.Items) != 1 || runs.Items[0].Status != "succeeded" {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	runID := runs.Items[0].ID
	if r := h.run("backup", "runs", "nightly"); !strings.Contains(r.out, runID) || !strings.Contains(r.out, "manual") {
		t.Errorf("runs table: %q", r.out)
	}

	// Into a new database: the source is untouched.
	h.env.Fake.SetData(id("database", "shop"), "orders=5")
	if r := h.run("backup", "restore", "nightly", "--run", runID, "--new", "shop-copy", "--wait"); r.code != 0 || !strings.Contains(r.out, "ready") {
		t.Fatalf("restore --new: %d %q %q", r.code, r.out, r.err)
	}
	if got := h.env.Fake.Data(id("database", "shop-copy")); got != "orders=3" {
		t.Errorf("restored copy holds %q", got)
	}
	if got := h.env.Fake.Data(id("database", "shop")); got != "orders=5" {
		t.Errorf("source changed by a restore into a new database: %q", got)
	}

	// Over the source: asks first, and refuses without a terminal or --yes.
	if r := h.run("backup", "restore", "nightly", "--run", runID, "--replace"); r.code != cli.ExitUsage || !strings.Contains(r.err, "--yes") {
		t.Errorf("replace without --yes: %d %q", r.code, r.err)
	}
	if r := h.run("backup", "restore", "nightly", "--run", runID, "--replace", "--yes", "--wait"); r.code != 0 {
		t.Fatalf("restore --replace: %d %q %q", r.code, r.out, r.err)
	}
	if got := h.env.Fake.Data(id("database", "shop")); got != "orders=3" {
		t.Errorf("replace restored %q", got)
	}
	_ = json.Unmarshal([]byte(h.run("backup", "runs", "nightly", "-o", "json").out), &runs)
	if len(runs.Items) != 2 || runs.Items[0].Trigger != "pre_restore" {
		t.Errorf("no safety backup before replacing: %+v", runs.Items)
	}

	if r := h.run("backup", "update", "nightly", "--keep", "10", "--schedule", "0 4 * * *", "--wait"); r.code != 0 {
		t.Fatalf("update: %d %q", r.code, r.err)
	}
	var b api.Resource
	_ = json.Unmarshal([]byte(h.run("get", "backup", "nightly", "-o", "json").out), &b)
	var bs api.BackupSpec
	if err := json.Unmarshal(b.Spec, &bs); err != nil || bs.Keep != 10 || bs.Schedule != "0 4 * * *" || bs.Source.Database != "shop" || bs.TimeZone != "Asia/Kolkata" {
		t.Errorf("spec after update %+v (%v)", bs, err)
	}

	if r := h.run("backup", "restore", "nightly", "--run", runID); r.code != cli.ExitUsage {
		t.Errorf("restore with neither --new nor --replace: %d %q", r.code, r.err)
	}
	if r := h.run("backup", "restore", "nightly", "--run", "run_aaaaaaaaaaaaaaaa", "--new", "x-copy"); r.code == 0 {
		t.Error("restoring an unknown run succeeded")
	}
	h.interactive, h.stdin = true, "n\n"
	if r := h.run("delete", "backup", "nightly"); !strings.Contains(r.err+r.out, "backups already stored in the bucket are kept") {
		t.Errorf("delete prompt doesn't say the objects stay: %q %q", r.out, r.err)
	}
	h.interactive = false
	if r := h.run("backup", "create", "nosrc", "--source", "shop", "--bucket", "vault"); r.code != cli.ExitUsage || !strings.Contains(r.err, "database/NAME") {
		t.Errorf("source without a kind: %d %q", r.code, r.err)
	}
	if r := h.run("backup", "create", "ghost", "--source", "cache/nope", "--bucket", "vault"); r.code != cli.ExitError || !strings.Contains(r.err, "spec.source") {
		t.Errorf("unknown source: %d %q", r.code, r.err)
	}
	if r := h.run("backup", "create", "plain", "--source", "database/shop", "--s3-endpoint", "http://s3.example.com", "--s3-bucket", "b", "--s3-credentials", "k"); r.code != cli.ExitError || !strings.Contains(r.err, "endpoint") {
		t.Errorf("http S3 endpoint: %d %q", r.code, r.err)
	}
}

func TestBuildsFromGitThroughTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	const repo = "https://git.example.com/acme/web.git"
	first := h.env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo building $LEVEL\n"})
	for args, want := range map[string]string{
		"app create none --port 80":                                   "--image IMAGE or --git",
		"app create both --image nginx --git " + repo + " --ref main": "--image IMAGE or --git",
		"app create noref --git " + repo:                              "--ref",
	} {
		if r := h.run(strings.Fields(args)...); r.code != cli.ExitUsage || !strings.Contains(r.err, want) {
			t.Errorf("%s: %d %q", args, r.code, r.err)
		}
	}
	r := h.run("app", "create", "web", "--git", repo, "--ref", "main", "--port", "8080", "--build-arg", "LEVEL=prod", "--wait")
	if r.code != 0 || !strings.Contains(r.out, "builtCommit") || !strings.Contains(r.out, first) {
		t.Fatalf("create from git: %d %q %q", r.code, r.out, r.err)
	}
	if !strings.Contains(r.out, `{"buildArgs":{"LEVEL":"prod"},"context":".","dockerfile":"Dockerfile","git":"`+repo+`","ref":"main"}`) || strings.Contains(r.out, "map[") {
		t.Errorf("nested spec shown as Go's map syntax:\n%s", r.out)
	}
	if r := h.run("logs", "app", "web", "--build"); r.code != 0 || !strings.Contains(r.out, "RUN echo building $LEVEL") || !strings.Contains(r.out, "building prod") {
		t.Errorf("build log: %d %q %q", r.code, r.out, r.err)
	}
	second := h.env.Git.Push(repo, "main", map[string]string{"Dockerfile": "FROM busybox:1.37\n"})
	if r := h.run("app", "redeploy", "web", "--wait"); r.code != 0 {
		t.Fatalf("redeploy: %d %q", r.code, r.err)
	}
	var got api.Resource
	_ = json.Unmarshal([]byte(h.run("get", "app", "web", "-o", "json").out), &got)
	if got.Outputs["builtCommit"] != second {
		t.Errorf("redeploy built %v, want %s", got.Outputs["builtCommit"], second)
	}
	// Changing one Git flag keeps the rest of the source.
	if r := h.run("app", "update", "web", "--dockerfile", "docker/Dockerfile"); r.code != 0 {
		t.Fatalf("update dockerfile: %d %q", r.code, r.err)
	}
	_ = json.Unmarshal([]byte(h.run("get", "app", "web", "-o", "json").out), &got)
	var spec api.AppSpec
	_ = json.Unmarshal(got.Spec, &spec)
	if spec.Source == nil || spec.Source.Git != repo || spec.Source.Dockerfile != "docker/Dockerfile" || spec.Source.BuildArgs["LEVEL"] != "prod" {
		t.Errorf("merged source %+v", spec.Source)
	}
	// Switching to an image drops the source.
	if r := h.run("app", "update", "web", "--image", "nginx:1.27-alpine", "--wait"); r.code != 0 {
		t.Fatalf("switch to image: %d %q", r.code, r.err)
	}
	_ = json.Unmarshal([]byte(h.run("get", "app", "web", "-o", "json").out), &got)
	spec = api.AppSpec{}
	_ = json.Unmarshal(got.Spec, &spec)
	if spec.Source != nil || spec.Image != "nginx:1.27-alpine" {
		t.Errorf("after switching: %+v", spec)
	}
	if r := h.run("logs", "db", "x", "--build"); r.code == 0 {
		t.Error("build logs of a missing database succeeded")
	}
}
