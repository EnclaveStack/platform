package vmlink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The link helper's pure parts (L5-002 §8): frames, checksum repair, rules, the build context.

func TestFramesRoundTripAndRefuseOversized(t *testing.T) {
	var wire []byte
	for _, f := range [][]byte{[]byte("one"), bytes.Repeat([]byte{7}, 1514), {}} {
		wire = AppendFrame(wire, f)
	}
	r := bytes.NewReader(wire)
	buf := make([]byte, MaxFrame)
	for _, want := range []int{3, 1514, 0} {
		f, err := ReadFrame(r, buf)
		if err != nil || len(f) != want {
			t.Fatalf("frame %d bytes, %v; want %d", len(f), err, want)
		}
	}
	big := binary.BigEndian.AppendUint32(nil, MaxFrame+1)
	if _, err := ReadFrame(bytes.NewReader(big), buf); !errors.Is(err, ErrOversized) {
		t.Errorf("oversized frame: %v", err)
	}
}

// ipv4TCP builds an Ethernet + IPv4 + TCP frame with the given payload and checksum field.
func ipv4TCP(payload []byte, sum uint16) []byte {
	f := make([]byte, 14+20+20+len(payload))
	f[12], f[13] = 0x08, 0x00
	ip := f[14:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+20+len(payload)))
	ip[8], ip[9] = 64, 6
	copy(ip[12:16], []byte{1, 1, 1, 1})
	copy(ip[16:20], []byte{198, 18, 0, 2})
	tcp := ip[20:]
	binary.BigEndian.PutUint16(tcp[0:2], 80)
	binary.BigEndian.PutUint16(tcp[2:4], 47344)
	tcp[12], tcp[13] = 0x50, 0x12 // header length 5, SYN+ACK
	binary.BigEndian.PutUint16(tcp[16:18], sum)
	copy(tcp[20:], payload)
	return f
}

// referenceSum is RFC 1071 over the pseudo-header and segment, written independently of FixChecksum.
func referenceSum(f []byte) uint16 {
	ip := f[14:]
	seg := append([]byte(nil), ip[20:]...)
	seg[16], seg[17] = 0, 0
	pseudo := append(append(append([]byte{}, ip[12:20]...), 0, 6), byte(len(seg)>>8), byte(len(seg)))
	data := append(pseudo, seg...)
	if len(data)%2 == 1 {
		data = append(data, 0)
	}
	var s uint32
	for i := 0; i < len(data); i += 2 {
		s += uint32(data[i])<<8 | uint32(data[i+1])
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

func TestFixChecksumRepairsUnfilledTCP(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("hello"), []byte("odd")} {
		f := ipv4TCP(payload, 0xdead) // what Docker Desktop delivers: never filled in
		want := referenceSum(f)
		FixChecksum(f)
		if got := binary.BigEndian.Uint16(f[14+20+16:]); got != want {
			t.Errorf("payload %q: checksum %#04x, want %#04x", payload, got, want)
		}
	}
	frag := ipv4TCP([]byte("x"), 0xdead)
	frag[14+6] = 0x20 // more fragments
	FixChecksum(frag)
	if binary.BigEndian.Uint16(frag[14+20+16:]) != 0xdead {
		t.Error("a fragment was changed")
	}
	arp := make([]byte, 60)
	arp[12], arp[13] = 0x08, 0x06
	before := append([]byte(nil), arp...)
	FixChecksum(arp)
	if !bytes.Equal(arp, before) {
		t.Error("a non-IPv4 frame was changed")
	}
}

func TestRules(t *testing.T) {
	r := Rules(Config{Data: "eth0", Mgmt: "eth1", Gateways: []netip.Addr{netip.MustParseAddr("10.40.0.1"), netip.MustParseAddr("172.31.0.1")}, MgmtPorts: []int{80, 22, 80}})
	for _, want := range []string{
		`iifname "eth0" tcp dport 61998 return`, `iifname "eth0" dnat to 198.18.0.2`,
		`iifname "eth1" tcp dport { 22, 80 } dnat to 198.18.0.2`, `oifname "eth0" ip saddr 198.18.0.2 masquerade`,
		`tcp dport 61998 ip saddr != { 10.40.0.1, 172.31.0.1 } drop`, `iifname "vm0" oifname "eth1" drop`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("rules lack %q:\n%s", want, r)
		}
	}
	single := Rules(Config{Data: "eth0", Gateways: []netip.Addr{netip.MustParseAddr("10.40.0.1")}})
	if strings.Contains(single, "eth1") || strings.Contains(single, `oifname ""`) {
		t.Errorf("no management network, yet:\n%s", single)
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
	for _, f := range []string{"dns.go", "frame.go", "link.go", "rules.go"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if !strings.HasPrefix(string(b), "package main\n") {
			t.Errorf("%s isn't package main", f)
		}
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "es-vmlink"), ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the build context doesn't compile: %v\n%s", err, out)
	}
	if tag, again := ImageTag(), ImageTag(); tag != again || !strings.HasPrefix(ImageTag(), "enclave.local/vmlink:") {
		t.Errorf("tag %s", ImageTag())
	}
}
