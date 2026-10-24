// Package domain holds the control plane's resource model and its lifecycle rules (ADR-004).
// It is pure: no I/O, no clock reads except through arguments.
package domain

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Resource is the stored form of any kind. Spec never contains a secret value (that is SecretCiphertext).
type Resource struct {
	ID string
	// Project is the namespace the resource lives in (ADR-035); "default" for everything before v0.7.
	Project            string
	Kind               api.Kind
	Name               string
	Spec               json.RawMessage
	SecretCiphertext   []byte
	Generation         int64
	ObservedGeneration int64
	Phase              api.Phase
	Message            string
	Steps              []api.Step
	Outputs            map[string]any
	OwnerID            *string
	Attempts           int
	// RequestedAction is a pending restart or redeploy for the reconciler to carry out ("" when none).
	RequestedAction string
	// ServingRevision is the app revision receiving traffic (ADR-016); 0 before the first deploy.
	ServingRevision int64
	// NextRunAt is a job's or backup's next scheduled run (ADR-017); nil for other kinds.
	NextRunAt *time.Time
	// ConsecutiveFailures counts failed runs in a row; a success resets it.
	ConsecutiveFailures int
	NextAttemptAt       *time.Time
	// Machine is the ID of the machine the resource runs on (ADR-044); "" is read as the main machine, and a
	// placed kind that isn't placed yet has "" until the reconciler chooses.
	Machine string
	// Stack is the ID of the stack the resource is a member of (ADR-048), or "".
	Stack            string
	LastTransitionAt time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ToAPI converts to the wire representation.
func (r *Resource) ToAPI() api.Resource {
	spec := r.Spec
	if len(spec) == 0 {
		spec = json.RawMessage(`{}`)
	}
	if r.Kind == api.KindSecret { // only a synced secret's source is shown, never a value (ADR-041)
		spec = json.RawMessage(`{}`)
		var s api.SecretSpec
		if json.Unmarshal(r.Spec, &s) == nil && s.Source != nil {
			spec, _ = json.Marshal(api.SecretSpec{Source: s.Source})
		}
	}
	steps := r.Steps
	if steps == nil {
		steps = []api.Step{}
	}
	outputs := r.Outputs
	if outputs == nil {
		outputs = map[string]any{}
	}
	return api.Resource{
		ID: r.ID, Project: r.ProjectName(), Kind: r.Kind, Name: r.Name, Generation: r.Generation, OwnerID: r.OwnerID, Spec: spec,
		Status: api.Status{
			Phase: r.Phase, Message: r.Message, ObservedGeneration: r.ObservedGeneration, Attempts: r.Attempts,
			Steps: steps, LastTransitionAt: r.LastTransitionAt,
		},
		Outputs: outputs, Stack: r.StackRef(), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// StackRef is the stack's ID, or nil when the resource isn't a member of one.
func (r *Resource) StackRef() *string {
	if r.Stack == "" {
		return nil
	}
	s := r.Stack
	return &s
}

// MachineOn is the machine the resource runs on: mainID when it has none yet.
func (r *Resource) MachineOn(mainID string) string {
	if r.Machine == "" {
		return mainID
	}
	return r.Machine
}

// ProjectName is the resource's project, "default" when unset (rows built in tests).
func (r *Resource) ProjectName() string {
	if r.Project == "" {
		return api.DefaultProject
	}
	return r.Project
}

// Qualified is how engine-side names carry a project (ADR-035): the name in "default", "name.project"
// elsewhere. Names can't contain dots, so two projects never collide.
func Qualified(project, name string) string {
	if project == "" || project == api.DefaultProject {
		return name
	}
	return name + "." + project
}

// BrokerKey is how the broker knows a queue or topic (ADR-035): the name in "default", "project_name"
// elsewhere. Names have no underscores, so SplitBrokerKey reverses it.
func BrokerKey(project, name string) string {
	if project == "" || project == api.DefaultProject {
		return name
	}
	return project + "_" + name
}

// SplitBrokerKey returns the project and name of a broker key.
func SplitBrokerKey(key string) (project, name string) {
	if p, n, ok := strings.Cut(key, "_"); ok {
		return p, n
	}
	return api.DefaultProject, key
}

// Ref is "kind/name", how events, alerts and dashboards name a resource.
func (r *Resource) Ref() string { return string(r.Kind) + "/" + r.Name }

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns "<prefix>_<16 lowercase base32 chars>" from 10 crypto-random bytes (L3-005 §2).
func NewID(prefix string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(b))
}

// NewResourceID returns an ID with the kind's prefix.
func NewResourceID(kind api.Kind) string { return NewID(api.IDPrefix[kind]) }

// allowed lists the legal phase transitions (L3-002 §5). Reaching the same phase again is always allowed.
var allowed = map[api.Phase][]api.Phase{
	api.PhasePending:      {api.PhaseProvisioning, api.PhaseUpdating, api.PhaseReady, api.PhaseFailed, api.PhaseDeleting},
	api.PhaseProvisioning: {api.PhaseReady, api.PhaseFailed, api.PhaseUpdating, api.PhaseDeleting},
	// ready → failed: a backup whose runs failed BackupFailureLimit times in a row (L2-005 BAK-09).
	api.PhaseReady:    {api.PhaseUpdating, api.PhaseProvisioning, api.PhaseDeleting, api.PhaseFailed},
	api.PhaseUpdating: {api.PhaseReady, api.PhaseFailed, api.PhaseDeleting},
	api.PhaseFailed:   {api.PhaseProvisioning, api.PhaseUpdating, api.PhaseDeleting},
	api.PhaseDeleting: {},
}

// CanTransition reports whether a resource may move from one phase to another.
func CanTransition(from, to api.Phase) bool {
	if from == to {
		return true
	}
	for _, p := range allowed[from] {
		if p == to {
			return true
		}
	}
	return false
}

// SetPhase moves r to phase with message, recording the transition time. It refuses illegal moves.
func (r *Resource) SetPhase(phase api.Phase, message string, now time.Time) error {
	if !CanTransition(r.Phase, phase) {
		return fmt.Errorf("illegal phase transition %s → %s for %s", r.Phase, phase, r.ID)
	}
	if r.Phase != phase {
		r.LastTransitionAt = now
	}
	r.Phase, r.Message = phase, message
	return nil
}

// NewSteps returns the named steps, all pending.
func NewSteps(names ...string) []api.Step {
	steps := make([]api.Step, len(names))
	for i, n := range names {
		steps[i] = api.Step{Name: n, State: api.StepPending}
	}
	return steps
}

// StepNames are each kind's provisioning steps, as the console shows them.
var StepNames = map[api.Kind][]string{
	api.KindApp:      {"pull", "start", "health", "switch"},
	api.KindDatabase: {"pull", "create", "start", "healthy"},
	api.KindCache:    {"pull", "create", "start", "healthy"},
	api.KindBucket:   {"objectstore", "bucket"},
	api.KindSecret:   {"store"},
	api.KindDomain:   {"edge", "route", "certificate"},
	api.KindJob:      {"schedule"},
	api.KindBackup:   {"check", "schedule"},
	// v0.4 (L2-006).
	api.KindNetwork:      {"network", "attach"},
	api.KindFirewall:     {"resolve", "apply"},
	api.KindLoadBalancer: {"pull", "configure", "start", "healthy"},
	api.KindZone:         {"credentials", "zone"},
	api.KindRecord:       {"zone", "record"},
	// v0.5 (L2-007).
	api.KindVM:       {"image", "disk", "network", "boot"},
	api.KindVolume:   {"create", "fill"},
	api.KindSnapshot: {"source", "copy"},
	// v0.6 (L2-008).
	api.KindQueue:      {"broker", "queue"},
	api.KindTopic:      {"broker", "topic", "subscriptions"},
	api.KindSearch:     {"pull", "create", "start", "healthy"},
	api.KindTranscoder: {"check", "watch"},
	api.KindCDN:        {"pull", "cache", "edge", "certificate"},
	api.KindEmail:      {"pull", "create", "start", "healthy"},
	// v0.7 (L2-009).
	api.KindDashboard:   {"check"},
	api.KindAlert:       {"check"},
	api.KindChannel:     {"check"},
	api.KindLogExport:   {"check", "start"},
	api.KindSecretStore: {"credentials", "connect"},
	// v0.8 (L2-010).
	api.KindStack: {"members"},
}

// SecretRefs returns the secret names an app spec references, in key order.
func SecretRefs(spec api.AppSpec) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range sortedKeys(spec.SecretEnv) {
		if n := spec.SecretEnv[k]; !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// MemoryReservedMiB is what a resource reserves on the machine (CP-16).
func MemoryReservedMiB(r *Resource) int {
	switch r.Kind {
	case api.KindApp:
		var s api.AppSpec
		if json.Unmarshal(r.Spec, &s) == nil && s.MemoryMiB > 0 {
			return s.MemoryMiB * max(s.Replicas, 1)
		}
		return api.AppMemoryDefaultMiB
	case api.KindLoadBalancer:
		return LoadBalancerMemoryMiB
	case api.KindDatabase:
		var s api.DatabaseSpec
		if json.Unmarshal(r.Spec, &s) == nil {
			if m, ok := EngineMemoryMiB[s.Engine]; ok {
				return m
			}
		}
		return DatabaseMemoryMiB
	case api.KindSearch:
		var s api.SearchSpec
		if json.Unmarshal(r.Spec, &s) == nil && s.MemoryMiB > 0 {
			return s.MemoryMiB
		}
		return api.SearchMemoryDefaultMiB
	case api.KindEmail:
		return EmailMemoryMiB
	case api.KindCDN:
		return CDNMemoryMiB
	case api.KindTranscoder:
		var s api.TranscoderSpec
		if json.Unmarshal(r.Spec, &s) == nil && s.MemoryMiB > 0 {
			return s.MemoryMiB * max(s.Concurrency, 1)
		}
		return api.TranscodeMemoryDefault
	case api.KindCache:
		var s api.CacheSpec
		if json.Unmarshal(r.Spec, &s) == nil && s.MaxMemoryMiB > 0 {
			return s.MaxMemoryMiB + CacheOverheadMiB
		}
		return api.CacheMemoryDefault + CacheOverheadMiB
	case api.KindVM:
		if s, ok := runningVM(r); ok {
			return s.MemoryMiB + VMLinkMemoryMiB
		}
	}
	return 0
}

// CPUsReserved is the CPUs a resource holds: a running VM's (L3-002 §8.2).
func CPUsReserved(r *Resource) int {
	if s, ok := runningVM(r); ok {
		return s.CPUs
	}
	return 0
}

// runningVM returns a VM's spec when its power is running.
func runningVM(r *Resource) (api.VMSpec, bool) {
	if r.Kind != api.KindVM {
		return api.VMSpec{}, false
	}
	var s api.VMSpec
	if json.Unmarshal(r.Spec, &s) != nil || s.Power == api.PowerStopped {
		return s, false
	}
	return s, true
}

// Container memory policy.
const (
	DatabaseMemoryMiB     = 512
	CacheOverheadMiB      = 64
	LoadBalancerMemoryMiB = 128
	VMLinkMemoryMiB       = 64
	// v0.6 (ADR-029 – ADR-033).
	BrokerMemoryMiB = 512
	EmailMemoryMiB  = 128
	CDNMemoryMiB    = 128
)

// EngineMemoryMiB is each database engine's container memory (ADR-030).
var EngineMemoryMiB = map[string]int{api.EnginePostgres: DatabaseMemoryMiB, api.EngineMySQL: 1024, api.EngineMongoDB: 1024, api.EngineClickHouse: 2048}

// UsesBroker reports whether a kind lives on the message broker (ADR-029).
func UsesBroker(k api.Kind) bool { return k == api.KindQueue || k == api.KindTopic }

// DatabaseIdentifier turns a resource name into a PostgreSQL identifier (hyphens become underscores).
func DatabaseIdentifier(name string) string { return strings.ReplaceAll(name, "-", "_") }

// ManagedPasswordSecret is the name of a database's managed password secret.
func ManagedPasswordSecret(dbName string) string { return dbName + "-password" }

// HasManagedPassword reports whether a kind gets a managed password secret when created (CP-05, DBE-02).
func HasManagedPassword(k api.Kind) bool {
	return k == api.KindDatabase || k == api.KindQueue || k == api.KindTopic || k == api.KindSearch || k == api.KindEmail
}

// PasswordSecretFor names a resource's managed password secret: "<name>-password" for databases (as in
// v0.2), "<name>-<kind>-password" for the kinds that came later, so a queue and a database may share a
// name (L3-005 §8).
func PasswordSecretFor(kind api.Kind, name string) string {
	if kind == api.KindDatabase {
		return ManagedPasswordSecret(name)
	}
	return name + "-" + string(kind) + "-password"
}

// BrokerCredentialsSecret is the system-managed secret holding the broker's own password (ADR-029).
const BrokerCredentialsSecret = "broker-credentials"

// ObjectStoreCredentialsSecret is the system-managed secret holding the object store's credentials.
const ObjectStoreCredentialsSecret = "objectstore-credentials"

// Run is one execution of a job or backup (L3-005 §5).
type Run struct {
	ID           string
	ResourceID   string
	Trigger      string
	Status       string
	ScheduledFor *time.Time
	StartedAt    time.Time
	FinishedAt   *time.Time
	ExitCode     *int
	Message      string
	SkippedCount int
	OutputTail   []byte
	Artifact     *api.Artifact
	// Key and Outputs are a transcoder run's input object and the keys it wrote (ADR-034).
	Key     string
	Outputs []string
}

// ToAPI converts to the wire form, without the output tail.
func (r *Run) ToAPI() api.Run {
	return api.Run{ID: r.ID, Trigger: r.Trigger, Status: r.Status, ScheduledFor: r.ScheduledFor, StartedAt: r.StartedAt,
		FinishedAt: r.FinishedAt, ExitCode: r.ExitCode, Message: r.Message, SkippedCount: r.SkippedCount, Artifact: r.Artifact,
		Key: r.Key, Outputs: r.Outputs}
}

// SystemOwner marks resources owned by the control plane itself rather than another resource.
const SystemOwner = "system"

// ProjectStoreUser is the object store user of a project other than default, whose policy allows only
// that project's buckets (ADR-035).
func ProjectStoreUser(project string) string { return "es-p-" + project }

// StoreUserFor is the object store user of an app or job with bucket access (ADR-049): "esr-" and the
// random part of its ID, 20 characters.
func StoreUserFor(resourceID string) string {
	_, random, _ := strings.Cut(resourceID, "_")
	return "esr-" + random
}

// BucketGrantsOf returns the bucket access an app's or job's spec gives it of its own (nil for other
// kinds, and for a job that runs as an app, which uses the app's).
func BucketGrantsOf(spec any) []api.BucketGrant {
	switch s := spec.(type) {
	case api.AppSpec:
		return s.BucketAccess
	case api.JobSpec:
		return s.BucketAccess
	}
	return nil
}
