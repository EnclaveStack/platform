package vmlink

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// The link container's own rules (ADR-026), in table ip hcvm: traffic for the container goes to the VM,
// the VM's traffic leaves as the container, the frame port only answers the network gateway, and nothing
// the VM sends leaves through the management network, which only a network without internet has: there it
// would be the VM's way out (VMN-04). Firewalls are separate: table inet enclave (ADR-021).

// Link addresses: the same pair in every link container (each is its own namespace), from the
// benchmarking range no network may use.
const (
	Gateway   = "198.18.0.1"
	VMAddress = "198.18.0.2"
	Prefix    = 30
	Device    = "vm0"
	// DeviceMAC is the link device's fixed address: a recreated link container looks the same to the VM,
	// whose neighbour cache would otherwise keep the old one for minutes (found live, ADR-026).
	DeviceMAC = "02:68:63:00:00:01"
	FramePort = 61998
	Table     = "hcvm"
)

// Config is what the driver tells the helper, through the environment.
type Config struct {
	// Data is the interface on the VM's network; Mgmt the one on es-vmlink, "" when there is none.
	Data, Mgmt string
	// Gateways may open the frame port: where published-port traffic arrives from.
	Gateways []netip.Addr
	// MgmtPorts are the VM ports published through the management network (SSH, publish).
	MgmtPorts []int
}

// Rules renders table ip hcvm. Values are interface names the helper read from the kernel and
// validated addresses and ports, never free text.
func Rules(c Config) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("add table ip %s\ndelete table ip %s\n", Table, Table)
	w("table ip %s {\n", Table)
	w("\tchain pre {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	w("\t\tiifname %q tcp dport %d return\n", c.Data, FramePort)
	w("\t\tiifname %q dnat to %s\n", c.Data, VMAddress)
	if c.Mgmt != "" && len(c.MgmtPorts) > 0 {
		w("\t\tiifname %q tcp dport %s dnat to %s\n", c.Mgmt, ports(c.MgmtPorts), VMAddress)
	}
	w("\t}\n")
	w("\tchain post {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	w("\t\toifname %q ip saddr %s masquerade\n", c.Data, VMAddress)
	w("\t}\n")
	w("\tchain frame_guard {\n\t\ttype filter hook input priority -10; policy accept;\n")
	if len(c.Gateways) > 0 {
		gws := make([]string, len(c.Gateways))
		for i, g := range c.Gateways {
			gws[i] = g.String()
		}
		slices.Sort(gws)
		w("\t\ttcp dport %d ip saddr != { %s } drop\n", FramePort, strings.Join(slices.Compact(gws), ", "))
	} else {
		w("\t\ttcp dport %d drop\n", FramePort)
	}
	w("\t}\n")
	w("\tchain vm_forward {\n\t\ttype filter hook forward priority -10; policy accept;\n")
	w("\t\tct state established,related accept\n")
	if c.Mgmt != "" {
		w("\t\tiifname %q oifname %q drop\n", Device, c.Mgmt)
	}
	w("\t}\n}\n")
	return b.String()
}

func ports(ps []int) string {
	s := slices.Clone(ps)
	slices.Sort(s)
	s = slices.Compact(s)
	out := make([]string, len(s))
	for i, p := range s {
		out[i] = strconv.Itoa(p)
	}
	return "{ " + strings.Join(out, ", ") + " }"
}
