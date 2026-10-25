package fake

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// defaultRange stands in for the enclave network's addresses.
var defaultRange = netip.MustParsePrefix("172.30.0.0/16")

type network struct {
	id   string
	spec api.NetworkSpec
}

// EnsureNetwork implements engine.Driver.
func (d *Driver) EnsureNetwork(ctx context.Context, in engine.NetworkInput) (engine.Outputs, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	if err := d.sleep(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.faults[in.Resource.Name]; f != nil && f.Permanent {
		return nil, engine.Permanent(fmt.Errorf("%s overlaps a network on this machine that EnclaveStack doesn't manage (injected fault)", in.Spec.CIDR))
	}
	name := engine.DockerNetwork(in.Resource.Project, in.Resource.Name)
	if n := d.networks[name]; n != nil && n.id != in.Resource.ID {
		return nil, engine.Permanent(fmt.Errorf("a network named %s already exists and isn't this one", name))
	}
	d.networks[name] = &network{id: in.Resource.ID, spec: in.Spec}
	cidr, _ := api.ParseCIDR(in.Spec.CIDR)
	return engine.Outputs{"dockerNetwork": name, "gateway": cidr.Addr().Next().String()}, nil
}

// DeleteNetwork implements engine.Driver.
func (d *Driver) DeleteNetwork(ctx context.Context, r *domain.Resource) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	name := engine.DockerNetwork(r.Project, r.Name)
	for _, rt := range d.runtimes {
		if rt.placement.Network == name {
			return fmt.Errorf("remove network %s: containers still attached", name)
		}
	}
	delete(d.networks, name)
	return nil
}

// KillNetwork simulates a network removed outside EnclaveStack (drift). name is the network resource's
// name, qualified with ".<project>" outside the default project.
func (d *Driver) KillNetwork(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.networks, engine.DockerNetwork("", name))
}

// HasNetwork reports whether the engine network of a network resource exists.
func (d *Driver) HasNetwork(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.networks[engine.DockerNetwork("", name)] != nil
}

// PlacementOf returns where a resource's runtime was placed.
func (d *Driver) PlacementOf(resourceID string) engine.Placement {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rt := d.runtimes[resourceID]; rt != nil {
		return rt.placement
	}
	return engine.Placement{}
}

// address hands out the next free address for p, as the Docker driver does: the subnet's lowest free
// one, or the network's automatic range (its first subnet). Callers hold d.mu.
func (d *Driver) address(p engine.Placement) (string, error) {
	rng := defaultRange
	if p.Network != "" {
		n := d.networks[p.Network]
		if n == nil {
			return "", fmt.Errorf("the network %s doesn't exist yet; it is being restored", p.Network)
		}
		rng, _ = api.ParseCIDR(n.spec.EffectiveSubnets()[0].CIDR)
		if p.Subnet != "" {
			rng, _ = api.ParseCIDR(p.Subnet)
		}
	}
	used := map[string]bool{}
	for _, rt := range d.runtimes {
		used[rt.ip] = true
	}
	if p.Address != "" { // a move keeps its address (ADR-047)
		if used[p.Address] {
			return "", fmt.Errorf("the address %s is taken on this machine", p.Address)
		}
		return p.Address, nil
	}
	// With a mesh, only this machine's pool may be used in the networks it spans (ADR-045).
	var pool map[string]bool
	if _, spans := d.meshNetworks()[p.NetworkName()]; spans || (d.mesh != nil && d.mesh.Enabled && p.NetworkName() == engine.NetworkName) {
		pool = map[string]bool{}
		for _, a := range d.mesh.Pools[p.NetworkName()] {
			pool[a] = true
		}
	}
	gateway := rng.Addr().Next()
	for a := gateway.Next(); rng.Contains(a); a = a.Next() {
		if !used[a.String()] && (pool == nil || pool[a.String()]) {
			return a.String(), nil
		}
	}
	if pool != nil {
		return "", fmt.Errorf("this machine has no free addresses left in %s; waiting for more", rng)
	}
	return "", engine.Permanent(fmt.Errorf("the subnet %s has no free addresses left", rng))
}

// publishes reports whether containers in p can publish host ports. Callers hold d.mu.
func (d *Driver) publishes(p engine.Placement) bool {
	n := d.networks[p.Network]
	return n == nil || n.spec.HasInternet()
}

// ConfigureMesh implements engine.Driver: it records the configuration (tests read it with Mesh), and
// its pools bound the addresses handed out from then on.
func (d *Driver) ConfigureMesh(ctx context.Context, cfg engine.MeshConfig) (engine.MeshStatus, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.MeshStatus{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c := cfg
	d.mesh = &c
	return d.meshStatus(), nil
}

// MeshStatus implements engine.Driver: every peer was heard from just now.
func (d *Driver) MeshStatus(ctx context.Context) (engine.MeshStatus, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.MeshStatus{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.meshStatus(), nil
}

func (d *Driver) meshStatus() engine.MeshStatus {
	st := engine.MeshStatus{PublicKey: fmt.Sprintf("fake-key-%p", d), Handshakes: map[string]time.Time{}}
	if d.mesh != nil {
		for _, p := range d.mesh.Peers {
			st.Handshakes[p.PublicKey] = time.Now()
		}
	}
	return st
}

// Mesh returns the last mesh configuration applied, or nil.
func (d *Driver) Mesh() *engine.MeshConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mesh
}

// NetworkEndpoints implements engine.Driver: the runtimes, by network, with the names they answer to.
func (d *Driver) NetworkEndpoints(ctx context.Context) ([]engine.NetworkInfo, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	byNet := map[string]*engine.NetworkInfo{engine.NetworkName: {Name: engine.NetworkName, Subnet: defaultRange.String()}}
	if d.mesh != nil && d.mesh.DefaultRange != "" {
		byNet[engine.NetworkName].Subnet = d.mesh.DefaultRange
	}
	for name, n := range d.networks {
		byNet[name] = &engine.NetworkInfo{Name: name, Subnet: n.spec.CIDR}
	}
	ids := make([]string, 0, len(d.runtimes))
	for id := range d.runtimes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		rt := d.runtimes[id]
		if !rt.running || rt.ip == "" {
			continue
		}
		if n := byNet[rt.placement.NetworkName()]; n != nil {
			n.Members = append(n.Members, engine.NetworkMember{Container: rt.container, Address: rt.ip, Aliases: rt.aliases, ResourceID: id})
		}
	}
	// The edge proxy and relays are on the default network too (ADR-052), like the real ones.
	if d.edge.running {
		byNet[engine.NetworkName].Members = append(byNet[engine.NetworkName].Members, engine.NetworkMember{Container: engine.EdgeName, Address: fakeEdgeAddress})
	}
	conns := make([]string, 0, len(d.connectors))
	for name := range d.connectors {
		conns = append(conns, name)
	}
	slices.Sort(conns)
	for _, name := range conns {
		if k := d.connectors[name]; k.running && k.cfg.Network == engine.NetworkName {
			byNet[engine.NetworkName].Members = append(byNet[engine.NetworkName].Members, engine.NetworkMember{Container: name, Address: k.address, ResourceID: k.cfg.TunnelID})
		}
	}
	names := make([]string, 0, len(byNet))
	for n := range byNet {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]engine.NetworkInfo, 0, len(names))
	for _, n := range names {
		out = append(out, *byNet[n])
	}
	return out, nil
}

// meshNetworks are the networks the mesh spans, or none without a mesh.
func (d *Driver) meshNetworks() map[string]string {
	if d.mesh == nil || !d.mesh.Enabled {
		return nil
	}
	return d.mesh.Networks
}
