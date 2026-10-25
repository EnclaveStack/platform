package docker

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/vmlink"
	"github.com/enclavestack/platform/pkg/api"
)

// Firewalls (ADR-021): a one-off helper joins a container's network namespace with NET_ADMIN only and
// replaces EnclaveStack's nftables table in one transaction. The rules live in the namespace, so they keep
// working without the helper or the control plane.

const firewallApplyTimeout = 30 * time.Second

// appliedRules remembers what was loaded into each container: its ID, start time and ruleset hash. A
// restarted container has a fresh namespace without rules, so its start time is part of the key.
type appliedRules struct {
	startedAt string
	hash      string
}

// ApplyFirewall implements engine.Driver.
func (d *Driver) ApplyFirewall(ctx context.Context, r *domain.Resource, p *firewall.Policy) (int, error) {
	names, err := d.runningContainersOf(ctx, r)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, name := range names {
		did, err := d.applyTo(ctx, name, p)
		if err != nil {
			return changed, err
		}
		if did {
			changed++
		}
	}
	return changed, nil
}

// applyTo loads p into one container unless it already holds exactly that.
func (d *Driver) applyTo(ctx context.Context, name string, p *firewall.Policy) (bool, error) {
	c, err := d.inspect(ctx, name)
	if err != nil || c == nil || c.State == nil || !c.State.Running {
		return false, err
	}
	ruleset := firewall.Clear()
	if p != nil {
		fc, err := d.firewallContext(ctx, c)
		if err != nil {
			return false, err
		}
		ruleset = firewall.Render(*p, fc)
	}
	want := appliedRules{startedAt: c.State.StartedAt, hash: firewall.Hash(ruleset)}
	d.fwMu.Lock()
	have, known := d.applied[c.ID]
	d.fwMu.Unlock()
	if known && have == want {
		return false, nil
	}
	if err := d.runNetfilter(ctx, c.ID, name, ruleset); err != nil {
		return false, err
	}
	d.fwMu.Lock()
	d.applied[c.ID] = want
	d.fwMu.Unlock()
	return true, nil
}

// firewallContext adds what the driver knows about a container: the edge and probe addresses in its
// network, its gateway, and its published container ports (FW-05, FW-06).
func (d *Driver) firewallContext(ctx context.Context, c *container.InspectResponse) (firewall.Context, error) {
	var fc firewall.Context
	if c.NetworkSettings == nil {
		return fc, nil
	}
	// Health checks run from the probe helper, so it must exist before its address goes into the rules
	// (it is otherwise created lazily, by the first check).
	if err := d.ensureProbe(ctx); err != nil {
		return fc, err
	}
	if c.Config != nil && c.Config.Labels[LabelKind] == string(api.KindVM) {
		fc.Forward = vmlink.Device // a VM's link: filter what is forwarded to the VM (ADR-026)
	}
	nets := map[string]bool{}
	for net, ep := range c.NetworkSettings.Networks {
		nets[net] = true
		if g, err := netip.ParseAddr(ep.Gateway); err == nil && !fc.Gateway.IsValid() {
			fc.Gateway = g
		}
	}
	for _, sys := range []string{engine.EdgeName, engine.ProbeName} {
		s, err := d.inspect(ctx, sys)
		if err != nil {
			return fc, err
		}
		if s == nil || s.NetworkSettings == nil {
			continue
		}
		for net, ep := range s.NetworkSettings.Networks {
			if a, err := netip.ParseAddr(ep.IPAddress); err == nil && nets[net] {
				fc.AlwaysFrom = append(fc.AlwaysFrom, a)
			}
		}
	}
	if c.HostConfig != nil {
		for port, bindings := range c.HostConfig.PortBindings {
			if len(bindings) > 0 && port.Proto() == "tcp" {
				fc.PublishedPorts = append(fc.PublishedPorts, port.Int())
			}
		}
	}
	return fc, nil
}

// runNetfilter runs the helper in a container's network namespace and waits for it.
func (d *Driver) runNetfilter(ctx context.Context, id, target, ruleset string) error {
	if err := d.ensureNetfilterImage(ctx); err != nil {
		return err
	}
	actx, cancel := context.WithTimeout(ctx, firewallApplyTimeout)
	defer cancel()
	name := "es-netfilter-" + strings.TrimPrefix(domain.NewID("x"), "x_")
	cfg := &container.Config{Image: firewall.ImageTag(), Cmd: firewall.ApplyScript, Env: []string{"ES_RULES=" + ruleset},
		Labels: map[string]string{LabelManaged: "true", LabelKind: "system"}}
	// NET_ADMIN only, in the target's namespace; no mounts, no host namespaces (L3-006 §6).
	host := &container.HostConfig{NetworkMode: container.NetworkMode("container:" + id), CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN"},
		Resources: container.Resources{Memory: 32 << 20}}
	if _, err := d.cli.ContainerCreate(actx, cfg, host, nil, nil, name); err != nil {
		return d.classify(fmt.Errorf("create the firewall helper for %s: %w", target, err))
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stepTimeout)
		defer cancel()
		_ = d.cli.ContainerRemove(rctx, name, container.RemoveOptions{Force: true})
	}()
	waitC, errC := d.cli.ContainerWait(actx, name, container.WaitConditionNextExit)
	if err := d.cli.ContainerStart(actx, name, container.StartOptions{}); err != nil {
		return d.classify(fmt.Errorf("start the firewall helper for %s: %w", target, err))
	}
	select {
	case res := <-waitC:
		if res.StatusCode != 0 {
			out := d.tail(context.WithoutCancel(ctx), name, 5)
			if strings.Contains(out, "Error:") || strings.Contains(out, "syntax error") {
				return engine.Permanent(fmt.Errorf("nftables rejected the rules for %s: %s", target, out))
			}
			return fmt.Errorf("the firewall helper for %s exited with %d: %s", target, res.StatusCode, out)
		}
	case err := <-errC:
		return d.classify(fmt.Errorf("wait for the firewall helper for %s: %w", target, err))
	case <-actx.Done():
		return fmt.Errorf("applying the firewall to %s timed out", target)
	}
	return nil
}

// ensureNetfilterImage builds the helper image on first use (ADR-021): the pinned Alpine base plus
// nftables, through the same BuildKit path as source apps.
func (d *Driver) ensureNetfilterImage(ctx context.Context) error {
	d.imgMu.Lock()
	defer d.imgMu.Unlock()
	if d.netfilterReady {
		return nil
	}
	tag := firewall.ImageTag()
	if ok, err := d.ImageExists(ctx, tag); err != nil {
		return err
	} else if ok {
		d.netfilterReady = true
		return nil
	}
	dir, err := os.MkdirTemp("", "es-netfilter-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(firewall.Dockerfile), 0o600); err != nil {
		return err
	}
	sys := &domain.Resource{ID: "system", Kind: "system", Name: "netfilter"}
	if _, err := d.Build(ctx, engine.BuildInput{Resource: sys, Tag: tag, ContextDir: dir, Dockerfile: "Dockerfile"}, io.Discard); err != nil {
		return fmt.Errorf("build the firewall helper image: %w", err)
	}
	d.netfilterReady = true
	return nil
}

// runningContainersOf lists a resource's running containers: every container of an app (all its
// revisions and replicas), or the resource's own container.
func (d *Driver) runningContainersOf(ctx context.Context, r *domain.Resource) ([]string, error) {
	if r.Kind != api.KindApp {
		c, err := d.inspect(ctx, engine.ContainerName(r))
		if err != nil || c == nil || c.State == nil || !c.State.Running {
			return nil, err
		}
		return []string{engine.ContainerName(r)}, nil
	}
	revs, err := d.revisions(ctx, r)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rev := range revs {
		if rev.running {
			out = append(out, rev.name)
		}
	}
	return out, nil
}

// Endpoints implements engine.Driver.
func (d *Driver) Endpoints(ctx context.Context, r *domain.Resource) ([]netip.Addr, error) {
	names, err := d.runningContainersOf(ctx, r)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, n := range names {
		c, err := d.inspect(ctx, n)
		if err != nil {
			return nil, err
		}
		if c == nil || c.NetworkSettings == nil {
			continue
		}
		for _, ep := range c.NetworkSettings.Networks {
			if a, err := netip.ParseAddr(ep.IPAddress); err == nil {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

// applyToNew loads a resource's own policy into containers it just started (before health checks).
func (d *Driver) applyToNew(ctx context.Context, in engine.EnsureInput, names ...string) error {
	if in.Firewall == nil {
		return nil
	}
	for _, n := range names {
		if _, err := d.applyTo(ctx, n, in.Firewall); err != nil {
			return err
		}
	}
	return nil
}

// started runs the reconciler's hook for new containers, if any.
func started(ctx context.Context, in engine.EnsureInput) error {
	if in.Started == nil {
		return nil
	}
	return in.Started(ctx)
}
