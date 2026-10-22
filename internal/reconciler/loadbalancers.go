package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Load balancers (ADR-022, L2-006 LB-01 – LB-08).

// OutputBackends is bookkeeping: the targets and ports a load balancer was configured with
// ("api:8080,web:80"), so an app's port change reconfigures it.
const OutputBackends = "backends"

// loadBalancerConfig resolves a load balancer's targets to their stable aliases and ports, and places it
// in their network.
func (r *Reconciler) loadBalancerConfig(ctx context.Context, project string, spec api.LoadBalancerSpec) (*lb.Config, engine.Placement, string, error) {
	cfg := &lb.Config{HTTP: spec.Protocol == api.LBProtocolHTTP, Port: spec.Port, Algorithm: spec.Algorithm}
	if h := spec.HealthCheck; h != nil {
		cfg.Check = lb.Check{HTTP: h.Type == api.HealthHTTP, Path: h.Path, IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
			HealthyThreshold: h.HealthyThreshold, UnhealthyThreshold: h.UnhealthyThreshold}
	}
	var placement engine.Placement
	var backends []string
	for i, t := range spec.Targets {
		app, err := r.Store.GetResourceByName(ctx, project, api.KindApp, t.App)
		if errors.Is(err, store.ErrNotFound) {
			return nil, placement, "", engine.Permanent(fmt.Errorf("the app %s no longer exists", t.App))
		}
		if err != nil {
			return nil, placement, "", err
		}
		raw, _, err := api.NormalizeSpec(api.KindApp, app.Spec)
		if err != nil {
			return nil, placement, "", engine.Permanent(fmt.Errorf("the app %s has an unreadable spec", t.App))
		}
		as := raw.(api.AppSpec)
		port := t.Port
		if port == 0 {
			port = as.Port
		}
		if port == 0 {
			return nil, placement, "", engine.Permanent(fmt.Errorf("the app %s has no port; set one on the app or on the target", t.App))
		}
		if i == 0 {
			if placement, err = r.placement(ctx, project, as.Network, ""); err != nil {
				return nil, placement, "", err
			}
		}
		cfg.Targets = append(cfg.Targets, lb.Target{Host: engine.ContainerName(app), Port: port, Weight: t.Weight})
		backends = append(backends, t.App+":"+strconv.Itoa(port))
	}
	return cfg, placement, strings.Join(backends, ","), nil
}

// syncLoadBalancersForApp reconfigures load balancers whose backends no longer match an app (its port
// changed) through a normal reconcile, and refreshes the healthy-target count of the others.
func (r *Reconciler) syncLoadBalancersForApp(ctx context.Context, app *domain.Resource, log *slog.Logger) {
	deps, err := r.Store.Dependants(ctx, app.ID)
	if err != nil {
		log.Warn("list load balancers of app failed", "error", err)
		return
	}
	for _, d := range deps {
		if d.Kind != api.KindLoadBalancer || d.Phase != api.PhaseReady {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindLoadBalancer, d.Spec)
		if err != nil {
			continue
		}
		_, _, backends, err := r.loadBalancerConfig(ctx, d.Project, raw.(api.LoadBalancerSpec))
		if err != nil {
			continue
		}
		if d.Outputs[OutputBackends] == backends {
			// Same backends: HAProxy follows the new replicas through DNS on its own. Report them once it
			// has settled, rather than at the next drift pass.
			r.settleLoadBalancer(ctx, d, raw.(api.LoadBalancerSpec))
			continue
		}
		if err := d.SetPhase(api.PhaseUpdating, "Updating: the app "+app.Name+" changed.", r.Now()); err != nil {
			continue
		}
		d.Steps, d.UpdatedAt = domain.NewSteps(domain.StepNames[d.Kind]...), r.Now()
		if ok, err := r.Store.SaveStatus(ctx, d, d.Generation); err == nil && ok {
			r.event(ctx, d, "targets_changed", "The app "+app.Name+" changed; reconfiguring.")
			r.Enqueue(d.ID)
		}
	}
}

// lbSettleWait bounds how long a deploy waits for a load balancer to see its new replica set (DNS hold
// plus one check interval are a few seconds).
const lbSettleWait = 10 * time.Second

// settleLoadBalancer waits until the load balancer finds as many replicas as its targets run, then
// records its counts; after lbSettleWait it records whatever it sees.
func (r *Reconciler) settleLoadBalancer(ctx context.Context, d *domain.Resource, spec api.LoadBalancerSpec) {
	want := 0
	for _, t := range spec.Targets {
		if app, err := r.Store.GetResourceByName(ctx, d.Project, api.KindApp, t.App); err == nil {
			if raw, _, err := api.NormalizeSpec(api.KindApp, app.Spec); err == nil {
				want += max(raw.(api.AppSpec).Replicas, 1)
			}
		}
	}
	deadline := r.Now().Add(lbSettleWait)
	for {
		st, err := r.Driver.LoadBalancerStatus(ctx, d)
		if err != nil || (st.Targets == want && st.Healthy == want) || !r.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	r.refreshLoadBalancer(ctx, d)
}

// refreshLoadBalancer keeps a ready load balancer's healthy-target count current (each drift pass).
func (r *Reconciler) refreshLoadBalancer(ctx context.Context, res *domain.Resource) {
	st, err := r.Driver.LoadBalancerStatus(ctx, res)
	if err != nil {
		return
	}
	r.Metrics.LBHealthyTargets.WithLabelValues(res.Name).Set(float64(st.Healthy))
	if t, _ := asInt64(res.Outputs[engine.OutputTargets]); t == int64(st.Targets) {
		if h, _ := asInt64(res.Outputs[engine.OutputHealthyTargets]); h == int64(st.Healthy) {
			return
		}
	}
	res.Outputs = withOutput(withOutput(res.Outputs, engine.OutputTargets, st.Targets), engine.OutputHealthyTargets, st.Healthy)
	res.UpdatedAt = r.Now()
	if _, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil {
		r.event(ctx, res, "targets_health", fmt.Sprintf("%d of %d replicas are healthy.", st.Healthy, st.Targets))
	}
}

// afterLoadBalancerDeleted re-applies its target apps' firewalls without its address.
func (r *Reconciler) afterLoadBalancerDeleted(ctx context.Context, res *domain.Resource) error {
	raw, _, err := api.NormalizeSpec(api.KindLoadBalancer, res.Spec)
	if err != nil {
		return nil
	}
	var targets []string
	for _, t := range raw.(api.LoadBalancerSpec).Targets {
		targets = append(targets, "app/"+t.App)
	}
	_, _, err = r.syncTargets(ctx, res.ProjectName(), targets)
	return err
}
