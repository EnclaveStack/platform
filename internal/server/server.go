// Package server assembles the control plane — store, crypto, engine driver, services, reconciler and
// HTTP API — and runs it with graceful shutdown (ADR-012). Tests use Start to run it in-process.
package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/build"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/acme"
	"github.com/enclavestack/platform/internal/auth"
	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/dns/providers"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/engine/cluster"
	"github.com/enclavestack/platform/internal/engine/docker"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/engine/qemu"
	"github.com/enclavestack/platform/internal/httpapi"
	"github.com/enclavestack/platform/internal/machines"
	"github.com/enclavestack/platform/internal/mail"
	memorymail "github.com/enclavestack/platform/internal/mail/memory"
	"github.com/enclavestack/platform/internal/messaging"
	memorybroker "github.com/enclavestack/platform/internal/messaging/memory"
	"github.com/enclavestack/platform/internal/messaging/natsbroker"
	"github.com/enclavestack/platform/internal/monitor"
	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/reconciler"
	"github.com/enclavestack/platform/internal/secretbox"
	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/internal/webui"
	"github.com/enclavestack/platform/pkg/api"
)

// Config is everything `enclave server` can be told.
type Config struct {
	Listen         string        // host:port; default 127.0.0.1:7070
	DataDir        string        // default ~/.enclave
	Driver         string        // "docker" (default) or "fake"
	FakeStepDelay  time.Duration // fake driver only
	FakeFaults     string        // fake driver only, see fake.ParseFaults
	PublishAddress string        // where published ports bind; default 127.0.0.1
	EdgeAddress    string        // where the edge proxy binds 80/443; default 127.0.0.1 (ADR-015)
	QEMU           string        // qemu-system binary for VMs; default found on PATH (ADR-025)
	EdgeHTTPPort   int           // default 80
	EdgeHTTPSPort  int           // default 443
	ConsoleDir     string        // console assets on disk when not embedded
	LogLevel       string
	LogText        bool
	RateLimit      float64
	SecureCookies  bool
	PollInterval   time.Duration // reconciler poll; 0 = default
	DriftEvery     time.Duration // drift check; 0 = default
	CertWait       time.Duration // one wait for a domain certificate; 0 = default
	SchedulerTick  time.Duration // how often due job runs are claimed; 0 = default
	RecordCheck    time.Duration // how often DNS records are compared with their provider; 0 = default
	TranscodeWatch time.Duration // how often transcoders list their input folders; 0 = default
	ShutdownGrace  time.Duration // 0 = 15 s
	MetricInterval time.Duration // how often metrics are sampled and alerts evaluated; 0 = 30 s (ADR-037)
	TelegramAPI    string        // Telegram Bot API base URL for alert channels; "" = https://api.telegram.org
	ConsoleURL     string        // the address people open the console at, for links in alerts and invites; "" = none
	ManualMonitor  bool          // tests: don't run the monitor's loops; they call it directly
	// v0.8 (ADR-046): how often machines are checked and how long one may stay silent; 0 = 10 s and 30 s.
	MachineCheck   time.Duration
	MachineOffline time.Duration
	// TunnelAddress is where other machines reach this one's tunnel; TunnelPort its UDP port (ADR-045).
	TunnelAddress string
	TunnelPort    int
	// MeshEvery is how often the mesh is synced; 0 = 5 s. RescheduleAfter is how long a machine is offline
	// before its apps start elsewhere; 0 = 60 s (ADR-046).
	MeshEvery       time.Duration
	RescheduleAfter time.Duration
}

// Running is a started control plane.
type Running struct {
	URL       string
	SetupCode string
	Fake      *fake.Driver // set when Driver is "fake"
	// FakeHypervisor runs VMs with the fake driver (tests crash and power them off).
	FakeHypervisor *fake.Hypervisor
	// Backups is the in-memory backup storage used with the fake driver (tests inspect it).
	Backups *backup.MemoryOpener
	// Git is the in-memory Git source used with the fake driver (tests push commits to it).
	Git *build.Memory
	// DNS is the in-memory DNS provider used with the fake driver (tests read and change records).
	DNS *dns.Memory
	// ACME is the local DNS-01 issuer used with the fake driver.
	ACME *acme.Local
	// Broker and Mail are the in-memory broker and email services used with the fake driver.
	Broker *memorybroker.Broker
	Mail   *memorymail.Client
	// Monitor samples metrics, evaluates alerts, sends notifications and ships logs (tests drive it).
	Monitor *monitor.Monitor
	// Machines knows every machine and checks their agents (ADR-042 – ADR-046).
	Machines *machines.Manager
	Store    *store.Store
	Log      *slog.Logger

	cancel   context.CancelFunc
	done     chan struct{}
	err      error
	stopOnce sync.Once
}

// DefaultDataDir is ~/.enclave.
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".enclave"
	}
	return filepath.Join(home, ".enclave")
}

// Start opens everything, binds the listener and serves in the background. logOut receives logs.
func Start(ctx context.Context, cfg Config, logOut io.Writer) (*Running, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:7070"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if cfg.Driver == "" {
		cfg.Driver = "docker"
	}
	if cfg.ShutdownGrace == 0 {
		cfg.ShutdownGrace = 15 * time.Second
	}
	cfg.ConsoleURL = strings.TrimRight(strings.TrimSpace(cfg.ConsoleURL), "/")
	if cfg.ConsoleURL != "" && !api.ValidHTTPURL(cfg.ConsoleURL) {
		return nil, fmt.Errorf("--console-url must be an http:// or https:// address, like https://enclave.example.com")
	}
	log := observability.NewLogger(logOut, cfg.LogLevel, cfg.LogText)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "enclave.db"))
	if err != nil {
		return nil, err
	}
	key, err := secretbox.LoadOrCreateKey(filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		st.Close()
		return nil, err
	}
	box, err := secretbox.New(key)
	if err != nil {
		st.Close()
		return nil, err
	}

	var drv engine.Driver
	var fk *fake.Driver
	var hv engine.Hypervisor
	var fakeHV *fake.Hypervisor
	var opener backup.Opener = backup.S3Opener{}
	var memBackups *backup.MemoryOpener
	var memGit *build.Memory
	var dnsOpener dns.Opener = providers.Open
	var memDNS *dns.Memory
	var issuer acme.Issuer = &acme.Lego{Directory: acme.LetsEncrypt}
	var localACME *acme.Local
	var openBroker messaging.Opener = natsbroker.Open
	var memBroker *memorybroker.Broker
	var mailClient mail.Client = mail.Mailpit{}
	var memMail *memorymail.Client
	switch cfg.Driver {
	case "fake":
		memBroker, memMail = memorybroker.New(), memorymail.New() // nor a broker or email services
		openBroker, mailClient = memBroker.Open, memMail
		localACME = &acme.Local{} // nor a certificate authority
		issuer = localACME
		memDNS = dns.NewMemory() // nor real DNS providers
		dnsOpener = memDNS.Open
		fk = fake.New(cfg.FakeStepDelay)
		memBackups = backup.NewMemoryOpener() // there's no object store to talk to
		opener = memBackups
		memGit = build.NewMemory() // nor a network to clone from
		faults, err := fake.ParseFaults(cfg.FakeFaults)
		if err != nil {
			st.Close()
			return nil, err
		}
		for name, f := range faults {
			fk.SetFault(name, f)
		}
		drv = fk
		fakeHV = fake.NewHypervisor()
		hv = fakeHV
	case "docker":
		d, err := docker.New(docker.Options{PublishAddress: cfg.PublishAddress, EdgeAddress: cfg.EdgeAddress, EdgeHTTPPort: cfg.EdgeHTTPPort, EdgeHTTPSPort: cfg.EdgeHTTPSPort, Log: log})
		if err != nil {
			st.Close()
			return nil, err
		}
		drv = d
		hv = qemu.New(qemu.Options{DataDir: cfg.DataDir, QEMU: cfg.QEMU, Log: log})
	default:
		st.Close()
		return nil, fmt.Errorf("unknown driver %q (use docker or fake)", cfg.Driver)
	}

	// Every engine call goes through the router to the machine it concerns (ADR-044); with one machine it
	// reaches drv and hv as before.
	mgr := &machines.Manager{Store: st, Box: box, Local: drv, LocalHV: hv, Now: time.Now, Log: log.With("component", "machines"),
		Interval: cfg.MachineCheck, OfflineAfter: cfg.MachineOffline, TunnelAddress: cfg.TunnelAddress, TunnelPort: cfg.TunnelPort}
	if err := mgr.Load(ctx); err != nil {
		st.Close()
		return nil, err
	}
	router := cluster.New(mgr)
	var rec *reconciler.Reconciler
	mgr.Changed = func(_, state string) {
		if rec == nil {
			return
		}
		rec.MeshChanged() // peers and stand-ins follow who is online (ADR-045)
		if state == api.MachineOnline {
			rec.RetryWaiting(context.Background())
		}
	}
	// New containers are announced to every machine before they take traffic (XNET-07).
	router.Hooks = cluster.Hooks{
		Started: func(ctx context.Context, _ *domain.Resource) error { return rec.SyncMesh(ctx) },
		Changed: func(*domain.Resource) { rec.MeshChanged() },
	}
	metrics := observability.NewMetrics()
	rec = reconciler.New(st, router, box, log.With("component", "reconciler"), metrics)
	rec.Machines, rec.MeshEvery, rec.RescheduleAfter = mgr, cfg.MeshEvery, cfg.RescheduleAfter
	if cfg.PollInterval > 0 {
		rec.PollInterval = cfg.PollInterval
	}
	if cfg.CertWait > 0 {
		rec.CertWait = cfg.CertWait
	}
	if cfg.SchedulerTick > 0 {
		rec.SchedulerTick = cfg.SchedulerTick
	}
	rec.Opener, rec.TempDir = opener, filepath.Join(cfg.DataDir, "tmp")
	rec.Git = build.Git{TempDir: rec.TempDir}
	rec.DNS, rec.ACME = dnsOpener, issuer
	rec.Hypervisor, rec.SnapshotDir = router, filepath.Join(cfg.DataDir, "snapshots")
	if err := os.MkdirAll(rec.SnapshotDir, 0o700); err != nil {
		st.Close()
		return nil, fmt.Errorf("create %s: %w", rec.SnapshotDir, err)
	}
	if cfg.RecordCheck > 0 {
		rec.RecordCheckEvery = cfg.RecordCheck
	}
	if cfg.TranscodeWatch > 0 {
		rec.TranscodeWatch = cfg.TranscodeWatch
	}
	if memGit != nil {
		rec.Git = memGit
	}
	if err := os.MkdirAll(rec.TempDir, 0o700); err != nil {
		st.Close()
		return nil, fmt.Errorf("create %s: %w", rec.TempDir, err)
	}
	if cfg.DriftEvery > 0 {
		rec.DriftEvery = cfg.DriftEvery
	}
	rec.Broker, rec.HTTP = messaging.NewPool(openBroker, rec.BrokerEndpoint), &http.Client{}
	authSvc := &service.Auth{Store: st, Throttle: auth.NewThrottle(5, 15*time.Minute, time.Now), Now: time.Now, Log: log}
	authSvc.ConsoleURL = func(context.Context) string { return cfg.ConsoleURL } // invite links (IAM-04)
	resSvc := &service.Resources{Store: st, Box: box, Engine: router, Hypervisor: router, SnapshotDir: rec.SnapshotDir, Queue: rec, Now: time.Now, Log: log,
		Messaging: rec.Broker, Mail: mailClient, Metrics: metrics}
	authSvc.SendMail = resSvc.SendSystemEmail // invite links (IAM-04)
	resSvc.MachineOnline = mgr.Online
	idem := &service.Idempotency{Store: st, Now: time.Now}
	var listenURL string // set once the listener is bound
	serverURL := func(context.Context) string { return service.ServerURL(cfg.ConsoleURL, listenURL) }
	machineSvc := &service.Machines{Store: st, Manager: mgr, Queue: rec, Now: time.Now, Log: log,
		Throttle: auth.NewThrottle(10, 15*time.Minute, time.Now), ServerURL: serverURL, Links: rec.MeshLinks}
	h := httpapi.New(authSvc, resSvc, idem, st.Ping, log, metrics, httpapi.Config{
		RateLimit: cfg.RateLimit, SecureCookies: cfg.SecureCookies, Assets: webui.FS(cfg.ConsoleDir), Machines: machineSvc})

	code, err := authSvc.PrepareSetupCode(ctx)
	if err != nil {
		st.Close()
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	listenURL = "http://" + ln.Addr().String()
	r := &Running{URL: listenURL, SetupCode: code, Fake: fk, FakeHypervisor: fakeHV, Backups: memBackups, Git: memGit, DNS: memDNS, ACME: localACME, Broker: memBroker, Mail: memMail, Store: st, Log: log, Machines: mgr, cancel: cancel, done: make(chan struct{})}
	mon := &monitor.Monitor{Store: st, Box: box, Driver: router, Hypervisor: router, Broker: rec.Broker, Mail: mailClient, HTTP: &http.Client{}, Opener: opener,
		Metrics: metrics, Now: time.Now, Log: log.With("component", "monitor"), DataDir: cfg.DataDir, Interval: cfg.MetricInterval, TelegramAPI: cfg.TelegramAPI,
		ConsoleURL: serverURL}
	r.Monitor = mon

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		mgr.Run(runCtx)
	}()
	go func() {
		defer wg.Done()
		rec.Run(runCtx)
	}()
	go func() {
		defer wg.Done()
		if !cfg.ManualMonitor {
			mon.Run(runCtx)
		}
	}()
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				authSvc.Sweep(runCtx)
			}
		}
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	go func() {
		defer close(r.done)
		var err error
		select {
		case <-runCtx.Done():
		case err = <-serveErr:
			cancel()
		}
		log.Info("shutting down")
		shutCtx, c := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer c()
		if e := srv.Shutdown(shutCtx); e != nil && err == nil {
			err = e
		}
		wg.Wait()
		_ = rec.Broker.Close()
		if e := st.Close(); e != nil && err == nil {
			err = e
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		r.err = err
		log.Info("stopped")
	}()
	log.Info("control plane started", "version", version.Version, "url", r.URL, "driver", drv.Name(), "data_dir", cfg.DataDir)
	return r, nil
}

// Stop shuts down gracefully and waits.
func (r *Running) Stop() error {
	r.stopOnce.Do(r.cancel)
	<-r.done
	return r.err
}

// Done is closed when the server has stopped.
func (r *Running) Done() <-chan struct{} { return r.done }

// Err is the reason the server stopped (nil for a clean shutdown).
func (r *Running) Err() error { return r.err }
