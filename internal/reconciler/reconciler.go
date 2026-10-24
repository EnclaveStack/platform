// Package reconciler converges actual state to desired state (ADR-004, ADR-012, L3-007): a scheduler
// that wakes on Enqueue and every PollInterval, a bounded worker pool with one in-flight operation per
// resource, exponential backoff with jitter, recovery after restart, and periodic drift repair.
package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/acme"
	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/build"
	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/internal/secretbox"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Policy constants (L3-007 §2–3).
const (
	MaxAttempts        = 8
	BaseBackoff        = 2 * time.Second
	MaxBackoff         = 5 * time.Minute
	EngineDownRetry    = 5 * time.Second
	NoVMsRetry         = 30 * time.Second
	OperationTimeout   = 30 * time.Minute // pull (10 min) + health budget (≤ 15 min) + switch
	DefaultPoll        = 2 * time.Second
	DefaultDrift       = 60 * time.Second
	DefaultWorkers     = 4
	claimLease         = 15 * time.Minute
	engineWaitMessage  = "Waiting for the container engine to come back."
	maxMessageLen      = 500
	queueBufferEntries = 1024
)

// Backoff returns the delay before attempt n (n ≥ 1): min(2s·2ⁿ⁻¹, 5m) × U(0.8, 1.2).
func Backoff(n int, jitter func() float64) time.Duration {
	if n < 1 {
		n = 1
	}
	d := BaseBackoff
	for i := 1; i < n && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	return time.Duration(float64(d) * (0.8 + 0.4*jitter()))
}

// Reconciler drives resources through the engine.
type Reconciler struct {
	Store   *store.Store
	Driver  engine.Driver
	Box     *secretbox.Box
	Log     *slog.Logger
	Metrics *observability.Metrics
	Now     func() time.Time
	Jitter  func() float64

	Workers      int
	PollInterval time.Duration
	DriftEvery   time.Duration
	// CertWait bounds one wait for a domain's certificate (DefaultCertWait).
	CertWait time.Duration
	// SchedulerTick is how often due job occurrences are claimed (DefaultSchedulerTick).
	SchedulerTick time.Duration
	// Opener reaches backup destinations (ADR-018); TempDir holds a restore's download while it is verified.
	Opener  backup.Opener
	TempDir string
	// Git reads source apps' repositories (ADR-019); nil disables builds.
	Git build.Source
	// DNS opens zones' providers (ADR-023); RecordCheckEvery is how often records are compared with
	// them (DefaultRecordCheck).
	DNS              dns.Opener
	RecordCheckEvery time.Duration
	// ACME issues DNS-01 certificates from ACMEDirectory (ADR-024); nil disables them.
	ACME          acme.Issuer
	ACMEDirectory string
	// Hypervisor runs VMs and owns disk volumes (ADR-025); nil leaves them pending. SnapshotDir holds
	// snapshot copies (ADR-028).
	Hypervisor  engine.Hypervisor
	SnapshotDir string
	// Broker reaches the message broker as the system user (ADR-029); HTTP delivers webhooks. DeliveryTick
	// is how often subscriptions are matched with deliverers, TranscodeWatch how often transcoders list
	// their input folders (DefaultDeliveryTick, DefaultTranscodeWatch).
	Broker         *messaging.Pool
	HTTP           *http.Client
	DeliveryTick   time.Duration
	TranscodeWatch time.Duration
	// NotifyBackoff is when a transcoder's webhook is tried (DefaultNotifyBackoff, L2-012 TRX-08).
	NotifyBackoff []time.Duration
	// Machines places resources on machines (ADR-044); nil keeps everything on the main machine.
	Machines Directory
	// MeshEvery is how often the mesh is synced (DefaultMeshEvery, ADR-045); RescheduleAfter how long a
	// machine is offline before its apps start elsewhere (DefaultRescheduleAfter, ADR-046).
	MeshEvery       time.Duration
	RescheduleAfter time.Duration
	// Public access (ADR-051 – ADR-053): CloudflareAPI is the base of Cloudflare's API ("" for the real one),
	// Gate how the edge reaches the gate listener. TunnelConnectWait bounds one wait for a connector's first
	// connection; TunnelCheckEvery and TunnelDriftEvery pace the tunnel loop (DefaultTunnel…).
	CloudflareAPI     string
	Gate              engine.GateConfig
	TunnelConnectWait time.Duration
	TunnelCheckEvery  time.Duration
	TunnelDriftEvery  time.Duration
	// TunnelRestartAfter is how long a connector may stay without a connection before it is restarted
	// (TunnelDownRestart when 0).
	TunnelRestartAfter time.Duration

	queue    chan string
	mu       sync.Mutex
	inflight map[string]bool
	wg       sync.WaitGroup
	edgeMu   sync.Mutex
	acmeMu   sync.Mutex // one DNS-01 order at a time
	runSlots chan struct{}
	// recordChecked is when each record was last compared with its provider.
	recordChecked map[string]time.Time
	// v0.6: the broker's configuration lock, bcrypt hashes by password digest, when each transcoder last
	// listed its input, and when this process started (runs started before it were interrupted).
	brokerMu  sync.Mutex
	hashMu    sync.Mutex
	hashes    map[string]string
	watchedAt map[string]time.Time
	startedAt time.Time
	// v0.8: the mesh sync's state, one pass at a time, and the addresses moves keep, by resource ID.
	mesh   meshState
	meshMu sync.Mutex
	keep   sync.Map
	// Public access: the tunnel loop's state, and the hash of the edge configuration last applied.
	tun      tunnelState
	edgeHash string
}

// New builds a reconciler with production defaults; tests override fields before Run.
func New(st *store.Store, d engine.Driver, box *secretbox.Box, log *slog.Logger, m *observability.Metrics) *Reconciler {
	return &Reconciler{Store: st, Driver: d, Box: box, Log: log, Metrics: m, Now: time.Now, Jitter: rand.Float64,
		Workers: DefaultWorkers, PollInterval: DefaultPoll, DriftEvery: DefaultDrift,
		queue: make(chan string, queueBufferEntries), inflight: map[string]bool{}, runSlots: make(chan struct{}, maxConcurrentRuns),
		recordChecked: map[string]time.Time{}, hashes: map[string]string{}, watchedAt: map[string]time.Time{}, startedAt: time.Now()}
}

// Enqueue asks for a resource to be looked at now. It never blocks; if the buffer is full the
// scheduler's poll picks the resource up instead.
func (r *Reconciler) Enqueue(id string) {
	select {
	case r.queue <- id:
	default:
	}
}

// Run blocks until ctx is cancelled and every worker has returned.
func (r *Reconciler) Run(ctx context.Context) {
	r.interruptTranscodes(ctx) // before the scheduler could adopt them
	work := make(chan string)
	for i := 0; i < r.Workers; i++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			for id := range work {
				r.process(ctx, id)
			}
		}()
	}
	r.wg.Add(5)
	go func() {
		defer r.wg.Done()
		r.driftLoop(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.tunnelLoop(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.meshLoop(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.deliveryLoop(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.schedulerLoop(ctx)
	}()

	dispatch := func(id string) {
		if !r.claim(id) {
			return
		}
		select {
		case work <- id:
		case <-ctx.Done():
			r.release(id)
		}
	}
	poll := time.NewTicker(r.PollInterval)
	defer poll.Stop()
	r.pollDue(ctx, dispatch) // recovery: resume everything in progress at start (CP-09)
	for {
		select {
		case <-ctx.Done():
			close(work)
			r.wg.Wait()
			return
		case id := <-r.queue:
			dispatch(id)
		case <-poll.C:
			r.pollDue(ctx, dispatch)
		}
	}
}

func (r *Reconciler) pollDue(ctx context.Context, dispatch func(string)) {
	due, err := r.Store.DueResources(ctx, r.Now(), 64)
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Error("list due resources failed", "error", err)
		}
		return
	}
	for _, res := range due {
		dispatch(res.ID)
	}
	r.updateGauges(ctx)
}

func (r *Reconciler) claim(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight[id] {
		return false
	}
	r.inflight[id] = true
	return true
}

func (r *Reconciler) release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inflight, id)
}

// process runs one reconcile pass for a resource. Panics become transient failures (ADR-012).
func (r *Reconciler) process(ctx context.Context, id string) {
	defer r.release(id)
	res, err := r.Store.GetResource(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			r.Log.Error("load resource failed", "resource_id", id, "error", err)
		}
		return
	}
	if !res.Phase.InProgress() && res.RequestedAction == "" {
		return
	}
	if res.NextAttemptAt != nil && res.NextAttemptAt.After(r.Now()) {
		return
	}
	log := r.Log.With("resource_id", res.ID, "kind", res.Kind, "generation", res.Generation, "attempt", res.Attempts+1)
	started := r.Now()
	opCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()

	var opErr error
	func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error("reconcile panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				opErr = fmt.Errorf("internal error while reconciling: %v", p)
			}
		}()
		switch {
		case res.Phase == api.PhaseDeleting:
			opErr = r.delete(opCtx, res, log)
		case res.RequestedAction == api.ActionRestart:
			opErr = r.restart(opCtx, res, log)
		case res.RequestedAction == api.ActionPurge:
			opErr = r.purge(opCtx, res)
		case res.RequestedAction == domain.ActionMove:
			opErr = r.ensureWithMove(opCtx, res, log)
		default:
			opErr = r.ensureWithMove(opCtx, res, log)
		}
	}()
	r.Metrics.ReconcileDuration.WithLabelValues(string(res.Kind)).Observe(r.Now().Sub(started).Seconds())
	if opErr != nil {
		if ctx.Err() != nil {
			log.Info("reconcile interrupted by shutdown; it resumes on next start")
			return
		}
		r.fail(context.WithoutCancel(ctx), res, opErr, log)
	}
}

// progress persists step changes while a driver works, so clients see them live.
type progress struct {
	r   *Reconciler
	ctx context.Context
	res *domain.Resource
	gen int64
}

func (p *progress) Step(name string, state api.StepState) {
	for i := range p.res.Steps {
		if p.res.Steps[i].Name == name {
			p.res.Steps[i].State = state
		}
	}
	p.res.UpdatedAt = p.r.Now()
	if _, err := p.r.Store.SaveStatus(p.ctx, p.res, p.gen); err != nil && p.ctx.Err() == nil {
		p.r.Log.Warn("save step failed", "resource_id", p.res.ID, "error", err)
	}
}

// Note shows a progress message, e.g. a health-check attempt (engine.Narrator).
func (p *progress) Note(message string) {
	p.res.Message, p.res.UpdatedAt = truncate(message), p.r.Now()
	if _, err := p.r.Store.SaveStatus(p.ctx, p.res, p.gen); err != nil && p.ctx.Err() == nil {
		p.r.Log.Warn("save progress message failed", "resource_id", p.res.ID, "error", err)
	}
}

// Event records a deploy event, e.g. a revision started or traffic switched (engine.Narrator).
func (p *progress) Event(typ, message string) {
	p.r.event(p.ctx, p.res, typ, truncate(message))
}

func (r *Reconciler) ensure(ctx context.Context, res *domain.Resource, log *slog.Logger) error {
	gen := res.Generation
	now := r.Now()
	recreate := res.RequestedAction == api.ActionRedeploy
	if res.Phase == api.PhasePending {
		if err := res.SetPhase(api.PhaseProvisioning, "", now); err != nil {
			return engine.Permanent(err)
		}
	}
	if res.Phase == api.PhaseProvisioning || res.Phase == api.PhaseUpdating {
		res.Steps = domain.NewSteps(domain.StepNames[res.Kind]...)
	}
	if err := r.place(ctx, res); err != nil {
		return err
	}
	res.Message, res.UpdatedAt = "", now
	if res.Kind == api.KindApp {
		res.Outputs = withOutput(res.Outputs, engine.OutputLastDeploy, engine.DeployInProgress)
	}
	if _, err := r.Store.SaveStatus(ctx, res, gen); err != nil {
		return err
	}
	log.Info("reconcile started", "phase", res.Phase)

	var outputs engine.Outputs
	if res.Kind == api.KindDomain {
		var err error
		outputs, err = r.ensureDomain(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindJob {
		var err error
		outputs, err = r.ensureJob(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindBackup {
		var err error
		outputs, err = r.ensureBackup(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindNetwork {
		var err error
		outputs, err = r.ensureNetwork(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindFirewall {
		var err error
		outputs, err = r.ensureFirewall(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindZone {
		var err error
		outputs, err = r.ensureZone(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindRecord {
		var err error
		outputs, err = r.ensureRecord(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindVolume {
		var err error
		outputs, err = r.ensureVolume(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindSnapshot {
		var err error
		outputs, err = r.ensureSnapshot(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindVM {
		var err error
		outputs, err = r.ensureVM(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen}, false)
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindQueue || res.Kind == api.KindTopic || res.Kind == api.KindCDN || res.Kind == api.KindTranscoder {
		ensure := map[api.Kind]func(context.Context, *domain.Resource, engine.Reporter) (engine.Outputs, error){
			api.KindQueue: r.ensureQueue, api.KindTopic: r.ensureTopic, api.KindCDN: r.ensureCDN, api.KindTranscoder: r.ensureTranscoder}[res.Kind]
		var err error
		outputs, err = ensure(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindStack {
		var err error
		outputs, err = r.ensureStack(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if res.Kind == api.KindTunnel {
		var err error
		outputs, err = r.ensureTunnel(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if isOperateKind(res.Kind) {
		var err error
		outputs, err = r.ensureOperate(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: gen})
		if err != nil {
			return err
		}
	} else if src := secretSource(res); res.Kind == api.KindSecret && src != nil {
		var err error
		if outputs, err = r.syncSecret(ctx, res, *src); err != nil {
			return err
		}
		for i := range res.Steps {
			res.Steps[i].State = api.StepDone
		}
	} else if res.Kind == api.KindSecret {
		for i := range res.Steps {
			res.Steps[i].State = api.StepDone
		}
		outputs = engine.Outputs{"version": res.Generation, "valueUpdatedAt": res.UpdatedAt.UTC().Format(time.RFC3339)}
	} else {
		in, err := r.inputs(ctx, res)
		if err != nil {
			return err
		}
		in.Recreate = recreate
		if res.Kind == api.KindApp {
			latest, _ := asInt64(res.Outputs[engine.OutputLatestRevision])
			in.Revision = max(latest, res.ServingRevision) + 1
		}
		pending := pendingRestoreOf(res.Outputs) // read before outputs are replaced below
		rep := &progress{r: r, ctx: ctx, res: res, gen: gen}
		var src *built
		prevImage, _ := res.Outputs[OutputBuiltImage].(string)
		if s, ok := in.Spec.(api.AppSpec); ok && s.Source != nil {
			// A new revision resolves the ref again; so does a Retry after a failed build (it must not fall
			// back to the old image). Drift repair reuses the serving image.
			fresh := recreate || res.ObservedGeneration != gen || res.Outputs[OutputLastBuild] == store.BuildFailed
			if src, err = r.sourceImage(ctx, res, s, fresh, in.Revision, rep); err != nil {
				return err // the serving revision keeps running: nothing was deployed
			}
			s.Image = src.image
			in.Spec = s
		}
		outputs, err = r.Driver.Ensure(ctx, in, rep)
		if err != nil {
			if res.Kind == api.KindApp && engine.IsPermanent(err) {
				// The attempted revision's number is spent even though it was discarded.
				res.Outputs = withOutput(res.Outputs, engine.OutputLatestRevision, in.Revision)
			}
			return err
		}
		if pending != nil && (res.Kind == api.KindDatabase || res.Kind == api.KindCache) {
			// On failure the marker stays in res.Outputs, so a retry restores again — except when a replace
			// fails permanently: those failures all come before any data changes, and retrying can't
			// succeed, so the marker goes and Retry brings the source back as it was.
			from, err := r.restoreInto(ctx, res, in, pending, rep)
			if err != nil {
				if pending.Replace && engine.IsPermanent(err) {
					res.Outputs = withoutOutput(res.Outputs, OutputPendingRestore)
					return engine.Permanent(fmt.Errorf("%w. Nothing was changed; use Retry to bring %s back as it was", err, res.Name))
				}
				if engine.IsPermanent(err) {
					return engine.Permanent(fmt.Errorf("%w. Delete %s and restore from another run", err, res.Name))
				}
				return err
			}
			outputs[OutputRestoredFrom] = from
		}
		if res.Kind == api.KindApp {
			r.afterSourceDeploy(ctx, res, outputs, src, prevImage, log)
		}
		if res.Kind == api.KindBucket {
			if err := r.syncProjectStore(ctx, res.ProjectName(), ""); err != nil {
				return err
			}
		}
		if s, ok := in.Spec.(api.LoadBalancerSpec); ok {
			if _, _, backends, err := r.loadBalancerConfig(ctx, res.Project, s); err == nil {
				outputs[OutputBackends] = backends
			}
		}
	}
	done := r.Now()
	prevLatest, _ := asInt64(res.Outputs[engine.OutputLatestRevision])
	res.Outputs = r.withMachine(ctx, res, outputs)
	if res.Kind == api.KindApp || res.Kind == api.KindJob {
		if spec, _, err := api.NormalizeSpec(res.Kind, res.Spec); err == nil {
			res.Outputs = withBucketAccess(res, spec)
		}
	}
	if res.Kind == api.KindApp {
		res.Outputs = withOutput(res.Outputs, engine.OutputLastDeploy, engine.DeploySucceeded)
		if n, ok := asInt64(outputs[engine.OutputServingRevision]); ok {
			res.ServingRevision = n
		}
		res.Outputs = withOutput(res.Outputs, engine.OutputLatestRevision, max(prevLatest, res.ServingRevision))
	}
	if err := res.SetPhase(api.PhaseReady, "", done); err != nil {
		return engine.Permanent(err)
	}
	res.ObservedGeneration, res.Attempts, res.NextAttemptAt, res.RequestedAction, res.UpdatedAt = gen, 0, nil, "", done
	if res.Kind == api.KindApp {
		// Before ready, so ready means its domains already route to the new port.
		r.syncEdgeForApp(ctx, res, log)
	}
	ok, err := r.Store.SaveStatus(ctx, res, gen)
	if err != nil {
		return err
	}
	if !ok {
		log.Info("spec changed while reconciling; going again")
		r.Enqueue(res.ID)
		return nil
	}
	if recreate {
		msg := "Image pulled again and a new revision deployed."
		if commit, ok := res.Outputs[OutputBuiltCommit].(string); ok { // a source app pulls nothing (ADR-019)
			msg = fmt.Sprintf("Built %s and deployed a new revision.", short(commit))
			if res.Outputs[OutputLastBuild] == store.BuildSkipped {
				msg = fmt.Sprintf("Nothing new since %s; a new revision of the same build deployed.", short(commit))
			}
		}
		r.event(ctx, res, "redeployed", msg)
	}
	r.event(ctx, res, "ready", "Ready.")
	r.refreshStack(ctx, res)
	if res.Kind == api.KindApp {
		r.syncLoadBalancersForApp(ctx, res, log)
	}
	if slices.Contains(firewallKinds, res.Kind) {
		r.afterContainersChanged(ctx, res, log)
	}
	r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "ready").Inc()
	log.Info("resource ready", "duration_ms", done.Sub(now).Milliseconds())
	return nil
}

// inputs resolves everything the driver needs, including secret material (never logged).
func (r *Reconciler) inputs(ctx context.Context, res *domain.Resource) (engine.EnsureInput, error) {
	spec, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return engine.EnsureInput{}, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	in := engine.EnsureInput{Resource: res, Spec: spec}
	network, subnet := placementOf(spec)
	if in.Placement, err = r.placement(ctx, res.Project, network, subnet); err != nil {
		return in, err
	}
	if a, ok := r.kept(res.ID); ok {
		in.Placement.Address = a // a move keeps its address (ADR-047)
	}
	if slices.Contains(firewallKinds, res.Kind) {
		if in.Firewall, err = r.policyFor(ctx, res); err != nil {
			return in, err
		}
		in.Started = func(ctx context.Context) error { return r.syncFirewallsNaming(ctx, res) }
	}
	switch s := spec.(type) {
	case api.AppSpec:
		if in.Mounts, err = r.resolveMounts(ctx, res.Project, s.Volumes); err != nil {
			return in, err
		}
		in.Env = map[string]string{}
		for k, v := range s.Env {
			in.Env[k] = v
		}
		for k, name := range s.SecretEnv {
			v, err := r.secretValue(ctx, res.Project, name)
			if err != nil {
				return in, err
			}
			in.Env[k] = v
		}
		access, err := r.bucketAccessEnv(ctx, res, s)
		if err != nil {
			return in, err
		}
		for k, v := range access { // after env and secretEnv, which may not use these names (BKA-04)
			in.Env[k] = v
		}
	case api.DatabaseSpec, api.SearchSpec:
		v, err := r.secretValue(ctx, res.Project, domain.PasswordSecretFor(res.Kind, res.Name))
		if err != nil {
			return in, err
		}
		in.Password = v
	case api.EmailSpec:
		v, err := r.secretValue(ctx, res.Project, domain.PasswordSecretFor(res.Kind, res.Name))
		if err != nil {
			return in, err
		}
		in.Password = v
		if s.Relay != nil {
			if in.Relay, err = r.relay(ctx, res.Project, *s.Relay); err != nil {
				return in, err
			}
		}
	case api.BucketSpec:
		os, err := r.objectStore(ctx)
		if err != nil {
			return in, err
		}
		in.ObjectStore = os
	case api.LoadBalancerSpec:
		cfg, placement, _, err := r.loadBalancerConfig(ctx, res.Project, s)
		if err != nil {
			return in, err
		}
		in.LoadBalancer, in.Placement = cfg, placement
	}
	return in, nil
}

func (r *Reconciler) secretValue(ctx context.Context, project, name string) (string, error) {
	sec, err := r.Store.GetResourceByName(ctx, project, api.KindSecret, name)
	if errors.Is(err, store.ErrNotFound) {
		return "", engine.Permanent(fmt.Errorf("the secret %s no longer exists", name))
	}
	if err != nil {
		return "", err
	}
	v, err := r.Box.Open(sec.SecretCiphertext, []byte(sec.ID))
	if err != nil {
		return "", engine.Permanent(fmt.Errorf("the secret %s can't be decrypted with this machine's key", name))
	}
	return string(v), nil
}

func (r *Reconciler) objectStore(ctx context.Context) (engine.ObjectStore, error) {
	raw, err := r.secretValue(ctx, api.DefaultProject, domain.ObjectStoreCredentialsSecret)
	if err != nil {
		return engine.ObjectStore{}, err
	}
	var c struct{ AccessKey, SecretKey string }
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return engine.ObjectStore{}, engine.Permanent(fmt.Errorf("object store credentials are unreadable: %w", err))
	}
	return engine.ObjectStore{AccessKey: c.AccessKey, SecretKey: c.SecretKey}, nil
}

func (r *Reconciler) restart(ctx context.Context, res *domain.Resource, log *slog.Logger) error {
	if res.Kind == api.KindVM {
		out, err := r.ensureVM(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: res.Generation}, true)
		if err != nil {
			return err
		}
		res.Outputs = out
	} else if err := r.Driver.Restart(ctx, res); err != nil {
		return err
	}
	res.RequestedAction, res.Attempts, res.NextAttemptAt, res.UpdatedAt = "", 0, nil, r.Now()
	if _, err := r.Store.SaveStatus(ctx, res, res.Generation); err != nil {
		return err
	}
	r.event(ctx, res, "restarted", "Restarted.")
	log.Info("resource restarted")
	return nil
}

func (r *Reconciler) delete(ctx context.Context, res *domain.Resource, log *slog.Logger) error {
	if res.Kind == api.KindDomain {
		// A public domain takes its record, routing rule and Access application with it (PUB-04).
		if err := r.leavePublic(ctx, res); err != nil {
			return err
		}
		// The domain is already in phase deleting, so the configuration applied now drops its route.
		if err := r.syncEdge(ctx); err != nil {
			return err
		}
	} else if res.Kind == api.KindTunnel {
		if err := r.deleteTunnel(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindNetwork {
		if err := r.Driver.DeleteNetwork(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindFirewall {
		if err := r.deleteFirewall(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindRecord {
		if err := r.deleteRecord(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindZone {
		// Nothing at the provider: EnclaveStack never creates or deletes zones (DNS-01).
	} else if res.Kind == api.KindSnapshot {
		if err := r.deleteSnapshot(res); err != nil {
			return err
		}
	} else if res.Kind == api.KindVM {
		if err := r.deleteVM(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindQueue || res.Kind == api.KindTopic {
		if err := r.deleteFromBroker(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindStack {
		if err := r.deleteStack(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindTranscoder || isOperateKind(res.Kind) {
		// Nothing on the engine: a transcoder's runs are rows (deleted with it) and what it wrote stays
		// (TRN-08); the v0.7 kinds' state is rows the monitor keeps, deleted with them.
	} else if res.Kind == api.KindCDN {
		if err := r.deleteCDN(ctx, res); err != nil {
			return err
		}
	} else if res.Kind == api.KindVolume && volumeType(res) == api.VolumeDisk {
		if err := r.hypervisorReady(ctx); err != nil {
			return err
		}
		if err := r.Hypervisor.DeleteDisk(ctx, res.ID); err != nil {
			return err
		}
	} else if res.Kind != api.KindSecret {
		var os engine.ObjectStore
		if res.Kind == api.KindBucket {
			if c, err := r.objectStore(ctx); err == nil {
				os = c
			}
		}
		if err := r.Driver.Delete(ctx, res, os); err != nil {
			return err
		}
		if res.Kind == api.KindApp || res.Kind == api.KindJob {
			if err := r.revokeBucketAccess(ctx, res); err != nil {
				return err
			}
		}
		if res.Kind == api.KindBucket {
			if err := r.syncProjectStore(ctx, res.ProjectName(), res.ID); err != nil {
				return err
			}
		}
		if res.Kind == api.KindLoadBalancer {
			if err := r.afterLoadBalancerDeleted(ctx, res); err != nil {
				return err
			}
		}
	}
	err := r.Store.WithTx(ctx, func(q *store.Queries) error {
		owned, err := q.OwnedBy(ctx, res.ID)
		if err != nil {
			return err
		}
		if err := q.DeleteResource(ctx, res.ID); err != nil {
			return err
		}
		for _, o := range owned {
			if err := q.DeleteResource(ctx, o.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		return engine.Permanent(errors.New("another resource started using this one while it was being deleted"))
	}
	if err != nil {
		return err
	}
	r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "deleted").Inc()
	if res.Stack != "" {
		r.Enqueue(res.Stack) // the stack goes after its last member
	}
	log.Info("resource deleted")
	return nil
}

// fail records a failed attempt: retry with backoff, wait for the engine, or give up (L3-007 §1).
func (r *Reconciler) fail(ctx context.Context, res *domain.Resource, cause error, log *slog.Logger) {
	now := r.Now()
	msg := truncate(cause.Error())
	gen := res.Generation
	switch {
	case errors.Is(cause, engine.ErrUnavailable):
		next := now.Add(EngineDownRetry)
		res.NextAttemptAt, res.Message = &next, engineWaitMessage
		log.Warn("engine unavailable; will retry", "retry_in", EngineDownRetry.String())
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "retry").Inc()
	case errors.Is(cause, engine.ErrMachineOffline):
		// Not an attempt: the resource waits for its machine with the reason (FAIL-01).
		next := now.Add(NotPlacedRetry)
		res.NextAttemptAt, res.Message = &next, truncate(offlineMessage(cause))
		log.Warn("machine offline; will retry", "reason", res.Message, "retry_in", NotPlacedRetry.String())
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "retry").Inc()
	case errors.Is(cause, errMembers):
		next := now.Add(StackRetry)
		res.NextAttemptAt, res.Message = &next, truncate(strings.TrimPrefix(cause.Error(), errMembers.Error()+": "))
		if res.Phase != api.PhaseDeleting {
			res.Message = "Waiting for members: " + res.Message + "."
		}
	case errors.Is(cause, errNotPlaced):
		next := now.Add(NotPlacedRetry)
		res.NextAttemptAt, res.Message = &next, truncate(strings.TrimPrefix(cause.Error(), errNotPlaced.Error()+": "))
		log.Info("no machine fits yet; will retry", "reason", res.Message, "retry_in", NotPlacedRetry.String())
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "retry").Inc()
	case errors.Is(cause, engine.ErrNoVMs) && res.Phase != api.PhaseDeleting:
		// Not a failure: the VM waits, with the fix in its message, until QEMU is there (VM-11).
		next := now.Add(NoVMsRetry)
		res.NextAttemptAt, res.Message = &next, truncate(strings.TrimPrefix(cause.Error(), engine.ErrNoVMs.Error()+": "))
		log.Warn("VMs unavailable; will retry", "reason", res.Message, "retry_in", NoVMsRetry.String())
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "retry").Inc()
	case res.Phase == api.PhaseDeleting:
		res.Attempts++
		next := now.Add(Backoff(res.Attempts, r.Jitter))
		res.NextAttemptAt, res.Message = &next, "Delete failed, retrying: "+msg
		log.Warn("delete failed; will retry", "error", msg, "retry_at", next)
		r.event(ctx, res, "retry_scheduled", res.Message)
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "retry").Inc()
	case engine.IsPermanent(cause) || res.Attempts+1 >= MaxAttempts:
		res.Attempts++
		if res.RequestedAction == api.ActionRestart {
			// A restart that cannot run leaves the resource as it was; report and stop asking.
			res.RequestedAction, res.NextAttemptAt = "", nil
			r.event(ctx, res, "failed", "Restart failed: "+msg)
			log.Error("restart failed", "error", msg)
			break
		}
		if err := res.SetPhase(api.PhaseFailed, msg, now); err != nil {
			log.Error("cannot mark failed", "error", err)
			return
		}
		res.NextAttemptAt, res.RequestedAction = nil, ""
		markActiveFailed(res)
		if res.Kind == api.KindApp {
			// The serving revision (if any) keeps running; only this deploy failed (ADR-016).
			res.Outputs = withOutput(res.Outputs, engine.OutputLastDeploy, engine.DeployFailed)
		}
		r.event(ctx, res, "failed", msg)
		r.refreshStack(ctx, res)
		log.Error("reconcile failed", "error", msg, "permanent", engine.IsPermanent(cause))
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "failed").Inc()
	default:
		res.Attempts++
		next := now.Add(Backoff(res.Attempts, r.Jitter))
		res.NextAttemptAt, res.Message = &next, fmt.Sprintf("Attempt %d of %d failed: %s Retrying.", res.Attempts, MaxAttempts, msg)
		markActiveFailed(res)
		r.event(ctx, res, "retry_scheduled", res.Message)
		log.Warn("reconcile failed; will retry", "error", msg, "retry_at", next)
		r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "retry").Inc()
	}
	res.UpdatedAt = now
	if ok, err := r.Store.SaveStatus(ctx, res, gen); err != nil {
		log.Error("save failure status failed", "error", err)
	} else if !ok {
		r.Enqueue(res.ID)
	}
}

// withOutput returns outputs with key set, copying so a shared map is never mutated.
func withOutput(outputs map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(outputs)+1)
	for k, v := range outputs {
		out[k] = v
	}
	out[key] = value
	return out
}

func withoutOutput(outputs map[string]any, key string) map[string]any {
	out := make(map[string]any, len(outputs))
	for k, v := range outputs {
		if k != key {
			out[k] = v
		}
	}
	return out
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

func markActiveFailed(res *domain.Resource) {
	for i := range res.Steps {
		if res.Steps[i].State == api.StepActive {
			res.Steps[i].State = api.StepFailed
		}
	}
}

func truncate(s string) string {
	if len(s) > maxMessageLen {
		return s[:maxMessageLen] + "…"
	}
	return s
}

func (r *Reconciler) event(ctx context.Context, res *domain.Resource, typ, msg string) {
	if err := r.Store.AddEvent(ctx, res.ID, typ, msg, r.Now()); err != nil && !errors.Is(err, context.Canceled) {
		r.Log.Warn("record event failed", "resource_id", res.ID, "type", typ, "error", err)
	}
}

// driftLoop checks ready resources against the engine and repairs what disappeared (CP-10).
func (r *Reconciler) driftLoop(ctx context.Context) {
	t := time.NewTicker(r.DriftEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.CheckDrift(ctx)
		}
	}
}

// CheckDrift runs one drift pass (exported for tests).
func (r *Reconciler) CheckDrift(ctx context.Context) {
	if err := r.Driver.Ping(ctx); err != nil {
		r.Metrics.EngineUp.Set(0)
		return
	}
	r.Metrics.EngineUp.Set(1)
	ready, err := r.Store.ResourcesInPhase(ctx, api.PhaseReady)
	if err != nil {
		r.Log.Error("list ready resources failed", "error", err)
		return
	}
	r.checkRecords(ctx, ready)
	for _, res := range ready {
		if res.Kind == api.KindSecret || res.Kind == api.KindJob || res.Kind == api.KindBackup || res.Kind == api.KindStack || isOperateKind(res.Kind) || res.RequestedAction != "" {
			continue // no long-running runtime to drift
		}
		if res.Kind == api.KindTunnel {
			continue // the tunnel loop watches connectors (TUN-05)
		}
		if res.Kind == api.KindFirewall {
			r.refreshFirewall(ctx, res)
			continue
		}
		if res.Kind == api.KindZone || res.Kind == api.KindRecord {
			continue // checked against the provider on their own schedule (ADR-023)
		}
		if res.Kind == api.KindSnapshot || (res.Kind == api.KindVolume && volumeType(res) == api.VolumeDisk) {
			continue // files on the machine, not a runtime
		}
		if res.Kind == api.KindVM {
			r.refreshVM(ctx, res, r.Log)
			continue
		}
		if res.Kind == api.KindQueue || res.Kind == api.KindTopic {
			r.refreshBroker(ctx, res)
			continue
		}
		if res.Kind == api.KindTranscoder {
			continue // runs, not a runtime
		}
		var os engine.ObjectStore
		if res.Kind == api.KindBucket {
			os, _ = r.objectStore(ctx)
		}
		h, err := r.Driver.Health(ctx, res, os)
		if err != nil {
			continue
		}
		if h == engine.Healthy {
			if res.Kind == api.KindDomain {
				r.refreshCertificate(ctx, res)
			}
			if res.Kind == api.KindLoadBalancer {
				r.refreshLoadBalancer(ctx, res)
			}
			if res.Kind == api.KindVolume {
				r.refreshVolume(ctx, res)
			}
			if res.Kind == api.KindCDN {
				r.refreshCDN(ctx, res)
			}
			continue
		}
		what := map[engine.Health]string{engine.Missing: "is missing", engine.Stopped: "was stopped"}[h]
		if err := res.SetPhase(api.PhaseProvisioning, "Restoring: the runtime "+what+" outside EnclaveStack.", r.Now()); err != nil {
			continue
		}
		res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), r.Now()
		if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err != nil || !ok {
			continue
		}
		r.event(ctx, res, "drift_detected", "The runtime "+what+" outside EnclaveStack; restoring it.")
		r.Log.Warn("drift detected", "resource_id", res.ID, "kind", res.Kind, "health", what)
		r.Enqueue(res.ID)
	}
}

func (r *Reconciler) updateGauges(ctx context.Context) {
	all, err := r.Store.AllResources(ctx)
	if err != nil {
		return
	}
	r.Metrics.Resources.Reset()
	for _, res := range all {
		r.Metrics.Resources.WithLabelValues(string(res.Kind), string(res.Phase)).Inc()
	}
}

// offlineMessage is what a resource waiting for its machine says: "rack-1 is offline since 10:42 on 3 Nov."
func offlineMessage(err error) string {
	msg := strings.TrimPrefix(err.Error(), engine.ErrMachineOffline.Error()+": ")
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}
