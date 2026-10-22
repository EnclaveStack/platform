// Package firewall renders firewall policies as nftables rulesets (ADR-021). It is pure: no I/O, and
// it imports nothing of the control plane, so the rendering is tested on its own. Values come only
// from typed prefixes, protocols and port numbers, never from free text (L3-006 §6).
package firewall

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Protocols a rule can name.
const (
	TCP  = "tcp"
	UDP  = "udp"
	ICMP = "icmp"
	All  = "all"
)

// PortRange is an inclusive range of ports; From == To for one port.
type PortRange struct{ From, To int }

// Rule allows traffic with peers (sources for ingress, destinations for egress).
type Rule struct {
	Peers    []netip.Prefix
	Protocol string
	// Ports empty means every port (or no ports, for icmp and all).
	Ports []PortRange
}

// Policy is everything one target allows: the union of the firewalls that name it (FW-03).
type Policy struct {
	Ingress []Rule
	// RestrictEgress drops outgoing traffic except Egress (FW-04).
	RestrictEgress bool
	Egress         []Rule
}

// Context is what the driver knows about one container and adds to the policy (FW-05, FW-06):
// EnclaveStack's own services that always reach it, and the container ports published on the host, which
// stay reachable through the network gateway.
type Context struct {
	AlwaysFrom     []netip.Addr
	Gateway        netip.Addr
	PublishedPorts []int
	// Forward names the device a VM's traffic crosses inside its link container (ADR-026): the policy
	// then filters forwarded traffic to and from it, and the container's own input is left alone.
	Forward string
}

// Table is the nftables table EnclaveStack owns inside a container's network namespace.
const Table = "enclave"

// Render returns a ruleset that replaces EnclaveStack's table atomically in one `nft -f` transaction. The
// output is deterministic for a given input.
func Render(p Policy, c Context) string {
	if c.Forward != "" {
		return renderForward(p, c)
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	// "add" then "delete" then define: works whether or not the table exists, in one transaction.
	w("add table inet %s\ndelete table inet %s\n", Table, Table)
	w("table inet %s {\n", Table)
	w("\tchain input {\n\t\ttype filter hook input priority 0; policy drop;\n")
	w("\t\tiif \"lo\" accept\n\t\tct state established,related accept\n\t\tct state invalid drop\n")
	always := slices.Clone(c.AlwaysFrom)
	slices.SortFunc(always, func(a, b netip.Addr) int { return a.Compare(b) })
	always = slices.Compact(always)
	if len(always) > 0 {
		w("\t\tip saddr %s accept\n", set(addrs(always)))
	}
	if c.Gateway.IsValid() && len(c.PublishedPorts) > 0 {
		ports := slices.Clone(c.PublishedPorts)
		slices.Sort(ports)
		ports = slices.Compact(ports)
		pr := make([]PortRange, len(ports))
		for i, p := range ports {
			pr[i] = PortRange{p, p}
		}
		w("\t\tip saddr %s tcp dport %s accept\n", c.Gateway, portSet(pr))
	}
	for _, r := range normalize(p.Ingress) {
		w("\t\t%s\n", match(r, "saddr", "dport"))
	}
	w("\t}\n")
	policy := "accept"
	if p.RestrictEgress {
		policy = "drop"
	}
	w("\tchain output {\n\t\ttype filter hook output priority 0; policy %s;\n", policy)
	if p.RestrictEgress {
		// Loopback keeps Docker's embedded DNS (127.0.0.11) working; replies leave as established.
		w("\t\toif \"lo\" accept\n\t\tct state established,related accept\n")
		for _, r := range normalize(p.Egress) {
			w("\t\t%s\n", match(r, "daddr", "dport"))
		}
	}
	w("\t}\n}\n")
	return b.String()
}

// renderForward is Render for a VM's link container: the same rules and implicit allows, matched on
// traffic forwarded to (ingress) and from (egress) the VM's device.
func renderForward(p Policy, c Context) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	to, from := fmt.Sprintf("oifname %q ", c.Forward), fmt.Sprintf("iifname %q ", c.Forward)
	w("add table inet %s\ndelete table inet %s\n", Table, Table)
	w("table inet %s {\n", Table)
	w("\tchain forward {\n\t\ttype filter hook forward priority 0; policy accept;\n")
	w("\t\tct state established,related accept\n\t\tct state invalid drop\n")
	always := slices.Clone(c.AlwaysFrom)
	slices.SortFunc(always, func(a, b netip.Addr) int { return a.Compare(b) })
	always = slices.Compact(always)
	if len(always) > 0 {
		w("\t\t%sip saddr %s accept\n", to, set(addrs(always)))
	}
	if c.Gateway.IsValid() && len(c.PublishedPorts) > 0 {
		ports := slices.Clone(c.PublishedPorts)
		slices.Sort(ports)
		ports = slices.Compact(ports)
		pr := make([]PortRange, len(ports))
		for i, p := range ports {
			pr[i] = PortRange{p, p}
		}
		w("\t\t%sip saddr %s tcp dport %s accept\n", to, c.Gateway, portSet(pr))
	}
	for _, r := range normalize(p.Ingress) {
		w("\t\t%s%s\n", to, match(r, "saddr", "dport"))
	}
	w("\t\t%sdrop\n", to)
	if p.RestrictEgress {
		for _, r := range normalize(p.Egress) {
			w("\t\t%s%s\n", from, match(r, "daddr", "dport"))
		}
		w("\t\t%sdrop\n", from)
	}
	w("\t}\n}\n")
	return b.String()
}

// Clear returns a ruleset that removes EnclaveStack's table (a target no firewall names any more).
func Clear() string {
	return fmt.Sprintf("add table inet %s\ndelete table inet %s\n", Table, Table)
}

// Hash fingerprints a ruleset, so a driver applies it only when it changed.
func Hash(ruleset string) string {
	h := sha256.Sum256([]byte(ruleset))
	return hex.EncodeToString(h[:8])
}

func match(r Rule, addrDir, portDir string) string {
	var parts []string
	if peers := r.Peers; !(len(peers) == 1 && peers[0].Bits() == 0) {
		parts = append(parts, "ip "+addrDir+" "+set(prefixes(peers)))
	} else {
		parts = append(parts, "meta nfproto ipv4")
	}
	switch r.Protocol {
	case TCP, UDP:
		if len(r.Ports) > 0 {
			parts = append(parts, r.Protocol+" "+portDir+" "+portSet(r.Ports))
		} else {
			parts = append(parts, "meta l4proto "+r.Protocol)
		}
	case ICMP:
		parts = append(parts, "meta l4proto icmp")
	}
	return strings.Join(append(parts, "accept"), " ")
}

// normalize sorts and dedupes peers and ports and drops rules that can never match.
func normalize(rules []Rule) []Rule {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		peers := make([]netip.Prefix, 0, len(r.Peers))
		for _, p := range r.Peers {
			if p.IsValid() && p.Addr().Is4() {
				peers = append(peers, p.Masked())
			}
		}
		if len(peers) == 0 {
			continue // e.g. a resource source with no running containers
		}
		slices.SortFunc(peers, func(a, b netip.Prefix) int {
			return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
		})
		peers = slices.Compact(peers)
		if slices.ContainsFunc(peers, func(p netip.Prefix) bool { return p.Bits() == 0 }) {
			peers = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
		}
		ports := slices.Clone(r.Ports)
		slices.SortFunc(ports, func(a, b PortRange) int { return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To)) })
		ports = slices.Compact(ports)
		if r.Protocol == ICMP || r.Protocol == All {
			ports = nil
		}
		out = append(out, Rule{Peers: peers, Protocol: r.Protocol, Ports: ports})
	}
	return out
}

func set(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	return "{ " + strings.Join(items, ", ") + " }"
}

func portSet(ports []PortRange) string {
	items := make([]string, len(ports))
	for i, p := range ports {
		items[i] = fmt.Sprint(p.From)
		if p.To != p.From {
			items[i] = fmt.Sprintf("%d-%d", p.From, p.To)
		}
	}
	return set(items)
}

func prefixes(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

func addrs(as []netip.Addr) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.String()
	}
	return out
}

// BaseImage is the helper image's base: Alpine 3.20, pinned by its index digest (ADR-021).
const BaseImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

// Dockerfile builds the helper image: nftables on the pinned base, nothing else.
const Dockerfile = "FROM " + BaseImage + "\nRUN apk add --no-cache nftables\n"

// ImageTag is the helper image's local tag, derived from its Dockerfile so a change builds a new one.
func ImageTag() string {
	h := sha256.Sum256([]byte(Dockerfile))
	return "enclave.local/netfilter:" + hex.EncodeToString(h[:6])
}

// ApplyScript is the helper's fixed command: the ruleset arrives in ES_RULES, never as code.
var ApplyScript = []string{"/bin/sh", "-c", `printf '%s' "$ES_RULES" | nft -f -`}
