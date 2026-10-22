package fake

import (
	"context"
	"fmt"
	"net/netip"

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
	gateway := rng.Addr().Next()
	for a := gateway.Next(); rng.Contains(a); a = a.Next() {
		if !used[a.String()] {
			return a.String(), nil
		}
	}
	return "", engine.Permanent(fmt.Errorf("the subnet %s has no free addresses left", rng))
}

// publishes reports whether containers in p can publish host ports. Callers hold d.mu.
func (d *Driver) publishes(p engine.Placement) bool {
	n := d.networks[p.Network]
	return n == nil || n.spec.HasInternet()
}
