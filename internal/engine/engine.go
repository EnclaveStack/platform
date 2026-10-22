// Package engine defines how the reconciler turns resources into running services (ADR-006).
// Drivers must be idempotent: Ensure inspects first and only creates what is missing or outdated,
// so repeating it after a crash is safe (L3-007 §4).
package engine

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// ErrUnavailable means the engine cannot be reached (Docker stopped). It is transient.
var ErrUnavailable = errors.New("container engine unavailable")

// ErrMachineOffline means the resource's machine doesn't answer (ADR-046). Like ErrUnavailable it is
// transient and not counted as an attempt; the wrapped message names the machine and since when.
var ErrMachineOffline = errors.New("machine offline")

// PermanentError marks a failure that retrying cannot fix (L3-007 §1).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err as permanent.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err (or anything it wraps) is permanent.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// Reporter receives step progress while a driver works.
type Reporter interface {
	Step(name string, state api.StepState)
}

// Narrator is implemented by reporters that also show a progress message and record events — how a
// rolling deploy reports health-check attempts and traffic switches (ADR-016, L2-005 DEP-06).
type Narrator interface {
	Note(message string)
	Event(typ, message string)
}

// Note shows a progress message when the reporter supports it.
func Note(r Reporter, message string) {
	if n, ok := r.(Narrator); ok {
		n.Note(message)
	}
}

// Event records an event when the reporter supports it.
func Event(r Reporter, typ, message string) {
	if n, ok := r.(Narrator); ok {
		n.Event(typ, message)
	}
}

// Output keys shared by drivers and the reconciler.
const (
	OutputServingRevision = "servingRevision"
	OutputRunningReplicas = "runningReplicas"
	OutputTargets         = "targets"
	OutputHealthyTargets  = "healthyTargets"
	OutputLatestRevision  = "latestRevision"
	OutputLastDeploy      = "lastDeploy"
	// OutputFilledFrom is set on a volume once it holds its snapshot's copy; from then on the snapshot
	// may be deleted (L2-007 SNP-06), so the volume no longer checks or references it.
	OutputFilledFrom = "filledFrom"
	// OutputPower is a VM's power state; it is set once the VM has been created (L3-002 §8.2).
	OutputPower = "power"
)

// Deploy outcomes reported in OutputLastDeploy.
const (
	DeployInProgress = "in_progress"
	DeploySucceeded  = "succeeded"
	DeployFailed     = "failed"
)

// ObjectStore credentials for buckets.
type ObjectStore struct {
	AccessKey string
	SecretKey string
}

// EnsureInput is everything a driver needs to converge one resource. Secret material is resolved by
// the reconciler and never logged.
type EnsureInput struct {
	Resource *domain.Resource
	// Spec is the typed spec: api.AppSpec, api.DatabaseSpec, api.CacheSpec or api.BucketSpec.
	Spec any
	// Env is the full container environment for apps (plain and resolved secret values).
	Env map[string]string
	// Password is a database's owner password.
	Password string
	// ObjectStore holds the object store's credentials for buckets.
	ObjectStore ObjectStore
	// Recreate forces the container to be recreated and the image pulled again (redeploy).
	Recreate bool
	// Revision is the number an app's next revision takes. Numbers only grow — a discarded revision's
	// number is never reused (ADR-016). 0 lets the driver choose (one above the newest it finds).
	Revision int64
	// Placement is the network the resource's containers join (ADR-020); zero means the default network.
	Placement Placement
	// Firewall is the resource's own policy (ADR-021): a new container gets it right after it starts and
	// before it takes traffic. Nil means no firewall names the resource.
	Firewall *firewall.Policy
	// LoadBalancer is a load balancer's resolved configuration (ADR-022).
	LoadBalancer *lb.Config
	// Started, when set, runs once new containers of the resource are up and before they take traffic,
	// so firewalls that name the resource as a source allow its new addresses (ADR-021).
	Started func(ctx context.Context) error
	// Mounts are the filesystem volumes an app's containers mount (ADR-027).
	Mounts []Mount
	// HostPorts are the host ports a VM's link container keeps across recreation, by container port
	// (ADR-026): QEMU reconnects to the same frame port, and the SSH address stays.
	HostPorts map[int]int
	// CDN is a CDN's resolved origin and cache settings (ADR-033).
	CDN *CDNConfig
	// Relay is an email service's relay with its credentials resolved (ADR-032); nil in capture mode.
	Relay *Relay
}

// CDNConfig is what a CDN's cache container needs (ADR-033). Upstream is host:port on the origin's
// network; a bucket origin adds the bucket as PathPrefix and the object store's host as HostHeader.
type CDNConfig struct {
	Upstream    string
	PathPrefix  string
	HostHeader  string
	TTLSeconds  int
	MaxSizeMiB  int
	OriginIsApp bool
}

// Relay is where an email service forwards mail (EML-02); the password never reaches logs.
type Relay struct {
	Host     string
	Port     int
	TLS      string
	Username string
	Password string
}

// BrokerConfig is the broker's whole configuration file, rendered by the reconciler (ADR-029). Empty
// means no queue or topic exists: the broker is removed.
type BrokerConfig struct {
	Config []byte
	Empty  bool
}

// BrokerStatus reports whether the broker runs and where. Address is host:port on the machine.
type BrokerStatus struct {
	Available bool
	Message   string
	Address   string
}

// TranscodeRun is one file converted by FFmpeg in a container with no network (ADR-034). InputName is
// the name the input gets inside it (input.<ext>).
type TranscodeRun struct {
	RunID      string
	ResourceID string
	Name       string // the transcoder's name
	InputName  string
	InputSize  int64
	Presets    []string
	MemoryMiB  int
	Timeout    time.Duration
}

// Mount is a filesystem volume mounted into a container (ADR-027). Volume is the engine volume's name.
type Mount struct {
	Volume   string
	Path     string
	ReadOnly bool
}

// Placement is where a container joins. The reconciler resolves it from the spec's network and subnet.
type Placement struct {
	// Address is an address to keep, given by the control plane when a resource moves (ADR-047); "" lets
	// the driver choose.
	Address string
	// Network is the engine network's name; "" means NetworkName.
	Network string
	// Subnet is the CIDR to take a fixed address from, or "" for the engine's automatic range (the
	// network's first subnet).
	Subnet string
}

// NetworkName returns the engine network, defaulting to the enclave network.
func (p Placement) NetworkName() string {
	if p.Network == "" {
		return NetworkName
	}
	return p.Network
}

// NetworkInput is everything a driver needs to converge a network (ADR-020).
type NetworkInput struct {
	Resource *domain.Resource
	Spec     api.NetworkSpec
}

// Outputs are the non-secret facts a client reads back (L3-002 §3).
type Outputs map[string]any

// Health is what drift detection observes.
type Health int

const (
	// Healthy means the runtime exists and is running.
	Healthy Health = iota
	// Missing means the runtime does not exist.
	Missing
	// Stopped means it exists but is not running.
	Stopped
)

// Info describes the engine and host.
type Info struct {
	Version          string
	MemoryTotalBytes int64
	CPUs             int
}

// Route is one domain the edge proxy serves (ADR-015). Upstream is host:port on the enclave network:
// the app's stable alias, so deploys never need an edge change (ADR-016).
type Route struct {
	Hostname     string
	Upstream     string
	TLS          string // api.TLSAuto, api.TLSInternal or api.TLSOff
	RedirectHTTP bool
	// DNS01 marks a domain whose certificate the control plane obtains (ADR-024): the edge serves the
	// loaded certificate and never obtains one itself.
	DNS01 bool
}

// LoadedCertificate is a certificate the edge serves as given (ADR-024).
type LoadedCertificate struct {
	Hostname string
	ChainPEM []byte
	KeyPEM   []byte
}

// EdgeConfig is the edge proxy's whole configuration, rendered from every domain in the store.
type EdgeConfig struct {
	Routes       []Route
	ACMEEmail    string
	Certificates []LoadedCertificate
}

// Certificate states (L3-002 §6.2).
const (
	CertPending = "pending"
	CertIssued  = "issued"
	CertFailed  = "failed"
	CertNone    = "none"
)

// Certificate is what the edge serves for one hostname.
type Certificate struct {
	State     string
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
}

// EdgeStatus reports whether the edge proxy runs and where it listens.
type EdgeStatus struct {
	Available bool
	Message   string
	// Address is where 80/443 are bound (127.0.0.1 unless the operator opened them up).
	Address   string
	HTTPPort  int
	HTTPSPort int
}

// JobRun is one execution of a job: a one-off container (ADR-017).
type JobRun struct {
	RunID      string
	ResourceID string
	Name       string // the job's name
	Image      string
	Command    []string
	Env        map[string]string
	MemoryMiB  int
	Placement  Placement
	Mounts     []Mount
}

// JobResult is how a run ended. Output is the last OutputLimit bytes, stdout and stderr interleaved.
type JobResult struct {
	ExitCode int
	Output   []byte
	TimedOut bool
}

// DumpInfo describes a logical backup (ADR-018).
type DumpInfo struct {
	Ext    string // file extension of the format: "pgdump" or "rdb"
	Engine string // engine and version, e.g. "postgres 16", recorded with the object
}

// OutputLimit bounds a run's stored output (L2-005 JOB-04).
const OutputLimit = 64 << 10

// ErrJobMissing means a run's container doesn't exist (never started, or removed outside EnclaveStack).
var ErrJobMissing = errors.New("the run's container doesn't exist")

// Driver converges resources onto an engine.
type Driver interface {
	Name() string
	Ping(ctx context.Context) error
	Info(ctx context.Context) (Info, error)
	Ensure(ctx context.Context, in EnsureInput, report Reporter) (Outputs, error)
	Delete(ctx context.Context, r *domain.Resource, store ObjectStore) error
	Health(ctx context.Context, r *domain.Resource, store ObjectStore) (Health, error)
	Restart(ctx context.Context, r *domain.Resource) error
	Logs(ctx context.Context, r *domain.Resource, tail int) ([]api.LogLine, error)

	// ConfigureEdge applies the whole edge configuration atomically: it starts the edge proxy when there
	// are routes and stops it when there are none (ADR-015).
	ConfigureEdge(ctx context.Context, cfg EdgeConfig) error
	// Certificate reports the certificate the edge serves for hostname.
	Certificate(ctx context.Context, hostname string) (Certificate, error)
	// EdgeStatus never fails: an unreachable edge is reported as unavailable with a reason.
	EdgeStatus(ctx context.Context) EdgeStatus
	// RootCA returns the PEM root certificate of the edge's internal CA, or ErrNoCA before it exists.
	RootCA(ctx context.Context) ([]byte, error)

	// StartJob starts a run's container; starting a run that already has one does nothing.
	StartJob(ctx context.Context, run JobRun) error
	// WaitJob waits for a run to end, stops it after timeout, collects its output and removes it.
	// ErrJobMissing when the run has no container.
	WaitJob(ctx context.Context, runID string, timeout time.Duration) (JobResult, error)

	// Build builds an image from a tar build context with BuildKit, writing readable log lines to log
	// (ADR-019). A failing Dockerfile is a permanent error naming the failing step.
	Build(ctx context.Context, in BuildInput, log io.Writer) (BuildResult, error)
	// HasImage reports whether an image is present on the machine r runs on (a source app's last build).
	HasImage(ctx context.Context, r *domain.Resource, ref string) (bool, error)
	// RemoveBuiltImages removes an app's built images except those tagged in keep.
	RemoveBuiltImages(ctx context.Context, r *domain.Resource, keep []string) error

	// EnsureNetwork creates a network if it is missing and connects EnclaveStack's own services to it
	// (ADR-020). DeleteNetwork disconnects them and removes it.
	EnsureNetwork(ctx context.Context, in NetworkInput) (Outputs, error)
	DeleteNetwork(ctx context.Context, r *domain.Resource) error

	// ApplyFirewall loads a policy into every running container of a resource, skipping containers that
	// already have exactly it; nil clears EnclaveStack's rules (ADR-021). It returns how many containers it
	// changed. Endpoints returns the addresses of a resource's running containers.
	ApplyFirewall(ctx context.Context, r *domain.Resource, p *firewall.Policy) (int, error)
	Endpoints(ctx context.Context, r *domain.Resource) ([]netip.Addr, error)
	// LoadBalancerStatus reads how many replicas a load balancer found and how many are healthy.
	LoadBalancerStatus(ctx context.Context, r *domain.Resource) (lb.Status, error)

	// Dump streams a logical backup of a ready database or cache into w (ADR-018).
	Dump(ctx context.Context, in EnsureInput, w io.Writer) (DumpInfo, error)
	// Restore replaces a database's or cache's data with a dump read from src, starting it as needed.
	Restore(ctx context.Context, in EnsureInput, src io.Reader) error

	// EnsureVolume creates a filesystem volume's engine volume if it is missing (ADR-027); Delete removes
	// it, and Health reports it missing. VolumeUsage reports the bytes its files use.
	EnsureVolume(ctx context.Context, r *domain.Resource) error
	// EnsureVMLink converges a VM's link container from in.Spec (api.VMSpec), in.Placement, in.Firewall
	// and in.HostPorts, and returns the VM's network outputs (ADR-026). DeleteVMLink removes it.
	EnsureVMLink(ctx context.Context, in EnsureInput) (Outputs, error)
	DeleteVMLink(ctx context.Context, r *domain.Resource) error
	VolumeUsage(ctx context.Context, r *domain.Resource) (int64, error)
	// SnapshotVolume writes a filesystem volume's files into w as a tar archive, while apps may be using
	// it; RestoreVolume unpacks one into a volume (ADR-028).
	SnapshotVolume(ctx context.Context, r *domain.Resource, w io.Writer) error
	RestoreVolume(ctx context.Context, r *domain.Resource, src io.Reader) error

	// ConfigureBroker writes the broker's configuration and starts it, or reloads it in place when it
	// runs; an empty configuration removes it and its data (ADR-029). BrokerStatus never fails.
	ConfigureBroker(ctx context.Context, cfg BrokerConfig) (BrokerStatus, error)
	BrokerStatus(ctx context.Context) BrokerStatus
	// PurgeCache empties a CDN's cache; CacheUsage reports the bytes it holds (ADR-033).
	PurgeCache(ctx context.Context, r *domain.Resource) error
	CacheUsage(ctx context.Context, r *domain.Resource) (int64, error)
	// SetBucketPublicRead lets anyone read a bucket's objects by key (no listing), or takes that away (CDN-02).
	SetBucketPublicRead(ctx context.Context, bucket string, public bool, store ObjectStore) error
	// Transcode runs FFmpeg on src in a container without network and hands each file it wrote to out,
	// streaming (ADR-034). The result's Output holds the end of FFmpeg's output.
	Transcode(ctx context.Context, run TranscodeRun, src io.Reader, out func(name string, size int64, r io.Reader) error) (JobResult, error)

	// SetProjectStoreAccess makes the object store user of a project reach exactly buckets, or removes it
	// when there are none (ADR-035). root is the store's own credentials.
	SetProjectStoreAccess(ctx context.Context, user ObjectStore, buckets []string, root ObjectStore) error

	// v0.7, read-only (ADR-037, ADR-040).
	// Stats reads the cumulative counters of a resource's running containers now; none when nothing runs.
	Stats(ctx context.Context, r *domain.Resource) ([]ContainerStats, error)
	// EdgeRequests reads the edge's cumulative request counters by hostname; empty when it doesn't run.
	EdgeRequests(ctx context.Context) (map[string]RequestCounters, error)
	// LogsSince returns up to limit lines of every container of r (all replicas and revisions), oldest
	// first, each with its container's name: lines at or after the container's entry in since (so the
	// caller sees whether the checkpoint's own line is still kept), or after fallback for a container it
	// doesn't name.
	LogsSince(ctx context.Context, r *domain.Resource, since map[string]time.Time, fallback time.Time, limit int) ([]ContainerLogLine, error)

	// v0.8 (ADR-045): networks across machines. Each is a call to one machine's engine.
	// ConfigureMesh applies this machine's whole mesh configuration; with Enabled false it removes the mesh,
	// its stand-ins and the address pools. MeshStatus reads the tunnel's public key and handshakes.
	ConfigureMesh(ctx context.Context, cfg MeshConfig) (MeshStatus, error)
	MeshStatus(ctx context.Context) (MeshStatus, error)
	// NetworkEndpoints lists this machine's EnclaveStack networks with their ranges and the managed
	// containers on them (stand-ins and the mesh left out).
	NetworkEndpoints(ctx context.Context) ([]NetworkInfo, error)

	// v0.8 (ADR-047): moving a resource with its data.
	// StopResource stops a resource's containers and keeps them, with their data; Restart starts them again.
	StopResource(ctx context.Context, r *domain.Resource) error
	// ExportData writes a database's, cache's, search or email service's data volume into w as a tar
	// stream, consistent while it is stopped; ImportData fills the same volume on this machine from one.
	ExportData(ctx context.Context, r *domain.Resource, w io.Writer) error
	ImportData(ctx context.Context, r *domain.Resource, src io.Reader) error
}

// MeshConfig is one machine's part of the mesh (ADR-045).
type MeshConfig struct {
	Enabled bool
	// TunnelPort is the tunnel's UDP port, published on TunnelBind (default every address).
	TunnelPort int
	TunnelBind string
	// DefaultRange is the default network's range on every machine: the main machine's.
	DefaultRange string
	// Networks are the ranges of every EnclaveStack network, by engine name, so the mesh joins them all.
	Networks map[string]string
	Peers    []MeshPeer
	// Pools are the addresses this machine gives its containers, by engine network.
	Pools    map[string][]string
	StandIns []StandIn
}

// MeshMember is a joined machine as the mesh sync sees it: its engine while it is online, and its tunnel.
type MeshMember struct {
	ID, Name  string
	Main      bool
	Online    bool
	Driver    Driver
	PublicKey string
	// Endpoint is where the other machines reach its tunnel ("host:port"), "" when it can't be reached.
	Endpoint   string
	TunnelPort int
}

// MeshPeer is another machine of the mesh: its tunnel's key and address, and the addresses that live there.
type MeshPeer struct {
	MachineID string
	PublicKey string
	Endpoint  string
	Addresses []string
}

// StandIn holds, on this machine, a container's address and names that live on another one.
type StandIn struct {
	Network string
	Address string
	Aliases []string
	// Of names the container it stands in for, and Machine the machine it is on.
	Of      string
	Machine string
}

// MeshStatus is a machine's tunnel: its public key and when it last heard from each peer, by public key.
type MeshStatus struct {
	PublicKey  string
	Handshakes map[string]time.Time
}

// NetworkInfo is one engine network on a machine and what is on it.
type NetworkInfo struct {
	Name    string
	Subnet  string
	Members []NetworkMember
}

// NetworkMember is one managed container on a network: its address and the names it answers to.
type NetworkMember struct {
	Container  string
	Address    string
	Aliases    []string
	ResourceID string
}

// ContainerStats are one container's counters. CPUSeconds, the network and disk byte counts are
// cumulative since the container started; the sampler stores their rates (ADR-037).
type ContainerStats struct {
	Container   string
	ID          string
	StartedAt   time.Time
	Restarts    int
	CPUSeconds  float64
	MemoryBytes int64
	MemoryLimit int64
	NetRx       uint64
	NetTx       uint64
	DiskRead    uint64
	DiskWrite   uint64
}

// RequestCounters are an edge hostname's cumulative requests, 5xx responses and seconds spent answering.
type RequestCounters struct {
	Requests        float64
	Errors5xx       float64
	DurationSeconds float64
}

// ContainerLogLine is a log line with the container it came from.
type ContainerLogLine struct {
	api.LogLine
	Container string
}

// BuildInput is one image build of a source app.
type BuildInput struct {
	Resource   *domain.Resource
	Tag        string
	ContextDir string   // the checked build context directory
	Dockerfile string   // relative to ContextDir
	Excludes   []string // .dockerignore patterns
	BuildArgs  map[string]string
}

// BuildResult is what a build produced.
type BuildResult struct{ ImageID string }

// ErrNoCA means the internal CA has not been created yet (no internal-TLS domain so far).
var ErrNoCA = errors.New("the internal certificate authority doesn't exist yet")

// ContainerName is the Docker container name for a resource (L3-005 §2). For apps it is the stable
// network alias every revision carries — the app's internalHost (ADR-016).
// In a project other than default the name carries the project (ADR-035).
func ContainerName(r *domain.Resource) string {
	return "es-" + string(r.Kind) + "-" + domain.Qualified(r.Project, r.Name)
}

// RevisionName is the container name of one app revision (ADR-016).
func RevisionName(r *domain.Resource, revision int64) string {
	return ContainerName(r) + "-r" + strconv.FormatInt(revision, 10)
}

// DockerNetwork is the engine network of a network resource (ADR-020, ADR-035).
func DockerNetwork(project, name string) string { return "es-net-" + domain.Qualified(project, name) }

// PrivateDNS is the name a resource answers to inside its network (L2-006 NET-07): "<name>.<kind>.internal"
// in the default project, "<name>.<kind>.<project>.internal" elsewhere (ADR-035).
func PrivateDNS(r *domain.Resource) string {
	if p := r.ProjectName(); p != api.DefaultProject {
		return r.Name + "." + string(r.Kind) + "." + p + ".internal"
	}
	return r.Name + "." + string(r.Kind) + ".internal"
}

// Output keys of placed resources.
const (
	OutputPrivateIP  = "privateIp"
	OutputPrivateDNS = "privateDns"
	// A VM's link (ADR-026): the loopback port QEMU streams frames to, and where SSH answers.
	OutputLinkPort   = "linkPort"
	OutputSSHAddress = "sshAddress"
	OutputHost       = "host"
)

// VM link container ports (ADR-026).
const (
	VMLinkFramePort = 61998
	VMLinkNetwork   = "es-vmlink"
)

// VolumeName is the data volume for a resource.
func VolumeName(r *domain.Resource) string { return "es-" + r.ID + "-data" }

// FilesystemVolume is the engine volume of a filesystem volume resource (ADR-027).
func FilesystemVolume(id string) string { return "es-vol-" + id }

// v0.6 container names and ports (ADR-029 – ADR-033).
const (
	BrokerName       = "es-system-broker"
	BrokerVolume     = "es-system-broker-data"
	BrokerPort       = 4222
	MySQLPort        = 3306
	MongoPort        = 27017
	ClickHousePort   = 8123
	ClickHouseNative = 9000
	SearchPort       = 9200
	SMTPPort         = 1025
	MailWebPort      = 8025
	CDNPort          = 8080
	// OutputNativePort and OutputCachedBytes are outputs the reconciler reads back.
	OutputNativePort  = "nativePort"
	OutputCachedBytes = "cachedBytes"
)

// BrokerInternalURL is where apps reach the broker inside EnclaveStack's networks.
const BrokerInternalURL = "nats://" + BrokerName + ":4222"

// Container ports by kind.
const (
	PostgresPort    = 5432
	RedisPort       = 6379
	ObjectStorePort = 9000
	ObjectStoreName = "es-system-objectstore"
	ProbeName       = "es-system-probe"
	EdgeName        = "es-system-edge"
	NetworkName     = "enclave"
)
