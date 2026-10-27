//go:build docker

package docker

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Networks on real Docker (ADR-020, L5-002 §7): isolation, subnet addresses, private DNS names, system
// services joined, internet off.

func newNetwork(t *testing.T, d *Driver, spec api.NetworkSpec) (*domain.Resource, engine.Outputs) {
	t.Helper()
	r := resource(api.KindNetwork, "it-net-"+suffix())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := d.EnsureNetwork(ctx, engine.NetworkInput{Resource: r, Spec: spec})
	if err != nil {
		t.Fatalf("ensure network: %v", err)
	}
	t.Cleanup(func() {
		if err := d.DeleteNetwork(context.Background(), r); err != nil {
			t.Errorf("delete network: %v", err)
		}
	})
	return r, out
}

// runIn runs a one-off busybox command placed like a job and returns its exit code and output.
func runIn(t *testing.T, d *Driver, p engine.Placement, cmd ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := engine.JobRun{RunID: domain.NewID("run"), ResourceID: "it-probe", Name: "it-probe", Image: "busybox:1.37", Command: cmd, MemoryMiB: 32, Placement: p}
	if err := d.StartJob(ctx, run); err != nil {
		t.Fatal(err)
	}
	res, err := d.WaitJob(ctx, run.RunID, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return res.ExitCode, string(res.Output)
}

// randomCIDR is a /24 no network on the engine overlaps, with its two halves (cidr_test.go).
func randomCIDR() (string, string, string) {
	return claimCIDR(engineSubnets())
}

// engineSubnets lists every subnet of every network the engine has.
func engineSubnets() []netip.Prefix {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nets, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, n := range nets {
		for _, c := range n.IPAM.Config {
			if p, err := netip.ParsePrefix(c.Subnet); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

func TestNetworksIsolatePlaceAndName(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	cidr, first, second := randomCIDR()
	n, out := newNetwork(t, d, api.NetworkSpec{CIDR: cidr, Subnets: []api.Subnet{{Name: "auto", CIDR: first}, {Name: "fixed", CIDR: second}}})
	dockerNet := engine.DockerNetwork("", n.Name)
	if out["dockerNetwork"] != dockerNet || !strings.HasSuffix(out["gateway"].(string), ".0.1") {
		t.Fatalf("outputs %v", out)
	}
	// Idempotent: a second ensure finds it.
	if _, err := d.EnsureNetwork(ctx, engine.NetworkInput{Resource: n, Spec: api.NetworkSpec{CIDR: cidr, Subnets: []api.Subnet{{Name: "auto", CIDR: first}, {Name: "fixed", CIDR: second}}}}); err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	spec := api.AppSpec{Image: "traefik/whoami:v1.11", Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 1}
	fixed := resource(api.KindApp, "it-fixed-"+suffix())
	cleanup(t, d, fixed, engine.ObjectStore{})
	fixedOut := ensure(t, d, engine.EnsureInput{Resource: fixed, Spec: spec, Revision: 1, Placement: engine.Placement{Network: dockerNet, Subnet: second}})
	auto := resource(api.KindApp, "it-auto-"+suffix())
	cleanup(t, d, auto, engine.ObjectStore{})
	autoOut := ensure(t, d, engine.EnsureInput{Resource: auto, Spec: spec, Revision: 1, Placement: engine.Placement{Network: dockerNet}})

	in := func(ip any, cidr string) bool {
		a, err := netip.ParseAddr(fmt.Sprint(ip))
		return err == nil && netip.MustParsePrefix(cidr).Contains(a)
	}
	if !in(fixedOut[engine.OutputPrivateIP], second) || fixedOut[engine.OutputPrivateDNS] != engine.PrivateDNS(fixed) {
		t.Errorf("fixed-subnet app outputs %v", fixedOut)
	}
	if !in(autoOut[engine.OutputPrivateIP], first) || autoOut[engine.OutputPrivateDNS] != engine.PrivateDNS(auto) {
		t.Errorf("automatic-range app outputs %v", autoOut)
	}

	// Inside the network both private names answer; from the default network neither name nor address does.
	inside := engine.Placement{Network: dockerNet}
	if code, out := runIn(t, d, inside, "wget", "-q", "-T", "3", "-O", "-", "http://"+engine.PrivateDNS(fixed)+"/"); code != 0 || !strings.Contains(out, "Hostname") {
		t.Errorf("private DNS inside the network: exit %d, %q", code, out)
	}
	if code, out := runIn(t, d, engine.Placement{}, "wget", "-q", "-T", "3", "-O", "-", "http://"+engine.PrivateDNS(fixed)+"/"); code == 0 {
		t.Errorf("private name resolved from the default network: %q", out)
	}
	if code, out := runIn(t, d, engine.Placement{}, "wget", "-q", "-T", "3", "-O", "-", fmt.Sprintf("http://%s/", fixedOut[engine.OutputPrivateIP])); code == 0 {
		t.Errorf("address reached from the default network: %q", out)
	}

	// System services join the network, so health checks reach apps in it (the probe ran above).
	insp, err := d.cli.NetworkInspect(ctx, dockerNet, network.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	joined := map[string]bool{}
	for _, ep := range insp.Containers {
		joined[ep.Name] = true
	}
	if !joined[engine.ProbeName] {
		t.Errorf("probe helper not on the network: %v", joined)
	}
	if h, err := d.networkHealth(ctx, n); err != nil || h != engine.Healthy {
		t.Errorf("network health %v %v", h, err)
	}
	if err := d.cli.NetworkDisconnect(ctx, dockerNet, engine.ProbeName, true); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.networkHealth(ctx, n); h != engine.Stopped {
		t.Errorf("a system service missing from the network isn't drift: %v", h)
	}

	// Deleting while apps are attached fails (transient), then succeeds once they're gone.
	if err := d.DeleteNetwork(ctx, n); err == nil {
		t.Error("network removed with apps attached")
	}
	for _, r := range []*domain.Resource{fixed, auto} {
		if err := d.Delete(ctx, r, engine.ObjectStore{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DeleteNetwork(ctx, n); err != nil {
		t.Fatalf("delete network: %v", err)
	}
	if h, _ := d.networkHealth(ctx, n); h != engine.Missing {
		t.Errorf("network still there: %v", h)
	}
}

func TestNetworkWithoutInternetIsInternalAndOverlapsAreReported(t *testing.T) {
	d := newDriver(t)
	ctx := context.Background()
	cidr, _, _ := randomCIDR()
	f := false
	n, _ := newNetwork(t, d, api.NetworkSpec{CIDR: cidr, Internet: &f})
	insp, err := d.cli.NetworkInspect(ctx, engine.DockerNetwork("", n.Name), network.InspectOptions{})
	if err != nil || !insp.Internal || insp.IPAM.Config[0].Subnet != cidr {
		t.Fatalf("network %+v %v", insp, err)
	}
	if code, _ := runIn(t, d, engine.Placement{Network: engine.DockerNetwork("", n.Name)}, "wget", "-q", "-T", "3", "-O", "/dev/null", "http://1.1.1.1/"); code == 0 {
		t.Error("reached the internet from a network without internet")
	}
	other := resource(api.KindNetwork, "it-overlap-"+suffix())
	_, err = d.EnsureNetwork(ctx, engine.NetworkInput{Resource: other, Spec: api.NetworkSpec{CIDR: cidr}})
	if err == nil || !engine.IsPermanent(err) || !strings.Contains(err.Error(), "overlaps") {
		_ = d.DeleteNetwork(ctx, other)
		t.Fatalf("overlap: %v", err)
	}
}
