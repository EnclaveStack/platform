package reconciler_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Firewalls (ADR-021, L2-006 FW-01 – FW-09) through the real API on the fake engine, which records each
// target's resolved policy.

func peerSet(r firewall.Rule) map[string]bool {
	out := map[string]bool{}
	for _, p := range r.Peers {
		out[p.String()] = true
	}
	return out
}

func TestFirewallsResolveSourcesAndFollowRedeploys(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	db, _ := c.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{})
	apiApp, _ := c.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	web, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	for _, id := range []string{db.ID, apiApp.ID, web.ID} {
		waitReadyWithin(t, c, id, 10*time.Second)
	}
	apiReady, _ := c.GetResource(ctx, apiApp.ID)
	apiIP := apiReady.Outputs[engine.OutputPrivateIP].(string)

	fw, err := c.CreateResource(ctx, api.KindFirewall, "db-access", api.FirewallSpec{Targets: []string{"database/orders"},
		Ingress: []api.FirewallRule{{From: []string{"app/api", "10.9.0.0/16"}, Ports: []string{"5432"}}}})
	if err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, fw.ID, 10*time.Second)
	if ready.Outputs["appliedContainers"] != float64(1) || ready.Outputs["appliedAt"] == nil {
		t.Errorf("firewall outputs %v", ready.Outputs)
	}
	p, _ := env.Fake.FirewallOf(db.ID)
	if p == nil || len(p.Ingress) != 1 || p.RestrictEgress {
		t.Fatalf("database policy %+v", p)
	}
	if got := peerSet(p.Ingress[0]); !got[apiIP+"/32"] || !got["10.9.0.0/16"] || len(got) != 2 || p.Ingress[0].Ports[0] != (firewall.PortRange{From: 5432, To: 5432}) {
		t.Errorf("ingress %+v (api at %s)", p.Ingress[0], apiIP)
	}
	if wp, _ := env.Fake.FirewallOf(web.ID); wp != nil {
		t.Errorf("an app no firewall names got rules: %+v", wp)
	}

	// A redeploy of the source moves its address; the target's rules follow (FW-07).
	if _, err := c.Action(ctx, apiApp.ID, api.ActionRedeploy); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, _ := c.GetResource(ctx, apiApp.ID)
		ip, _ := r.Outputs[engine.OutputPrivateIP].(string)
		return r.Status.Phase == api.PhaseReady && ip != apiIP
	})
	moved, _ := c.GetResource(ctx, apiApp.ID)
	newIP := moved.Outputs[engine.OutputPrivateIP].(string)
	p, _ = env.Fake.FirewallOf(db.ID)
	if got := peerSet(p.Ingress[0]); !got[newIP+"/32"] || got[apiIP+"/32"] {
		t.Errorf("rules didn't follow the redeploy: %v (old %s, new %s)", got, apiIP, newIP)
	}

	// Lost rules (a restarted container) come back on the next drift pass (FW-09). The redeploy reports
	// ready just before it re-syncs firewalls, and that sync may restore the first restart's rules without
	// an event; once they're back it's done, so the second restart is the drift pass's alone.
	env.Fake.RestartContainer(db.ID)
	waitFor(t, 10*time.Second, func() bool { p, _ := env.Fake.FirewallOf(db.ID); return p != nil })
	env.Fake.RestartContainer(db.ID)
	waitFor(t, 10*time.Second, func() bool {
		p, _ := env.Fake.FirewallOf(db.ID)
		return p != nil && eventTypes(t, c, fw.ID)["firewall_reapplied"] > 0
	})

	// Dependencies (FW-08) and validation.
	if _, err := c.DeleteResource(ctx, apiApp.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a source a firewall names: %v", err)
	}
	if _, err := c.DeleteResource(ctx, db.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a target: %v", err)
	}
	for name, spec := range map[string]api.FirewallSpec{
		"missing-target": {Targets: []string{"app/nope"}},
		"missing-source": {Targets: []string{"app/web"}, Ingress: []api.FirewallRule{{From: []string{"network/nope"}}}},
	} {
		if _, err := c.CreateResource(ctx, api.KindFirewall, name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Changing the targets clears the one it no longer names; egress deny restricts the new one.
	upd, err := c.UpdateResource(ctx, fw.ID, map[string]any{"targets": []string{"app/web"}, "egressDefault": "deny",
		"egress": []api.FirewallRule{{To: []string{"any"}, Ports: []string{"443"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, upd.ID, 10*time.Second)
	if p, _ := env.Fake.FirewallOf(db.ID); p != nil {
		t.Errorf("a target no longer named keeps rules: %+v", p)
	}
	wp, _ := env.Fake.FirewallOf(web.ID)
	if wp == nil || !wp.RestrictEgress || len(wp.Egress) != 1 || !peerSet(wp.Egress[0])["0.0.0.0/0"] {
		t.Errorf("web policy %+v", wp)
	}

	// Applications are counted (L3-007 §9).
	if m := httpGet(t, env.URL+"/metrics"); !strings.Contains(m, `enclave_firewall_apply_total{result="ok"}`) || !strings.Contains(m, "enclave_firewall_apply_seconds_count") {
		t.Error("firewall metrics not exported")
	}

	// Deleting the firewall clears its targets.
	if _, err := c.DeleteResource(ctx, fw.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, fw.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if p, _ := env.Fake.FirewallOf(web.ID); p != nil {
		t.Errorf("rules left after the firewall was deleted: %+v", p)
	}
}

func TestSeveralFirewallsAddUpAndNetworksResolveToTheirRange(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	n, _ := c.CreateResource(ctx, api.KindNetwork, "backend", api.NetworkSpec{CIDR: "10.40.0.0/16"})
	waitReadyWithin(t, c, n.ID, 10*time.Second)
	cache, _ := c.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{Network: "backend"})
	waitReadyWithin(t, c, cache.ID, 10*time.Second)
	for name, rule := range map[string]api.FirewallRule{
		"from-backend": {From: []string{"network/backend"}, Ports: []string{"6379"}},
		"ping":         {From: []string{"any"}, Protocol: api.ProtocolICMP},
	} {
		fw, err := c.CreateResource(ctx, api.KindFirewall, name, api.FirewallSpec{Targets: []string{"cache/sessions"}, Ingress: []api.FirewallRule{rule}})
		if err != nil {
			t.Fatal(err)
		}
		waitReadyWithin(t, c, fw.ID, 10*time.Second)
	}
	p, _ := env.Fake.FirewallOf(cache.ID)
	if p == nil || len(p.Ingress) != 2 {
		t.Fatalf("policy %+v", p)
	}
	seen := map[string]bool{}
	for _, r := range p.Ingress {
		for k := range peerSet(r) {
			seen[r.Protocol+" "+k] = true
		}
	}
	if !seen["tcp "+netip.MustParsePrefix("10.40.0.0/16").String()] || !seen["icmp 0.0.0.0/0"] {
		t.Errorf("union of firewalls %v", seen)
	}
	if _, err := c.DeleteResource(ctx, n.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a network a firewall names: %v", err)
	}
}
