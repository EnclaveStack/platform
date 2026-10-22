package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Firewalls (ADR-021, L2-006 FW-01 – FW-09): one policy per target, the union of every firewall that
// names it, with peers resolved to the current addresses of the resources they name.

// OutputAppliedTargets is bookkeeping: the targets a firewall's rules were last applied to, so a target
// it no longer names is cleared.
const OutputAppliedTargets = "appliedTargets"

// firewallKinds have containers a firewall can protect.
var firewallKinds = api.FirewallKinds

// policyFor computes a target's policy, or nil when no live firewall names it.
func (r *Reconciler) policyFor(ctx context.Context, target *domain.Resource) (*firewall.Policy, error) {
	if !slices.Contains(firewallKinds, target.Kind) {
		return nil, nil
	}
	fws, err := r.Store.ProjectResourcesOfKind(ctx, target.ProjectName(), api.KindFirewall) // firewalls protect their own project (ADR-035)
	if err != nil {
		return nil, err
	}
	key := target.Ref()
	var p *firewall.Policy
	for _, fw := range fws {
		if fw.Phase == api.PhaseDeleting {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindFirewall, fw.Spec)
		if err != nil {
			continue
		}
		spec := raw.(api.FirewallSpec)
		if !slices.Contains(spec.Targets, key) {
			continue
		}
		if p == nil {
			p = &firewall.Policy{}
		}
		for _, rule := range spec.Ingress {
			fr, err := r.resolveRule(ctx, target.ProjectName(), rule, rule.From)
			if err != nil {
				return nil, err
			}
			p.Ingress = append(p.Ingress, fr)
		}
		if spec.EgressDefault == api.EgressDeny {
			p.RestrictEgress = true
		}
		for _, rule := range spec.Egress {
			fr, err := r.resolveRule(ctx, target.ProjectName(), rule, rule.To)
			if err != nil {
				return nil, err
			}
			p.Egress = append(p.Egress, fr)
		}
	}
	if p == nil {
		return nil, nil
	}
	// Load balancers and CDNs that send traffic to this app always reach its target ports (FW-05).
	lbs, err := r.loadBalancersOf(ctx, target)
	if err != nil {
		return nil, err
	}
	p.Ingress = append(p.Ingress, lbs...)
	return p, nil
}

// resolveRule turns a rule's peers into prefixes: a CIDR as given, any as 0.0.0.0/0, a network as its
// range, and a resource as the addresses of its running containers.
func (r *Reconciler) resolveRule(ctx context.Context, project string, rule api.FirewallRule, peers []string) (firewall.Rule, error) {
	out := firewall.Rule{Protocol: rule.Protocol}
	for _, ps := range rule.Ports {
		lo, hi, _ := api.ParsePortRange(ps)
		out.Ports = append(out.Ports, firewall.PortRange{From: lo, To: hi})
	}
	for _, s := range peers {
		peer, ok := api.ParsePeer(s)
		switch {
		case !ok:
			continue
		case peer.Any:
			out.Peers = append(out.Peers, netip.MustParsePrefix("0.0.0.0/0"))
		case peer.CIDR.IsValid():
			out.Peers = append(out.Peers, peer.CIDR)
		case peer.Kind == api.KindNetwork:
			n, err := r.Store.GetResourceByName(ctx, project, api.KindNetwork, peer.Name)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			if raw, _, err := api.NormalizeSpec(api.KindNetwork, n.Spec); err == nil {
				if cidr, ok := api.ParseCIDR(raw.(api.NetworkSpec).CIDR); ok {
					out.Peers = append(out.Peers, cidr)
				}
			}
		default:
			res, err := r.Store.GetResourceByName(ctx, project, peer.Kind, peer.Name)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			addrs, err := r.Driver.Endpoints(ctx, res)
			if err != nil {
				return out, err
			}
			for _, a := range addrs {
				out.Peers = append(out.Peers, netip.PrefixFrom(a, 32))
			}
		}
	}
	return out, nil
}

// loadBalancersOf returns allow rules from each load balancer that targets an app, on its target port.
func (r *Reconciler) loadBalancersOf(ctx context.Context, target *domain.Resource) ([]firewall.Rule, error) {
	if target.Kind != api.KindApp {
		return nil, nil
	}
	deps, err := r.Store.Dependants(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	var appPort int
	if raw, _, err := api.NormalizeSpec(api.KindApp, target.Spec); err == nil {
		appPort = raw.(api.AppSpec).Port
	}
	var out []firewall.Rule
	for _, d := range deps {
		if (d.Kind != api.KindLoadBalancer && d.Kind != api.KindCDN) || d.Phase == api.PhaseDeleting {
			continue
		}
		raw, _, err := api.NormalizeSpec(d.Kind, d.Spec)
		if err != nil {
			continue
		}
		var targets []api.LoadBalancerTarget
		if lbs, ok := raw.(api.LoadBalancerSpec); ok {
			targets = lbs.Targets
		} else if c := raw.(api.CDNSpec); c.Origin == string(api.KindApp)+"/"+target.Name {
			// A CDN in front of the app reaches its origin port, as a load balancer does (ADR-033).
			targets = []api.LoadBalancerTarget{{App: target.Name, Port: c.OriginPort}}
		}
		for _, t := range targets {
			if t.App != target.Name {
				continue
			}
			port := t.Port
			if port == 0 {
				port = appPort
			}
			addrs, err := r.Driver.Endpoints(ctx, d)
			if err != nil {
				return nil, err
			}
			rule := firewall.Rule{Protocol: firewall.TCP, Ports: []firewall.PortRange{{From: port, To: port}}}
			for _, a := range addrs {
				rule.Peers = append(rule.Peers, netip.PrefixFrom(a, 32))
			}
			out = append(out, rule)
		}
	}
	return out, nil
}

// syncTargets applies the current policy of each named target ("kind/name") and returns how many
// containers changed. Targets that don't exist (yet) are skipped: they get their policy when they deploy.
func (r *Reconciler) syncTargets(ctx context.Context, project string, targets []string) (int, int, error) {
	changed, containers := 0, 0
	for _, t := range targets {
		kind, name, ok := api.ParseTarget(t)
		if !ok {
			continue
		}
		res, err := r.Store.GetResourceByName(ctx, project, kind, name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return changed, containers, err
		}
		p, err := r.policyFor(ctx, res)
		if err != nil {
			return changed, containers, err
		}
		started := r.Now()
		n, err := r.Driver.ApplyFirewall(ctx, res, p)
		r.Metrics.FirewallApplySeconds.Observe(r.Now().Sub(started).Seconds())
		r.Metrics.FirewallApply.WithLabelValues(result(err)).Inc()
		changed += n
		if err != nil {
			return changed, containers, fmt.Errorf("apply the firewall to %s: %w", t, err)
		}
		if p != nil {
			addrs, err := r.Driver.Endpoints(ctx, res)
			if err != nil {
				return changed, containers, err
			}
			containers += len(addrs)
		}
	}
	return changed, containers, nil
}

// ensureFirewall applies the policy of every target the firewall names now or named last time.
func (r *Reconciler) ensureFirewall(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindFirewall, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	spec := raw.(api.FirewallSpec)
	report.Step("resolve", api.StepActive)
	targets := appliedTargets(res)
	for _, t := range spec.Targets {
		if !slices.Contains(targets, t) {
			targets = append(targets, t)
		}
	}
	report.Step("resolve", api.StepDone)
	report.Step("apply", api.StepActive)
	_, containers, err := r.syncTargets(ctx, res.ProjectName(), targets)
	if err != nil {
		return nil, err
	}
	report.Step("apply", api.StepDone)
	return engine.Outputs{"appliedContainers": containers, "appliedAt": r.Now().UTC().Format(time.RFC3339),
		OutputAppliedTargets: strings.Join(spec.Targets, ",")}, nil
}

func appliedTargets(res *domain.Resource) []string {
	s, _ := res.Outputs[OutputAppliedTargets].(string)
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// deleteFirewall re-applies its targets without it (the resource is already in phase deleting).
func (r *Reconciler) deleteFirewall(ctx context.Context, res *domain.Resource) error {
	targets := appliedTargets(res)
	if raw, _, err := api.NormalizeSpec(api.KindFirewall, res.Spec); err == nil {
		for _, t := range raw.(api.FirewallSpec).Targets {
			if !slices.Contains(targets, t) {
				targets = append(targets, t)
			}
		}
	}
	_, _, err := r.syncTargets(ctx, res.ProjectName(), targets)
	return err
}

// syncFirewallsNaming re-applies the targets of every firewall whose rules name res as a peer, so they
// follow res's addresses (a redeploy). It also re-applies res's own policy to all its containers.
func (r *Reconciler) syncFirewallsNaming(ctx context.Context, res *domain.Resource) error {
	if !slices.Contains(firewallKinds, res.Kind) {
		return nil
	}
	deps, err := r.Store.Dependants(ctx, res.ID)
	if err != nil {
		return err
	}
	var targets []string
	if res.Kind == api.KindLoadBalancer { // its target apps allow its address (FW-05)
		if raw, _, err := api.NormalizeSpec(api.KindLoadBalancer, res.Spec); err == nil {
			for _, t := range raw.(api.LoadBalancerSpec).Targets {
				targets = append(targets, "app/"+t.App)
			}
		}
	}
	for _, d := range deps {
		if d.Kind != api.KindFirewall || d.Phase == api.PhaseDeleting {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindFirewall, d.Spec)
		if err != nil {
			continue
		}
		targets = append(targets, raw.(api.FirewallSpec).Targets...)
	}
	targets = append(targets, res.Ref())
	slices.Sort(targets)
	_, _, err = r.syncTargets(ctx, res.ProjectName(), slices.Compact(targets))
	return err
}

// afterContainersChanged keeps firewalls current once a resource's containers changed (ADR-021): the
// old revision is gone, so its addresses leave the rules that named it.
func (r *Reconciler) afterContainersChanged(ctx context.Context, res *domain.Resource, log *slog.Logger) {
	if err := r.syncFirewallsNaming(ctx, res); err != nil {
		log.Warn("update firewalls after a deploy failed; drift repair retries", "error", err)
	}
}

// refreshFirewall re-applies a ready firewall's targets during drift repair (FW-09): new or restarted
// containers get their rules back.
func (r *Reconciler) refreshFirewall(ctx context.Context, res *domain.Resource) {
	raw, _, err := api.NormalizeSpec(api.KindFirewall, res.Spec)
	if err != nil {
		return
	}
	changed, _, err := r.syncTargets(ctx, res.ProjectName(), raw.(api.FirewallSpec).Targets)
	if err != nil {
		r.Log.Warn("firewall drift repair failed", "resource_id", res.ID, "error", err)
		return
	}
	if changed > 0 {
		r.event(ctx, res, "firewall_reapplied", fmt.Sprintf("Rules loaded again into %d container(s) that were new or had restarted.", changed))
	}
}

// result labels a metric with an operation's outcome.
func result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
