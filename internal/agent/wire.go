package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// The arguments of each call, as JSON. Field names are Go's own: both ends are this package.

// wireInput is engine.EnsureInput with its spec as JSON (rebuilt by the resource's kind) and its Started
// hook as a callback.
type wireInput struct {
	Resource     *domain.Resource
	Spec         json.RawMessage
	Env          map[string]string `json:",omitempty"`
	Password     string            `json:",omitempty"`
	ObjectStore  engine.ObjectStore
	Recreate     bool              `json:",omitempty"`
	Revision     int64             `json:",omitempty"`
	Placement    engine.Placement  `json:",omitempty"`
	Firewall     *firewall.Policy  `json:",omitempty"`
	LoadBalancer *lb.Config        `json:",omitempty"`
	HasStarted   bool              `json:",omitempty"`
	Mounts       []engine.Mount    `json:",omitempty"`
	HostPorts    map[int]int       `json:",omitempty"`
	CDN          *engine.CDNConfig `json:",omitempty"`
	Relay        *engine.Relay     `json:",omitempty"`
}

func toWireInput(in engine.EnsureInput) (wireInput, error) {
	spec, err := json.Marshal(in.Spec)
	if err != nil {
		return wireInput{}, fmt.Errorf("encode the spec: %w", err)
	}
	return wireInput{Resource: bare(in.Resource), Spec: spec, Env: in.Env, Password: in.Password, ObjectStore: in.ObjectStore,
		Recreate: in.Recreate, Revision: in.Revision, Placement: in.Placement, Firewall: in.Firewall, LoadBalancer: in.LoadBalancer,
		HasStarted: in.Started != nil, Mounts: in.Mounts, HostPorts: in.HostPorts, CDN: in.CDN, Relay: in.Relay}, nil
}

func (w wireInput) input() (engine.EnsureInput, error) {
	if w.Resource == nil {
		return engine.EnsureInput{}, engine.Permanent(fmt.Errorf("no resource"))
	}
	spec, err := typedSpec(w.Resource.Kind, w.Spec)
	if err != nil {
		return engine.EnsureInput{}, err
	}
	return engine.EnsureInput{Resource: w.Resource, Spec: spec, Env: w.Env, Password: w.Password, ObjectStore: w.ObjectStore,
		Recreate: w.Recreate, Revision: w.Revision, Placement: w.Placement, Firewall: w.Firewall, LoadBalancer: w.LoadBalancer,
		Mounts: w.Mounts, HostPorts: w.HostPorts, CDN: w.CDN, Relay: w.Relay}, nil
}

// bare is a resource without its secret ciphertext: the agent never needs it.
func bare(r *domain.Resource) *domain.Resource {
	if r == nil {
		return nil
	}
	c := *r
	c.SecretCiphertext = nil
	return &c
}

// specDecoders rebuild a spec as the type the reconciler handed the driver.
var specDecoders = map[api.Kind]func(json.RawMessage) (any, error){
	api.KindApp: decodeAs[api.AppSpec], api.KindDatabase: decodeAs[api.DatabaseSpec], api.KindCache: decodeAs[api.CacheSpec],
	api.KindBucket: decodeAs[api.BucketSpec], api.KindJob: decodeAs[api.JobSpec], api.KindLoadBalancer: decodeAs[api.LoadBalancerSpec],
	api.KindVM: decodeAs[api.VMSpec], api.KindVolume: decodeAs[api.VolumeSpec], api.KindSearch: decodeAs[api.SearchSpec],
	api.KindEmail: decodeAs[api.EmailSpec], api.KindCDN: decodeAs[api.CDNSpec], api.KindNetwork: decodeAs[api.NetworkSpec],
	api.KindFunction: decodeAs[api.FunctionSpec],
}

// typedSpec decodes a spec into its kind's type.
func typedSpec(kind api.Kind, raw json.RawMessage) (any, error) {
	decode, ok := specDecoders[kind]
	if !ok {
		return nil, engine.Permanent(fmt.Errorf("a %s doesn't run on another machine", kind))
	}
	return decode(raw)
}

func decodeAs[T any](raw json.RawMessage) (any, error) {
	var s T
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, engine.Permanent(fmt.Errorf("decode the spec: %w", err))
		}
	}
	return s, nil
}

type resourceArgs struct{ R *domain.Resource }

type resourceStoreArgs struct {
	R     *domain.Resource
	Store engine.ObjectStore
}

type logsArgs struct {
	R    *domain.Resource
	Tail int
}

type hostnameArgs struct{ Hostname string }

type jobArgs struct{ Run engine.JobRun }

type waitArgs struct {
	RunID   string
	Timeout time.Duration
}

type buildArgs struct {
	Resource   *domain.Resource
	Tag        string
	Dockerfile string
	Excludes   []string
	BuildArgs  map[string]string
}

type refArgs struct {
	R   *domain.Resource
	Ref string
}

type keepArgs struct {
	R    *domain.Resource
	Keep []string
}

type networkArgs struct {
	R    *domain.Resource
	Spec api.NetworkSpec
}

type firewallArgs struct {
	R      *domain.Resource
	Policy *firewall.Policy
}

type edgeArgs struct{ Cfg engine.EdgeConfig }

type brokerArgs struct{ Cfg engine.BrokerConfig }

type publicReadArgs struct {
	Bucket string
	Public bool
	Store  engine.ObjectStore
}

type storeAccessArgs struct {
	User    engine.ObjectStore
	Buckets []string
	Root    engine.ObjectStore
}

type storeUserArgs struct {
	User   engine.ObjectStore
	Grants []api.BucketGrant
	Root   engine.ObjectStore
}

type logsSinceArgs struct {
	R        *domain.Resource
	Since    map[string]time.Time
	Fallback time.Time
	Limit    int
}

type vmArgs struct{ VMID string }

type consoleArgs struct {
	VMID string
	Tail int
}

type diskArgs struct {
	D       engine.DiskRef
	SizeGiB int
}

type diskIDArgs struct{ DiskID string }

type copyDiskArgs struct {
	// File is the digest of a file uploaded to the agent (PUT /agent/v1/files).
	File    string
	DiskID  string
	SizeGiB int
}

type vmStatsResult struct {
	Stats engine.ProcessStats
	OK    bool
}

type sweepArgs struct{ Keep []string }

type sweepResult struct{ Removed []string }

type meshArgs struct{ Cfg engine.MeshConfig }

// Public access (ADR-051, ADR-052): a relay runs on an edge machine.
type connectorArgs struct{ C engine.Connector }
type connectorExecArgs struct {
	C   engine.Connector
	Cmd []string
}
type connectorExecResult struct {
	Output   string
	ExitCode int
}
type connectorFetchArgs struct {
	C    engine.Connector
	Port int
	Path string
}
type edgeNetworkArgs struct{ Network string }
