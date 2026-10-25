package fake

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// VM links (ADR-026): a runtime per VM with its placement, address and the host ports it keeps.

type linkPorts struct{ frame, ssh int }

// EnsureVMLink implements engine.Driver.
func (d *Driver) EnsureVMLink(ctx context.Context, in engine.EnsureInput) (engine.Outputs, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	r := in.Resource
	spec, _ := in.Spec.(api.VMSpec)
	d.mu.Lock()
	if d.links == nil {
		d.links = map[string]linkPorts{}
	}
	ports, ok := d.links[r.ID]
	if !ok {
		ports = linkPorts{frame: in.HostPorts[engine.VMLinkFramePort], ssh: in.HostPorts[api.SSHPort]}
		if ports.frame == 0 {
			ports.frame = d.nextPort
			d.nextPort++
		}
		if ports.ssh == 0 {
			ports.ssh = d.nextPort
			d.nextPort++
		}
		d.links[r.ID] = ports
	}
	rt := d.runtimes[r.ID]
	if rt == nil || rt.placement != in.Placement || !rt.running {
		delete(d.runtimes, r.ID)
		ip, err := d.address(in.Placement)
		if err != nil {
			d.mu.Unlock()
			return nil, err
		}
		rt = (&runtime{placement: in.Placement, ip: ip}).named(r)
		d.runtimes[r.ID] = rt
	}
	rt.generation, rt.running = r.Generation, true
	if in.Firewall != nil {
		d.load(r.ID, in.Firewall)
	}
	published := d.publishes(rt.placement)
	d.mu.Unlock()
	if len(spec.Publish) > 0 && !published {
		return nil, engine.Permanent(fmt.Errorf("the network has no internet access, so the VM can't publish ports"))
	}
	if in.Started != nil {
		if err := in.Started(ctx); err != nil {
			return nil, err
		}
	}
	out := engine.Outputs{engine.OutputPrivateIP: rt.ip, engine.OutputPrivateDNS: engine.PrivateDNS(r), engine.OutputLinkPort: ports.frame,
		engine.OutputSSHAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(ports.ssh))}
	if len(spec.Publish) > 0 {
		out[engine.OutputHost] = "127.0.0.1"
	}
	return out, nil
}

// DeleteVMLink implements engine.Driver.
func (d *Driver) DeleteVMLink(_ context.Context, r *domain.Resource) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.runtimes, r.ID)
	delete(d.links, r.ID)
	delete(d.rules, r.ID)
	return nil
}

// HasLink reports whether a VM's link runs.
func (d *Driver) HasLink(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	rt := d.runtimes[id]
	return rt != nil && rt.running
}
