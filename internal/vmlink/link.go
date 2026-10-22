package vmlink

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Main runs the link (ADR-026): it creates the tap device the VM's frames come and go through, loads the
// link's rules, forwards DNS, and moves frames between QEMU's connection and the tap. The driver
// configures it through the environment:
//
//	ES_DATA_CIDR      the VM's network range; the interface with an address in it is the data side
//	ES_GATEWAYS       addresses that may open the frame port (where published traffic comes from)
//	ES_MGMT_PORTS     VM ports published through es-vmlink (SSH and publish), when it is attached
func Main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg, err := configure()
	if err != nil {
		log.Fatalf("vmlink: %v", err)
	}
	tap, err := openTap(Device)
	if err != nil {
		log.Fatalf("vmlink: %v", err)
	}
	for _, args := range [][]string{{"link", "set", Device, "address", DeviceMAC}, {"addr", "add", fmt.Sprintf("%s/%d", Gateway, Prefix), "dev", Device}, {"link", "set", Device, "up"}} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			log.Fatalf("vmlink: ip %v: %v %s", args, err, out)
		}
	}
	nft := exec.Command("nft", "-f", "-")
	nft.Stdin = strings.NewReader(Rules(cfg))
	if out, err := nft.CombinedOutput(); err != nil {
		log.Fatalf("vmlink: load rules: %v %s", err, out)
	}
	if err := ForwardDNS(net.JoinHostPort(Gateway, "53"), "127.0.0.11:53"); err != nil {
		log.Fatalf("vmlink: dns: %v", err)
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(FramePort))
	if err != nil {
		log.Fatalf("vmlink: %v", err)
	}
	log.Printf("vmlink: ready (data %s, management %q)", cfg.Data, cfg.Mgmt)
	serve(ln, tap)
}

// configure reads the environment and finds the data and management interfaces.
func configure() (Config, error) {
	var c Config
	data, err := netip.ParsePrefix(os.Getenv("ES_DATA_CIDR"))
	if err != nil {
		return c, fmt.Errorf("ES_DATA_CIDR: %w", err)
	}
	for _, g := range strings.Split(os.Getenv("ES_GATEWAYS"), ",") {
		if a, err := netip.ParseAddr(strings.TrimSpace(g)); err == nil {
			c.Gateways = append(c.Gateways, a)
		}
	}
	for _, p := range strings.Split(os.Getenv("ES_MGMT_PORTS"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 && n < 65536 {
			c.MgmtPorts = append(c.MgmtPorts, n)
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return c, err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Name == Device {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			p, err := netip.ParsePrefix(a.String())
			if err != nil || !p.Addr().Is4() {
				continue
			}
			if data.Contains(p.Addr()) {
				c.Data = ifc.Name
			} else {
				c.Mgmt = ifc.Name
			}
		}
	}
	if c.Data == "" {
		return c, fmt.Errorf("no interface has an address in %s", data)
	}
	return c, nil
}

// openTap creates a tap device without packet information headers.
func openTap(name string) (*os.File, error) {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	var req [40]byte
	copy(req[:16], name)
	*(*uint16)(unsafe.Pointer(&req[16])) = 0x0002 | 0x1000                                                           // IFF_TAP | IFF_NO_PI
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0x400454ca, uintptr(unsafe.Pointer(&req[0]))); e != 0 { // TUNSETIFF
		f.Close()
		return nil, fmt.Errorf("create tap %s: %w", name, e)
	}
	return f, nil
}

// serve moves frames: QEMU reconnects every second, and a new connection replaces the old one.
func serve(ln net.Listener, tap io.ReadWriter) {
	var mu sync.Mutex
	var cur net.Conn
	go func() {
		buf := make([]byte, MaxFrame)
		out := make([]byte, 0, MaxFrame+4)
		for {
			n, err := tap.Read(buf)
			if err != nil {
				log.Fatalf("vmlink: read tap: %v", err)
			}
			FixChecksum(buf[:n])
			mu.Lock()
			c := cur
			mu.Unlock()
			if c != nil {
				_, _ = c.Write(AppendFrame(out[:0], buf[:n]))
			}
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatalf("vmlink: accept: %v", err)
		}
		log.Printf("vmlink: QEMU connected from %s", c.RemoteAddr())
		mu.Lock()
		if cur != nil {
			cur.Close()
		}
		cur = c
		mu.Unlock()
		go func(c net.Conn) {
			buf := make([]byte, MaxFrame)
			for {
				f, err := ReadFrame(c, buf)
				if err != nil {
					log.Printf("vmlink: QEMU disconnected: %v", err)
					c.Close()
					return
				}
				_, _ = tap.Write(f)
			}
		}(c)
	}
}
