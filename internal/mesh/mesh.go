package mesh

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The mesh helper (ADR-045): one container per machine joins every EnclaveStack network and carries a
// WireGuard interface to the other machines. For each address on another machine it routes the /32 over the
// tunnel and answers ARP for it on that network's interface, so local containers reach it as if it were on
// their bridge. Nothing is translated: the receiver sees the real source. nftables lets traffic cross only
// between a network's interface and the tunnel, and only inside that network's range.

// Paths inside the mesh container.
const (
	ConfigPath = "/etc/es-mesh/config.json"
	KeyPath    = "/var/lib/es-mesh/private.key"
	wgConfPath = "/run/es-mesh/wg.conf"
	nftPath    = "/run/es-mesh/rules.nft"
	Interface  = "es0"
	MTU        = 1420
)

// Config is what the driver writes for apply.
type Config struct {
	ListenPort int       `json:"listenPort"`
	Peers      []Peer    `json:"peers"`
	Networks   []Network `json:"networks"`
}

// Peer is another machine: its public key, where its tunnel answers, and the addresses that live there.
type Peer struct {
	PublicKey string   `json:"publicKey"`
	Endpoint  string   `json:"endpoint"`
	Addresses []string `json:"addresses"`
}

// Network is an EnclaveStack network this machine is on, by range; the helper finds its interface.
type Network struct {
	CIDR string `json:"cidr"`
}

// Status is what apply and status print: this machine's public key and the last handshake with each peer.
type Status struct {
	PublicKey  string           `json:"publicKey"`
	Handshakes map[string]int64 `json:"handshakes"` // public key → unix seconds, 0 for never
}

// Main runs a subcommand: run (the container's main process), apply, status, standin.
func Main() {
	if len(os.Args) < 2 {
		fatal(errors.New("usage: es-mesh run|apply|status|standin ADDRESS PREFIXLEN MESHIP MESHMAC"))
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run()
	case "apply":
		err = printStatus(apply)
	case "status":
		err = printStatus(nil)
	case "standin":
		if len(os.Args) != 6 {
			err = errors.New("usage: es-mesh standin ADDRESS PREFIXLEN MESHIP MESHMAC")
			break
		}
		err = standIn(os.Args[2], os.Args[3], os.Args[4], os.Args[5])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "es-mesh:", err)
	os.Exit(1)
}

// run is the container's main process: it restores the last configuration after a restart, then waits.
func run() error {
	if _, err := os.Stat(ConfigPath); err == nil {
		if err := apply(); err != nil {
			fmt.Fprintln(os.Stderr, "es-mesh: restoring the last configuration:", err)
		}
	}
	return wait()
}

func wait() error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	return nil
}

func printStatus(before func() error) error {
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	st, err := status()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(st)
}

// apply converges the interface, the peers, the routes, the proxy neighbours and the rules on Config.
func apply() error {
	raw, err := os.ReadFile(ConfigPath)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	priv, _, err := key()
	if err != nil {
		return err
	}
	if err := ensureInterface(); err != nil {
		return err
	}
	ifaces, err := interfacesByNetwork(cfg.Networks)
	if err != nil {
		return err
	}
	conf, err := WireGuardConf(priv, cfg)
	if err != nil {
		return err
	}
	if err := writeFile(wgConfPath, []byte(conf), 0o600); err != nil {
		return err
	}
	if err := sh("wg", "syncconf", Interface, wgConfPath); err != nil {
		return err
	}
	remote := RemoteAddresses(cfg)
	if err := syncRoutes(remote); err != nil {
		return err
	}
	if err := syncProxies(remote, cfg.Networks, ifaces); err != nil {
		return err
	}
	rules, err := Rules(cfg.Networks, ifaces)
	if err != nil {
		return err
	}
	if err := writeFile(nftPath, []byte(rules), 0o600); err != nil {
		return err
	}
	return sh("nft", "-f", nftPath)
}

// key reads this machine's private key, making it on first use (it never leaves the machine).
func key() (priv, pub string, err error) {
	b, err := os.ReadFile(KeyPath)
	if errors.Is(err, os.ErrNotExist) {
		k, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return "", "", err
		}
		b = []byte(base64.StdEncoding.EncodeToString(k.Bytes()))
		if err := writeFile(KeyPath, b, 0o600); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	priv = strings.TrimSpace(string(b))
	pub, err = PublicKey(priv)
	return priv, pub, err
}

// PublicKey derives a WireGuard public key from a base64 private key.
func PublicKey(priv string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(priv)
	if err != nil || len(raw) != 32 {
		return "", errors.New("the private key isn't 32 bytes of base64")
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func ensureInterface() error {
	if _, err := net.InterfaceByName(Interface); err != nil {
		if err := sh("ip", "link", "add", Interface, "type", "wireguard"); err != nil {
			return err
		}
	}
	if err := sh("ip", "link", "set", Interface, "mtu", strconv.Itoa(MTU)); err != nil {
		return err
	}
	return sh("ip", "link", "set", Interface, "up")
}

// interfacesByNetwork finds, for each network range, the interface whose address is in it.
func interfacesByNetwork(nets []Network) (map[string]string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip, _ := netip.AddrFromSlice(ipn.IP.To4())
			for _, n := range nets {
				if p, err := netip.ParsePrefix(n.CIDR); err == nil && p.Contains(ip) {
					out[n.CIDR] = ifc.Name
				}
			}
		}
	}
	return out, nil
}

// WireGuardConf renders the tunnel's configuration for `wg syncconf`.
func WireGuardConf(priv string, cfg Config) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nListenPort = %d\n", priv, cfg.ListenPort)
	for _, p := range cfg.Peers {
		if _, err := base64.StdEncoding.DecodeString(p.PublicKey); err != nil || len(p.PublicKey) != 44 {
			return "", fmt.Errorf("a peer's public key %q isn't WireGuard's", p.PublicKey)
		}
		if _, _, err := net.SplitHostPort(p.Endpoint); err != nil {
			return "", fmt.Errorf("a peer's endpoint %q isn't host:port", p.Endpoint)
		}
		allowed := make([]string, 0, len(p.Addresses))
		for _, a := range p.Addresses {
			ip, err := netip.ParseAddr(a)
			if err != nil || !ip.Is4() {
				return "", fmt.Errorf("a peer's address %q isn't IPv4", a)
			}
			allowed = append(allowed, ip.String()+"/32")
		}
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nPersistentKeepalive = 25\n", p.PublicKey, p.Endpoint)
		if len(allowed) > 0 {
			fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(allowed, ", "))
		}
	}
	return b.String(), nil
}

// RemoteAddresses is every address on another machine, sorted.
func RemoteAddresses(cfg Config) []netip.Addr {
	var out []netip.Addr
	for _, p := range cfg.Peers {
		for _, a := range p.Addresses {
			if ip, err := netip.ParseAddr(a); err == nil && ip.Is4() {
				out = append(out, ip)
			}
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(out)
}

// syncRoutes keeps exactly one /32 route over the tunnel per remote address.
func syncRoutes(remote []netip.Addr) error {
	out, err := exec.Command("ip", "-4", "route", "show", "dev", Interface).Output()
	if err != nil {
		return fmt.Errorf("ip route show: %w", err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			have[strings.TrimSuffix(f[0], "/32")] = true
		}
	}
	want := map[string]bool{}
	for _, a := range remote {
		want[a.String()] = true
		if !have[a.String()] {
			if err := sh("ip", "route", "replace", a.String()+"/32", "dev", Interface); err != nil {
				return err
			}
		}
	}
	for a := range have {
		if !want[a] {
			if err := sh("ip", "route", "del", a+"/32", "dev", Interface); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncProxies answers ARP for every remote address on its network's interface, and for nothing else.
func syncProxies(remote []netip.Addr, nets []Network, ifaces map[string]string) error {
	out, err := exec.Command("ip", "-4", "neigh", "show", "proxy").Output()
	if err != nil {
		return fmt.Errorf("ip neigh show proxy: %w", err)
	}
	type entry struct{ addr, dev string }
	have := map[entry]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == "dev" {
			have[entry{f[0], f[2]}] = true
		}
	}
	want := map[entry]bool{}
	for _, a := range remote {
		for _, n := range nets {
			p, err := netip.ParsePrefix(n.CIDR)
			if err != nil || !p.Contains(a) {
				continue
			}
			if dev := ifaces[n.CIDR]; dev != "" {
				want[entry{a.String(), dev}] = true
			}
		}
	}
	for e := range want {
		if !have[e] {
			if err := sh("ip", "neigh", "replace", "proxy", e.addr, "dev", e.dev); err != nil {
				return err
			}
		}
	}
	for e := range have {
		if !want[e] {
			if err := sh("ip", "neigh", "del", "proxy", e.addr, "dev", e.dev); err != nil {
				return err
			}
		}
	}
	return nil
}

// Rules is the mesh's nftables table: forwarding only between a network's interface and the tunnel, both
// ends inside that network's range; TCP's MSS clamped to the path; everything else dropped.
func Rules(nets []Network, ifaces map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString("table inet es_mesh\ndelete table inet es_mesh\ntable inet es_mesh {\n")
	b.WriteString("  chain forward {\n    type filter hook forward priority 0; policy drop;\n")
	b.WriteString("    tcp flags syn tcp option maxseg size set rt mtu\n")
	b.WriteString("    ct state established,related accept\n")
	cidrs := make([]string, 0, len(nets))
	for _, n := range nets {
		cidrs = append(cidrs, n.CIDR)
	}
	sort.Strings(cidrs)
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil || !p.Addr().Is4() || p.Masked() != p {
			return "", fmt.Errorf("%q isn't an IPv4 range", c)
		}
		dev := ifaces[c]
		if dev == "" {
			continue // not on this network yet
		}
		if !validIfName(dev) {
			return "", fmt.Errorf("%q isn't an interface name", dev)
		}
		fmt.Fprintf(&b, "    iifname %q oifname %q ip saddr %s ip daddr %s accept\n", dev, Interface, p, p)
		fmt.Fprintf(&b, "    iifname %q oifname %q ip saddr %s ip daddr %s accept\n", Interface, dev, p, p)
	}
	b.WriteString("  }\n}\n")
	return b.String(), nil
}

func validIfName(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// status reads the public key and the peers' last handshakes.
func status() (Status, error) {
	_, pub, err := key()
	if err != nil {
		return Status{}, err
	}
	st := Status{PublicKey: pub, Handshakes: map[string]int64{}}
	if _, err := net.InterfaceByName(Interface); err != nil {
		return st, nil
	}
	out, err := exec.Command("wg", "show", Interface, "latest-handshakes").Output()
	if err != nil {
		return st, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			st.Handshakes[f[0]] = n
		}
	}
	return st, nil
}

// standIn is a stand-in's main process (ADR-045): it drops its own copy of the remote address, so it never
// answers for it, points the address at the mesh, and forwards whatever reaches it there.
func standIn(address, prefixLen, meshIP, meshMAC string) error {
	addr, err := netip.ParseAddr(address)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("%q isn't an IPv4 address", address)
	}
	bits, err := strconv.Atoi(prefixLen)
	if err != nil || bits < 8 || bits > 30 {
		return fmt.Errorf("%q isn't a prefix length", prefixLen)
	}
	mesh, err := netip.ParseAddr(meshIP)
	if err != nil || !mesh.Is4() {
		return fmt.Errorf("%q isn't the mesh's address", meshIP)
	}
	if _, err := net.ParseMAC(meshMAC); err != nil {
		return fmt.Errorf("%q isn't a MAC address", meshMAC)
	}
	dev, err := deviceWith(addr)
	if err != nil {
		return err
	}
	prefix := netip.PrefixFrom(addr, bits).Masked()
	steps := [][]string{
		{"ip", "addr", "del", addr.String() + "/" + strconv.Itoa(bits), "dev", dev},
		{"ip", "route", "replace", prefix.String(), "dev", dev},
		{"ip", "neigh", "replace", mesh.String(), "lladdr", meshMAC, "dev", dev, "nud", "permanent"},
		{"ip", "route", "replace", addr.String() + "/32", "via", mesh.String(), "dev", dev, "onlink"},
	}
	for _, s := range steps {
		if err := sh(s[0], s[1:]...); err != nil {
			return err
		}
	}
	return wait()
}

// deviceWith finds the interface holding addr, waiting a little for the engine to finish setting it up.
func deviceWith(addr netip.Addr) (string, error) {
	for i := 0; i < 50; i++ {
		ifs, _ := net.Interfaces()
		for _, ifc := range ifs {
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok {
					if ip, ok := netip.AddrFromSlice(ipn.IP.To4()); ok && ip == addr {
						return ifc.Name, nil
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("no interface holds %s", addr)
}

func sh(name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, mode)
}
