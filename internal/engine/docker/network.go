package docker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Networks (ADR-020): one bridge network per network resource, its CIDR as the IPAM subnet and its
// first subnet as the automatic range. Resources in another subnet get a fixed address chosen here.

// systemContainers join every EnclaveStack network, so domains, health checks and buckets work everywhere
// (L2-006 NET-04).
var systemContainers = []string{engine.EdgeName, engine.ProbeName, engine.ObjectStoreName, engine.BrokerName}

// reservationTTL bounds how long an address chosen for a container that hasn't started yet is held
// back from other containers of this process.
const reservationTTL = 2 * time.Minute

// EnsureNetwork implements engine.Driver.
func (d *Driver) EnsureNetwork(ctx context.Context, in engine.NetworkInput) (engine.Outputs, error) {
	r, spec := in.Resource, in.Spec
	name := engine.DockerNetwork(r.Project, r.Name)
	cidr, ok := api.ParseCIDR(spec.CIDR)
	if !ok {
		return nil, engine.Permanent(fmt.Errorf("the network's range %q isn't valid", spec.CIDR))
	}
	gateway := cidr.Addr().Next().String()
	existing, err := d.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	switch {
	case err == nil:
		if existing.Labels[LabelResourceID] != r.ID {
			return nil, engine.Permanent(fmt.Errorf("a network named %s already exists and isn't this one. Remove it, then retry", name))
		}
		if len(existing.IPAM.Config) == 0 || existing.IPAM.Config[0].Subnet != spec.CIDR {
			return nil, engine.Permanent(fmt.Errorf("the engine network %s has other addresses than %s. Remove it, then retry", name, spec.CIDR))
		}
	case isNotFound(err):
		subnets := spec.EffectiveSubnets()
		ipam := network.IPAMConfig{Subnet: spec.CIDR, Gateway: gateway}
		if subnets[0].CIDR != spec.CIDR {
			ipam.IPRange = subnets[0].CIDR
		}
		cctx, cancel := context.WithTimeout(ctx, stepTimeout)
		_, err := d.cli.NetworkCreate(cctx, name, network.CreateOptions{Driver: "bridge", Internal: !spec.HasInternet(),
			IPAM:   &network.IPAM{Driver: "default", Config: []network.IPAMConfig{ipam}},
			Labels: map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelKind: string(api.KindNetwork)}})
		cancel()
		if err != nil && strings.Contains(err.Error(), "overlaps") {
			return nil, engine.Permanent(fmt.Errorf("%s overlaps a network on this machine that EnclaveStack doesn't manage (%v). Choose another range", spec.CIDR, err))
		}
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return nil, d.classify(fmt.Errorf("create network %s: %w", name, err))
		}
	default:
		return nil, d.classify(fmt.Errorf("inspect network %s: %w", name, err))
	}
	for _, c := range systemContainers {
		if err := d.connectIfMissing(ctx, name, c); err != nil {
			return nil, err
		}
	}
	return engine.Outputs{"dockerNetwork": name, "gateway": gateway}, nil
}

// DeleteNetwork implements engine.Driver.
func (d *Driver) DeleteNetwork(ctx context.Context, r *domain.Resource) error {
	name := engine.DockerNetwork(r.Project, r.Name)
	n, err := d.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return d.classify(fmt.Errorf("inspect network %s: %w", name, err))
	}
	if n.Labels[LabelResourceID] != r.ID {
		return nil // not ours: leave it alone
	}
	for _, ep := range n.Containers {
		if slices.Contains(systemContainers, ep.Name) {
			if err := d.cli.NetworkDisconnect(ctx, name, ep.Name, true); err != nil && !isNotFound(err) {
				return d.classify(fmt.Errorf("disconnect %s from %s: %w", ep.Name, name, err))
			}
		}
	}
	rctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.NetworkRemove(rctx, name); err != nil && !isNotFound(err) {
		return d.classify(fmt.Errorf("remove network %s (containers still attached?): %w", name, err))
	}
	return nil
}

// networkHealth reports a network missing, or "stopped" when a system service isn't connected to it, so
// drift repair runs EnsureNetwork again.
func (d *Driver) networkHealth(ctx context.Context, r *domain.Resource) (engine.Health, error) {
	n, err := d.cli.NetworkInspect(ctx, engine.DockerNetwork(r.Project, r.Name), network.InspectOptions{})
	if isNotFound(err) {
		return engine.Missing, nil
	}
	if err != nil {
		return engine.Missing, d.classify(err)
	}
	attached := map[string]bool{}
	for _, ep := range n.Containers {
		attached[ep.Name] = true
	}
	for _, c := range systemContainers {
		// Only running containers show as attached; a stopped system container isn't drift of the network.
		if ci, err := d.inspect(ctx, c); err == nil && ci != nil && ci.State != nil && ci.State.Running && !attached[c] {
			return engine.Stopped, nil
		}
	}
	return engine.Healthy, nil
}

// connectIfMissing connects an existing container to a network it isn't on yet.
func (d *Driver) connectIfMissing(ctx context.Context, net, name string) error {
	c, err := d.inspect(ctx, name)
	if err != nil || c == nil {
		return err
	}
	if c.NetworkSettings != nil {
		if _, ok := c.NetworkSettings.Networks[net]; ok {
			return nil
		}
	}
	ep, err := d.systemEndpoint(ctx, net)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	err = d.cli.NetworkConnect(cctx, net, name, ep)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return d.classify(fmt.Errorf("connect %s to %s: %w", name, net, err))
	}
	return nil
}

// joinManagedNetworks connects a freshly created system container to every EnclaveStack network.
func (d *Driver) joinManagedNetworks(ctx context.Context, name string) error {
	lctx, cancel := context.WithTimeout(ctx, stepTimeout)
	list, err := d.cli.NetworkList(lctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("label", LabelKind+"="+string(api.KindNetwork)))})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("list networks: %w", err))
	}
	for _, n := range list {
		if err := d.connectIfMissing(ctx, n.Name, name); err != nil {
			return err
		}
	}
	return nil
}

// endpoint is the networking config of a container joining p under aliases. With a fixed subnet it
// reserves the lowest free address there.
func (d *Driver) endpoint(ctx context.Context, p engine.Placement, aliases []string) (*network.NetworkingConfig, error) {
	net := p.NetworkName()
	if net == engine.NetworkName {
		if err := d.ensureNetwork(ctx); err != nil {
			return nil, err
		}
	} else if _, err := d.cli.NetworkInspect(ctx, net, network.InspectOptions{}); isNotFound(err) {
		return nil, fmt.Errorf("the network %s doesn't exist yet; it is being restored", strings.TrimPrefix(net, "es-net-"))
	} else if err != nil {
		return nil, d.classify(err)
	}
	ep := &network.EndpointSettings{Aliases: aliases}
	switch {
	case p.Address != "": // a move keeps its address (ADR-047)
		if err := d.removeStandInAt(ctx, net, p.Address); err != nil {
			return nil, err
		}
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: p.Address}
	case d.meshCfg() != nil: // with a mesh, this machine's pool (ADR-045)
		addr, err := d.poolAddress(ctx, net, p.Subnet)
		if err != nil {
			return nil, err
		}
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: addr}
	case p.Subnet != "":
		addr, err := d.freeAddress(ctx, net, p.Subnet)
		if err != nil {
			return nil, err
		}
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: addr}
	}
	return &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{net: ep}}, nil
}

// freeAddress picks the lowest address of subnet that no endpoint uses and this process hasn't just
// handed out. The network address, the gateway and the broadcast address are never used.
func (d *Driver) freeAddress(ctx context.Context, net, subnet string) (string, error) {
	sub, ok := api.ParseCIDR(subnet)
	if !ok {
		return "", engine.Permanent(fmt.Errorf("subnet %q isn't valid", subnet))
	}
	n, err := d.cli.NetworkInspect(ctx, net, network.InspectOptions{})
	if err != nil {
		return "", d.classify(fmt.Errorf("inspect network %s: %w", net, err))
	}
	used := map[netip.Addr]bool{}
	for _, cfg := range n.IPAM.Config {
		if g, err := netip.ParseAddr(cfg.Gateway); err == nil {
			used[g] = true
		}
	}
	for _, ep := range n.Containers {
		if p, err := netip.ParsePrefix(ep.IPv4Address); err == nil {
			used[p.Addr()] = true
		}
	}
	d.addrMu.Lock()
	defer d.addrMu.Unlock()
	now := time.Now()
	for k, at := range d.reserved {
		if now.Sub(at) > reservationTTL {
			delete(d.reserved, k)
		}
	}
	last := lastAddr(sub)
	for a := sub.Addr().Next(); sub.Contains(a) && a != last; a = a.Next() {
		key := net + "/" + a.String()
		if !used[a] && d.reserved[key].IsZero() {
			d.reserved[key] = now
			return a.String(), nil
		}
	}
	return "", engine.Permanent(errors.New("the subnet " + subnet + " has no free addresses left"))
}

// lastAddr is a prefix's broadcast address.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := 32 - p.Bits()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v |= (1 << host) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// systemEndpoint is how a system container joins net: an address of this machine's pool with a mesh
// (ADR-045), else the engine's choice.
func (d *Driver) systemEndpoint(ctx context.Context, net string) (*network.EndpointSettings, error) {
	addr, err := d.poolAddress(ctx, net, "")
	if err != nil {
		return nil, err
	}
	if addr == "" {
		return &network.EndpointSettings{}, nil
	}
	return &network.EndpointSettings{IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: addr}}, nil
}

// systemNetworking joins a new system container to the default network, as systemEndpoint does.
func (d *Driver) systemNetworking(ctx context.Context, aliases ...string) (*network.NetworkingConfig, error) {
	if err := d.ensureNetwork(ctx); err != nil {
		return nil, err
	}
	ep, err := d.systemEndpoint(ctx, engine.NetworkName)
	if err != nil {
		return nil, err
	}
	ep.Aliases = aliases
	return &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: ep}}, nil
}
