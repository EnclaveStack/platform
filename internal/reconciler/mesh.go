package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The mesh sync (ADR-045): every MeshEvery, and at once when a placed resource's containers change, each
// online machine gets its whole mesh configuration: its peers with the addresses that live there, its
// address pools, and stand-ins for every container elsewhere. A configuration is only sent when it changed.

// DefaultMeshEvery is how often the mesh is synced without a reason.
const DefaultMeshEvery = 5 * time.Second

// meshState is what the sync remembers between passes.
type meshState struct {
	mu      sync.Mutex
	applied map[string]string            // machine ID → hash of the configuration it has
	status  map[string]engine.MeshStatus // machine ID → its tunnel's last status
	enabled bool
	wake    chan struct{}
}

func (r *Reconciler) meshInit() {
	r.mesh.mu.Lock()
	defer r.mesh.mu.Unlock()
	if r.mesh.applied == nil {
		r.mesh.applied, r.mesh.status, r.mesh.wake = map[string]string{}, map[string]engine.MeshStatus{}, make(chan struct{}, 1)
	}
}

// MeshChanged asks for a sync soon (a resource's containers changed).
func (r *Reconciler) MeshChanged() {
	r.meshInit()
	select {
	case r.mesh.wake <- struct{}{}:
	default:
	}
}

func (r *Reconciler) meshLoop(ctx context.Context) {
	r.meshInit()
	every := r.MeshEvery
	if every <= 0 {
		every = DefaultMeshEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.mesh.wake:
		}
		if err := r.SyncMesh(ctx); err != nil && ctx.Err() == nil {
			r.Log.Warn("mesh sync failed", "error", err)
		}
		r.rescheduleOffline(ctx)
	}
}

// SyncMesh runs one pass (exported for tests and for the Started hook, which must see new containers
// announced before they take traffic, XNET-07).
func (r *Reconciler) SyncMesh(ctx context.Context) error {
	if r.Machines == nil {
		return nil
	}
	r.meshInit()
	r.meshMu.Lock()
	defer r.meshMu.Unlock()
	members, err := r.Machines.Members(ctx)
	if err != nil {
		return err
	}
	if len(members) < 2 {
		return r.disableMesh(ctx, members)
	}
	var online []engine.MeshMember
	infos := map[string][]engine.NetworkInfo{}
	for _, m := range members {
		if !m.Online {
			continue
		}
		ni, err := m.Driver.NetworkEndpoints(ctx)
		if err != nil {
			r.Log.Warn("read a machine's networks", "machine", m.Name, "error", err)
			continue
		}
		online = append(online, m)
		infos[m.ID] = ni
	}
	defaultRange := ""
	for _, n := range infos[r.Store.MainID()] {
		if n.Name == engine.NetworkName {
			defaultRange = n.Subnet
		}
	}
	if defaultRange == "" {
		// The main machine didn't list its networks (logged above): the others wait for its range.
		return nil
	}
	nets, err := r.meshNetworks(ctx, defaultRange)
	if err != nil {
		return err
	}
	pools, err := r.topUpPools(ctx, nets, online, infos)
	if err != nil {
		return err
	}
	networks := map[string]string{}
	for _, n := range nets {
		networks[n.name] = n.cidr
	}
	var errs []error
	for _, m := range online {
		cfg := engine.MeshConfig{Enabled: true, TunnelPort: m.TunnelPort, DefaultRange: defaultRange, Networks: networks, Pools: pools[m.ID]}
		for _, p := range online {
			if p.ID == m.ID {
				continue
			}
			peer := engine.MeshPeer{MachineID: p.ID, PublicKey: p.PublicKey, Endpoint: p.Endpoint}
			for _, n := range infos[p.ID] {
				for _, mem := range n.Members {
					peer.Addresses = append(peer.Addresses, mem.Address)
					cfg.StandIns = append(cfg.StandIns, engine.StandIn{Network: n.Name, Address: mem.Address, Aliases: mem.Aliases, Of: mem.Container, Machine: p.ID})
				}
			}
			slices.Sort(peer.Addresses)
			cfg.Peers = append(cfg.Peers, peer)
		}
		if err := r.applyMesh(ctx, m, cfg); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Name, err))
		}
	}
	r.mesh.mu.Lock()
	r.mesh.enabled = true
	r.mesh.mu.Unlock()
	if len(errs) > 0 {
		return fmt.Errorf("%v", errs)
	}
	return nil
}

// applyMesh sends a machine its configuration when it changed, and keeps the key and handshakes it reports.
func (r *Reconciler) applyMesh(ctx context.Context, m engine.MeshMember, cfg engine.MeshConfig) error {
	b, _ := json.Marshal(cfg)
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	r.mesh.mu.Lock()
	same := r.mesh.applied[m.ID] == hash
	r.mesh.mu.Unlock()
	var st engine.MeshStatus
	var err error
	if same {
		st, err = m.Driver.MeshStatus(ctx)
	} else {
		st, err = m.Driver.ConfigureMesh(ctx, cfg)
	}
	if err != nil {
		r.mesh.mu.Lock()
		delete(r.mesh.applied, m.ID) // try again next pass
		r.mesh.mu.Unlock()
		return err
	}
	r.mesh.mu.Lock()
	r.mesh.applied[m.ID], r.mesh.status[m.ID] = hash, st
	r.mesh.mu.Unlock()
	if st.PublicKey != "" && st.PublicKey != m.PublicKey {
		if err := r.Machines.RecordTunnelKey(ctx, m.ID, st.PublicKey); err != nil {
			return err
		}
		r.MeshChanged() // the peers learn the new key on the next pass
	}
	if !same {
		r.Log.Info("mesh configured", "machine", m.Name, "peers", len(cfg.Peers), "stand_ins", len(cfg.StandIns))
	}
	return nil
}

// disableMesh takes the mesh away from the main machine when it is the only one again.
func (r *Reconciler) disableMesh(ctx context.Context, members []engine.MeshMember) error {
	r.mesh.mu.Lock()
	was := r.mesh.enabled
	r.mesh.mu.Unlock()
	if !was {
		return nil
	}
	for _, m := range members {
		if m.Online {
			if _, err := m.Driver.ConfigureMesh(ctx, engine.MeshConfig{}); err != nil {
				return err
			}
		}
	}
	r.mesh.mu.Lock()
	r.mesh.enabled, r.mesh.applied = false, map[string]string{}
	r.mesh.mu.Unlock()
	return nil
}

type meshNetwork struct {
	name, cidr string
	ranges     []string
}

// meshNetworks are the default network and every network resource, with the ranges placements draw from.
func (r *Reconciler) meshNetworks(ctx context.Context, defaultRange string) ([]meshNetwork, error) {
	out := []meshNetwork{{name: engine.NetworkName, cidr: defaultRange}}
	rows, err := r.Store.ResourcesOfKind(ctx, api.KindNetwork)
	if err != nil {
		return nil, err
	}
	for _, n := range rows {
		if n.Phase == api.PhaseDeleting {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindNetwork, n.Spec)
		if err != nil {
			continue
		}
		spec := raw.(api.NetworkSpec)
		mn := meshNetwork{name: engine.DockerNetwork(n.ProjectName(), n.Name), cidr: spec.CIDR}
		for _, s := range spec.EffectiveSubnets() {
			mn.ranges = append(mn.ranges, s.CIDR)
		}
		out = append(out, mn)
	}
	return out, nil
}

// topUpPools keeps every online machine's pools full and returns them by machine and network.
func (r *Reconciler) topUpPools(ctx context.Context, nets []meshNetwork, online []engine.MeshMember, infos map[string][]engine.NetworkInfo) (map[string]map[string][]string, error) {
	ids := make([]string, 0, len(online))
	for _, m := range online {
		ids = append(ids, m.ID)
	}
	out := map[string]map[string][]string{}
	err := r.Store.WithTx(ctx, func(q *store.Queries) error {
		for _, n := range nets {
			pooled := map[string]string{}
			rows, err := q.AddressPool(ctx, n.name)
			if err != nil {
				return err
			}
			for _, p := range rows {
				pooled[p.Address] = p.MachineID
			}
			used := map[string]string{}
			for id, ni := range infos {
				for _, info := range ni {
					if info.Name != n.name {
						continue
					}
					for _, m := range info.Members {
						used[m.Address] = id
					}
				}
			}
			ch := domain.TopUp(domain.PoolNetwork{CIDR: n.cidr, Ranges: n.ranges, Machines: ids, Pooled: pooled, Used: used})
			for a, m := range ch.Give {
				if err := q.PoolAddressTo(ctx, n.name, a, m); err != nil {
					return err
				}
				pooled[a] = m
			}
			for _, a := range ch.Conflicts {
				r.Log.Error("an address is on another machine than its pool says", "network", n.name, "address", a)
			}
			for a, m := range pooled {
				if out[m] == nil {
					out[m] = map[string][]string{}
				}
				out[m][n.name] = append(out[m][n.name], a)
			}
		}
		return nil
	})
	for _, byNet := range out {
		for n := range byNet {
			slices.Sort(byNet[n])
		}
	}
	return out, err
}

// MeshLinks is a machine's tunnel to each other online machine, from its last status (XNET-06).
func (r *Reconciler) MeshLinks(ctx context.Context, machineID string) []api.MachineLink {
	if r.Machines == nil {
		return nil
	}
	r.meshInit()
	members, err := r.Machines.Members(ctx)
	if err != nil {
		return nil
	}
	r.mesh.mu.Lock()
	st, ok := r.mesh.status[machineID]
	r.mesh.mu.Unlock()
	links := []api.MachineLink{}
	for _, m := range members {
		if m.ID == machineID || !m.Online {
			continue
		}
		l := api.MachineLink{Machine: m.Name}
		if t, heard := st.Handshakes[m.PublicKey]; ok && heard && m.PublicKey != "" {
			l.LastHandshakeAt = &t
			l.Connected = time.Since(t) < 3*time.Minute // WireGuard renews its session every two minutes
		}
		links = append(links, l)
	}
	return links
}
