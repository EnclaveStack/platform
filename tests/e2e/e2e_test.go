//go:build e2e

// Package e2e drives the built binaries against real Docker and real OpenTofu (L5-002 §1, "E2E —
// real engine"): go test -tags e2e ./tests/e2e/...  It needs Docker running and `tofu` on PATH.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/enclavestack/platform/internal/build/gittest"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// TestMain removes the probe helper the servers start for health checks, when this run created it; one
// that was already there (a real EnclaveStack on this machine) is left alone.
func TestMain(m *testing.M) {
	existed := exec.Command("docker", "inspect", "es-system-probe").Run() == nil
	code := m.Run()
	if !existed {
		_ = exec.Command("docker", "rm", "-f", "es-system-probe").Run()
	}
	os.Exit(code)
}

var (
	binDir    string
	buildOnce sync.Once
	buildErr  error
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// build compiles enclave and the provider once per test run.
func build(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "enclave-e2e-bin-")
		if buildErr != nil {
			return
		}
		for _, target := range []string{"enclave", "terraform-provider-enclave"} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, target), "./cmd/"+target)
			cmd.Dir = repoRoot()
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", target, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binDir
}

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("Docker is not running: %s", out)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func suffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// server is a running `enclave server` process.
type server struct {
	t       *testing.T
	cmd     *exec.Cmd
	url     string
	code    string
	dataDir string
	port    int
	logs    *bytes.Buffer
}

func startServer(t *testing.T, dataDir string, port int, extra ...string) *server {
	t.Helper()
	bin := build(t)
	args := append([]string{"server", "--data-dir", dataDir, "--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--log-format", "json", "--drift-interval", "2s", "--poll-interval", "500ms"}, extra...)
	cmd := exec.Command(filepath.Join(bin, "enclave"), args...)
	stdout, _ := cmd.StdoutPipe()
	logs := &bytes.Buffer{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, cmd: cmd, url: fmt.Sprintf("http://127.0.0.1:%d", port), dataDir: dataDir, port: port, logs: logs}
	codeRe := regexp.MustCompile(`^\s+([A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4})\s*$`)
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m := codeRe.FindStringSubmatch(sc.Text()); m != nil {
				s.code = m[1]
			}
			if strings.HasPrefix(sc.Text(), "Console:") {
				go func() { time.Sleep(300 * time.Millisecond); close(ready) }()
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatalf("server did not start:\n%s", logs.String())
	}
	t.Cleanup(func() { s.stop(syscall.SIGTERM) })
	return s
}

func (s *server) stop(sig syscall.Signal) {
	if s.cmd.ProcessState != nil {
		return
	}
	_ = s.cmd.Process.Signal(sig)
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = s.cmd.Process.Kill()
	}
}

// cli runs the built CLI with the given token.
func cli(t *testing.T, s *server, token string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(build(t), "enclave"), args...)
	cmd.Env = append(os.Environ(), "ENCLAVE_SERVER="+s.url, "ENCLAVE_TOKEN="+token, "ENCLAVE_CONFIG="+filepath.Join(t.TempDir(), "c.json"))
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func mustCLI(t *testing.T, s *server, token, stdin string, args ...string) string {
	t.Helper()
	out, errOut, code := cli(t, s, token, stdin, args...)
	if code != 0 {
		t.Fatalf("enclave %s: exit %d\n%s\n%s\nserver log:\n%s", strings.Join(args, " "), code, out, errOut, tail(s.logs.String(), 30))
	}
	return out
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func setupAndToken(t *testing.T, s *server) (string, *client.Client) {
	t.Helper()
	ctx := context.Background()
	if s.code == "" {
		t.Fatal("no setup code printed")
	}
	session := client.New(s.url, "")
	if _, err := session.Setup(ctx, api.SetupRequest{SetupCode: s.code, MachineName: "e2e", Email: "e2e@example.com", Password: "e2e-password-123"}); err != nil {
		t.Fatal(err)
	}
	tok, err := session.CreateToken(ctx, api.CreateTokenRequest{Name: "e2e", Scope: api.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Secret, client.New(s.url, tok.Secret)
}

func getJSON(t *testing.T, s *server, token string, kind, name string) api.Resource {
	t.Helper()
	var r api.Resource
	if err := json.Unmarshal([]byte(mustCLI(t, s, token, "", "get", kind, name, "-o", "json")), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func outInt(r api.Resource, k string) int {
	if f, ok := r.Outputs[k].(float64); ok {
		return int(f)
	}
	return 0
}

// TestEveryKindWorksForRealThroughTheCLI proves each service actually serves: SQL, PING, objects and an
// app that answers HTTP with a value injected from a secret.
func TestEveryKindWorksForRealThroughTheCLI(t *testing.T) {
	requireTools(t, "docker")
	s := startServer(t, t.TempDir(), freePort(t))
	token, _ := setupAndToken(t, s)
	ctx := context.Background()
	id := suffix()
	db, cache, bucket, sec, app := "e2e-db-"+id, "e2e-cache-"+id, "e2e-bkt-"+id, "e2e-key-"+id, "e2e-app-"+id
	t.Cleanup(func() {
		for _, kr := range [][2]string{{"backup", "e2e-bak-" + id}, {"database", db + "-copy"}, {"job", "e2e-job-" + id}, {"app", app}, {"secret", sec}, {"bucket", bucket}, {"cache", cache}, {"database", db}} {
			cli(t, s, token, "", "delete", kr[0], kr[1], "--yes", "--wait", "--timeout", "3m")
		}
		_ = exec.Command("docker", "rm", "-f", "es-system-objectstore").Run()
		_ = exec.Command("docker", "volume", "rm", "-f", "es-system-objectstore-data").Run()
	})

	mustCLI(t, s, token, "", "db", "create", db, "--wait", "--timeout", "5m")
	mustCLI(t, s, token, "", "cache", "create", cache, "--max-memory", "64", "--wait", "--timeout", "5m")
	mustCLI(t, s, token, "", "bucket", "create", bucket, "--wait", "--timeout", "5m")
	mustCLI(t, s, token, "injected-"+id+"\n", "secret", "create", sec, "--from-stdin", "--wait")
	mustCLI(t, s, token, "", "app", "create", app, "--image", "busybox:1.36", "--port", "8080", "--publish", "--memory", "64",
		"--secret-env", "PAYLOAD="+sec,
		"--command", "sh", "--command", "-c", "--command", `mkdir -p /www && echo "$PAYLOAD" > /www/index.html && exec httpd -f -p 8080 -h /www`,
		"--wait", "--timeout", "5m")

	// Database: real SQL with the managed password.
	dbRes := getJSON(t, s, token, "database", db)
	pw := strings.TrimSpace(mustCLI(t, s, token, "", "secret", "get", db+"-password", "--reveal"))
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%v:%s@127.0.0.1:%d/%v?sslmode=disable", dbRes.Outputs["username"], pw, outInt(dbRes, "port"), dbRes.Outputs["database"]))
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("SELECT 1: %v", err)
	}
	conn.Close(ctx)

	// Cache: PING.
	cacheRes := getJSON(t, s, token, "cache", cache)
	nc, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", outInt(cacheRes, "port")))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = nc.Write([]byte("PING\r\n"))
	line, _ := bufio.NewReader(nc).ReadString('\n')
	nc.Close()
	if strings.TrimSpace(line) != "+PONG" {
		t.Errorf("redis said %q", line)
	}

	// Bucket: object round trip with the managed credentials.
	bucketRes := getJSON(t, s, token, "bucket", bucket)
	var creds struct{ AccessKey, SecretKey string }
	_ = json.Unmarshal([]byte(strings.TrimSpace(mustCLI(t, s, token, "", "secret", "get", "objectstore-credentials", "--reveal"))), &creds)
	mc, err := minio.New(strings.TrimPrefix(fmt.Sprint(bucketRes.Outputs["endpoint"]), "http://"), &minio.Options{Creds: credentials.NewStaticV4(creds.AccessKey, creds.SecretKey, "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mc.PutObject(ctx, bucket, "hello.txt", strings.NewReader("hi"), 2, minio.PutObjectOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}

	// App: serves the secret's value, proving secret injection end to end.
	appRes := getJSON(t, s, token, "app", app)
	body := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", outInt(appRes, "port")))
	if strings.TrimSpace(body) != "injected-"+id {
		t.Errorf("app served %q, want the secret value", body)
	}
	if out := mustCLI(t, s, token, "", "logs", "app", app, "--tail", "50"); out == "" {
		t.Log("app wrote no logs (busybox httpd is quiet) — fine")
	}

	// Job: a one-off container with the app's image and secret environment, run now on real Docker (ADR-017).
	job := "e2e-job-" + id
	mustCLI(t, s, token, "", "job", "create", job, "--schedule", "@daily", "--app", app, "--run-timeout", "2m", "--wait", "--timeout", "2m",
		"--", "sh", "-c", `echo "job sees $PAYLOAD"`)
	if out := mustCLI(t, s, token, "", "job", "run", job, "--wait", "--timeout", "3m"); !strings.Contains(out, "job sees injected-"+id) {
		t.Errorf("job output %q, want the app's secret", out)
	}

	// Backup: real rows → pg_dump streamed into the object store → restored into a new database (ADR-018).
	conn, err = pgx.Connect(ctx, fmt.Sprintf("postgres://%v:%s@127.0.0.1:%d/%v?sslmode=disable", dbRes.Outputs["username"], pw, outInt(dbRes, "port"), dbRes.Outputs["database"]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE orders (id int PRIMARY KEY, note text); INSERT INTO orders SELECT g, 'order ' || g FROM generate_series(1, 500) g`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	bak := "e2e-bak-" + id
	mustCLI(t, s, token, "", "backup", "create", bak, "--source", "database/"+db, "--bucket", bucket, "--keep", "2", "--wait", "--timeout", "2m")
	mustCLI(t, s, token, "", "backup", "run", bak, "--wait", "--timeout", "5m")
	var runs api.RunList
	if err := json.Unmarshal([]byte(mustCLI(t, s, token, "", "backup", "runs", bak, "-o", "json")), &runs); err != nil || len(runs.Items) != 1 || runs.Items[0].Artifact == nil {
		t.Fatalf("backup runs: %+v %v", runs, err)
	}
	art := runs.Items[0].Artifact
	obj, err := mc.StatObject(ctx, bucket, art.Key, minio.StatObjectOptions{})
	if err != nil || obj.Size != art.SizeBytes {
		t.Fatalf("stored object %s: %+v %v (run says %d bytes)", art.Key, obj, err, art.SizeBytes)
	}
	mustCLI(t, s, token, "", "backup", "restore", bak, "--run", runs.Items[0].ID, "--new", db+"-copy", "--wait", "--timeout", "5m")
	copyRes := getJSON(t, s, token, "database", db+"-copy")
	copyPw := strings.TrimSpace(mustCLI(t, s, token, "", "secret", "get", db+"-copy-password", "--reveal"))
	conn, err = pgx.Connect(ctx, fmt.Sprintf("postgres://%v:%s@127.0.0.1:%d/%v?sslmode=disable", copyRes.Outputs["username"], copyPw, outInt(copyRes, "port"), copyRes.Outputs["database"]))
	if err != nil {
		t.Fatalf("connect to the restored database: %v", err)
	}
	var rows int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&rows); err != nil || rows != 500 {
		t.Errorf("restored database has %d rows (%v), want 500", rows, err)
	}
	conn.Close(ctx)

	// Rotating the secret and redeploying serves the new value.
	mustCLI(t, s, token, "rotated-"+id+"\n", "secret", "update", sec, "--from-stdin", "--wait")
	mustCLI(t, s, token, "", "app", "redeploy", app, "--wait", "--timeout", "5m")
	appRes = getJSON(t, s, token, "app", app)
	if got := strings.TrimSpace(httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", outInt(appRes, "port")))); got != "rotated-"+id {
		t.Errorf("after rotation app served %q", got)
	}

	// Drift: remove the cache container behind EnclaveStack's back; it comes back.
	if out, err := exec.Command("docker", "rm", "-f", "es-cache-"+cache).CombinedOutput(); err != nil {
		t.Fatalf("docker rm: %v %s", err, out)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		evs := mustCLI(t, s, token, "", "events", "cache", cache)
		r := getJSON(t, s, token, "cache", cache)
		if strings.Contains(evs, "drift_detected") && r.Status.Phase == api.PhaseReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drift not repaired:\n%s", evs)
		}
		time.Sleep(time.Second)
	}
	if out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", "es-cache-"+cache).Output(); strings.TrimSpace(string(out)) != "true" {
		t.Error("cache container not running after drift repair")
	}
	if strings.Contains(s.logs.String(), "injected-"+id) || strings.Contains(s.logs.String(), pw) {
		t.Error("secret material appears in the server log")
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	var lastErr error
	for i := 0; i < 20; i++ {
		resp, err := http.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return string(b)
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, lastErr)
	return ""
}

// TestRecoveryAfterSIGKILL kills the server mid-provisioning and checks a fresh start finishes the job.
func TestRecoveryAfterSIGKILL(t *testing.T) {
	requireTools(t, "docker")
	dataDir, port := t.TempDir(), freePort(t)
	s := startServer(t, dataDir, port)
	token, c := setupAndToken(t, s)
	name := "e2e-recover-" + suffix()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", "es-database-"+name).Run()
	})
	_ = exec.Command("docker", "rmi", "postgres:17-alpine").Run() // force a slow pull so the kill lands mid-flight
	mustCLI(t, s, token, "", "db", "create", name, "--version", "17")
	ctx := context.Background()
	r, err := c.FindResource(ctx, api.KindDatabase, name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	s.stop(syscall.SIGKILL)

	s = startServer(t, dataDir, port)
	if s.code != "" {
		t.Error("restart reopened setup")
	}
	c = client.New(s.url, token)
	wctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	ready, err := c.WaitReady(wctx, r.ID, time.Second, nil)
	if err != nil {
		t.Fatalf("not recovered: %v %+v\n%s", err, ready.Status, tail(s.logs.String(), 30))
	}
	mustCLI(t, s, token, "", "delete", "database", name, "--yes", "--wait", "--timeout", "3m")
}

// TestOpenTofuAppliesAndDestroysRealInfrastructure runs the real provider binary through tofu.
func TestOpenTofuAppliesAndDestroysRealInfrastructure(t *testing.T) {
	requireTools(t, "docker", "tofu")
	s := startServer(t, t.TempDir(), freePort(t))
	token, c := setupAndToken(t, s)
	bin := build(t)
	work := t.TempDir()
	rc := filepath.Join(work, "dev.tofurc")
	_ = os.WriteFile(rc, []byte(fmt.Sprintf(`provider_installation {
  dev_overrides { "enclavestack/enclave" = %q }
  direct {}
}
`, bin)), 0o600)
	id := suffix()
	main := fmt.Sprintf(`
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}
provider "enclave" {}

resource "enclave_secret" "greeting" {
  name  = "tf-greeting-%[1]s"
  value = "hello-from-tofu-%[1]s"
}

resource "enclave_cache" "c" {
  name           = "tf-cache-%[1]s"
  max_memory_mib = 64
}

resource "enclave_app" "web" {
  name       = "tf-web-%[1]s"
  image      = "busybox:1.36"
  port       = 8080
  publish    = true
  memory_mib = 64
  command    = ["sh", "-c", "mkdir -p /www && echo \"$GREETING\" > /www/index.html && exec httpd -f -p 8080 -h /www"]
  secret_env = { GREETING = enclave_secret.greeting.name }
  env        = { CACHE_HOST = enclave_cache.c.internal_host }
}

output "url" { value = "http://${enclave_app.web.host}:${enclave_app.web.host_port}/" }
`, id)
	_ = os.WriteFile(filepath.Join(work, "main.tf"), []byte(main), 0o600)
	tofu := func(args ...string) string {
		cmd := exec.Command("tofu", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+rc, "ENCLAVE_SERVER="+s.url, "ENCLAVE_TOKEN="+token, "TF_IN_AUTOMATION=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tofu %s: %v\n%s\nserver log:\n%s", strings.Join(args, " "), err, out, tail(s.logs.String(), 30))
		}
		return string(out)
	}
	t.Cleanup(func() {
		cmd := exec.Command("tofu", "destroy", "-auto-approve", "-no-color")
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+rc, "ENCLAVE_SERVER="+s.url, "ENCLAVE_TOKEN="+token)
		_ = cmd.Run()
	})
	plan := tofu("plan", "-no-color")
	if !strings.Contains(plan, "3 to add") {
		t.Errorf("plan:\n%s", plan)
	}
	tofu("apply", "-auto-approve", "-no-color")
	url := strings.Trim(strings.TrimSpace(tofu("output", "-raw", "url")), `"`)
	if got := strings.TrimSpace(httpGet(t, url)); got != "hello-from-tofu-"+id {
		t.Errorf("app at %s served %q", url, got)
	}
	if again := tofu("plan", "-no-color", "-detailed-exitcode"); !strings.Contains(again, "No changes") {
		t.Errorf("second plan is not empty:\n%s", again)
	}
	// The console/CLI see what OpenTofu made (one API, ADR-003).
	if _, err := c.FindResource(context.Background(), api.KindApp, "tf-web-"+id); err != nil {
		t.Errorf("app made by OpenTofu not visible through the API: %v", err)
	}
	out := tofu("destroy", "-auto-approve", "-no-color")
	if !strings.Contains(out, "3 destroyed") {
		t.Errorf("destroy:\n%s", out)
	}
	if _, err := c.FindResource(context.Background(), api.KindApp, "tf-web-"+id); !client.IsNotFound(err) {
		t.Errorf("app survived destroy: %v", err)
	}
}

// An app built from a private Git repository on real Docker with BuildKit (ADR-019, L5-001 builds).
func TestAppBuiltFromGitServesItsCommit(t *testing.T) {
	requireTools(t, "docker", "git")
	s := startServer(t, t.TempDir(), freePort(t))
	token, _ := setupAndToken(t, s)
	id := suffix()
	app := "e2e-site-" + id
	gs := gittest.New(t, "e2e-git-token")
	work, repoURL := gs.Repo("site")
	dockerfile := "FROM busybox:1.37\nARG MODE=dev\nCOPY index.html /www/index.html\nRUN echo \"built for $MODE\"\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n"
	gs.Push(work, map[string]string{"Dockerfile": dockerfile, "index.html": "version one\n"}, "one")
	t.Cleanup(func() {
		cli(t, s, token, "", "delete", "app", app, "--yes", "--wait", "--timeout", "3m")
		cli(t, s, token, "", "delete", "secret", "e2e-git-"+id, "--yes", "--wait")
	})
	mustCLI(t, s, token, `{"token":"e2e-git-token"}`+"\n", "secret", "create", "e2e-git-"+id, "--from-stdin", "--wait")
	mustCLI(t, s, token, "", "app", "create", app, "--git", repoURL, "--ref", "main", "--git-credentials", "e2e-git-"+id, "--build-arg", "MODE=prod",
		"--port", "8080", "--publish", "--memory", "64", "--wait", "--timeout", "8m")
	serve := func() string {
		r := getJSON(t, s, token, "app", app)
		return strings.TrimSpace(httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", outInt(r, "port"))))
	}
	if got := serve(); got != "version one" {
		t.Fatalf("served %q", got)
	}
	if out := mustCLI(t, s, token, "", "logs", "app", app, "--build"); !strings.Contains(out, "built for prod") || !strings.Contains(out, "COPY index.html") {
		t.Errorf("build log:\n%s", out)
	}

	second := gs.Push(work, map[string]string{"index.html": "version two\n"}, "two")
	mustCLI(t, s, token, "", "app", "redeploy", app, "--wait", "--timeout", "8m")
	if got := serve(); got != "version two" {
		t.Errorf("after redeploy served %q", got)
	}
	if r := getJSON(t, s, token, "app", app); r.Outputs["builtCommit"] != second {
		t.Errorf("built %v, want %s", r.Outputs["builtCommit"], second)
	}

	gs.Push(work, map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo broken && exit 3\n"}, "broken")
	if _, errOut, code := cli(t, s, token, "", "app", "redeploy", app, "--wait", "--timeout", "8m"); code == 0 || !strings.Contains(errOut, "exit code: 3") {
		t.Errorf("broken build: %d %s", code, errOut)
	}
	if got := serve(); got != "version two" {
		t.Errorf("a failed build stopped the serving revision: %q", got)
	}
	out, _ := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}", "enclave.local/"+app).Output()
	if n := len(strings.Fields(string(out))); n == 0 || n > 2 {
		t.Errorf("built images kept: %s", out)
	}
	mustCLI(t, s, token, "", "delete", "app", app, "--yes", "--wait", "--timeout", "3m")
	if out, _ := exec.Command("docker", "images", "-q", "enclave.local/"+app).Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("images left after deleting the app: %s", out)
	}
	if strings.Contains(s.logs.String(), "e2e-git-token") {
		t.Error("the Git token appears in the server log")
	}
}
