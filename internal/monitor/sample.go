package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The sampler (ADR-037): every interval, one point per metric of every resource that runs, written in one
// transaction. Cumulative counters become rates against the previous sample, kept in memory; the first
// sample after a start, or a counter that went down (a restarted container), stores no rate.

// sample is what one pass found, before it is written.
type sample struct {
	at     int64
	points []store.Point
	gauges []gauge
}

type gauge struct {
	r      *domain.Resource
	metric string
	value  float64
}

func (s *sample) add(r *domain.Resource, metric string, v float64) {
	id := store.MachineSeries
	if r != nil {
		id = r.ID
		s.gauges = append(s.gauges, gauge{r, metric, v})
	}
	s.points = append(s.points, store.Point{ResourceID: id, Metric: metric, TS: s.at, Value: v})
}

// addMachine adds a point to a machine's own series.
func (s *sample) addMachine(machineID, metric string, v float64) {
	s.points = append(s.points, store.Point{ResourceID: store.MachineSeriesOf(machineID), Metric: metric, TS: s.at, Value: v})
}

// machineTotals is what runs on one machine, summed.
type machineTotals struct{ cpu, mem, failed float64 }

// rates turns cumulative counters into per-second rates against the previous ones under key; deltas
// holds counters whose increase (not rate) is wanted, such as restarts.
func (m *Monitor) rates(key string, now time.Time, cur map[string]float64) (rates map[string]float64, deltas map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prev == nil {
		m.prev = map[string]counters{}
	}
	prev, ok := m.prev[key]
	m.prev[key] = counters{at: now, values: cur}
	if !ok {
		return nil, nil
	}
	dt := now.Sub(prev.at).Seconds()
	if dt <= 0 {
		return nil, nil
	}
	rates, deltas = map[string]float64{}, map[string]float64{}
	for k, v := range cur {
		p, had := prev.values[k]
		if !had || v < p {
			continue // new or reset: no rate this time
		}
		rates[k], deltas[k] = (v-p)/dt, v-p
	}
	return rates, deltas
}

// SampleOnce takes one sample of every resource and the machine.
func (m *Monitor) SampleOnce(ctx context.Context) error {
	if err := m.Driver.Ping(ctx); err != nil {
		return fmt.Errorf("the container engine isn't reachable: %w", err)
	}
	now := m.Now()
	all, err := m.Store.AllResources(ctx)
	if err != nil {
		return err
	}
	s := &sample{at: now.Unix()}
	edge, err := m.Driver.EdgeRequests(ctx)
	if err != nil {
		m.Log.Debug("read edge request counters failed", "error", err)
	}
	// Every machine's series, the main one's always (MET-03, v0.8 one per machine).
	machines := map[string]*machineTotals{api.MainMachine: {}}
	if list, err := m.Store.ListMachines(ctx); err == nil {
		for _, mc := range list {
			machines[mc.ID] = &machineTotals{}
		}
	}
	on := func(r *domain.Resource) *machineTotals {
		t, ok := machines[r.MachineID()]
		if !ok {
			t = &machineTotals{}
			machines[r.MachineID()] = t
		}
		return t
	}
	for _, r := range all {
		if r.Phase == api.PhaseFailed {
			on(r).failed++
		}
		if r.Phase != api.PhaseReady && r.Phase != api.PhaseUpdating {
			continue
		}
		switch {
		case slices.Contains(api.ContainerKinds, r.Kind):
			cpu, mem := m.sampleContainers(ctx, s, r, now)
			on(r).cpu, on(r).mem = on(r).cpu+cpu, on(r).mem+mem
		case r.Kind == api.KindVM:
			cpu, mem := m.sampleVM(ctx, s, r, now)
			on(r).cpu, on(r).mem = on(r).cpu+cpu, on(r).mem+mem
		case r.Kind == api.KindQueue:
			m.sampleQueue(ctx, s, r, now)
		case r.Kind == api.KindTopic:
			m.sampleTopic(ctx, s, r, now)
		case r.Kind == api.KindVolume:
			sampleVolume(s, r)
		}
		if r.Kind == api.KindApp || r.Kind == api.KindCDN {
			m.sampleRequests(s, r, all, edge, now)
		}
		if r.Kind == api.KindCDN {
			if n, ok := asFloat(r.Outputs[engine.OutputCachedBytes]); ok {
				s.add(r, api.MetricCachedBytes, n)
			}
		}
	}
	for id, t := range machines {
		s.addMachine(id, api.MetricCPU, t.cpu)
		s.addMachine(id, api.MetricMemory, t.mem)
		s.addMachine(id, api.MetricResourcesFailed, t.failed)
	}
	// The disk is the main machine's data folder, where EnclaveStack keeps its database.
	if free, pct, err := diskUsage(m.DataDir); err == nil {
		s.add(nil, api.MetricDiskFree, free)
		s.add(nil, api.MetricDiskPercentFull, pct)
	}
	if err := m.Store.WithTx(ctx, func(q *store.Queries) error { return q.InsertPoints(ctx, s.points) }); err != nil {
		return err
	}
	m.Metrics.ResourceValue.Reset() // deleted resources leave /metrics with their last sample
	for _, g := range s.gauges {
		m.Metrics.ResourceValue.WithLabelValues(g.r.ProjectName(), string(g.r.Kind), g.r.Name, g.metric).Set(g.value)
	}
	return nil
}

// sampleContainers sums a resource's containers (every replica of an app) and returns its CPU and memory.
func (m *Monitor) sampleContainers(ctx context.Context, s *sample, r *domain.Resource, now time.Time) (float64, float64) {
	list, err := m.Driver.Stats(ctx, r)
	if err != nil {
		m.Log.Debug("read container stats failed", "resource_id", r.ID, "error", err)
		return 0, 0
	}
	if len(list) == 0 {
		return 0, 0
	}
	var mem, limit float64
	var cpu, rx, tx, rd, wr float64
	restarts := 0.0
	for _, c := range list {
		mem, limit = mem+float64(c.MemoryBytes), limit+float64(c.MemoryLimit)
		rates, deltas := m.rates(r.ID+"|"+c.ID, now, map[string]float64{"cpu": c.CPUSeconds, "rx": float64(c.NetRx), "tx": float64(c.NetTx),
			"rd": float64(c.DiskRead), "wr": float64(c.DiskWrite), "restarts": float64(c.Restarts)})
		cpu, rx, tx, rd, wr = cpu+rates["cpu"], rx+rates["rx"], tx+rates["tx"], rd+rates["rd"], wr+rates["wr"]
		restarts += deltas["restarts"]
	}
	s.add(r, api.MetricCPU, cpu)
	s.add(r, api.MetricMemory, mem)
	if limit > 0 {
		s.add(r, api.MetricMemoryLimit, limit)
	}
	s.add(r, api.MetricNetRx, rx)
	s.add(r, api.MetricNetTx, tx)
	s.add(r, api.MetricDiskRead, rd)
	s.add(r, api.MetricDiskWrite, wr)
	s.add(r, api.MetricRestarts, restarts)
	return cpu, mem
}

// sampleVM reads the VM's QEMU process; a new process ID while it ran before is a restart.
func (m *Monitor) sampleVM(ctx context.Context, s *sample, r *domain.Resource, now time.Time) (float64, float64) {
	if m.Hypervisor == nil {
		return 0, 0
	}
	st, ok, err := m.Hypervisor.VMStats(ctx, r.ID)
	if err != nil || !ok {
		return 0, 0
	}
	m.mu.Lock()
	prevPID := 0.0
	if p, ok := m.prev[r.ID+"|pid"]; ok {
		prevPID = p.values["pid"]
	}
	if m.prev == nil {
		m.prev = map[string]counters{}
	}
	m.prev[r.ID+"|pid"] = counters{at: now, values: map[string]float64{"pid": float64(st.PID)}}
	m.mu.Unlock()
	restarts := 0.0
	if prevPID != 0 && prevPID != float64(st.PID) {
		restarts = 1
	}
	rates, _ := m.rates(r.ID+"|"+strconv.Itoa(st.PID), now, map[string]float64{"cpu": st.CPUSeconds})
	s.add(r, api.MetricCPU, rates["cpu"])
	s.add(r, api.MetricMemory, float64(st.MemoryBytes))
	s.add(r, api.MetricRestarts, restarts)
	return rates["cpu"], float64(st.MemoryBytes)
}

// sampleRequests turns the edge's counters for an app's domains (or a CDN's hostname) into rates.
func (m *Monitor) sampleRequests(s *sample, r *domain.Resource, all []*domain.Resource, edge map[string]engine.RequestCounters, now time.Time) {
	var hosts []string
	if r.Kind == api.KindCDN {
		var c api.CDNSpec
		if json.Unmarshal(r.Spec, &c) == nil {
			hosts = append(hosts, c.Hostname)
		}
	} else {
		for _, d := range all {
			var ds api.DomainSpec
			if d.Kind == api.KindDomain && d.ProjectName() == r.ProjectName() && json.Unmarshal(d.Spec, &ds) == nil && ds.App == r.Name {
				hosts = append(hosts, ds.Hostname)
			}
		}
	}
	if len(hosts) == 0 {
		return
	}
	var total engine.RequestCounters
	for _, h := range hosts {
		c := edge[h]
		total.Requests, total.Errors5xx, total.DurationSeconds = total.Requests+c.Requests, total.Errors5xx+c.Errors5xx, total.DurationSeconds+c.DurationSeconds
	}
	rates, deltas := m.rates(r.ID+"|edge", now, map[string]float64{"req": total.Requests, "err": total.Errors5xx, "dur": total.DurationSeconds})
	if rates == nil {
		return
	}
	s.add(r, api.MetricRequests, rates["req"])
	s.add(r, api.MetricErrors5xx, rates["err"])
	if deltas["req"] > 0 {
		s.add(r, api.MetricLatency, deltas["dur"]/deltas["req"]*1000)
	}
}

func (m *Monitor) sampleQueue(ctx context.Context, s *sample, r *domain.Resource, now time.Time) {
	if m.Broker == nil {
		return
	}
	b, err := m.Broker.Broker(ctx)
	if err != nil {
		return
	}
	qs, err := b.QueueStats(ctx, domain.BrokerKey(r.Project, r.Name))
	if err != nil {
		return
	}
	s.add(r, api.MetricQueueDepth, float64(qs.Messages))
	s.add(r, api.MetricInFlight, float64(qs.InFlight))
	age := 0.0
	if qs.Oldest != nil {
		age = now.Sub(*qs.Oldest).Seconds()
	}
	s.add(r, api.MetricOldestAge, max(age, 0))
}

func (m *Monitor) sampleTopic(ctx context.Context, s *sample, r *domain.Resource, now time.Time) {
	counts, err := m.Store.SubscriptionCounts(ctx, r.ID)
	if err != nil {
		return
	}
	var delivered, failed float64
	for _, c := range counts {
		delivered, failed = delivered+float64(c.Delivered), failed+float64(c.Failed)
	}
	rates, _ := m.rates(r.ID+"|topic", now, map[string]float64{"ok": delivered, "fail": failed})
	if rates == nil {
		return
	}
	s.add(r, api.MetricDelivered, rates["ok"])
	s.add(r, api.MetricDeliveryFailures, rates["fail"])
}

// sampleVolume reads the used bytes the reconciler's drift pass keeps current.
func sampleVolume(s *sample, r *domain.Resource) {
	used, ok := asFloat(r.Outputs["usedBytes"])
	if !ok {
		return
	}
	s.add(r, api.MetricUsedBytes, used)
	var vs api.VolumeSpec
	if json.Unmarshal(r.Spec, &vs) == nil && vs.SizeGiB > 0 {
		s.add(r, api.MetricPercentFull, min(100, used/float64(int64(vs.SizeGiB)<<30)*100))
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// diskUsage is the free bytes and percent full of the filesystem holding dir.
func diskUsage(dir string) (float64, float64, error) {
	if dir == "" {
		return 0, 0, errors.New("no data directory")
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	total := float64(st.Blocks) * float64(st.Bsize)
	free := float64(st.Bavail) * float64(st.Bsize)
	if total == 0 {
		return free, 0, nil
	}
	return free, (total - free) / total * 100, nil
}
