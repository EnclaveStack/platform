//go:build docker

package docker

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/vmlink"
	"github.com/enclavestack/platform/pkg/api"
)

// The mesh between two real engines (ADR-045, L5-002 §10): this machine's engine is A, and a
// Docker-in-Docker container is B, the "other machine". Both carry the same network; an app on A reaches an
// app on B by name and address through the two meshes' WireGuard tunnel, a firewall on B sees A's real
// source, and other networks stay out.

const dindImage = "docker:28.5.1-dind"

// secondEngine starts a Docker-in-Docker engine, publishing its API on loopback and tunnelPort/udp.
func secondEngine(t *testing.T, a *Driver, tunnelPort int) *Driver {
	t.Helper()
	ctx := context.Background()
	if err := a.ensureImage(ctx, dindImage, false); err != nil {
		t.Fatal(err)
	}
	name := "es-test-machine2-" + suffix()
	api2 := nat.Port("2375/tcp")
	udp := nat.Port(strconv.Itoa(tunnelPort) + "/udp")
	_, err := a.cli.ContainerCreate(ctx, &container.Config{Image: dindImage, Env: []string{"DOCKER_TLS_CERTDIR="}, ExposedPorts: nat.PortSet{api2: {}, udp: {}},
		Cmd: []string{"dockerd", "--host=tcp://0.0.0.0:2375", "--tls=false"}},
		&container.HostConfig{Privileged: true, PortBindings: nat.PortMap{api2: {{HostIP: "127.0.0.1"}}, udp: {{HostIP: "0.0.0.0", HostPort: strconv.Itoa(tunnelPort)}}}},
		nil, nil, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.remove(context.Background(), name) })
	if err := a.cli.ContainerStart(ctx, name, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	c, _ := a.inspect(ctx, name)
	host := "tcp://127.0.0.1:" + c.NetworkSettings.Ports[api2][0].HostPort
	b, err := New(Options{DockerHost: host, Agent: true})
	if err != nil {
		t.Fatal(err)
	}
	b.opts.Drain = 500 * time.Millisecond
	deadline := time.Now().Add(time.Minute)
	for b.Ping(ctx) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the second engine didn't start")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return b
}

// copyImage moves an image from engine a to engine b without a registry.
func copyImage(t *testing.T, a, b *Driver, ref string) {
	t.Helper()
	ctx := context.Background()
	if err := a.ensureImage(ctx, ref, false); err != nil {
		t.Fatal(err)
	}
	rc, err := a.cli.ImageSave(ctx, []string{ref})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	res, err := b.cli.ImageLoad(ctx, rc)
	if err != nil {
		t.Fatalf("load %s: %v", ref, err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
}

func gatewayOf(t *testing.T, d *Driver, net string) string {
	t.Helper()
	n, err := d.cli.NetworkInspect(context.Background(), net, network.InspectOptions{})
	if err != nil || len(n.IPAM.Config) == 0 {
		t.Fatalf("network %s: %v", net, err)
	}
	return n.IPAM.Config[0].Gateway
}

func addrs(prefix string, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("%s.%d", prefix, i))
	}
	return out
}

func TestMeshBetweenTwoEngines(t *testing.T) {
	a := fastDriver(t)
	ctx := context.Background()
	if c, _ := a.inspect(ctx, MeshName); c != nil {
		t.Skip("a real EnclaveStack mesh runs on this engine; this test would replace it")
	}
	portA, portB := 52000+rand.IntN(500), 52600+rand.IntN(500)
	b := secondEngine(t, a, portB)
	for _, ref := range []string{"busybox:1.37", DefaultProbeImage, vmlink.GoImage, vmlink.AlpineImage} {
		copyImage(t, a, b, ref)
	}

	// The same network on both machines, as the router makes it (XNET-01).
	y := 100 + rand.IntN(150)
	cidr := fmt.Sprintf("10.%d.0.0/24", y)
	n := resource(api.KindNetwork, "it-mesh-"+suffix())
	spec := api.NetworkSpec{CIDR: cidr}
	if err := a.ensureNetwork(ctx); err != nil { // A's default network exists before the mesh
		t.Fatal(err)
	}
	for _, d := range []*Driver{a, b} {
		t.Cleanup(func() { _ = d.DeleteNetwork(context.Background(), n) })
	}
	defaultRange := ""
	if nn, err := a.cli.NetworkInspect(ctx, engine.NetworkName, network.InspectOptions{}); err == nil && len(nn.IPAM.Config) > 0 {
		defaultRange = nn.IPAM.Config[0].Subnet
	}
	def := netip.MustParsePrefix(defaultRange).Addr().As4()
	defPrefix := fmt.Sprintf("%d.%d", def[0], def[1])
	netName := engine.DockerNetwork("", n.Name)
	pools := map[*Driver]map[string][]string{
		a: {engine.NetworkName: addrs(defPrefix+".250", 10, 19), netName: addrs(fmt.Sprintf("10.%d.0", y), 10, 19)},
		b: {engine.NetworkName: addrs(defPrefix+".251", 10, 19), netName: addrs(fmt.Sprintf("10.%d.0", y), 20, 29)},
	}
	gw := gatewayOf(t, a, "bridge") // both machines reach each other's published tunnel here
	cfgOf := func(d *Driver, port int, peers []engine.MeshPeer, standIns []engine.StandIn) engine.MeshConfig {
		return engine.MeshConfig{Enabled: true, TunnelPort: port, DefaultRange: defaultRange, Networks: map[string]string{netName: cidr},
			Pools: pools[d], Peers: peers, StandIns: standIns}
	}
	t.Cleanup(func() {
		_, _ = a.ConfigureMesh(context.Background(), engine.MeshConfig{})
		_, _ = b.ConfigureMesh(context.Background(), engine.MeshConfig{})
	})
	start := time.Now()
	stA, err := a.ConfigureMesh(ctx, cfgOf(a, portA, nil, nil))
	if err != nil {
		t.Fatalf("mesh on A: %v", err)
	}
	stB, err := b.ConfigureMesh(ctx, cfgOf(b, portB, nil, nil))
	if err != nil {
		t.Fatalf("mesh on B: %v", err)
	}
	t.Logf("both meshes up in %s (B built its image)", time.Since(start).Round(time.Second))
	if len(stA.PublicKey) != 44 || len(stB.PublicKey) != 44 || stA.PublicKey == stB.PublicKey {
		t.Fatalf("keys %q %q", stA.PublicKey, stB.PublicKey)
	}
	if _, err := a.EnsureNetwork(ctx, engine.NetworkInput{Resource: n, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureNetwork(ctx, engine.NetworkInput{Resource: n, Spec: spec}); err != nil {
		t.Fatal(err)
	}

	// An app on each machine; B's answers on 9000. Their addresses come from their machines' pools.
	place := engine.Placement{Network: netName}
	srv := resource(api.KindApp, "it-mtb-"+suffix())
	cleanup(t, b, srv, engine.ObjectStore{})
	srvOut := ensure(t, b, engine.EnsureInput{Resource: srv, Revision: 1, Placement: place, Spec: api.AppSpec{Image: "busybox:1.37", Port: 9000,
		Command: []string{"sh", "-c", "while true; do nc -l -p 9000 -e echo from-b; done"}, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 1}})
	cli := resource(api.KindApp, "it-mta-"+suffix())
	cleanup(t, a, cli, engine.ObjectStore{})
	cliOut := ensure(t, a, engine.EnsureInput{Resource: cli, Revision: 1, Placement: place, Spec: api.AppSpec{Image: "busybox:1.37",
		Command: []string{"sleep", "3600"}, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 1, HealthCheck: &api.HealthCheck{Type: api.HealthNone}}})
	srvIP, cliIP := fmt.Sprint(srvOut[engine.OutputPrivateIP]), fmt.Sprint(cliOut[engine.OutputPrivateIP])
	if !strings.HasPrefix(srvIP, fmt.Sprintf("10.%d.0.2", y)) || !strings.HasPrefix(cliIP, fmt.Sprintf("10.%d.0.1", y)) {
		t.Fatalf("addresses not from the pools: server %s, client %s", srvIP, cliIP)
	}

	// Each machine learns the other's containers: peers with their addresses, stand-ins with their names.
	members := func(d *Driver) ([]string, []engine.StandIn) {
		infos, err := d.NetworkEndpoints(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ips []string
		var sis []engine.StandIn
		for _, ni := range infos {
			if ni.Name != netName && ni.Name != engine.NetworkName {
				continue // other tests' networks on this engine
			}
			for _, m := range ni.Members {
				ips = append(ips, m.Address)
				sis = append(sis, engine.StandIn{Network: ni.Name, Address: m.Address, Aliases: m.Aliases, Of: m.Container, Machine: "other"})
			}
		}
		return ips, sis
	}
	ipsA, sisA := members(a)
	ipsB, sisB := members(b)
	if _, err := a.ConfigureMesh(ctx, cfgOf(a, portA, []engine.MeshPeer{{PublicKey: stB.PublicKey, Endpoint: fmt.Sprintf("%s:%d", gw, portB), Addresses: ipsB}}, sisB)); err != nil {
		t.Fatalf("A with its peer: %v", err)
	}
	if _, err := b.ConfigureMesh(ctx, cfgOf(b, portB, []engine.MeshPeer{{PublicKey: stA.PublicKey, Endpoint: fmt.Sprintf("%s:%d", gw, portA), Addresses: ipsA}}, sisA)); err != nil {
		t.Fatalf("B with its peer: %v", err)
	}

	// By name and by address, from A to B (XNET-01). B's answer is a one-connection-at-a-time nc loop that
	// its health checks also reach, so a connection can close empty: try until B answers.
	call := func(what string, cmd ...string) string {
		t.Helper()
		var out string
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			res, err := a.exec(ctx, engine.RevisionName(cli, 1), cmd, nil)
			if err == nil && res.ExitCode == 0 && strings.Contains(res.Output, "from-b") {
				return res.Output
			}
			out = fmt.Sprint(res, err)
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("%s: %s", what, out)
		return ""
	}
	if got := call("by name", "sh", "-c", "echo | nc -w 3 "+engine.PrivateDNS(srv)+" 9000"); !strings.Contains(got, "from-b") {
		t.Errorf("by name: %q", got)
	}
	if got := call("by address", "sh", "-c", "echo | nc -w 3 "+srvIP+" 9000"); !strings.Contains(got, "from-b") {
		t.Errorf("by address: %q", got)
	}
	st, err := a.MeshStatus(ctx)
	if err != nil || st.Handshakes[stB.PublicKey].IsZero() {
		t.Errorf("A never heard from B: %+v %v", st, err)
	}

	// A firewall on B that admits only the client's address: the client gets through, another container
	// on A doesn't, so B sees A's real source addresses (XNET-03).
	policy := &firewall.Policy{Ingress: []firewall.Rule{{Peers: []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr(cliIP), 32)}, Protocol: firewall.TCP,
		Ports: []firewall.PortRange{{From: 9000, To: 9000}}}}}
	if _, err := b.ApplyFirewall(ctx, srv, policy); err != nil {
		t.Fatal(err)
	}
	if got := call("allowed by the firewall", "sh", "-c", "echo | nc -w 3 "+srvIP+" 9000"); !strings.Contains(got, "from-b") {
		t.Errorf("the allowed client: %q", got)
	}
	if code, out := runIn(t, a, place, "sh", "-c", "echo | nc -w 3 "+srvIP+" 9000"); code == 0 && strings.Contains(out, "from-b") {
		t.Error("a container the firewall doesn't name got through: B doesn't see real sources")
	}

	// Another network on A doesn't reach B's app (NET-03 across machines).
	// As the mesh sync does, the network's pool goes out before the network is made (ADR-045).
	otherCIDR := fmt.Sprintf("10.%d.0.0/24", 1+(y+1)%250)
	otherPrefix := strings.TrimSuffix(otherCIDR, ".0/24")
	peerB := []engine.MeshPeer{{PublicKey: stB.PublicKey, Endpoint: fmt.Sprintf("%s:%d", gw, portB), Addresses: ipsB}}
	otherRes := resource(api.KindNetwork, "it-other-"+suffix())
	pools[a][engine.DockerNetwork("", otherRes.Name)] = addrs(otherPrefix, 10, 19)
	if _, err := a.ConfigureMesh(ctx, cfgOf(a, portA, peerB, sisB)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureNetwork(ctx, engine.NetworkInput{Resource: otherRes, Spec: api.NetworkSpec{CIDR: otherCIDR}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.DeleteNetwork(context.Background(), otherRes) })
	other := otherRes
	if code, out := runIn(t, a, engine.Placement{Network: engine.DockerNetwork("", other.Name)}, "sh", "-c", "echo | nc -w 3 "+srvIP+" 9000"); code == 0 && strings.Contains(out, "from-b") {
		t.Error("another network reached B's app")
	}

	// A stand-in that restarts still delivers (it may announce its own address again).
	for _, s := range sisB {
		if s.Address == srvIP {
			if err := a.cli.ContainerRestart(ctx, standInName(s), container.StopOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := b.ApplyFirewall(ctx, srv, nil); err != nil {
		t.Fatal(err)
	}
	if got := call("after a stand-in restart", "sh", "-c", "echo | nc -w 3 "+engine.PrivateDNS(srv)+" 9000"); !strings.Contains(got, "from-b") {
		t.Errorf("after a stand-in restart: %q", got)
	}

	// One machine again: the mesh and its stand-ins go.
	if _, err := a.ConfigureMesh(ctx, engine.MeshConfig{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := a.inspect(ctx, MeshName); c != nil {
		t.Error("the mesh stayed after it was disabled")
	}
	if c, _ := a.inspect(ctx, standInName(sisB[0])); c != nil {
		t.Error("a stand-in stayed after the mesh was disabled")
	}
}

// A network the mesh doesn't span — a leftover of another install on the same engine — is left to the
// engine's own addresses; a network it spans takes only the machine's pool (ADR-045). A leftover once
// made the health-check helper unable to join it, failing a rescheduled app.
func TestANetworkOutsideTheMeshIsLeftToTheEngine(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	other := resource(api.KindNetwork, "it-left-"+suffix())
	spans := resource(api.KindNetwork, "it-spans-"+suffix())
	for _, n := range []*domain.Resource{other, spans} {
		y := 100 + rand.IntN(150)
		if _, err := d.EnsureNetwork(ctx, engine.NetworkInput{Resource: n, Spec: api.NetworkSpec{CIDR: fmt.Sprintf("10.%d.%d.0/24", y, rand.IntN(250))}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteNetwork(context.Background(), n) })
	}
	spansName := engine.DockerNetwork("", spans.Name)
	d.meshMu.Lock()
	prior := d.mesh
	d.mesh = &meshState{cfg: engine.MeshConfig{Enabled: true, Networks: map[string]string{spansName: "10.0.0.0/24"}}}
	d.meshMu.Unlock()
	t.Cleanup(func() { d.meshMu.Lock(); d.mesh = prior; d.meshMu.Unlock() })

	if a, err := d.poolAddress(ctx, engine.DockerNetwork("", other.Name), ""); a != "" || err != nil {
		t.Errorf("a network outside the mesh: %q %v", a, err)
	}
	if _, err := d.poolAddress(ctx, spansName, ""); err == nil || !strings.Contains(err.Error(), "waiting for the main machine") {
		t.Errorf("a mesh network without a pool: %v", err)
	}
}
