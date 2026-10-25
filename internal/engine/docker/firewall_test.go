//go:build docker

package docker

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/pkg/api"
)

// Firewalls on real Docker (ADR-021, L5-002 §7): rules enforced in the target's namespace, the probe
// always allowed, rules restored after a restart, egress deny, clearing.

func TestFirewallEnforcesRulesInTheTargetsNamespace(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	cidr, first, second := randomCIDR()
	n, _ := newNetwork(t, d, api.NetworkSpec{CIDR: cidr, Subnets: []api.Subnet{{Name: "auto", CIDR: first}, {Name: "trusted", CIDR: second}}})
	net := engine.DockerNetwork("", n.Name)
	trusted := engine.Placement{Network: net, Subnet: second}
	other := engine.Placement{Network: net}

	// The target serves TCP 80 with busybox httpd, so its shell can also test egress.
	target := resource(api.KindApp, "it-fw-"+suffix())
	cleanup(t, d, target, engine.ObjectStore{})
	policy := &firewall.Policy{Ingress: []firewall.Rule{{Peers: []netip.Prefix{netip.MustParsePrefix(second)}, Protocol: firewall.TCP, Ports: []firewall.PortRange{{From: 80, To: 80}}}}}
	spec := api.AppSpec{Image: "busybox:1.37", Command: []string{"httpd", "-f", "-p", "80"}, Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 1}
	hooked := 0
	out := ensure(t, d, engine.EnsureInput{Resource: target, Spec: spec, Revision: 1, Placement: other, Firewall: policy,
		Started: func(context.Context) error { hooked++; return nil }})
	if hooked != 1 {
		t.Errorf("start hook ran %d times", hooked)
	}
	ip := fmt.Sprint(out[engine.OutputPrivateIP])
	// The deploy's tcp health check passed through the probe helper: always allowed (FW-05).

	connect := func(p engine.Placement) int {
		code, _ := runIn(t, d, p, "nc", "-z", "-w", "2", ip, "80")
		return code
	}
	if connect(trusted) != 0 {
		t.Error("an allowed source can't connect")
	}
	if connect(other) == 0 {
		t.Error("a source no rule allows connected")
	}

	// The start-to-rules window, measured on this engine.
	name := engine.RevisionName(target, 1)
	c, _ := d.inspect(ctx, name)
	startedAt, _ := time.Parse(time.RFC3339Nano, c.State.StartedAt)
	t.Logf("rules loaded within the deploy; container started %s", startedAt.Format(time.RFC3339Nano))

	// Applying the same policy again changes nothing; a restart loses the rules and they are re-applied.
	if changed, err := d.ApplyFirewall(ctx, target, policy); err != nil || changed != 0 {
		t.Errorf("re-apply unchanged: %d %v", changed, err)
	}
	timeout := 2
	if err := d.cli.ContainerRestart(ctx, name, container.StopOptions{Timeout: &timeout}); err != nil {
		t.Fatal(err)
	}
	if connect(other) != 0 {
		t.Error("a restarted container kept its rules (the namespace should be fresh)")
	}
	t0 := time.Now()
	if changed, err := d.ApplyFirewall(ctx, target, policy); err != nil || changed != 1 {
		t.Fatalf("re-apply after restart: %d %v", changed, err)
	}
	t.Logf("re-applying rules to one container took %s", time.Since(t0).Round(time.Millisecond))
	if connect(other) == 0 {
		t.Error("rules not restored after the restart")
	}

	// Egress deny: the target can't open connections except what a rule allows.
	egress := &firewall.Policy{Ingress: policy.Ingress, RestrictEgress: true,
		Egress: []firewall.Rule{{Peers: []netip.Prefix{netip.MustParsePrefix(second)}, Protocol: firewall.TCP, Ports: []firewall.PortRange{{From: 81, To: 81}}}}}
	if _, err := d.ApplyFirewall(ctx, target, egress); err != nil {
		t.Fatal(err)
	}
	res, err := d.exec(ctx, name, []string{"nc", "-z", "-w", "2", cidrGateway(cidr), "53"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Error("egress deny let an outgoing connection through")
	}
	if connect(trusted) != 0 {
		t.Error("replies to allowed connections were dropped under egress deny")
	}

	// Clearing removes EnclaveStack's table.
	if changed, err := d.ApplyFirewall(ctx, target, nil); err != nil || changed != 1 {
		t.Fatalf("clear: %d %v", changed, err)
	}
	if connect(other) != 0 {
		t.Error("rules left after clearing")
	}
}

func cidrGateway(cidr string) string {
	return netip.MustParsePrefix(cidr).Addr().Next().String()
}

func TestFirewallHelperRejectsNothingButValidRules(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	target := resource(api.KindCache, "it-fw-"+suffix())
	cleanup(t, d, target, engine.ObjectStore{})
	ensure(t, d, engine.EnsureInput{Resource: target, Spec: api.CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 64}})
	if err := d.runNetfilter(ctx, engine.ContainerName(target), target.Name, "table inet enclave { this is not nft }"); err == nil || !engine.IsPermanent(err) {
		t.Errorf("invalid ruleset: %v", err)
	}
	addrs, err := d.Endpoints(ctx, target)
	if err != nil || len(addrs) != 1 {
		t.Errorf("endpoints %v %v", addrs, err)
	}
}
