// Package fake is an in-memory engine driver (ADR-006). It backs API and reconciler integration
// tests, provider acceptance tests, console e2e tests, load tests and demos. Faults can be injected
// per resource name to exercise retry, permanent failure and recovery (L5-002 §4).
package fake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// Fault describes how Ensure misbehaves for one resource name.
type Fault struct {
	// TransientFailures makes the next N Ensure calls fail with a retryable error.
	TransientFailures int
	// Permanent makes Ensure fail permanently.
	Permanent bool
	// Panic makes Ensure panic (tests the reconciler's recovery).
	Panic bool
	// Message replaces the permanent failure's message.
	Message string
	// DeleteFailures makes the next N Delete calls fail with a retryable error.
	DeleteFailures int
	// DeleteDelay makes each Delete take this long (a slow container removal).
	DeleteDelay time.Duration
	// RestartPermanent makes Restart fail permanently.
	RestartPermanent bool
	// Unhealthy makes a new app revision fail its health check (ADR-016): the serving revision stays.
	Unhealthy bool
	// DumpFails makes backups of this database or cache fail.
	DumpFails bool
}

type runtime struct {
	generation int64
	running    bool
	port       int
	revision   int64
	placement  engine.Placement
	ip         string
	// container and aliases are what the Docker driver would call it on its network (ADR-045).
	container string
	aliases   []string
}

// named records the names a resource's runtime answers to on its network.
func (rt *runtime) named(r *domain.Resource) *runtime {
	rt.container, rt.aliases = engine.ContainerName(r), []string{engine.ContainerName(r), engine.PrivateDNS(r)}
	return rt
}

// Driver is the fake engine.
type Driver struct {
	// StepDelay is slept before each step (0 in unit tests).
	StepDelay time.Duration

	mu        sync.Mutex
	available bool
	memory    int64
	// mesh is the last mesh configuration applied (ADR-045); its pools bound the addresses handed out.
	mesh      *engine.MeshConfig
	nextPort  int
	runtimes  map[string]*runtime
	buckets   map[string]bool
	faults    map[string]*Fault
	calls     map[string]int
	edge      edge
	jobs      map[string]*fakeRun
	jobStarts map[string]int // survives a finished run, so a second start shows
	data      map[string]string
	images    map[string]string // built image tag → resource ID
	jobImages map[string]string // run ID → the image it started with
	builds    int
	networks  map[string]*network // engine network name → network
	rules     map[string]*fakeRules
	lbs       map[string]*lb.Config // load balancer resource ID → its configuration
	// replicasByAlias is each app's running replica count, by its stable alias.
	replicasByAlias map[string]int
	lbUnhealthy     map[string]int
	// volumes are filesystem volumes by engine volume name: file path → contents (ADR-027).
	volumes map[string]map[string]string
	// mounts are the mounts each app or run was last given.
	mounts map[string][]engine.Mount
	// links are each VM link's host ports (frame, SSH), kept across recreation.
	links map[string]linkPorts
	// v6 holds the broker, caches and transcodes (data.go).
	v6 *dataState
	// v7 holds scripted stats, edge counters and logs (operate.go).
	v7     *operateState
	opOnce sync.Once
}

// New returns an available fake engine.
func New(stepDelay time.Duration) *Driver {
	return &Driver{StepDelay: stepDelay, available: true, nextPort: 41000,
		runtimes: map[string]*runtime{}, buckets: map[string]bool{}, faults: map[string]*Fault{}, calls: map[string]int{}, images: map[string]string{},
		networks: map[string]*network{}, rules: map[string]*fakeRules{}, lbs: map[string]*lb.Config{},
		replicasByAlias: map[string]int{}, lbUnhealthy: map[string]int{}}
}

// ParseFaults reads "name:transient=3;other:permanent" (ENCLAVE_FAKE_FAULTS) into faults.
func ParseFaults(spec string) (map[string]Fault, error) {
	out := map[string]Fault{}
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rule, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("fault %q: want name:rule", part)
		}
		var f Fault
		switch {
		case rule == "permanent":
			f.Permanent = true
		case rule == "unhealthy":
			f.Unhealthy = true
		case strings.HasPrefix(rule, "transient="):
			n, err := strconv.Atoi(strings.TrimPrefix(rule, "transient="))
			if err != nil || n < 1 {
				return nil, fmt.Errorf("fault %q: transient=N needs N ≥ 1", part)
			}
			f.TransientFailures = n
		default:
			return nil, fmt.Errorf("fault %q: rule must be permanent, unhealthy or transient=N", part)
		}
		out[name] = f
	}
	return out, nil
}

// SetFault installs a fault for a resource name.
func (d *Driver) SetFault(name string, f Fault) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.faults[name] = &f
}

// SetAvailable simulates the engine going down or coming back.
func (d *Driver) SetAvailable(up bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.available = up
}

// Kill simulates a container removed outside EnclaveStack (drift).
func (d *Driver) Kill(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.runtimes, id)
}

// KillBucket simulates a bucket removed outside EnclaveStack (drift).
func (d *Driver) KillBucket(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.buckets, name)
}

// EnsureCalls returns how many times Ensure ran for a resource name.
func (d *Driver) EnsureCalls(name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[name]
}

// Name implements engine.Driver.
func (d *Driver) Name() string { return "fake" }

// Ping implements engine.Driver.
func (d *Driver) Ping(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.available {
		return engine.ErrUnavailable
	}
	return nil
}

// Info implements engine.Driver.
func (d *Driver) Info(ctx context.Context) (engine.Info, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.Info{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	mem := d.memory
	if mem == 0 {
		mem = 16 << 30
	}
	return engine.Info{Version: "fake-1.0", MemoryTotalBytes: mem, CPUs: 8}, nil
}

// SetMemory sets the memory Info reports (placement tests, ADR-044); 0 is 16 GiB.
func (d *Driver) SetMemory(bytes int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.memory = bytes
}

func (d *Driver) sleep(ctx context.Context) error {
	if d.StepDelay <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d.StepDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Ensure implements engine.Driver.
func (d *Driver) Ensure(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	r := in.Resource
	d.mu.Lock()
	d.calls[r.Name]++
	f := d.faults[r.Name]
	up := d.available
	d.mu.Unlock()
	if !up {
		return nil, engine.ErrUnavailable
	}
	if r.Kind == api.KindApp {
		return d.ensureApp(ctx, in, f, report)
	}
	steps := domain.StepNames[r.Kind]
	for i, step := range steps {
		report.Step(step, api.StepActive)
		if err := d.sleep(ctx); err != nil {
			return nil, err
		}
		// Faults strike on the second step, so progress before the failure is visible.
		if f != nil && i == min(1, len(steps)-1) {
			if f.Panic {
				panic("injected panic in the fake driver")
			}
			d.mu.Lock()
			switch {
			case f.Permanent:
				msg := f.Message
				if msg == "" {
					msg = "image not found: the registry has no such image (injected fault)"
				}
				d.mu.Unlock()
				report.Step(step, api.StepFailed)
				return nil, engine.Permanent(errors.New(msg))
			case f.TransientFailures > 0:
				f.TransientFailures--
				d.mu.Unlock()
				report.Step(step, api.StepFailed)
				return nil, errors.New("engine busy (injected transient fault)")
			}
			d.mu.Unlock()
		}
		report.Step(step, api.StepDone)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Kind == api.KindBucket {
		d.buckets[r.Name] = true
		return engine.Outputs{"endpoint": "http://127.0.0.1:41999", "internalEndpoint": "http://" + engine.ObjectStoreName + ":9000",
			"bucket": r.Name, "credentialsSecret": domain.ObjectStoreCredentialsSecret}, nil
	}
	rt := d.runtimes[r.ID]
	if rt == nil || in.Recreate || rt.generation != r.Generation {
		port := d.nextPort
		if rt != nil {
			port = rt.port
		} else {
			d.nextPort++
		}
		delete(d.runtimes, r.ID) // its address is free again
		ip, err := d.address(in.Placement)
		if err != nil {
			return nil, err
		}
		rt = (&runtime{port: port, placement: in.Placement, ip: ip}).named(r)
		d.runtimes[r.ID] = rt
	}
	rt.generation, rt.running = r.Generation, true
	if in.Firewall != nil {
		d.load(r.ID, in.Firewall)
	}
	out := outputs(r, in, rt, d.publishes(rt.placement))
	if r.Kind == api.KindLoadBalancer {
		var err error
		if out, err = d.lbOutputs(r, in, rt); err != nil {
			return nil, err
		}
	}
	if in.Started != nil {
		d.mu.Unlock()
		err := in.Started(ctx)
		d.mu.Lock()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ensureApp mirrors the Docker driver's revisions (ADR-016): a new generation or a redeploy starts a
// new revision; a revision that fails its health check is discarded and the serving one stays.
func (d *Driver) ensureApp(ctx context.Context, in engine.EnsureInput, f *Fault, report engine.Reporter) (engine.Outputs, error) {
	r := in.Resource
	spec, _ := in.Spec.(api.AppSpec)
	d.recordMounts(r.ID, in.Mounts)
	d.mu.Lock()
	cur := d.runtimes[r.ID]
	_, built := d.images[spec.Image]
	d.mu.Unlock()
	if strings.HasPrefix(spec.Image, "enclave.local/") && !built {
		return nil, engine.Permanent(fmt.Errorf("the built image %s is gone; redeploy to build it again", spec.Image))
	}
	if cur != nil && cur.generation == r.Generation && !in.Recreate {
		for _, step := range domain.StepNames[r.Kind] {
			report.Step(step, api.StepDone)
		}
		d.mu.Lock()
		cur.running = true
		out := outputs(r, in, cur, d.publishes(cur.placement))
		out[engine.OutputServingRevision] = cur.revision
		out[engine.OutputRunningReplicas] = max(spec.Replicas, 1)
		d.mu.Unlock()
		return out, nil
	}
	recreate := spec.Strategy == api.StrategyRecreate || spec.Publish
	next := in.Revision
	if cur != nil && cur.revision >= next {
		next = cur.revision + 1
	}
	if next < 1 {
		next = 1
	}
	for i, step := range domain.StepNames[r.Kind] {
		report.Step(step, api.StepActive)
		if err := d.sleep(ctx); err != nil {
			return nil, err
		}
		if f != nil && i == 1 {
			if f.Panic {
				panic("injected panic in the fake driver")
			}
			d.mu.Lock()
			switch {
			case f.Permanent:
				msg := f.Message
				if msg == "" {
					msg = "image not found: the registry has no such image (injected fault)"
				}
				d.mu.Unlock()
				report.Step(step, api.StepFailed)
				return nil, engine.Permanent(errors.New(msg))
			case f.TransientFailures > 0:
				f.TransientFailures--
				d.mu.Unlock()
				report.Step(step, api.StepFailed)
				return nil, errors.New("engine busy (injected transient fault)")
			}
			d.mu.Unlock()
		}
		if step == "start" {
			engine.Event(report, "revision_started", fmt.Sprintf("Revision %d started.", next))
		}
		if step == "health" && f != nil && f.Unhealthy {
			report.Step(step, api.StepFailed)
			msg := fmt.Sprintf("Revision %d failed its health check: the health check did not pass after 12 attempts (last: connection refused) (injected fault).", next)
			d.mu.Lock()
			if !recreate && cur != nil {
				msg += fmt.Sprintf(" Revision %d is still serving.", cur.revision)
			} else {
				delete(d.runtimes, r.ID)
				msg += " The app is not running."
			}
			d.mu.Unlock()
			engine.Event(report, "health_check_failed", fmt.Sprintf("Revision %d failed its health check and was discarded.", next))
			return nil, engine.Permanent(errors.New(msg))
		}
		if step == "switch" {
			engine.Event(report, "traffic_switched", fmt.Sprintf("Revision %d is serving.", next))
		}
		report.Step(step, api.StepDone)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	port := d.nextPort
	if cur != nil {
		port = cur.port
	} else {
		d.nextPort++
	}
	// A rolling deploy starts the new revision while the old one still holds its address, as on Docker;
	// recreate frees it first.
	if recreate {
		delete(d.runtimes, r.ID)
	}
	ip, err := d.address(in.Placement)
	if err != nil {
		return nil, err
	}
	rt := (&runtime{generation: r.Generation, running: true, port: port, revision: next, placement: in.Placement, ip: ip}).named(r)
	d.runtimes[r.ID] = rt
	d.replicasByAlias[engine.ContainerName(r)] = max(spec.Replicas, 1)
	if in.Firewall != nil {
		d.load(r.ID, in.Firewall)
	}
	out := outputs(r, in, rt, d.publishes(rt.placement))
	if in.Started != nil {
		d.mu.Unlock()
		err := in.Started(ctx)
		d.mu.Lock()
		if err != nil {
			return nil, err
		}
	}
	out[engine.OutputServingRevision] = next
	out[engine.OutputRunningReplicas] = max(spec.Replicas, 1)
	return out, nil
}

// outputs mirrors the Docker driver; published is false in a network without internet (ADR-020).
func outputs(r *domain.Resource, in engine.EnsureInput, rt *runtime, published bool) engine.Outputs {
	internal := engine.ContainerName(r)
	private := func(out engine.Outputs) engine.Outputs {
		out[engine.OutputPrivateIP], out[engine.OutputPrivateDNS] = rt.ip, engine.PrivateDNS(r)
		return out
	}
	hostPort := func(n int) int {
		if published {
			return n
		}
		return 0
	}
	switch r.Kind {
	case api.KindDatabase:
		spec, _ := in.Spec.(api.DatabaseSpec)
		return private(engine.DatabaseOutputs(r, spec, "127.0.0.1", hostPort(rt.port), hostPort(rt.port+10000)))
	case api.KindSearch:
		spec, _ := in.Spec.(api.SearchSpec)
		return private(engine.SearchOutputs(r, "127.0.0.1", hostPort(rt.port), engine.SearchVersions[spec.Version]))
	case api.KindEmail:
		spec, _ := in.Spec.(api.EmailSpec)
		return private(engine.EmailOutputs(r, spec, "127.0.0.1", hostPort(rt.port), hostPort(rt.port+10000)))
	case api.KindCDN:
		out := engine.Outputs{"internalHost": internal, "internalPort": engine.CDNPort}
		if in.CDN != nil {
			out["origin"] = in.CDN.Upstream
		}
		return out
	case api.KindCache:
		out := engine.Outputs{"internalHost": internal, "internalPort": engine.RedisPort}
		if published {
			out["host"], out["port"] = "127.0.0.1", rt.port
		}
		return private(out)
	case api.KindApp:
		spec, _ := in.Spec.(api.AppSpec)
		out := engine.Outputs{"internalHost": internal}
		if spec.Port > 0 {
			out["internalPort"] = spec.Port
			if spec.Publish && published {
				out["host"], out["port"] = "127.0.0.1", rt.port
			}
		}
		return private(out)
	}
	return engine.Outputs{}
}

// Delete implements engine.Driver.
func (d *Driver) Delete(ctx context.Context, r *domain.Resource, _ engine.ObjectStore) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	if f := d.faults[r.Name]; f != nil && f.DeleteFailures > 0 {
		f.DeleteFailures--
		d.mu.Unlock()
		return errors.New("volume busy (injected delete fault)")
	}
	var delay time.Duration
	if f := d.faults[r.Name]; f != nil {
		delay = f.DeleteDelay
	}
	d.mu.Unlock()
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	if err := d.sleep(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.runtimes, r.ID)
	delete(d.buckets, r.Name)
	delete(d.lbs, r.ID)
	delete(d.rules, r.ID)
	if r.Kind == api.KindVolume {
		d.deleteVolume(r)
	}
	if r.Kind == api.KindApp {
		delete(d.replicasByAlias, engine.ContainerName(r))
	}
	for tag, id := range d.images { // an app's built images go with it
		if id == r.ID {
			delete(d.images, tag)
		}
	}
	return nil
}

// Health implements engine.Driver.
func (d *Driver) Health(ctx context.Context, r *domain.Resource, _ engine.ObjectStore) (engine.Health, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.Missing, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Kind == api.KindBucket {
		if d.buckets[r.Name] {
			return engine.Healthy, nil
		}
		return engine.Missing, nil
	}
	if r.Kind == api.KindNetwork {
		if d.networks[engine.DockerNetwork(r.Project, r.Name)] != nil {
			return engine.Healthy, nil
		}
		return engine.Missing, nil
	}
	if r.Kind == api.KindVolume {
		if d.volumeFiles(engine.FilesystemVolume(r.ID)) != nil {
			return engine.Healthy, nil
		}
		return engine.Missing, nil
	}
	if r.Kind == api.KindDomain {
		if d.edge.running {
			return engine.Healthy, nil
		}
		return engine.Stopped, nil
	}
	rt := d.runtimes[r.ID]
	switch {
	case rt == nil:
		return engine.Missing, nil
	case !rt.running:
		return engine.Stopped, nil
	}
	return engine.Healthy, nil
}

// Restart implements engine.Driver.
func (d *Driver) Restart(ctx context.Context, r *domain.Resource) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.faults[r.Name]; f != nil && f.RestartPermanent {
		return engine.Permanent(errors.New("container cannot be restarted (injected fault)"))
	}
	rt := d.runtimes[r.ID]
	if rt == nil {
		return fmt.Errorf("%s is not running", r.Name)
	}
	rt.running = true
	return nil
}

// Logs implements engine.Driver.
func (d *Driver) Logs(ctx context.Context, r *domain.Resource, tail int) ([]api.LogLine, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	lines := []api.LogLine{
		{Time: now.Add(-3 * time.Second), Stream: "stdout", Message: r.Name + " starting"},
		{Time: now.Add(-2 * time.Second), Stream: "stdout", Message: "listening"},
		{Time: now.Add(-1 * time.Second), Stream: "stderr", Message: "warning: running on the fake engine"},
	}
	if tail < len(lines) {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

// Sweep removes the runtimes of every resource not in keep (ADR-046), as the Docker driver removes their
// containers; volumes and data stay.
func (d *Driver) Sweep(_ context.Context, keep []string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.available {
		return nil, engine.ErrUnavailable
	}
	var removed []string
	for id := range d.runtimes {
		if !slices.Contains(keep, id) {
			delete(d.runtimes, id)
			removed = append(removed, id)
		}
	}
	slices.Sort(removed)
	return removed, nil
}

// Running reports whether a resource has a runtime here (tests of moves and sweeps).
func (d *Driver) Running(resourceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	rt := d.runtimes[resourceID]
	return rt != nil && rt.running
}
