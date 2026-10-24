package mesh

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeysAreWireGuards(t *testing.T) {
	// The test vector of WireGuard's own tools: `echo <priv> | wg pubkey`.
	pub, err := PublicKey("YNDzkBUcNNaShdTaU7tvYvP71Oww9sJqTUNaC5i+l1g=")
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 44 {
		t.Errorf("public key %q", pub)
	}
	if _, err := PublicKey("short"); err == nil {
		t.Error("a short key accepted")
	}
}

func TestWireGuardConfRendersPeersAndRefusesJunk(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	conf, err := WireGuardConf(key, Config{ListenPort: 51820, Peers: []Peer{{PublicKey: key, Endpoint: "192.168.1.20:51820", Addresses: []string{"10.0.0.7", "10.0.0.5"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ListenPort = 51820", "Endpoint = 192.168.1.20:51820", "AllowedIPs = 10.0.0.7/32, 10.0.0.5/32", "PersistentKeepalive = 25"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in\n%s", want, conf)
		}
	}
	for _, bad := range []Peer{
		{PublicKey: "x\nPostUp = rm -rf /", Endpoint: "a:1"},
		{PublicKey: key, Endpoint: "no-port"},
		{PublicKey: key, Endpoint: "a:1", Addresses: []string{"10.0.0.0/8"}},
	} {
		if _, err := WireGuardConf(key, Config{Peers: []Peer{bad}}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestRulesKeepNetworksApart(t *testing.T) {
	nets := []Network{{CIDR: "10.20.0.0/16"}, {CIDR: "10.30.0.0/24"}, {CIDR: "10.40.0.0/24"}}
	rules, err := Rules(nets, map[string]string{"10.20.0.0/16": "eth1", "10.30.0.0/24": "eth2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"policy drop", "maxseg size set rt mtu",
		`iifname "eth1" oifname "es0" ip saddr 10.20.0.0/16 ip daddr 10.20.0.0/16 accept`,
		`iifname "es0" oifname "eth2" ip saddr 10.30.0.0/24 ip daddr 10.30.0.0/24 accept`,
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("missing %q in\n%s", want, rules)
		}
	}
	if strings.Contains(rules, "10.40.0.0/24") {
		t.Error("a network this machine isn't on got rules")
	}
	if _, err := Rules([]Network{{CIDR: "10.20.0.1/16"}}, map[string]string{"10.20.0.1/16": "eth1"}, nil); err == nil {
		t.Error("an unmasked range accepted")
	}
	if _, err := Rules(nets, map[string]string{"10.20.0.0/16": `eth1"; flush ruleset; "`}, nil); err == nil {
		t.Error("an interface name with quotes accepted")
	}
}

func TestRemoteAddressesAreSortedAndUnique(t *testing.T) {
	got := RemoteAddresses(Config{Peers: []Peer{{Addresses: []string{"10.0.0.9", "10.0.0.2"}}, {Addresses: []string{"10.0.0.2", "bad"}}}})
	if len(got) != 2 || got[0] != netip.MustParseAddr("10.0.0.2") || got[1] != netip.MustParseAddr("10.0.0.9") {
		t.Errorf("%v", got)
	}
}

func TestMACIsFixedPerNetworkAndLocal(t *testing.T) {
	a, b := MAC("es-net-shop"), MAC("es-net-shop")
	if a != b || MAC("enclave") == a {
		t.Errorf("%s %s %s", a, b, MAC("enclave"))
	}
	hw, err := net.ParseMAC(a)
	if err != nil || hw[0]&2 == 0 || hw[0]&1 != 0 {
		t.Errorf("%s isn't a locally administered unicast address", a)
	}
}

func TestBuildContextCompilesForLinux(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the helper")
	}
	dir := t.TempDir()
	for name, b := range BuildContext() {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "mesh.go")); !strings.HasPrefix(string(b), "package main\n") {
		t.Error("mesh.go isn't package main")
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "es-mesh"), ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the build context doesn't compile: %v\n%s", err, out)
	}
	// The tag is a hash of the build context: the same on every call, and on every machine.
	first, again := ImageTag(), ImageTag()
	if !strings.HasPrefix(first, "enclave.local/mesh:") || first != again {
		t.Errorf("tag %s, then %s", first, again)
	}
}

// A peer behind NAT has no endpoint: no Endpoint and no keepalive from this side (L2-014 EDG-05).
func TestPeerWithoutEndpoint(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	conf, err := WireGuardConf(key, Config{ListenPort: 51820, Peers: []Peer{{PublicKey: key, Addresses: []string{"10.0.0.7"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(conf, "Endpoint") || strings.Contains(conf, "PersistentKeepalive") || !strings.Contains(conf, "AllowedIPs = 10.0.0.7/32") {
		t.Fatalf("peer without endpoint:\n%s", conf)
	}
}

func TestAnEdgePeerReachesOnlyItsListeners(t *testing.T) {
	// L2-014 EDG-04: a rented server's relay reaches the edge proxy's relay listeners and nothing else here.
	nets := []Network{{CIDR: "10.20.0.0/16"}}
	edge := Peer{PublicKey: "k", Addresses: []string{"10.20.5.9"}, Limited: true, Reach: []string{"10.20.0.3:12000", "10.20.0.3:12001"}}
	rules, err := Rules(nets, map[string]string{"10.20.0.0/16": "eth1"}, []Peer{{PublicKey: "w", Addresses: []string{"10.20.7.1"}}, edge})
	if err != nil {
		t.Fatal(err)
	}
	allow := `iifname "es0" ip saddr { 10.20.5.9 } ip daddr . tcp dport { 10.20.0.3 . 12000, 10.20.0.3 . 12001 } accept`
	drop := `iifname "es0" ip saddr { 10.20.5.9 } drop`
	general := `iifname "es0" oifname "eth1"`
	if !strings.Contains(rules, allow) || !strings.Contains(rules, drop) || strings.Index(rules, drop) > strings.Index(rules, general) ||
		strings.Index(rules, allow) > strings.Index(rules, drop) || strings.Contains(rules, "10.20.7.1") {
		t.Fatalf("rules:\n%s", rules)
	}
	// A limited peer with nothing to reach is dropped entirely; bad entries are refused.
	none := edge
	none.Reach = nil
	if r, _ := Rules(nets, map[string]string{"10.20.0.0/16": "eth1"}, []Peer{none}); !strings.Contains(r, drop) || strings.Contains(r, "dport") {
		t.Errorf("rules:\n%s", r)
	}
	bad := edge
	bad.Reach = []string{"10.20.0.3:0; flush ruleset"}
	if _, err := Rules(nets, map[string]string{"10.20.0.0/16": "eth1"}, []Peer{bad}); err == nil {
		t.Error("a bad reach entry was accepted")
	}
}
