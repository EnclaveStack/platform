//go:build docker

package docker

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/firewall"
	"github.com/enclavestack/platform/internal/vmlink"
	"github.com/enclavestack/platform/pkg/api"
)

// VM link containers on real Docker (ADR-026, L5-002 §8). End-to-end traffic through a real VM is in
// internal/engine/qemu (make engine-qemu); here: the image, the rules, the frame port's guard, kept host
// ports, internal networks and the firewall's forward chain.

func TestVMLinkContainers(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	cidr, first, _ := randomCIDR()
	n, _ := newNetwork(t, d, api.NetworkSpec{CIDR: cidr, Subnets: []api.Subnet{{Name: "a", CIDR: first}}})
	vm := resource(api.KindVM, "it-vm-"+suffix())
	t.Cleanup(func() { _ = d.DeleteVMLink(context.Background(), vm) })
	in := engine.EnsureInput{Resource: vm, Spec: api.VMSpec{Publish: []int{8080}}, Placement: engine.Placement{Network: engine.DockerNetwork("", n.Name)}}

	lctx, cancel := context.WithTimeout(ctx, 10*time.Minute) // the first run builds the image
	out, err := d.EnsureVMLink(lctx, in)
	cancel()
	if err != nil {
		t.Fatalf("ensure link: %v", err)
	}
	ip, _ := netip.ParseAddr(out[engine.OutputPrivateIP].(string))
	frame := out[engine.OutputLinkPort].(int)
	ssh := out[engine.OutputSSHAddress].(string)
	if !netip.MustParsePrefix(first).Contains(ip) || frame == 0 || !strings.HasPrefix(ssh, "127.0.0.1:") || out[engine.OutputHost] == nil ||
		out[engine.OutputPrivateDNS] != vm.Name+".vm.internal" {
		t.Fatalf("outputs %v", out)
	}
	rules := execOut(t, d, engine.ContainerName(vm), "nft", "list", "table", "ip", vmlink.Table)
	for _, want := range []string{"dnat to 198.18.0.2", "masquerade", "61998"} {
		if !strings.Contains(rules, want) {
			t.Errorf("link rules lack %q:\n%s", want, rules)
		}
	}

	// The frame port answers the host (through the gateway) but not another container in the network.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(frame), 3*time.Second)
	if err != nil {
		t.Fatalf("frame port from the host: %v", err)
	}
	conn.Close()
	code, _ := runIn(t, d, engine.Placement{Network: engine.DockerNetwork("", n.Name)}, "sh", "-c", "nc -w 2 "+ip.String()+" "+strconv.Itoa(vmlink.FramePort)+" </dev/null")
	if code == 0 {
		t.Error("another container opened the frame port")
	}

	// A change (published ports) recreates the container on the same host ports.
	in.Spec = api.VMSpec{Publish: []int{8080, 8081}}
	in.HostPorts = map[int]int{engine.VMLinkFramePort: frame}
	out2, err := d.EnsureVMLink(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if out2[engine.OutputLinkPort] != frame || out2[engine.OutputSSHAddress] != ssh {
		t.Errorf("host ports moved: %v → %v", out, out2)
	}

	// Firewalls render into the forward chain of a VM's link.
	pol := &firewall.Policy{Ingress: []firewall.Rule{{Peers: []netip.Prefix{netip.MustParsePrefix("10.1.2.3/32")}, Protocol: api.ProtocolTCP, Ports: []firewall.PortRange{{From: 9000, To: 9000}}}}}
	if _, err := d.ApplyFirewall(ctx, vm, pol); err != nil {
		t.Fatalf("apply firewall: %v", err)
	}
	fw := execOut(t, d, engine.ContainerName(vm), "nft", "list", "table", "inet", firewall.Table)
	if !strings.Contains(fw, "hook forward") || !strings.Contains(fw, `oifname "vm0" ip saddr 10.1.2.3 tcp dport 9000 accept`) || strings.Contains(fw, "hook input") {
		t.Errorf("VM firewall:\n%s", fw)
	}

	// A network without internet: the link joins es-vmlink for its loopback ports.
	icidr, ifirst, _ := randomCIDR()
	off := false
	in2, _ := newNetwork(t, d, api.NetworkSpec{CIDR: icidr, Subnets: []api.Subnet{{Name: "a", CIDR: ifirst}}, Internet: &off})
	priv := resource(api.KindVM, "it-vmp-"+suffix())
	t.Cleanup(func() { _ = d.DeleteVMLink(context.Background(), priv) })
	pout, err := d.EnsureVMLink(ctx, engine.EnsureInput{Resource: priv, Spec: api.VMSpec{}, Placement: engine.Placement{Network: engine.DockerNetwork("", in2.Name)}})
	if err != nil {
		t.Fatalf("internal link: %v", err)
	}
	pc, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(pout[engine.OutputLinkPort].(int)), 3*time.Second)
	if err != nil {
		t.Fatalf("frame port of a link in a network without internet: %v", err)
	}
	pc.Close()
	prules := execOut(t, d, engine.ContainerName(priv), "nft", "list", "table", "ip", vmlink.Table)
	if !strings.Contains(prules, `iifname "vm0" oifname`) || !strings.Contains(prules, "tcp dport 22 dnat") {
		t.Errorf("internal link rules:\n%s", prules)
	}
}
