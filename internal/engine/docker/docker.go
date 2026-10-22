// Package docker is the Docker Engine driver (ADR-006). Every object it creates carries EnclaveStack
// labels, lives on the "enclave" bridge network, and is named from the resource (L3-005 §2). Ensure
// is idempotent: it inspects first and only pulls, creates or starts what is missing or outdated.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	hcbuild "github.com/enclavestack/platform/internal/build"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Labels on every managed object.
const (
	LabelManaged    = "enclave.managed"
	LabelResourceID = "enclave.resource-id"
	LabelGeneration = "enclave.generation"
	LabelKind       = "enclave.kind"
)

// Images. MinIO's own images were withdrawn from Docker Hub and quay.io in 2025; the object store uses
// Chainguard's build of MinIO from source, pinned by digest so a mutable "latest" tag can never change
// it underneath us (ADR-013). Override with Options.ObjectStoreImage.
const (
	DefaultObjectStoreImage = "cgr.dev/chainguard/minio@sha256:cff197fef764642223ef9f4444c903d51aee9b544a7be8bc007fef6b7b00d136"
	redisImage              = "redis:7-alpine"
)

// Step timeouts (L3-007 §3).
const (
	pullTimeout    = 10 * time.Minute
	stepTimeout    = 2 * time.Minute
	healthyTimeout = 2 * time.Minute
	settleFor      = 2 * time.Second
)

// Options configure the driver.
type Options struct {
	// PublishAddress is where published ports bind (127.0.0.1 by default).
	PublishAddress   string
	ObjectStoreImage string
	// ProbeImage runs HTTP/TCP health checks (DefaultProbeImage).
	ProbeImage string
	// Drain is how long an old app revision keeps running after the switch (DefaultDrain; < 0 = none).
	Drain time.Duration
	// EdgeImage is the edge proxy image (DefaultEdgeImage).
	EdgeImage string
	// BrokerImage and TranscodeImage are the broker's and FFmpeg's (DefaultBrokerImage, DefaultTranscodeImage).
	BrokerImage    string
	TranscodeImage string
	// EdgeAddress is where the edge binds 80/443: 127.0.0.1 by default, so nothing is exposed until the
	// operator opts in (L3-006 §5). EdgeHTTPPort / EdgeHTTPSPort default to 80 / 443.
	EdgeAddress   string
	EdgeHTTPPort  int
	EdgeHTTPSPort int
	// ACMEDirectory and ACMERootPEM point automatic certificates at another CA (tests use Pebble).
	ACMEDirectory string
	ACMERootPEM   []byte
	// Agent marks another machine's engine (ADR-045): its default network is made with the main machine's
	// range, so it waits for the first mesh configuration before making it.
	Agent bool
	// DockerHost reaches another engine than DOCKER_HOST's (tests with a second engine).
	DockerHost string
	Log        *slog.Logger
}

// Driver talks to the Docker Engine API.
type Driver struct {
	cli  *client.Client
	opts Options
	log  *slog.Logger

	edgeMu  sync.Mutex
	edgeErr string // the last edge start failure, shown in EdgeStatus

	addrMu   sync.Mutex
	reserved map[string]time.Time // "<network>/<address>" handed out to a container not yet started

	fwMu           sync.Mutex
	applied        map[string]appliedRules // container ID → the rules loaded into it (ADR-021)
	imgMu          sync.Mutex
	netfilterReady bool
	vmlinkReady    bool
	meshReady      bool

	meshMu sync.Mutex
	mesh   *meshState // the last mesh configuration (ADR-045); nil with one machine

	brokerMu   sync.Mutex
	brokerConf []byte // the configuration last written into the broker (ADR-029)
}

// New connects to Docker using DOCKER_HOST, falling back to Docker Desktop's per-user socket on
// macOS when the default socket is absent. It does not require Docker to be running yet.
func New(opts Options) (*Driver, error) {
	if opts.PublishAddress == "" {
		opts.PublishAddress = "127.0.0.1"
	}
	if opts.ObjectStoreImage == "" {
		opts.ObjectStoreImage = DefaultObjectStoreImage
	}
	if opts.ProbeImage == "" {
		opts.ProbeImage = DefaultProbeImage
	}
	if opts.EdgeImage == "" {
		opts.EdgeImage = DefaultEdgeImage
	}
	if opts.BrokerImage == "" {
		opts.BrokerImage = DefaultBrokerImage
	}
	if opts.TranscodeImage == "" {
		opts.TranscodeImage = DefaultTranscodeImage
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	clientOpts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if os.Getenv("DOCKER_HOST") == "" {
		if _, err := os.Stat("/var/run/docker.sock"); err != nil {
			if home, herr := os.UserHomeDir(); herr == nil {
				desktop := filepath.Join(home, ".docker", "run", "docker.sock")
				if _, err := os.Stat(desktop); err == nil {
					clientOpts = append(clientOpts, client.WithHost("unix://"+desktop))
				}
			}
		}
	}
	if opts.DockerHost != "" {
		clientOpts = append(clientOpts, client.WithHost(opts.DockerHost))
	}
	cli, err := client.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Driver{cli: cli, opts: opts, log: opts.Log.With("component", "docker"), reserved: map[string]time.Time{}, applied: map[string]appliedRules{}}, nil
}

// Name implements engine.Driver.
func (d *Driver) Name() string { return "docker" }

func unavailable(err error) error {
	if err != nil && (client.IsErrConnectionFailed(err) || strings.Contains(err.Error(), "connect: no such file") ||
		strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "Cannot connect to the Docker daemon")) {
		return fmt.Errorf("%w: %v", engine.ErrUnavailable, err)
	}
	return err
}

// Ping implements engine.Driver.
func (d *Driver) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := d.cli.Ping(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", engine.ErrUnavailable, err)
	}
	return nil
}

// Info implements engine.Driver.
func (d *Driver) Info(ctx context.Context) (engine.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := d.cli.Info(ctx)
	if err != nil {
		return engine.Info{}, fmt.Errorf("%w: %v", engine.ErrUnavailable, err)
	}
	return engine.Info{Version: info.ServerVersion, MemoryTotalBytes: info.MemTotal, CPUs: info.NCPU}, nil
}

// plan is the container a resource needs.
type plan struct {
	image      string
	cmd        []string
	env        []string
	port       int // container port, 0 for none
	publish    bool
	mount      string // volume mount path, "" for none
	memoryMiB  int
	healthTest []string
	placement  engine.Placement
	mounts     []engine.Mount
	// v0.6 (ADR-030 – ADR-033): more published ports (ClickHouse native, Mailpit's viewer), files written
	// before the first start (directory → name → contents), a longer first start, and open-file limits.
	extraPorts     []int
	files          map[string]map[string][]byte
	healthyTimeout time.Duration
	nofile         int64
}

func (d *Driver) planFor(in engine.EnsureInput) (plan, error) {
	p, err := d.basePlan(in)
	p.placement, p.mounts = in.Placement, in.Mounts
	return p, err
}

func (d *Driver) basePlan(in engine.EnsureInput) (plan, error) {
	r := in.Resource
	switch s := in.Spec.(type) {
	case api.DatabaseSpec:
		return databasePlan(r, s, in.Password)
	case api.SearchSpec:
		return searchPlan(s, in.Password), nil
	case api.EmailSpec:
		return emailPlan(r, s, in.Password, in.Relay), nil
	case api.CDNSpec:
		return cdnPlan(in.CDN)
	case api.CacheSpec:
		return plan{image: redisImage, port: engine.RedisPort, publish: true, mount: "/data", memoryMiB: s.MaxMemoryMiB + domain.CacheOverheadMiB,
			cmd:        []string{"redis-server", "--appendonly", "yes", "--maxmemory", strconv.Itoa(s.MaxMemoryMiB) + "mb", "--maxmemory-policy", "allkeys-lru"},
			healthTest: []string{"CMD", "redis-cli", "ping"}}, nil
	case api.AppSpec:
		env := make([]string, 0, len(in.Env))
		for k, v := range in.Env {
			env = append(env, k+"="+v)
		}
		return plan{image: s.Image, cmd: s.Command, env: env, port: s.Port, publish: s.Publish, memoryMiB: s.MemoryMiB}, nil
	}
	return plan{}, engine.Permanent(fmt.Errorf("the docker driver has no plan for a %s", r.Kind))
}

// Ensure implements engine.Driver.
func (d *Driver) Ensure(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	switch in.Resource.Kind {
	case api.KindBucket:
		return d.ensureBucket(ctx, in, report)
	case api.KindApp:
		return d.ensureApp(ctx, in, report)
	case api.KindLoadBalancer:
		return d.ensureLoadBalancer(ctx, in, report)
	}
	p, err := d.planFor(in)
	if err != nil {
		return nil, err
	}
	r := in.Resource
	name := engine.ContainerName(r)

	report.Step("pull", api.StepActive)
	if err := d.ensureImage(ctx, p.image, in.Recreate); err != nil {
		return nil, err
	}
	report.Step("pull", api.StepDone)

	create := firstStep(r.Kind, "create", "cache")
	report.Step(create, api.StepActive)
	labels := map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelGeneration: strconv.FormatInt(r.Generation, 10), LabelKind: string(r.Kind)}
	existing, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	keep := map[int]int{}
	if existing != nil && (in.Recreate || existing.Config.Labels[LabelGeneration] != labels[LabelGeneration] || existing.Config.Labels[LabelResourceID] != r.ID) {
		// Keep the published ports across recreation so connection strings stay valid.
		if existing.Config.Labels[LabelResourceID] == r.ID {
			for _, port := range append([]int{p.port}, p.extraPorts...) {
				if port > 0 {
					keep[port] = hostPort(existing, port)
				}
			}
		}
		if err := d.remove(ctx, name); err != nil {
			return nil, err
		}
		existing = nil
	}
	if existing == nil {
		if err := d.create(ctx, r, name, name, p, labels, keep); err != nil {
			return nil, err
		}
		for dir, files := range p.files { // configuration read at start (ADR-030, ADR-033)
			if err := d.copyFiles(ctx, name, dir, files, 0o644); err != nil {
				return nil, err
			}
		}
	}
	report.Step(create, api.StepDone)

	report.Step("start", api.StepActive)
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	err = d.cli.ContainerStart(sctx, name, container.StartOptions{})
	cancel()
	if err != nil {
		return nil, d.classify(fmt.Errorf("start %s: %w", name, err))
	}
	if err := d.applyToNew(ctx, in, name); err != nil {
		return nil, err
	}
	if err := started(ctx, in); err != nil {
		return nil, err
	}
	report.Step("start", api.StepDone)

	healthy := firstStep(r.Kind, "healthy", "cache")
	report.Step(healthy, api.StepActive)
	info, err := d.waitHealthyWithin(ctx, name, len(p.healthTest) > 0, p.healthyTimeout)
	if err != nil {
		return nil, explainStartFailure(err)
	}
	report.Step(healthy, api.StepDone)
	return d.outputs(r, p, info, in), nil
}

// firstStep returns the first of names that is one of kind's steps (a CDN has "cache" and "edge" where
// containers have "create" and "healthy"; its edge and certificate steps are the reconciler's).
func firstStep(kind api.Kind, names ...string) string {
	for _, n := range names {
		if slices.Contains(domain.StepNames[kind], n) {
			return n
		}
	}
	return names[0]
}

func (d *Driver) classify(err error) error {
	if err == nil {
		return nil
	}
	if u := unavailable(err); errors.Is(u, engine.ErrUnavailable) {
		return u
	}
	msg := err.Error()
	for _, permanent := range []string{"port is already allocated", "invalid reference format", "manifest unknown", "pull access denied",
		"repository does not exist", "unauthorized", "not found: manifest", "no matching manifest", "executable file not found", "invalid mount config"} {
		if strings.Contains(msg, permanent) {
			return engine.Permanent(err)
		}
	}
	return err
}

func (d *Driver) ensureImage(ctx context.Context, ref string, force bool) error {
	if hcbuild.IsLocal(ref) { // built here: never pulled, even on a redeploy (ADR-019)
		if ok, err := d.ImageExists(ctx, ref); err != nil {
			return err
		} else if !ok {
			return localImageMissing(ref)
		}
		return nil
	}
	if !force {
		if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
			return nil
		} else if !cerrdefs.IsNotFound(err) {
			return d.classify(fmt.Errorf("inspect image %s: %w", ref, err))
		}
	}
	pctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	rc, err := d.cli.ImagePull(pctx, ref, image.PullOptions{})
	if err != nil {
		return d.classify(fmt.Errorf("pull image %s: %w", ref, err))
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return d.classify(fmt.Errorf("pull image %s: %w", ref, err))
		}
		if msg.Error != "" {
			return d.classify(fmt.Errorf("pull image %s: %s", ref, msg.Error))
		}
	}
}

func (d *Driver) ensureNetwork(ctx context.Context) error {
	cfg := d.meshCfg()
	if n, err := d.cli.NetworkInspect(ctx, engine.NetworkName, network.InspectOptions{}); err == nil {
		if cfg != nil && cfg.DefaultRange != "" && len(n.IPAM.Config) > 0 && n.IPAM.Config[0].Subnet != cfg.DefaultRange {
			return engine.Permanent(fmt.Errorf("the default network here has the range %s, and every machine needs %s, the main machine's. Run `enclave agent leave` and join again",
				n.IPAM.Config[0].Subnet, cfg.DefaultRange))
		}
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return d.classify(fmt.Errorf("inspect network: %w", err))
	}
	opts := network.CreateOptions{Driver: "bridge", Labels: map[string]string{LabelManaged: "true"}}
	if d.opts.Agent {
		// Another machine takes the main machine's range for the default network (ADR-045).
		if cfg == nil || cfg.DefaultRange == "" {
			return errors.New("waiting for this machine's network settings from the main machine")
		}
		gw, _ := netip.ParsePrefix(cfg.DefaultRange)
		opts.IPAM = &network.IPAM{Driver: "default", Config: []network.IPAMConfig{{Subnet: cfg.DefaultRange, Gateway: gw.Addr().Next().String()}}}
	}
	_, err := d.cli.NetworkCreate(ctx, engine.NetworkName, opts)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return d.classify(fmt.Errorf("create network: %w", err))
	}
	return nil
}

func (d *Driver) inspect(ctx context.Context, name string) (*container.InspectResponse, error) {
	c, err := d.cli.ContainerInspect(ctx, name)
	if cerrdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, d.classify(fmt.Errorf("inspect %s: %w", name, err))
	}
	return &c, nil
}

func (d *Driver) remove(ctx context.Context, name string) error {
	rctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	err := d.cli.ContainerRemove(rctx, name, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return d.classify(fmt.Errorf("remove %s: %w", name, err))
	}
	return nil
}

// create makes a container in its placement's network, reachable as alias and as its private DNS name
// (ADR-016: every app revision shares the app's aliases; ADR-020).
func (d *Driver) create(ctx context.Context, r *domain.Resource, name, alias string, p plan, labels map[string]string, hostPorts map[int]int) error {
	cfg := &container.Config{Image: p.image, Env: p.env, Labels: labels, ExposedPorts: nat.PortSet{}}
	if len(p.cmd) > 0 {
		cfg.Cmd = p.cmd
	}
	if len(p.healthTest) > 0 {
		cfg.Healthcheck = &container.HealthConfig{Test: p.healthTest, Interval: 2 * time.Second, Timeout: 3 * time.Second, Retries: 30, StartPeriod: 5 * time.Second}
	}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		PortBindings:  nat.PortMap{},
		Resources:     container.Resources{Memory: int64(p.memoryMiB) << 20},
		// Never privileged, never host network, never host mounts (L3-006 §2).
	}
	for _, cp := range append([]int{p.port}, p.extraPorts...) {
		if cp <= 0 {
			continue
		}
		port := nat.Port(strconv.Itoa(cp) + "/tcp")
		cfg.ExposedPorts[port] = struct{}{}
		if p.publish {
			hp, err := d.hostPortFor(hostPorts[cp])
			if err != nil {
				return err
			}
			host.PortBindings[port] = []nat.PortBinding{{HostIP: d.opts.PublishAddress, HostPort: hp}}
		}
	}
	if p.nofile > 0 {
		host.Ulimits = []*container.Ulimit{{Name: "nofile", Soft: p.nofile, Hard: p.nofile}}
	}
	if p.mount != "" {
		vol := engine.VolumeName(r)
		vctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err := d.cli.VolumeCreate(vctx, volume.CreateOptions{Name: vol, Labels: map[string]string{LabelManaged: "true", LabelResourceID: r.ID}})
		cancel()
		if err != nil {
			return d.classify(fmt.Errorf("create volume %s: %w", vol, err))
		}
		host.Binds = []string{vol + ":" + p.mount}
	}
	host.Mounts = volumeMounts(p.mounts)
	netCfg, err := d.endpoint(ctx, p.placement, []string{alias, engine.PrivateDNS(r)})
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, name); err != nil {
		return d.classify(fmt.Errorf("create %s: %w", name, err))
	}
	return nil
}

// waitHealthy waits for the health check to pass (or, without one, for the container to keep
// running for settleFor). A container that exits reports its last log lines.
func (d *Driver) waitHealthy(ctx context.Context, name string, hasCheck bool) (*container.InspectResponse, error) {
	return d.waitHealthyWithin(ctx, name, hasCheck, 0)
}

// waitHealthyWithin is waitHealthy with a longer budget for engines whose first start is slow (0: the default).
func (d *Driver) waitHealthyWithin(ctx context.Context, name string, hasCheck bool, within time.Duration) (*container.InspectResponse, error) {
	if within <= 0 {
		within = healthyTimeout
	}
	deadline := time.Now().Add(within)
	var runningSince time.Time
	for {
		c, err := d.inspect(ctx, name)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("%s disappeared while starting", name)
		}
		st := c.State
		switch {
		case st == nil:
		case st.Status == "exited" || st.Status == "dead" || st.Restarting:
			tail := d.tail(ctx, name, 5)
			return nil, fmt.Errorf("%s stopped while starting (exit code %d). Last output: %s", name, st.ExitCode, tail)
		case hasCheck && st.Health != nil && st.Health.Status == "healthy":
			return c, nil
		case !hasCheck && st.Running:
			if runningSince.IsZero() {
				runningSince = time.Now()
			} else if time.Since(runningSince) >= settleFor {
				return c, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s did not become healthy within %s. Last output: %s", name, within, d.tail(ctx, name, 5))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (d *Driver) tail(ctx context.Context, name string, n int) string {
	lines, err := d.logs(ctx, name, n)
	if err != nil || len(lines) == 0 {
		return "(no output)"
	}
	var parts []string
	for _, l := range lines {
		parts = append(parts, l.Message)
	}
	return strings.Join(parts, " | ")
}

func hostPort(c *container.InspectResponse, port int) int {
	if c == nil || c.NetworkSettings == nil {
		return 0
	}
	for _, b := range c.NetworkSettings.Ports[nat.Port(strconv.Itoa(port)+"/tcp")] {
		if n, err := strconv.Atoi(b.HostPort); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func (d *Driver) outputs(r *domain.Resource, p plan, c *container.InspectResponse, in engine.EnsureInput) engine.Outputs {
	internal := engine.ContainerName(r)
	host := d.opts.PublishAddress
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	var out engine.Outputs
	switch s := in.Spec.(type) {
	case api.DatabaseSpec: // no host ports in a network without internet
		out = engine.DatabaseOutputs(r, s, host, hostPort(c, engine.DatabasePort(s.Engine)), hostPort(c, engine.ClickHouseNative))
	case api.SearchSpec:
		out = engine.SearchOutputs(r, host, hostPort(c, engine.SearchPort), engine.SearchVersions[s.Version])
	case api.EmailSpec:
		out = engine.EmailOutputs(r, s, host, hostPort(c, engine.SMTPPort), hostPort(c, engine.MailWebPort))
	case api.CDNSpec:
		out = engine.Outputs{"internalHost": internal, "internalPort": engine.CDNPort, "origin": in.CDN.Upstream}
	case api.CacheSpec:
		out = engine.Outputs{"internalHost": internal, "internalPort": engine.RedisPort}
		if hp := hostPort(c, engine.RedisPort); hp > 0 {
			out["host"], out["port"] = host, hp
		}
	default:
		out = engine.Outputs{"internalHost": internal}
		if p.port > 0 {
			out["internalPort"] = p.port
			if p.publish {
				out["host"], out["port"] = host, hostPort(c, p.port)
			}
		}
	}
	addPrivate(out, r, p.placement, c)
	return out
}

// addPrivate reports the container's address in its network and, once it carries the alias, its
// private DNS name (ADR-020: containers created before v0.4 gain it when next recreated).
func addPrivate(out engine.Outputs, r *domain.Resource, pl engine.Placement, c *container.InspectResponse) {
	if c == nil || c.NetworkSettings == nil {
		return
	}
	ep := c.NetworkSettings.Networks[pl.NetworkName()]
	if ep == nil {
		return
	}
	if ep.IPAddress != "" {
		out[engine.OutputPrivateIP] = ep.IPAddress
	}
	dns := engine.PrivateDNS(r)
	if slices.Contains(ep.Aliases, dns) || slices.Contains(ep.DNSNames, dns) {
		out[engine.OutputPrivateDNS] = dns
	}
}

// Delete implements engine.Driver.
func (d *Driver) Delete(ctx context.Context, r *domain.Resource, store engine.ObjectStore) error {
	switch r.Kind {
	case api.KindBucket:
		return d.deleteBucket(ctx, r, store)
	case api.KindVolume:
		return d.deleteVolume(ctx, r)
	case api.KindApp, api.KindJob:
		// Every container labelled with the resource: an app's revisions, a job's runs in progress.
		revs, err := d.revisions(ctx, r)
		if err != nil {
			return err
		}
		if err := d.removeOthers(ctx, revs, ""); err != nil {
			return err
		}
		return d.RemoveBuiltImages(ctx, r, nil) // a source app's images go with it
	}
	if err := d.remove(ctx, engine.ContainerName(r)); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.VolumeRemove(vctx, engine.VolumeName(r), true); err != nil && !cerrdefs.IsNotFound(err) {
		return d.classify(fmt.Errorf("remove volume: %w", err))
	}
	return nil
}

// Health implements engine.Driver.
func (d *Driver) Health(ctx context.Context, r *domain.Resource, store engine.ObjectStore) (engine.Health, error) {
	if r.Kind == api.KindBucket {
		return d.bucketHealth(ctx, r, store)
	}
	if r.Kind == api.KindNetwork {
		return d.networkHealth(ctx, r)
	}
	if r.Kind == api.KindApp {
		return d.appHealth(ctx, r)
	}
	if r.Kind == api.KindVolume {
		return d.volumeHealth(ctx, r)
	}
	name := engine.ContainerName(r)
	if r.Kind == api.KindDomain {
		name = engine.EdgeName
	}
	c, err := d.inspect(ctx, name)
	if err != nil {
		return engine.Missing, err
	}
	switch {
	case c == nil:
		return engine.Missing, nil
	case c.State == nil || !c.State.Running:
		return engine.Stopped, nil
	}
	return engine.Healthy, nil
}

// Restart implements engine.Driver. An app's replicas restart one after another.
func (d *Driver) Restart(ctx context.Context, r *domain.Resource) error {
	timeout := 10
	names, err := d.containersFor(ctx, r)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return engine.Permanent(fmt.Errorf("%s has no running revision to restart", r.Name))
	}
	for _, name := range names {
		rctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := d.cli.ContainerRestart(rctx, name, container.StopOptions{Timeout: &timeout})
		cancel()
		if err != nil {
			return d.classify(fmt.Errorf("restart: %w", err))
		}
	}
	return nil
}

// Logs implements engine.Driver: every replica's lines, in time order.
func (d *Driver) Logs(ctx context.Context, r *domain.Resource, tail int) ([]api.LogLine, error) {
	names, err := d.containersFor(ctx, r)
	if err != nil {
		return nil, err
	}
	all := []api.LogLine{}
	for _, name := range names {
		lines, err := d.logs(ctx, name, tail)
		if err != nil {
			return nil, err
		}
		all = append(all, lines...)
	}
	sortByTime(all)
	if len(all) > tail {
		all = all[len(all)-tail:]
	}
	return all, nil
}

// containersFor are the containers that answer for a resource: an app's serving replicas, or the
// resource's single container.
func (d *Driver) containersFor(ctx context.Context, r *domain.Resource) ([]string, error) {
	if r.Kind != api.KindApp {
		return []string{engine.ContainerName(r)}, nil
	}
	members, err := d.appContainers(ctx, r)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.name
	}
	return names, nil
}

func isNotFound(err error) bool { return cerrdefs.IsNotFound(err) }

func (d *Driver) logs(ctx context.Context, name string, tail int) ([]api.LogLine, error) {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rc, err := d.cli.ContainerLogs(lctx, name, container.LogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: strconv.Itoa(tail)})
	if cerrdefs.IsNotFound(err) {
		return []api.LogLine{}, nil
	}
	if err != nil {
		return nil, d.classify(err)
	}
	defer rc.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, rc); err != nil {
		return nil, err
	}
	lines := append(parseLines(stdout.String(), "stdout"), parseLines(stderr.String(), "stderr")...)
	sortByTime(lines)
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

func parseLines(s, stream string) []api.LogLine {
	var out []api.LogLine
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		stamp, msg, _ := strings.Cut(line, " ")
		t, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			t, msg = time.Time{}, line
		}
		out = append(out, api.LogLine{Time: t.UTC(), Stream: stream, Message: msg})
	}
	return out
}

func sortByTime(lines []api.LogLine) {
	slices.SortStableFunc(lines, func(a, b api.LogLine) int { return a.Time.Compare(b.Time) })
}

// explainStartFailure turns start failures a retry can't fix into a permanent error saying what to do.
func explainStartFailure(err error) error {
	if strings.Contains(err.Error(), "MongoDB cannot start: Linux kernel versions 6.19") {
		return engine.Permanent(errors.New("MongoDB 8 refuses the Linux kernel the container engine runs (6.19 to 7.0.13; MongoDB SERVER-121912). " +
			"Create the database with version 7.0, or update Docker Desktop or the machine's kernel to 7.0.14 or later and use Retry"))
	}
	return err
}

// hostPortFor returns the host port to publish on: wanted when set, otherwise a free one chosen now.
// Leaving the choice to Docker isn't enough: it picks a new port each time the container restarts (seen
// on Docker Desktop 29.7.2), and every address EnclaveStack reported would go stale.
func (d *Driver) hostPortFor(wanted int) (string, error) {
	return hostPortOn(d.opts.PublishAddress, wanted)
}

// hostPortOn is hostPortFor on another address (a VM link's loopback ports).
func hostPortOn(addr string, wanted int) (string, error) {
	if wanted > 0 {
		return strconv.Itoa(wanted), nil
	}
	l, err := net.Listen("tcp", net.JoinHostPort(addr, "0"))
	if err != nil {
		return "", fmt.Errorf("find a free port on %s: %w", addr, err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}
