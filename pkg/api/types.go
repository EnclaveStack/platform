package api

import (
	"bytes"
	"encoding/json"
	"time"
)

// Kind is a service kind (L2-004 §1).
type Kind string

const (
	KindApp      Kind = "app"
	KindDatabase Kind = "database"
	KindCache    Kind = "cache"
	KindBucket   Kind = "bucket"
	KindSecret   Kind = "secret"
	KindDomain   Kind = "domain"
	KindJob      Kind = "job"
	KindBackup   Kind = "backup"
	// v0.4 (L2-006).
	KindNetwork      Kind = "network"
	KindFirewall     Kind = "firewall"
	KindLoadBalancer Kind = "loadbalancer"
	KindZone         Kind = "zone"
	KindRecord       Kind = "record"
	// v0.5 (L2-007).
	KindVM       Kind = "vm"
	KindVolume   Kind = "volume"
	KindSnapshot Kind = "snapshot"
	// v0.6 (L2-008).
	KindQueue      Kind = "queue"
	KindTopic      Kind = "topic"
	KindSearch     Kind = "search"
	KindTranscoder Kind = "transcoder"
	KindCDN        Kind = "cdn"
	KindEmail      Kind = "email"
	// v0.7 (L2-009).
	KindDashboard   Kind = "dashboard"
	KindAlert       Kind = "alert"
	KindChannel     Kind = "channel"
	KindLogExport   Kind = "logexport"
	KindSecretStore Kind = "secretstore"
	// v0.8 (L2-010).
	KindStack Kind = "stack"
)

// Kinds lists every kind in display order.
var Kinds = []Kind{KindApp, KindDatabase, KindCache, KindBucket, KindSecret, KindDomain, KindJob, KindBackup,
	KindNetwork, KindFirewall, KindLoadBalancer, KindZone, KindRecord, KindVM, KindVolume, KindSnapshot,
	KindQueue, KindTopic, KindSearch, KindTranscoder, KindCDN, KindEmail,
	KindDashboard, KindAlert, KindChannel, KindLogExport, KindSecretStore, KindStack}

// IDPrefix is the prefix of resource IDs per kind (L3-005 §2).
var IDPrefix = map[Kind]string{KindApp: "app", KindDatabase: "db", KindCache: "cache", KindBucket: "bkt", KindSecret: "sec", KindDomain: "dom", KindJob: "job", KindBackup: "bak",
	KindNetwork: "net", KindFirewall: "fw", KindLoadBalancer: "lb", KindZone: "zone", KindRecord: "rec",
	KindVM: "vm", KindVolume: "vol", KindSnapshot: "snap",
	KindQueue: "q", KindTopic: "tpc", KindSearch: "srch", KindTranscoder: "tc", KindCDN: "cdn", KindEmail: "mail",
	KindDashboard: "dash", KindAlert: "alrt", KindChannel: "chan", KindLogExport: "logx", KindSecretStore: "sst", KindStack: "stk"}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	_, ok := IDPrefix[k]
	return ok
}

// Phase is a resource's lifecycle phase (L3-002 §5).
type Phase string

const (
	PhasePending      Phase = "pending"
	PhaseProvisioning Phase = "provisioning"
	PhaseReady        Phase = "ready"
	PhaseUpdating     Phase = "updating"
	PhaseDeleting     Phase = "deleting"
	PhaseFailed       Phase = "failed"
)

// Phases lists every phase.
var Phases = []Phase{PhasePending, PhaseProvisioning, PhaseReady, PhaseUpdating, PhaseDeleting, PhaseFailed}

// InProgress reports whether the reconciler still has work to do for the phase.
func (p Phase) InProgress() bool {
	return p == PhasePending || p == PhaseProvisioning || p == PhaseUpdating || p == PhaseDeleting
}

// StepState is the state of one provisioning step.
type StepState string

const (
	StepPending StepState = "pending"
	StepActive  StepState = "active"
	StepDone    StepState = "done"
	StepFailed  StepState = "failed"
)

// Step is one named provisioning step, e.g. pull, create, start, healthy.
type Step struct {
	Name  string    `json:"name"`
	State StepState `json:"state"`
}

// Status is the observed state of a resource.
type Status struct {
	Phase              Phase     `json:"phase"`
	Message            string    `json:"message"`
	ObservedGeneration int64     `json:"observedGeneration"`
	Attempts           int       `json:"attempts"`
	Steps              []Step    `json:"steps"`
	LastTransitionAt   time.Time `json:"lastTransitionAt"`
}

// Resource is the API representation of any kind. Spec holds the kind's spec as JSON.
type Resource struct {
	ID         string  `json:"id"`
	Project    string  `json:"project"`
	Kind       Kind    `json:"kind"`
	Name       string  `json:"name"`
	Generation int64   `json:"generation"`
	OwnerID    *string `json:"ownerId"`
	// Stack is the ID of the stack the resource belongs to (ADR-048), or null.
	Stack     *string         `json:"stack"`
	Spec      json.RawMessage `json:"spec"`
	Status    Status          `json:"status"`
	Outputs   map[string]any  `json:"outputs"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// AppSpec is the desired state of an app.
type AppSpec struct {
	// Exactly one of Image and Source (ADR-019).
	Image     string            `json:"image,omitempty"`
	Source    *GitSource        `json:"source,omitempty"`
	Port      int               `json:"port,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	SecretEnv map[string]string `json:"secretEnv,omitempty"`
	Publish   bool              `json:"publish,omitempty"`
	MemoryMiB int               `json:"memoryMiB,omitempty"`
	// HealthCheck decides when a new revision may take traffic (ADR-016). Nil means the default:
	// tcp on the port when there is one, otherwise "running for 5 s without restarting".
	HealthCheck *HealthCheck `json:"healthCheck,omitempty"`
	// Strategy is rolling (default) or recreate; a published app always deploys with recreate.
	Strategy string `json:"strategy,omitempty"`
	// Network and Subnet place the app (ADR-020); empty means the default network.
	Network string `json:"network,omitempty"`
	Subnet  string `json:"subnet,omitempty"`
	// Replicas is how many containers each revision runs, 1–10 (ADR-022).
	Replicas int `json:"replicas,omitempty"`
	// Volumes mounts filesystem volumes into every container (ADR-027).
	Volumes []VolumeMount `json:"volumes,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// GitSource builds an app from a Git repository and its Dockerfile (ADR-019, L2-005 BLD-01).
type GitSource struct {
	Git        string `json:"git"`                  // https://… or ssh://… (http:// for localhost only)
	Ref        string `json:"ref"`                  // branch, tag or commit
	Dockerfile string `json:"dockerfile,omitempty"` // default "Dockerfile", relative to Context
	Context    string `json:"context,omitempty"`    // default "."
	// CredentialsSecret names a secret holding {"token"} (https) or {"privateKey","knownHosts"} (ssh).
	CredentialsSecret string            `json:"credentialsSecret,omitempty"`
	BuildArgs         map[string]string `json:"buildArgs,omitempty"`
}

// HealthCheck types.
const (
	HealthHTTP    = "http"
	HealthTCP     = "tcp"
	HealthCommand = "command"
	HealthNone    = "none"
)

// Deploy strategies.
const (
	StrategyRolling  = "rolling"
	StrategyRecreate = "recreate"
)

// HealthCheck is how the control plane decides a revision is healthy (L3-002 §6.2).
type HealthCheck struct {
	Type               string   `json:"type"`
	Path               string   `json:"path,omitempty"`
	Port               int      `json:"port,omitempty"`
	Command            []string `json:"command,omitempty"`
	IntervalSeconds    int      `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int      `json:"timeoutSeconds,omitempty"`
	Retries            int      `json:"retries,omitempty"`
	StartPeriodSeconds int      `json:"startPeriodSeconds,omitempty"`
}

// DatabaseSpec is the desired state of a database.
type DatabaseSpec struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
	Network string `json:"network,omitempty"`
	Subnet  string `json:"subnet,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// CacheSpec is the desired state of a cache.
type CacheSpec struct {
	Engine       string `json:"engine"`
	Version      string `json:"version"`
	MaxMemoryMiB int    `json:"maxMemoryMiB,omitempty"`
	Network      string `json:"network,omitempty"`
	Subnet       string `json:"subnet,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// BucketSpec is the desired state of a bucket.
type BucketSpec struct {
	Versioning bool `json:"versioning"`
}

// TLS modes for domains (L2-005 DOM-03, DOM-04).
const (
	TLSAuto     = "auto"
	TLSInternal = "internal"
	TLSOff      = "off"
)

// DomainSpec routes a hostname to an app port through the edge proxy (ADR-015).
type DomainSpec struct {
	Hostname string `json:"hostname"`
	App      string `json:"app"`
	Port     int    `json:"port,omitempty"`
	TLS      string `json:"tls,omitempty"`
	// RedirectHTTP sends plain HTTP to HTTPS (default true; ignored when TLS is off).
	RedirectHTTP *bool `json:"redirectHttp,omitempty"`
	// Zone names a zone resource: the certificate comes through DNS-01, and a wildcard hostname is
	// allowed (ADR-024).
	Zone string `json:"zone,omitempty"`
}

// Overlap policies for jobs (L2-005 JOB-03).
const (
	OverlapForbid = "forbid"
	OverlapAllow  = "allow"
)

// JobSpec runs a command on a schedule (L2-005 JOB-01 – JOB-08). Exactly one of App or Image is set: an
// app's image, environment and secret environment are inherited, the job's own entries override them.
type JobSpec struct {
	Schedule       string            `json:"schedule"`
	TimeZone       string            `json:"timeZone,omitempty"`
	App            string            `json:"app,omitempty"`
	Image          string            `json:"image,omitempty"`
	Command        []string          `json:"command,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	SecretEnv      map[string]string `json:"secretEnv,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	Overlap        string            `json:"overlap,omitempty"`
	MemoryMiB      int               `json:"memoryMiB,omitempty"`
	// Network and Subnet place an image job; an app job runs in its app's network (ADR-020).
	Network string `json:"network,omitempty"`
	Subnet  string `json:"subnet,omitempty"`
	// Volumes mounts filesystem volumes into each run's container (ADR-027).
	Volumes []VolumeMount `json:"volumes,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// BackupSource names what a backup copies: exactly one of a database or a cache.
type BackupSource struct {
	Database string `json:"database,omitempty"`
	Cache    string `json:"cache,omitempty"`
}

// S3Destination is external S3-compatible storage. The credentials secret holds {"accessKey","secretKey"}.
type S3Destination struct {
	Endpoint          string `json:"endpoint"`
	Region            string `json:"region,omitempty"`
	Bucket            string `json:"bucket"`
	CredentialsSecret string `json:"credentialsSecret"`
}

// BackupDestination is exactly one of a EnclaveStack bucket or external S3.
type BackupDestination struct {
	Bucket string         `json:"bucket,omitempty"`
	S3     *S3Destination `json:"s3,omitempty"`
}

// BackupSpec takes scheduled logical backups of a database or cache (L2-005 BAK-01 – BAK-09).
type BackupSpec struct {
	Source         BackupSource      `json:"source"`
	Destination    BackupDestination `json:"destination"`
	Prefix         string            `json:"prefix,omitempty"`
	Schedule       string            `json:"schedule,omitempty"`
	TimeZone       string            `json:"timeZone,omitempty"`
	Keep           int               `json:"keep,omitempty"`
	KeepDays       int               `json:"keepDays,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

// Restore modes (L2-005 BAK-06, BAK-07).
const (
	RestoreNew     = "new"
	RestoreReplace = "replace"
)

// RestoreRequest is the body of POST /resources/{backup}/actions/restore.
type RestoreRequest struct {
	RunID       string `json:"runId"`
	Mode        string `json:"mode"`
	Name        string `json:"name,omitempty"`
	ConfirmName string `json:"confirmName,omitempty"`
}

// Run triggers and statuses (L3-002 §6.2).
const (
	TriggerSchedule   = "schedule"
	TriggerManual     = "manual"
	TriggerCatchUp    = "catch_up"
	TriggerPreRestore = "pre_restore"

	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunTimedOut  = "timed_out"
	RunSkipped   = "skipped"
)

// RunStatuses and RunTriggers are the allowed values.
var (
	RunStatuses = []string{RunRunning, RunSucceeded, RunFailed, RunTimedOut, RunSkipped}
	RunTriggers = []string{TriggerSchedule, TriggerManual, TriggerCatchUp, TriggerPreRestore, TriggerWatch}
)

// Artifact is what a backup run produced.
type Artifact struct {
	Key         string `json:"key"`
	SizeBytes   int64  `json:"sizeBytes"`
	SHA256      string `json:"sha256"`
	Destination string `json:"destination"`
}

// Run is one execution of a job or backup.
type Run struct {
	ID           string     `json:"id"`
	Trigger      string     `json:"trigger"`
	Status       string     `json:"status"`
	ScheduledFor *time.Time `json:"scheduledFor"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	ExitCode     *int       `json:"exitCode"`
	Message      string     `json:"message"`
	SkippedCount int        `json:"skippedCount,omitempty"`
	Artifact     *Artifact  `json:"artifact,omitempty"`
	// Key and Outputs are a transcoder run's input object and the keys it wrote (ADR-034).
	Key     string   `json:"key,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
}

// RunDetail adds the output tail (at most 64 KiB, as printed).
type RunDetail struct {
	Run
	OutputTail string `json:"outputTail"`
}

// RunList is a page of runs, newest first.
type RunList struct {
	Items      []Run   `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// SecretSpec carries a secret's value on writes only; responses never send it. A secret synced from a
// secret store has a Source instead (EXT-02), which responses do show.
type SecretSpec struct {
	Value  string        `json:"value,omitempty"`
	Source *SecretSource `json:"source,omitempty"`
}

// CreateResourceRequest is the body of POST /resources.
type CreateResourceRequest struct {
	Kind    Kind            `json:"kind"`
	Name    string          `json:"name"`
	Project string          `json:"project,omitempty"`
	Spec    json.RawMessage `json:"spec"`
}

// UpdateResourceRequest is the body of PATCH /resources/{id}.
type UpdateResourceRequest struct {
	Spec json.RawMessage `json:"spec"`
}

// ResourceList is a page of resources.
type ResourceList struct {
	Items      []Resource `json:"items"`
	NextCursor *string    `json:"nextCursor"`
}

// Action names for POST /resources/{id}/actions/{action}.
const (
	ActionRestart   = "restart"
	ActionRedeploy  = "redeploy"
	ActionReconcile = "reconcile"
	ActionRun       = "run"
	ActionRestore   = "restore"
	// ActionStart and ActionStop (VMs) are in compute.go, ActionPurge (queues, CDNs) in data.go.
)

// Event is one entry in a resource's history.
type Event struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// EventList wraps events.
type EventList struct {
	Items []Event `json:"items"`
}

// LogLine is one line of container output.
type LogLine struct {
	Time    time.Time `json:"time"`
	Stream  string    `json:"stream"`
	Message string    `json:"message"`
}

// LogList wraps log lines.
type LogList struct {
	Items []LogLine `json:"items"`
}

// SecretValue is the body of GET /resources/{id}/value.
type SecretValue struct {
	Value string `json:"value"`
}

// User is the signed-in principal's user.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// Scope values.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeFull  = "full"
)

// Session describes the current principal.
type Session struct {
	User        User   `json:"user"`
	MachineName string `json:"machineName"`
	AuthMethod  string `json:"authMethod"`
	Scope       string `json:"scope"`
	// v0.7 (ADR-036): the user's roles, and the token's limits when a token is used.
	MachineRole  *string           `json:"machineRole"`
	ProjectRoles map[string]string `json:"projectRoles"`
	Token        *TokenLimits      `json:"token"`
}

// SetupStatus is the body of GET /setup.
type SetupStatus struct {
	Required bool `json:"required"`
	// SuggestedMachineName is the computer's own name, for the setup form to start with (MCH-10).
	SuggestedMachineName string `json:"suggestedMachineName"`
}

// SetupRequest is the body of POST /setup.
type SetupRequest struct {
	SetupCode   string `json:"setupCode"`
	MachineName string `json:"machineName"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

// SignInRequest is the body of POST /session.
type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Token is an API token's public metadata.
type Token struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// Scope is kept for v0.6 clients: read for viewer tokens, write otherwise.
	Scope      string     `json:"scope"`
	Role       string     `json:"role"`
	Projects   []string   `json:"projects"`
	UserEmail  string     `json:"userEmail"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
}

// TokenList wraps tokens.
type TokenList struct {
	Items []Token `json:"items"`
}

// CreateTokenRequest is the body of POST /tokens.
type CreateTokenRequest struct {
	Name          string   `json:"name"`
	Role          string   `json:"role,omitempty"`
	Projects      []string `json:"projects,omitempty"`
	ExpiresInDays int      `json:"expiresInDays,omitempty"`
	// Scope is accepted from v0.6 clients: read means viewer, write the owner's strongest role (TOK-03).
	Scope string `json:"scope,omitempty"`
}

// CreatedToken is returned once, with the secret.
type CreatedToken struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret"`
}

// Engine describes the container engine's availability.
type Engine struct {
	Driver    string `json:"driver"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Message   string `json:"message"`
}

// Edge describes the edge proxy that serves domains.
type Edge struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

// MachineSettings is the body of PATCH /machine; a null field clears the setting.
type MachineSettings struct {
	PublicAddress *string `json:"publicAddress"`
	AcmeEmail     *string `json:"acmeEmail"`
	// InviteEmail sends invite and reset links by email (IAM-04). Unlike the fields above, leaving it
	// out keeps it; ClearInviteEmail (sent as "inviteEmail": null) turns it off.
	InviteEmail      *InviteEmail `json:"inviteEmail,omitempty"`
	ClearInviteEmail bool         `json:"-"`
}

// InviteEmail names the email service, in a project, that sends invite and reset links.
type InviteEmail struct {
	Project string `json:"project,omitempty"`
	Service string `json:"service"`
	From    string `json:"from"`
}

// UnmarshalJSON tells "inviteEmail": null (turn it off) from no inviteEmail at all (keep it).
func (m *MachineSettings) UnmarshalJSON(b []byte) error {
	type plain MachineSettings
	var raw struct {
		plain
		InviteEmail json.RawMessage `json:"inviteEmail"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = MachineSettings(raw.plain)
	switch v := bytes.TrimSpace(raw.InviteEmail); {
	case len(v) == 0:
	case string(v) == "null":
		m.ClearInviteEmail = true
	default:
		m.InviteEmail = &InviteEmail{}
		return json.Unmarshal(v, m.InviteEmail)
	}
	return nil
}

// MarshalJSON sends "inviteEmail": null when ClearInviteEmail is set.
func (m MachineSettings) MarshalJSON() ([]byte, error) {
	type plain MachineSettings
	if !m.ClearInviteEmail {
		return json.Marshal(plain(m))
	}
	return json.Marshal(struct {
		plain
		InviteEmail *InviteEmail `json:"inviteEmail"`
	}{plain: plain(m)})
}

// Machine describes the host.
type Machine struct {
	// ID and Handle are the main machine's own: its ID (mch_…) and the name commands use (MCH-01).
	ID                  string  `json:"id"`
	Handle              string  `json:"handle"`
	Name                string  `json:"name"`
	Hostname            string  `json:"hostname"`
	Version             string  `json:"version"`
	OS                  string  `json:"os"`
	Arch                string  `json:"arch"`
	CPUs                int     `json:"cpus"`
	MemoryTotalBytes    int64   `json:"memoryTotalBytes"`
	MemoryReservedBytes int64   `json:"memoryReservedBytes"`
	Engine              Engine  `json:"engine"`
	PublicAddress       *string `json:"publicAddress"`
	AcmeEmail           *string `json:"acmeEmail"`
	Edge                Edge    `json:"edge"`
	// InviteEmail is how invites are emailed, or nil when they aren't (IAM-04).
	InviteEmail *InviteEmail `json:"inviteEmail"`
	// v0.5 (L3-002 §8.2).
	VMs           VMSupport     `json:"vms"`
	ReservedCPUs  int           `json:"reservedCpus"`
	Images        []CachedImage `json:"images"`
	SnapshotBytes int64         `json:"snapshotBytes"`
	// v0.6 (L3-002 §9.2).
	Broker Broker `json:"broker"`
}

// Broker describes the message broker behind queues and topics (ADR-029).
type Broker struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

// VMSupport says whether this machine can run VMs, and if not, why and how to fix it (VM-11).
type VMSupport struct {
	Available   bool   `json:"available"`
	Message     string `json:"message"`
	QEMUVersion string `json:"qemuVersion,omitempty"`
	Accelerator string `json:"accelerator,omitempty"`
}

// CachedImage is a downloaded VM image (IMG-05).
type CachedImage struct {
	Name      string `json:"name"`
	Arch      string `json:"arch"`
	SizeBytes int64  `json:"sizeBytes"`
	UsedBy    int    `json:"usedBy"`
}

// Header names used by the API.
const (
	HeaderRequestID      = "X-Request-Id"
	HeaderIdempotencyKey = "Idempotency-Key"
	HeaderCSRF           = "X-Enclave-CSRF"
	SessionCookie        = "es_session"
	TokenPrefix          = "es_pat_"
)
