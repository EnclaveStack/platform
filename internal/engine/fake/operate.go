package fake

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// v0.7 read-only methods (ADR-037, ADR-040): stats, edge request counters and logs that tests script.

type operateState struct {
	mu       sync.Mutex
	access   map[string][]string              // project store user → buckets
	grants   map[string][]api.BucketGrant     // app or job store user → its grants (ADR-049)
	stats    map[string]engine.ContainerStats // by resource ID; set by tests, else made up
	ticks    map[string]int                   // Stats calls per resource, for counters that grow
	requests map[string]engine.RequestCounters
	logs     map[string][]engine.ContainerLogLine // by resource ID
}

func (d *Driver) op() *operateState {
	d.opOnce.Do(func() {
		d.v7 = &operateState{access: map[string][]string{}, grants: map[string][]api.BucketGrant{}, stats: map[string]engine.ContainerStats{}, ticks: map[string]int{},
			requests: map[string]engine.RequestCounters{}, logs: map[string][]engine.ContainerLogLine{}}
	})
	return d.v7
}

// SetStats fixes the counters Stats reports for a resource's one container.
func (d *Driver) SetStats(resourceID string, s engine.ContainerStats) {
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stats[resourceID] = s
}

// SetEdgeRequests sets a hostname's request counters.
func (d *Driver) SetEdgeRequests(hostname string, c engine.RequestCounters) {
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests[hostname] = c
}

// AddLogs appends lines a resource's container printed.
func (d *Driver) AddLogs(resourceID, container string, lines ...api.LogLine) {
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, l := range lines {
		o.logs[resourceID] = append(o.logs[resourceID], engine.ContainerLogLine{LogLine: l, Container: container})
	}
}

// RotateLogs drops a container's lines older than before, as the engine's log rotation does.
func (d *Driver) RotateLogs(resourceID, container string, before time.Time) {
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.logs[resourceID][:0]
	for _, l := range o.logs[resourceID] {
		if l.Container != container || !l.Time.Before(before) {
			kept = append(kept, l)
		}
	}
	o.logs[resourceID] = kept
}

// Stats implements engine.Driver: a running resource reports scripted counters, or ones that grow by
// half a CPU-second and 1 MiB of traffic on each call.
func (d *Driver) Stats(ctx context.Context, r *domain.Resource) ([]engine.ContainerStats, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	rt := d.runtimes[r.ID]
	running := rt != nil && rt.running
	d.mu.Unlock()
	if !running {
		return nil, nil
	}
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	if s, ok := o.stats[r.ID]; ok {
		if s.Container == "" {
			s.Container = engine.ContainerName(r)
		}
		return []engine.ContainerStats{s}, nil
	}
	o.ticks[r.ID]++
	n := float64(o.ticks[r.ID])
	return []engine.ContainerStats{{Container: engine.ContainerName(r), ID: r.ID, StartedAt: time.Unix(0, 0), CPUSeconds: n / 2,
		MemoryBytes: 64 << 20, MemoryLimit: 256 << 20, NetRx: uint64(n) << 20, NetTx: uint64(n) << 19, DiskRead: uint64(n) << 18, DiskWrite: uint64(n) << 18}}, nil
}

// EdgeRequests implements engine.Driver.
func (d *Driver) EdgeRequests(ctx context.Context) (map[string]engine.RequestCounters, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]engine.RequestCounters, len(o.requests))
	for k, v := range o.requests {
		out[k] = v
	}
	return out, nil
}

// LogsSince implements engine.Driver with the lines tests added.
func (d *Driver) LogsSince(ctx context.Context, r *domain.Resource, since map[string]time.Time, fallback time.Time, limit int) ([]engine.ContainerLogLine, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []engine.ContainerLogLine
	for _, l := range o.logs[r.ID] {
		after, ok := since[l.Container]
		if ok && !l.Time.Before(after) || !ok && l.Time.After(fallback) {
			out = append(out, l)
		}
	}
	slices.SortStableFunc(out, func(a, b engine.ContainerLogLine) int { return a.Time.Compare(b.Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// VMStats implements engine.Hypervisor.
func (h *Hypervisor) VMStats(ctx context.Context, id string) (engine.ProcessStats, bool, error) {
	st, err := h.VMStatus(ctx, id)
	if err != nil || st != engine.VMRunning {
		return engine.ProcessStats{}, false, err
	}
	return engine.ProcessStats{CPUSeconds: 1, MemoryBytes: 512 << 20, PID: 1}, true, nil
}

// SetProjectStoreAccess implements engine.Driver.
func (d *Driver) SetProjectStoreAccess(ctx context.Context, user engine.ObjectStore, buckets []string, _ engine.ObjectStore) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(buckets) == 0 {
		delete(o.access, user.AccessKey)
		return nil
	}
	o.access[user.AccessKey] = slices.Sorted(slices.Values(buckets))
	return nil
}

// SetStoreUserAccess implements engine.Driver.
func (d *Driver) SetStoreUserAccess(ctx context.Context, user engine.ObjectStore, grants []api.BucketGrant, _ engine.ObjectStore) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(grants) == 0 {
		delete(o.grants, user.AccessKey)
		return nil
	}
	o.grants[user.AccessKey] = append([]api.BucketGrant(nil), grants...)
	return nil
}

// StoreUserAccess returns what an app's or job's store user reaches (nil: no such user).
func (d *Driver) StoreUserAccess(user string) []api.BucketGrant {
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.grants[user]
}

// ProjectStoreAccess returns the buckets a project store user reaches (nil: no such user).
func (d *Driver) ProjectStoreAccess(user string) []string {
	o := d.op()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.access[user]
}
