package firewall

import (
	"net/netip"
	"strings"
	"testing"
)

func pfx(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, v := range s {
		out[i] = netip.MustParsePrefix(v)
	}
	return out
}

func TestRenderIngressImplicitAllowsAndEgress(t *testing.T) {
	p := Policy{
		Ingress: []Rule{
			{Peers: pfx("10.40.2.7/32", "10.40.2.5/32", "10.40.2.5/32"), Protocol: TCP, Ports: []PortRange{{5432, 5432}, {8000, 8100}, {5432, 5432}}},
			{Peers: pfx("0.0.0.0/0"), Protocol: ICMP},
			{Peers: pfx("10.0.0.0/8", "0.0.0.0/0"), Protocol: UDP},
			{Peers: nil, Protocol: TCP}, // a resource source with no containers: never matches
		},
		RestrictEgress: true,
		Egress:         []Rule{{Peers: pfx("0.0.0.0/0"), Protocol: TCP, Ports: []PortRange{{443, 443}}}},
	}
	c := Context{AlwaysFrom: []netip.Addr{netip.MustParseAddr("10.40.0.3"), netip.MustParseAddr("10.40.0.2"), netip.MustParseAddr("10.40.0.2")},
		Gateway: netip.MustParseAddr("10.40.0.1"), PublishedPorts: []int{8080}}
	got := Render(p, c)
	want := `add table inet enclave
delete table inet enclave
table inet enclave {
	chain input {
		type filter hook input priority 0; policy drop;
		iif "lo" accept
		ct state established,related accept
		ct state invalid drop
		ip saddr { 10.40.0.2, 10.40.0.3 } accept
		ip saddr 10.40.0.1 tcp dport 8080 accept
		ip saddr { 10.40.2.5/32, 10.40.2.7/32 } tcp dport { 5432, 8000-8100 } accept
		meta nfproto ipv4 meta l4proto icmp accept
		meta nfproto ipv4 meta l4proto udp accept
	}
	chain output {
		type filter hook output priority 0; policy drop;
		oif "lo" accept
		ct state established,related accept
		meta nfproto ipv4 tcp dport 443 accept
	}
}
`
	if got != want {
		t.Errorf("ruleset:\n%s\nwant:\n%s", got, want)
	}
	if Render(p, c) != got {
		t.Error("not deterministic")
	}
}

func TestRenderWithoutEgressRestrictionAcceptsOutput(t *testing.T) {
	got := Render(Policy{Ingress: []Rule{{Peers: pfx("10.1.0.0/16"), Protocol: All}}}, Context{})
	if !strings.Contains(got, "policy accept;\n\t}") || !strings.Contains(got, "ip saddr 10.1.0.0/16 accept") {
		t.Errorf("ruleset:\n%s", got)
	}
	if strings.Contains(got, "oif") {
		t.Error("egress rules without a restriction")
	}
}

func TestClearAndHashAndImage(t *testing.T) {
	if Clear() != "add table inet enclave\ndelete table inet enclave\n" {
		t.Errorf("clear %q", Clear())
	}
	a, b := Render(Policy{}, Context{}), Render(Policy{RestrictEgress: true}, Context{})
	if Hash(a) == Hash(b) || len(Hash(a)) != 16 {
		t.Errorf("hashes %s %s", Hash(a), Hash(b))
	}
	if !strings.HasPrefix(ImageTag(), "enclave.local/netfilter:") || !strings.Contains(Dockerfile, BaseImage) {
		t.Errorf("image %s", ImageTag())
	}
}
