package reconciler_test

import (
	"context"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Replicas and load balancers (ADR-022, L2-006 LB-01 – LB-08) through the real API on the fake engine.

func TestLoadBalancersFollowTheirTargets(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	n, _ := c.CreateResource(ctx, api.KindNetwork, "backend", api.NetworkSpec{CIDR: "10.40.0.0/16"})
	waitReadyWithin(t, c, n.ID, 10*time.Second)
	apiApp, _ := c.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx:1.27-alpine", Port: 8080, Replicas: 3, Network: "backend"})
	canary, _ := c.CreateResource(ctx, api.KindApp, "canary", api.AppSpec{Image: "nginx:1.27-alpine", Port: 8080, Network: "backend"})
	web, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	for _, id := range []string{apiApp.ID, canary.ID, web.ID} {
		waitReadyWithin(t, c, id, 10*time.Second)
	}
	if got, _ := c.GetResource(ctx, apiApp.ID); got.Outputs[engine.OutputRunningReplicas] != float64(3) {
		t.Errorf("replicas not reported: %v", got.Outputs)
	}
	if _, err := c.CreateResource(ctx, api.KindApp, "published", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Publish: true, Replicas: 2}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("published app with replicas: %v", err)
	}

	// A firewall on api: the load balancer is allowed in without a rule naming it (FW-05).
	fw, _ := c.CreateResource(ctx, api.KindFirewall, "api-only-lb", api.FirewallSpec{Targets: []string{"app/api"}})
	waitReadyWithin(t, c, fw.ID, 10*time.Second)

	lbr, err := c.CreateResource(ctx, api.KindLoadBalancer, "api-lb", api.LoadBalancerSpec{Port: 80, Publish: true,
		Targets: []api.LoadBalancerTarget{{App: "api", Weight: 3}, {App: "canary"}}, Algorithm: api.AlgorithmLeastConn})
	if err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, lbr.ID, 10*time.Second)
	if ready.Outputs[engine.OutputTargets] != float64(4) || ready.Outputs[engine.OutputHealthyTargets] != float64(4) ||
		ready.Outputs["internalHost"] != "es-loadbalancer-api-lb" || ready.Outputs[engine.OutputPrivateDNS] != "api-lb.loadbalancer.internal" || ready.Outputs["port"] == nil {
		t.Errorf("load balancer outputs %v", ready.Outputs)
	}
	cfg := env.Fake.LoadBalancerConfig(lbr.ID)
	if cfg == nil || !cfg.HTTP || cfg.Algorithm != api.AlgorithmLeastConn || len(cfg.Targets) != 2 ||
		cfg.Targets[0] != (lb.Target{Host: "es-app-api", Port: 8080, Weight: 3}) || cfg.Targets[1] != (lb.Target{Host: "es-app-canary", Port: 8080, Weight: 1}) {
		t.Fatalf("config %+v", cfg)
	}
	if p := env.Fake.PlacementOf(lbr.ID); p.Network != "es-net-backend" {
		t.Errorf("load balancer placed in %+v, want its targets' network", p)
	}
	lbIP := ready.Outputs[engine.OutputPrivateIP].(string)
	allowsLB := func() bool {
		p, _ := env.Fake.FirewallOf(apiApp.ID)
		if p == nil {
			return false
		}
		for _, r := range p.Ingress {
			if peerSet(r)[lbIP+"/32"] && len(r.Ports) == 1 && r.Ports[0].From == 8080 {
				return true
			}
		}
		return false
	}
	if !allowsLB() {
		p, _ := env.Fake.FirewallOf(apiApp.ID)
		t.Errorf("api's firewall doesn't let the load balancer in: %+v", p)
	}

	// Rules: one network, existing apps, the stats port reserved, targets can't be deleted.
	for name, spec := range map[string]api.LoadBalancerSpec{
		"two-networks": {Port: 80, Targets: []api.LoadBalancerTarget{{App: "api"}, {App: "web"}}},
		"missing":      {Port: 80, Targets: []api.LoadBalancerTarget{{App: "nope"}}},
		"stats-port":   {Port: api.LBStatsPort, Targets: []api.LoadBalancerTarget{{App: "api"}}},
	} {
		if _, err := c.CreateResource(ctx, api.KindLoadBalancer, name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c.DeleteResource(ctx, canary.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a target: %v", err)
	}
	if _, err := c.UpdateResource(ctx, canary.ID, map[string]any{"network": nil}, nil); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("moving one target away from the others: %v", err)
	}

	// A target's port change reconfigures the load balancer; health changes are reported (LB-05, LB-06).
	upd, _ := c.UpdateResource(ctx, canary.ID, map[string]any{"port": 9090}, nil)
	waitReadyWithin(t, c, upd.ID, 10*time.Second)
	waitFor(t, 10*time.Second, func() bool {
		cfg := env.Fake.LoadBalancerConfig(lbr.ID)
		return cfg != nil && cfg.Targets[1].Port == 9090
	})
	// Scaling a target updates the healthy count with the deploy, not at the next drift pass.
	scaled, _ := c.UpdateResource(ctx, apiApp.ID, map[string]any{"replicas": 4}, nil)
	waitReadyWithin(t, c, scaled.ID, 10*time.Second)
	waitFor(t, 5*time.Second, func() bool {
		r, _ := c.GetResource(ctx, lbr.ID)
		return r.Outputs[engine.OutputHealthyTargets] == float64(5)
	})
	env.Fake.SetUnhealthyTargets("api-lb", 2)
	waitFor(t, 10*time.Second, func() bool {
		r, _ := c.GetResource(ctx, lbr.ID)
		return r.Outputs[engine.OutputHealthyTargets] == float64(3)
	})

	// Deleting the load balancer removes its address from api's firewall.
	if _, err := c.DeleteResource(ctx, lbr.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, lbr.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if allowsLB() {
		t.Error("a deleted load balancer is still allowed in")
	}
}
