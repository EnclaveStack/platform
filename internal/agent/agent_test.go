package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/pkg/api"
)

// pair is an agent served over real mutual TLS with fake engines behind it, and a Remote reaching it.
type pair struct {
	ca     *CA
	drv    *fake.Driver
	hv     *fake.Hypervisor
	remote *Remote
	srv    *httptest.Server
	host   string
	port   int
	serial string
	cert   tls.Certificate
}

func newCA(t *testing.T) *CA {
	t.Helper()
	certPEM, keyPEM, err := NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ParseCA(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func newPair(t *testing.T, machineID string) *pair {
	t.Helper()
	ca := newCA(t)
	keyPEM, csrPEM, err := NewKeyAndRequest()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, serial, _, err := ca.SignAgent(csrPEM, machineID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stcfg, err := ServerConfig(certPEM, keyPEM, ca.PEM)
	if err != nil {
		t.Fatal(err)
	}
	drv, hv := fake.New(0), fake.NewHypervisor()
	s := &Server{Driver: drv, Hypervisor: hv, MachineID: machineID, FilesDir: t.TempDir(), Log: slog.New(slog.DiscardHandler),
		TunnelPublicKey: func(context.Context) string { return "pub-" + machineID }}
	srv := httptest.NewUnstartedServer(s.Handler())
	srv.TLS = stcfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	host, portS, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portS)
	cp, err := ca.ControlPlaneCertificate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ccfg, err := ClientConfig(cp, ca.PEM, machineID, serial)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRemote(machineID, host, port, ccfg)
	t.Cleanup(r.Close)
	return &pair{ca: ca, drv: drv, hv: hv, remote: r, srv: srv, host: host, port: port, serial: serial, cert: cp}
}

type rec struct {
	mu     sync.Mutex
	steps  []string
	events []string
}

func (r *rec) Step(name string, state api.StepState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, name+":"+string(state))
}
func (r *rec) Note(string) {}
func (r *rec) Event(typ, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, typ)
}

func appInput(name string) engine.EnsureInput {
	r := &domain.Resource{ID: domain.NewResourceID(api.KindApp), Project: api.DefaultProject, Kind: api.KindApp, Name: name, Generation: 1,
		SecretCiphertext: []byte("must not cross")}
	return engine.EnsureInput{Resource: r, Spec: api.AppSpec{Image: "nginx:1.27", Port: 80, MemoryMiB: 256, Replicas: 1, Strategy: api.StrategyRolling},
		Env: map[string]string{"K": "secret value"}, Revision: 1}
}

func TestEnsureCrossesWithStepsEventsAndTheStartedHook(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	in := appInput("web")
	hooked := 0
	in.Started = func(context.Context) error { hooked++; return nil }
	r := &rec{}
	out, err := p.remote.Ensure(ctx, in, r)
	if err != nil {
		t.Fatal(err)
	}
	if hooked != 1 {
		t.Errorf("the Started hook ran %d times", hooked)
	}
	if out["internalHost"] != "es-app-web" {
		t.Errorf("outputs: %v", out)
	}
	if len(r.steps) == 0 || len(r.events) == 0 || !strings.Contains(strings.Join(r.events, ","), "traffic_switched") {
		t.Errorf("steps %v events %v", r.steps, r.events)
	}
	if h, err := p.remote.Health(ctx, in.Resource, engine.ObjectStore{}); err != nil || h != engine.Healthy {
		t.Errorf("health %v %v", h, err)
	}
	if err := p.remote.Delete(ctx, in.Resource, engine.ObjectStore{}); err != nil {
		t.Fatal(err)
	}
	if h, _ := p.remote.Health(ctx, in.Resource, engine.ObjectStore{}); h != engine.Missing {
		t.Errorf("after delete: %v", h)
	}
}

// A hook that fails fails the deploy, as on one machine.
func TestAStartedHookErrorReachesTheAgent(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	in := appInput("web")
	in.Started = func(context.Context) error { return errors.New("firewall refused") }
	if _, err := p.remote.Ensure(ctx, in, &rec{}); err == nil || !strings.Contains(err.Error(), "firewall refused") {
		t.Errorf("got %v", err)
	}
}

func TestErrorsKeepTheirClassAcrossTheWire(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	p.drv.SetFault("broken", fake.Fault{Permanent: true, Message: "no such image"})
	_, err := p.remote.Ensure(ctx, appInput("broken"), &rec{})
	if !engine.IsPermanent(err) || !strings.Contains(err.Error(), "no such image") {
		t.Errorf("permanent: %v", err)
	}
	p.drv.SetFault("flaky", fake.Fault{TransientFailures: 1})
	if _, err := p.remote.Ensure(ctx, appInput("flaky"), &rec{}); err == nil || engine.IsPermanent(err) {
		t.Errorf("transient: %v", err)
	}
	p.drv.SetAvailable(false)
	if err := p.remote.Ping(ctx); !errors.Is(err, engine.ErrUnavailable) {
		t.Errorf("unavailable: %v", err)
	}
	p.drv.SetAvailable(true)
	if _, err := p.remote.WaitJob(ctx, "run_none", time.Second); !errors.Is(err, engine.ErrJobMissing) {
		t.Errorf("job missing: %v", err)
	}
	p.hv.SetSupport(false, "QEMU isn't installed")
	_, err = p.remote.EnsureVM(ctx, engine.VMInput{ID: "vm_1", Name: "box", Spec: api.VMSpec{Image: "debian-12", CPUs: 1, MemoryMiB: 512}}, &rec{})
	if !errors.Is(err, engine.ErrNoVMs) {
		t.Errorf("no VMs: %v", err)
	}
	p.srv.Close()
	if err := p.remote.Ping(ctx); !errors.Is(err, engine.ErrMachineOffline) {
		t.Errorf("a stopped agent: %v", err)
	}
}

func TestStreamsCrossBothWays(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	db := &domain.Resource{ID: domain.NewResourceID(api.KindDatabase), Project: api.DefaultProject, Kind: api.KindDatabase, Name: "orders", Generation: 1}
	in := engine.EnsureInput{Resource: db, Spec: api.DatabaseSpec{Engine: "postgres", Version: "16"}, Password: "pw"}
	if _, err := p.remote.Ensure(ctx, in, &rec{}); err != nil {
		t.Fatal(err)
	}
	p.drv.SetData(db.ID, "rows: 3")
	var dump bytes.Buffer
	info, err := p.remote.Dump(ctx, in, &dump)
	if err != nil || info.Ext == "" || !strings.Contains(dump.String(), "rows: 3") {
		t.Fatalf("dump %+v %q %v", info, dump.String(), err)
	}
	p.drv.SetData(db.ID, "")
	if err := p.remote.Restore(ctx, in, bytes.NewReader(dump.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got := p.drv.Data(db.ID); got != "rows: 3" {
		t.Errorf("restored %q", got)
	}
	if err := p.remote.Restore(ctx, in, strings.NewReader("garbage")); !engine.IsPermanent(err) {
		t.Errorf("a bad dump: %v", err)
	}
	p.drv.SetFault("orders", fake.Fault{DumpFails: true})
	if _, err := p.remote.Dump(ctx, in, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("a failing dump: %v", err)
	}
}

func TestBuildSendsTheContextAndStreamsTheLog(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.20\nCOPY app /app\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "app", "main.txt"), []byte("hello"), 0o644)
	app := &domain.Resource{ID: "app_x", Kind: api.KindApp, Name: "web", Project: api.DefaultProject}
	var log bytes.Buffer
	res, err := p.remote.Build(ctx, engine.BuildInput{Resource: app, Tag: "enclave.local/web:1", ContextDir: dir, Dockerfile: "Dockerfile"}, &log)
	if err != nil || res.ImageID == "" || log.Len() == 0 {
		t.Fatalf("build %+v %q %v", res, log.String(), err)
	}
	if _, err := p.remote.Build(ctx, engine.BuildInput{Resource: app, Tag: "t", ContextDir: dir, Dockerfile: "Missing"}, &log); !engine.IsPermanent(err) {
		t.Errorf("a missing Dockerfile: %v", err)
	}
}

func TestDisksAndSnapshotsCrossAsFiles(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	if err := p.remote.EnsureDisk(ctx, engine.DiskRef{DiskID: "vol_a"}, 2); err != nil {
		t.Fatal(err)
	}
	p.hv.WriteDisk("vol_a", "disk bytes")
	dest := filepath.Join(t.TempDir(), "snap.qcow2")
	if _, err := p.remote.SnapshotDisk(ctx, engine.DiskRef{DiskID: "vol_a"}, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); len(b) == 0 {
		t.Fatal("empty snapshot copy on the control plane")
	}
	if err := p.remote.CopyDisk(ctx, dest, "vol_b", 2); err != nil {
		t.Fatal(err)
	}
	if _, contents, ok := p.hv.Disk("vol_b"); !ok || contents != "disk bytes" {
		t.Errorf("copied disk %q %v", contents, ok)
	}
	// A second upload of the same file is skipped (HEAD), and a body with the wrong digest is refused.
	if _, err := p.remote.Upload(ctx, dest); err != nil {
		t.Fatal(err)
	}
	req, _ := p.remote.request(ctx, http.MethodPut, pathFiles+strings.Repeat("a", 64), strings.NewReader("x"))
	resp, err := p.remote.do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		t.Error("an upload with the wrong digest was kept")
	}
}

func TestInfoDescribesTheMachine(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	info, err := p.remote.AgentInfo(ctx)
	if err != nil || info.MachineID != "mch_one" || info.Protocol != Protocol || !info.Engine.Available || info.TunnelPublicKey != "pub-mch_one" {
		t.Errorf("info %+v %v", info, err)
	}
}

// Only the control plane's certificate opens an agent, and the control plane only accepts the certificate
// recorded for the machine (ADR-043).
func TestMutualTLSRefusesStrangers(t *testing.T) {
	p, ctx := newPair(t, "mch_one"), context.Background()
	call := func(cfg *tls.Config) error {
		r := NewRemote("mch_one", p.host, p.port, cfg)
		defer r.Close()
		_, err := r.AgentInfo(ctx)
		return err
	}
	// No client certificate.
	noCert, _ := ClientConfig(tls.Certificate{}, p.ca.PEM, "mch_one", p.serial)
	noCert.Certificates = nil
	if err := call(noCert); err == nil {
		t.Error("a caller without a certificate was served")
	}
	// A client certificate from another CA.
	other := newCA(t)
	otherCert, _ := other.ControlPlaneCertificate(time.Now())
	cfg, _ := ClientConfig(otherCert, p.ca.PEM, "mch_one", p.serial)
	if err := call(cfg); err == nil {
		t.Error("a caller from another CA was served")
	}
	// The right CA, but not the control plane: an agent's certificate used as a client.
	keyPEM, csrPEM, _ := NewKeyAndRequest()
	agentCert, _, _, _ := p.ca.SignAgent(csrPEM, "mch_two", time.Now())
	pair, err := tls.X509KeyPair(agentCert, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = ClientConfig(pair, p.ca.PEM, "mch_one", p.serial)
	if err := call(cfg); err == nil {
		t.Error("an agent's certificate opened another agent")
	}
	// The control plane expecting another machine, or another serial, refuses the agent.
	cfg, _ = ClientConfig(p.cert, p.ca.PEM, "mch_two", "")
	if err := call(cfg); err == nil {
		t.Error("the agent of mch_one was accepted as mch_two")
	}
	cfg, _ = ClientConfig(p.cert, p.ca.PEM, "mch_one", "abc")
	if err := call(cfg); err == nil {
		t.Error("a replaced certificate was accepted")
	}
	cfg, _ = ClientConfig(p.cert, p.ca.PEM, "mch_one", p.serial)
	if err := call(cfg); err != nil {
		t.Errorf("the real control plane was refused: %v", err)
	}
}

func TestDeadlinesReachTheAgent(t *testing.T) {
	p := newPair(t, "mch_one")
	p.drv.StepDelay = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.remote.Ensure(ctx, engine.EnsureInput{Resource: &domain.Resource{ID: "db_x", Kind: api.KindDatabase, Name: "slow", Generation: 1},
		Spec: api.DatabaseSpec{Engine: "postgres", Version: "16"}}, &rec{})
	if err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Errorf("a call past its deadline: %v after %s", err, time.Since(start))
	}
}

func TestSignAgentRefusesBadRequests(t *testing.T) {
	ca := newCA(t)
	if _, _, _, err := ca.SignAgent([]byte("not pem"), "mch_1", time.Now()); err == nil {
		t.Error("garbage signed")
	}
	_, csr, _ := NewKeyAndRequest()
	tampered := bytes.Replace(csr, []byte("A"), []byte("B"), 1)
	if _, _, _, err := ca.SignAgent(tampered, "mch_1", time.Now()); err == nil {
		t.Error("a tampered request signed")
	}
	cert, serial, notAfter, err := ca.SignAgent(csr, "mch_1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, s, _ := CertificateExpiry(cert)
	if s != serial || !got.Equal(notAfter.Truncate(time.Second)) && got.Sub(notAfter) > time.Second {
		t.Errorf("expiry %v serial %s", got, s)
	}
}
