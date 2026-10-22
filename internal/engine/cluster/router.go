// Package cluster routes every engine call to the machine it concerns (ADR-044). The reconciler, services
// and monitor keep one engine.Driver and one engine.Hypervisor: the Router. It sends a call naming a
// resource to that resource's machine, the edge, broker, object store and transcodes to the main machine,
// and networks to every online machine. It knows machines only through the Machines port.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// Machines is what the router needs to know about machines.
type Machines interface {
	// Engines returns a machine's driver and hypervisor, or an error wrapping engine.ErrMachineOffline when
	// it can't take calls now.
	Engines(machineID string) (engine.Driver, engine.Hypervisor, error)
	// OnlineMachines lists the machines that take calls now, the main machine first.
	OnlineMachines() []string
	// MachineOf returns the machine of a resource by ID, the main machine when it isn't known.
	MachineOf(ctx context.Context, resourceID string) string
}

// Hooks lets the mesh learn of containers that changed (ADR-045). Both may be nil.
type Hooks struct {
	// Started runs on the control plane when a resource's new containers are up, before they take traffic.
	Started func(ctx context.Context, r *domain.Resource) error
	// Changed runs after a call that added or removed a resource's containers.
	Changed func(r *domain.Resource)
}

// Router implements engine.Driver and engine.Hypervisor over every machine.
type Router struct {
	Machines Machines
	Hooks    Hooks

	mu   sync.Mutex
	runs map[string]string // job run ID → the machine it was started on
}

var (
	_ engine.Driver     = (*Router)(nil)
	_ engine.Hypervisor = (*Router)(nil)
)

// New returns a router over machines.
func New(m Machines) *Router { return &Router{Machines: m, runs: map[string]string{}} }

func (rt *Router) driver(machineID string) (engine.Driver, error) {
	d, _, err := rt.Machines.Engines(machineID)
	return d, err
}

func (rt *Router) hypervisor(machineID string) (engine.Hypervisor, error) {
	_, h, err := rt.Machines.Engines(machineID)
	if err == nil && h == nil {
		return nil, fmt.Errorf("%w: the machine has no hypervisor", engine.ErrNoVMs)
	}
	return h, err
}

func (rt *Router) main() engine.Driver {
	d, _, err := rt.Machines.Engines(api.MainMachine)
	if err != nil {
		panic("the main machine's engine is always there: " + err.Error())
	}
	return d
}

func (rt *Router) mainHV() engine.Hypervisor {
	_, h, _ := rt.Machines.Engines(api.MainMachine)
	return h
}

// of is the driver of r's machine.
func (rt *Router) of(r *domain.Resource) (engine.Driver, error) { return rt.driver(r.MachineID()) }

func (rt *Router) changed(r *domain.Resource) {
	if rt.Hooks.Changed != nil && r != nil {
		rt.Hooks.Changed(r)
	}
}

// Name is the main machine's driver name.
func (rt *Router) Name() string { return rt.main().Name() }

// Ping and Info are the main machine's: "the engine" is the one the control plane runs beside.
func (rt *Router) Ping(ctx context.Context) error { return rt.main().Ping(ctx) }

func (rt *Router) Info(ctx context.Context) (engine.Info, error) { return rt.main().Info(ctx) }

// Ensure runs on r's machine. The Started hook first tells the mesh about the new containers, so they are
// reachable from every machine before they take traffic (XNET-07).
func (rt *Router) Ensure(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	d, err := rt.of(in.Resource)
	if err != nil {
		return nil, err
	}
	in.Started = rt.wrapStarted(in.Resource, in.Started)
	out, err := d.Ensure(ctx, in, report)
	rt.changed(in.Resource)
	return out, err
}

func (rt *Router) wrapStarted(r *domain.Resource, inner func(context.Context) error) func(context.Context) error {
	if rt.Hooks.Started == nil {
		return inner
	}
	return func(ctx context.Context) error {
		if err := rt.Hooks.Started(ctx, r); err != nil {
			return err
		}
		if inner != nil {
			return inner(ctx)
		}
		return nil
	}
}

func (rt *Router) Delete(ctx context.Context, r *domain.Resource, store engine.ObjectStore) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	err = d.Delete(ctx, r, store)
	rt.changed(r)
	return err
}

func (rt *Router) Health(ctx context.Context, r *domain.Resource, store engine.ObjectStore) (engine.Health, error) {
	d, err := rt.of(r)
	if err != nil {
		return engine.Missing, err
	}
	return d.Health(ctx, r, store)
}

func (rt *Router) Restart(ctx context.Context, r *domain.Resource) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.Restart(ctx, r)
}

func (rt *Router) Logs(ctx context.Context, r *domain.Resource, tail int) ([]api.LogLine, error) {
	d, err := rt.of(r)
	if err != nil {
		return nil, err
	}
	return d.Logs(ctx, r, tail)
}

// The edge, its certificates and its CA live on the main machine (PLC-06).
func (rt *Router) ConfigureEdge(ctx context.Context, cfg engine.EdgeConfig) error {
	return rt.main().ConfigureEdge(ctx, cfg)
}

func (rt *Router) Certificate(ctx context.Context, hostname string) (engine.Certificate, error) {
	return rt.main().Certificate(ctx, hostname)
}

func (rt *Router) EdgeStatus(ctx context.Context) engine.EdgeStatus { return rt.main().EdgeStatus(ctx) }

func (rt *Router) RootCA(ctx context.Context) ([]byte, error) { return rt.main().RootCA(ctx) }

// StartJob runs on the job's machine, which WaitJob then reaches.
func (rt *Router) StartJob(ctx context.Context, run engine.JobRun) error {
	machine := rt.Machines.MachineOf(ctx, run.ResourceID)
	d, err := rt.driver(machine)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	rt.runs[run.RunID] = machine
	rt.mu.Unlock()
	return d.StartJob(ctx, run)
}

func (rt *Router) WaitJob(ctx context.Context, runID string, timeout time.Duration) (engine.JobResult, error) {
	rt.mu.Lock()
	machine, ok := rt.runs[runID]
	rt.mu.Unlock()
	if !ok {
		machine = api.MainMachine
	}
	d, err := rt.driver(machine)
	if err != nil {
		return engine.JobResult{}, err
	}
	res, err := d.WaitJob(ctx, runID, timeout)
	if !errors.Is(err, engine.ErrMachineOffline) && ctx.Err() == nil {
		rt.mu.Lock()
		delete(rt.runs, runID)
		rt.mu.Unlock()
	}
	return res, err
}

// Build runs on the app's machine, where it will run (PLC-07).
func (rt *Router) Build(ctx context.Context, in engine.BuildInput, log io.Writer) (engine.BuildResult, error) {
	d, err := rt.of(in.Resource)
	if err != nil {
		return engine.BuildResult{}, err
	}
	return d.Build(ctx, in, log)
}

func (rt *Router) HasImage(ctx context.Context, r *domain.Resource, ref string) (bool, error) {
	d, err := rt.of(r)
	if err != nil {
		return false, err
	}
	return d.HasImage(ctx, r, ref)
}

func (rt *Router) RemoveBuiltImages(ctx context.Context, r *domain.Resource, keep []string) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.RemoveBuiltImages(ctx, r, keep)
}

// EnsureNetwork makes the network on every online machine (XNET-01); the outputs are the main machine's.
func (rt *Router) EnsureNetwork(ctx context.Context, in engine.NetworkInput) (engine.Outputs, error) {
	var out engine.Outputs
	for _, id := range rt.Machines.OnlineMachines() {
		d, err := rt.driver(id)
		if err != nil {
			continue // it went offline meanwhile; the mesh sync makes the network when it returns
		}
		o, err := d.EnsureNetwork(ctx, in)
		if err != nil {
			if id == api.MainMachine {
				return nil, err
			}
			return nil, fmt.Errorf("on machine %s: %w", id, err)
		}
		if id == api.MainMachine {
			out = o
		}
	}
	return out, nil
}

// DeleteNetwork removes the network from every online machine; an offline one drops it when swept.
func (rt *Router) DeleteNetwork(ctx context.Context, r *domain.Resource) error {
	for _, id := range rt.Machines.OnlineMachines() {
		d, err := rt.driver(id)
		if err != nil {
			continue
		}
		if err := d.DeleteNetwork(ctx, r); err != nil {
			if id == api.MainMachine {
				return err
			}
			return fmt.Errorf("on machine %s: %w", id, err)
		}
	}
	return nil
}

func (rt *Router) ApplyFirewall(ctx context.Context, r *domain.Resource, p *firewall.Policy) (int, error) {
	d, err := rt.of(r)
	if err != nil {
		return 0, err
	}
	return d.ApplyFirewall(ctx, r, p)
}

func (rt *Router) Endpoints(ctx context.Context, r *domain.Resource) ([]netip.Addr, error) {
	d, err := rt.of(r)
	if err != nil {
		return nil, err
	}
	return d.Endpoints(ctx, r)
}

func (rt *Router) LoadBalancerStatus(ctx context.Context, r *domain.Resource) (lb.Status, error) {
	d, err := rt.of(r)
	if err != nil {
		return lb.Status{}, err
	}
	return d.LoadBalancerStatus(ctx, r)
}

// Dump takes the dump on the database's machine (PLC-06); Restore fills it there.
func (rt *Router) Dump(ctx context.Context, in engine.EnsureInput, w io.Writer) (engine.DumpInfo, error) {
	d, err := rt.of(in.Resource)
	if err != nil {
		return engine.DumpInfo{}, err
	}
	return d.Dump(ctx, in, w)
}

func (rt *Router) Restore(ctx context.Context, in engine.EnsureInput, src io.Reader) error {
	d, err := rt.of(in.Resource)
	if err != nil {
		return err
	}
	err = d.Restore(ctx, in, src)
	rt.changed(in.Resource)
	return err
}

func (rt *Router) EnsureVolume(ctx context.Context, r *domain.Resource) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.EnsureVolume(ctx, r)
}

func (rt *Router) EnsureVMLink(ctx context.Context, in engine.EnsureInput) (engine.Outputs, error) {
	d, err := rt.of(in.Resource)
	if err != nil {
		return nil, err
	}
	in.Started = rt.wrapStarted(in.Resource, in.Started)
	out, err := d.EnsureVMLink(ctx, in)
	rt.changed(in.Resource)
	return out, err
}

func (rt *Router) DeleteVMLink(ctx context.Context, r *domain.Resource) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	err = d.DeleteVMLink(ctx, r)
	rt.changed(r)
	return err
}

func (rt *Router) VolumeUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	d, err := rt.of(r)
	if err != nil {
		return 0, err
	}
	return d.VolumeUsage(ctx, r)
}

func (rt *Router) SnapshotVolume(ctx context.Context, r *domain.Resource, w io.Writer) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.SnapshotVolume(ctx, r, w)
}

func (rt *Router) RestoreVolume(ctx context.Context, r *domain.Resource, src io.Reader) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.RestoreVolume(ctx, r, src)
}

// The broker, the object store and their users live on the main machine (PLC-06).
func (rt *Router) ConfigureBroker(ctx context.Context, cfg engine.BrokerConfig) (engine.BrokerStatus, error) {
	return rt.main().ConfigureBroker(ctx, cfg)
}

func (rt *Router) BrokerStatus(ctx context.Context) engine.BrokerStatus {
	return rt.main().BrokerStatus(ctx)
}

func (rt *Router) PurgeCache(ctx context.Context, r *domain.Resource) error {
	return rt.main().PurgeCache(ctx, r)
}

func (rt *Router) CacheUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	return rt.main().CacheUsage(ctx, r)
}

func (rt *Router) SetBucketPublicRead(ctx context.Context, bucket string, public bool, store engine.ObjectStore) error {
	return rt.main().SetBucketPublicRead(ctx, bucket, public, store)
}

func (rt *Router) Transcode(ctx context.Context, run engine.TranscodeRun, src io.Reader, out func(string, int64, io.Reader) error) (engine.JobResult, error) {
	return rt.main().Transcode(ctx, run, src, out)
}

func (rt *Router) SetProjectStoreAccess(ctx context.Context, user engine.ObjectStore, buckets []string, root engine.ObjectStore) error {
	return rt.main().SetProjectStoreAccess(ctx, user, buckets, root)
}

func (rt *Router) Stats(ctx context.Context, r *domain.Resource) ([]engine.ContainerStats, error) {
	d, err := rt.of(r)
	if err != nil {
		return nil, err
	}
	return d.Stats(ctx, r)
}

func (rt *Router) EdgeRequests(ctx context.Context) (map[string]engine.RequestCounters, error) {
	return rt.main().EdgeRequests(ctx)
}

func (rt *Router) LogsSince(ctx context.Context, r *domain.Resource, since map[string]time.Time, fallback time.Time, limit int) ([]engine.ContainerLogLine, error) {
	d, err := rt.of(r)
	if err != nil {
		return nil, err
	}
	return d.LogsSince(ctx, r, since, fallback, limit)
}

// engine.Hypervisor: a VM's calls go to its machine, a disk volume's to the volume's machine.

func (rt *Router) vmHV(ctx context.Context, vmID string) (engine.Hypervisor, error) {
	return rt.hypervisor(rt.Machines.MachineOf(ctx, vmID))
}

func (rt *Router) diskHV(ctx context.Context, d engine.DiskRef) (engine.Hypervisor, error) {
	id := d.DiskID
	if id == "" {
		id = d.VMID
	}
	return rt.hypervisor(rt.Machines.MachineOf(ctx, id))
}

// Check is the main machine's; every other machine's VM support comes with its health check.
func (rt *Router) Check(ctx context.Context) engine.VMSupport {
	if h := rt.mainHV(); h != nil {
		return h.Check(ctx)
	}
	return engine.VMSupport{}
}

func (rt *Router) EnsureVM(ctx context.Context, in engine.VMInput, report engine.Reporter) (engine.VMState, error) {
	h, err := rt.vmHV(ctx, in.ID)
	if err != nil {
		return engine.VMState{}, err
	}
	return h.EnsureVM(ctx, in, report)
}

func (rt *Router) StopVM(ctx context.Context, vmID string) error {
	h, err := rt.vmHV(ctx, vmID)
	if err != nil {
		return err
	}
	return h.StopVM(ctx, vmID)
}

func (rt *Router) DeleteVM(ctx context.Context, vmID string) error {
	h, err := rt.vmHV(ctx, vmID)
	if err != nil {
		return err
	}
	return h.DeleteVM(ctx, vmID)
}

func (rt *Router) VMStatus(ctx context.Context, vmID string) (engine.VMStatus, error) {
	h, err := rt.vmHV(ctx, vmID)
	if err != nil {
		return engine.VMMissing, err
	}
	return h.VMStatus(ctx, vmID)
}

func (rt *Router) ConsoleLog(ctx context.Context, vmID string, tail int) ([]api.LogLine, error) {
	h, err := rt.vmHV(ctx, vmID)
	if err != nil {
		return nil, err
	}
	return h.ConsoleLog(ctx, vmID, tail)
}

func (rt *Router) EnsureDisk(ctx context.Context, d engine.DiskRef, sizeGiB int) error {
	h, err := rt.diskHV(ctx, d)
	if err != nil {
		return err
	}
	return h.EnsureDisk(ctx, d, sizeGiB)
}

func (rt *Router) DeleteDisk(ctx context.Context, diskID string) error {
	h, err := rt.hypervisor(rt.Machines.MachineOf(ctx, diskID))
	if err != nil {
		return err
	}
	return h.DeleteDisk(ctx, diskID)
}

// SnapshotDisk copies from the disk's machine into the main machine's snapshot directory.
func (rt *Router) SnapshotDisk(ctx context.Context, d engine.DiskRef, dest string) (int64, error) {
	h, err := rt.diskHV(ctx, d)
	if err != nil {
		return 0, err
	}
	return h.SnapshotDisk(ctx, d, dest)
}

func (rt *Router) CopyDisk(ctx context.Context, src string, diskID string, sizeGiB int) error {
	h, err := rt.hypervisor(rt.Machines.MachineOf(ctx, diskID))
	if err != nil {
		return err
	}
	return h.CopyDisk(ctx, src, diskID, sizeGiB)
}

func (rt *Router) CachedImages(ctx context.Context) []api.CachedImage {
	if h := rt.mainHV(); h != nil {
		return h.CachedImages(ctx)
	}
	return nil
}

func (rt *Router) VMStats(ctx context.Context, vmID string) (engine.ProcessStats, bool, error) {
	h, err := rt.vmHV(ctx, vmID)
	if err != nil {
		return engine.ProcessStats{}, false, err
	}
	return h.VMStats(ctx, vmID)
}

// The mesh calls name a machine, not a resource: the mesh sync calls each machine's own engine. Through the
// router they reach the main machine.

func (rt *Router) ConfigureMesh(ctx context.Context, cfg engine.MeshConfig) (engine.MeshStatus, error) {
	return rt.main().ConfigureMesh(ctx, cfg)
}

func (rt *Router) MeshStatus(ctx context.Context) (engine.MeshStatus, error) {
	return rt.main().MeshStatus(ctx)
}

func (rt *Router) NetworkEndpoints(ctx context.Context) ([]engine.NetworkInfo, error) {
	return rt.main().NetworkEndpoints(ctx)
}

// Moves (ADR-047): through the router these reach the resource's machine. A move itself addresses the old
// and the new machine directly.

func (rt *Router) StopResource(ctx context.Context, r *domain.Resource) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.StopResource(ctx, r)
}

func (rt *Router) ExportData(ctx context.Context, r *domain.Resource, w io.Writer) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.ExportData(ctx, r, w)
}

func (rt *Router) ImportData(ctx context.Context, r *domain.Resource, src io.Reader) error {
	d, err := rt.of(r)
	if err != nil {
		return err
	}
	return d.ImportData(ctx, r, src)
}

func (rt *Router) ExportDisk(ctx context.Context, d engine.DiskRef, w io.Writer) error {
	h, err := rt.diskHV(ctx, d)
	if err != nil {
		return err
	}
	return h.ExportDisk(ctx, d, w)
}

func (rt *Router) ImportDisk(ctx context.Context, d engine.DiskRef, src io.Reader) error {
	h, err := rt.diskHV(ctx, d)
	if err != nil {
		return err
	}
	return h.ImportDisk(ctx, d, src)
}
