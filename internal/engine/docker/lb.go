package docker

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// Load balancers (ADR-022): one HAProxy container each, in its targets' network, configured whole by
// the reconciler and reloaded gracefully (SIGUSR2 to the master) after `haproxy -c` accepts it.

// LabelConfigHash records the configuration a load balancer container runs.
const LabelConfigHash = "enclave.config-hash"

func (d *Driver) ensureLoadBalancer(ctx context.Context, in engine.EnsureInput, report engine.Reporter) (engine.Outputs, error) {
	r := in.Resource
	spec, _ := in.Spec.(api.LoadBalancerSpec)
	if in.LoadBalancer == nil {
		return nil, engine.Permanent(fmt.Errorf("the load balancer %s has no resolved targets", r.Name))
	}
	name := engine.ContainerName(r)
	conf := lb.Render(*in.LoadBalancer)

	report.Step("pull", api.StepActive)
	if err := d.ensureImage(ctx, lb.Image, false); err != nil {
		return nil, err
	}
	report.Step("pull", api.StepDone)

	report.Step("configure", api.StepActive)
	existing, err := d.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	keepPort := 0
	if existing != nil && !d.lbMatches(existing, r, spec, in.Placement) {
		if spec.Publish {
			keepPort = hostPort(existing, spec.Port)
		}
		if err := d.remove(ctx, name); err != nil {
			return nil, err
		}
		existing = nil
	}
	if existing == nil {
		if err := d.createLoadBalancer(ctx, r, spec, in.Placement, keepPort); err != nil {
			return nil, err
		}
	}
	dir, file := path.Split(lb.ConfigPath)
	if err := d.copyFiles(ctx, name, dir, map[string][]byte{file: []byte(conf)}, 0o644); err != nil { // HAProxy runs unprivileged
		return nil, err
	}
	report.Step("configure", api.StepDone)

	report.Step("start", api.StepActive)
	c, err := d.inspect(ctx, name)
	if err != nil || c == nil {
		return nil, fmt.Errorf("the load balancer's container disappeared: %v", err)
	}
	if c.State == nil || !c.State.Running {
		if err := d.start(ctx, name); err != nil {
			return nil, err
		}
		if err := d.waitStable(ctx, name, settleFor); err != nil {
			return nil, engine.Permanent(fmt.Errorf("the load balancer stopped right after starting: %w. Last output: %s", err, d.tail(context.WithoutCancel(ctx), name, 5)))
		}
	} else {
		// A running load balancer checks the new configuration, then reloads without dropping connections.
		res, err := d.exec(ctx, name, []string{"haproxy", "-c", "-f", lb.ConfigPath}, nil)
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, engine.Permanent(fmt.Errorf("HAProxy rejected the configuration: %s", res.Output))
		}
		kctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err = d.cli.ContainerKill(kctx, name, "USR2")
		cancel()
		if err != nil {
			return nil, d.classify(fmt.Errorf("reload the load balancer: %w", err))
		}
	}
	if err := d.applyToNew(ctx, in, name); err != nil {
		return nil, err
	}
	if err := started(ctx, in); err != nil {
		return nil, err
	}
	report.Step("start", api.StepDone)

	report.Step("healthy", api.StepActive)
	c, err = d.inspect(ctx, name)
	if err != nil || c == nil {
		return nil, fmt.Errorf("the load balancer's container disappeared: %v", err)
	}
	out := engine.Outputs{"internalHost": name, "internalPort": spec.Port}
	if spec.Publish {
		h := d.opts.PublishAddress
		if h == "0.0.0.0" {
			h = "127.0.0.1"
		}
		out["host"], out["port"] = h, hostPort(c, spec.Port)
	}
	addPrivate(out, r, in.Placement, c)
	if st, err := d.LoadBalancerStatus(ctx, r); err == nil {
		out[engine.OutputTargets], out[engine.OutputHealthyTargets] = st.Targets, st.Healthy
	}
	report.Step("healthy", api.StepDone)
	return out, nil
}

// lbMatches reports whether an existing container has the image, network, port and publishing wanted;
// anything else is a recreation (the host port is kept).
func (d *Driver) lbMatches(c *container.InspectResponse, r *domain.Resource, spec api.LoadBalancerSpec, p engine.Placement) bool {
	if c.Config == nil || c.Config.Image != lb.Image || c.Config.Labels[LabelResourceID] != r.ID || c.HostConfig == nil || c.NetworkSettings == nil {
		return false
	}
	if _, ok := c.NetworkSettings.Networks[p.NetworkName()]; !ok {
		return false
	}
	port := nat.Port(strconv.Itoa(spec.Port) + "/tcp")
	if _, ok := c.Config.ExposedPorts[port]; !ok {
		return false
	}
	_, published := c.HostConfig.PortBindings[port]
	return published == spec.Publish
}

func (d *Driver) createLoadBalancer(ctx context.Context, r *domain.Resource, spec api.LoadBalancerSpec, p engine.Placement, hostPortWanted int) error {
	name := engine.ContainerName(r)
	port := nat.Port(strconv.Itoa(spec.Port) + "/tcp")
	cfg := &container.Config{Image: lb.Image, ExposedPorts: nat.PortSet{port: {}},
		Labels: map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelKind: string(r.Kind), LabelGeneration: strconv.FormatInt(r.Generation, 10)}}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{Memory: int64(domain.LoadBalancerMemoryMiB) << 20},
		PortBindings:  nat.PortMap{},
		// HAProxy runs unprivileged; this lets it bind ports below 1024 inside its own namespace.
		Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
	}
	if spec.Publish {
		hp, err := d.hostPortFor(hostPortWanted)
		if err != nil {
			return err
		}
		host.PortBindings[port] = []nat.PortBinding{{HostIP: d.opts.PublishAddress, HostPort: hp}}
	}
	netCfg, err := d.endpoint(ctx, p, []string{name, engine.PrivateDNS(r)})
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, name); err != nil {
		return d.classify(fmt.Errorf("create the load balancer: %w", err))
	}
	// Published from a network without internet: publish through the default network (ADR-022).
	if spec.Publish && p.Network != "" {
		n, err := d.cli.NetworkInspect(ctx, p.Network, network.InspectOptions{})
		if err == nil && n.Internal {
			if err := d.ensureNetwork(ctx); err != nil {
				return err
			}
			if err := d.connectIfMissing(ctx, engine.NetworkName, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// LoadBalancerStatus implements engine.Driver: HAProxy's statistics, read inside its own container.
func (d *Driver) LoadBalancerStatus(ctx context.Context, r *domain.Resource) (lb.Status, error) {
	res, err := d.exec(ctx, engine.ContainerName(r), []string{"wget", "-q", "-T", "3", "-O", "-", "http://127.0.0.1:" + strconv.Itoa(lb.StatsPort) + "/stats;csv"}, nil)
	if err != nil {
		return lb.Status{}, err
	}
	if res.ExitCode != 0 {
		return lb.Status{}, fmt.Errorf("read the load balancer's statistics: %s", res.Output)
	}
	return lb.ParseStats(strings.NewReader(res.Output))
}
