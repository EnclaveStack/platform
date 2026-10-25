package vmlink

import (
	"io"
	"log"
	"net"
	"time"
)

// DNS forwarding (ADR-026): the VM's resolver is the link's gateway address, and the helper forwards to
// Docker's embedded resolver, which answers .internal names in the container's networks.

// ForwardDNS serves UDP and TCP DNS on listen and forwards each query to upstream. It returns when
// either listener fails to start.
func ForwardDNS(listen, upstream string) error {
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		pc.Close()
		return err
	}
	go serveUDP(pc, upstream)
	go serveTCP(ln, upstream)
	return nil
}

func serveUDP(pc net.PacketConn, upstream string) {
	buf := make([]byte, 65535)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			log.Printf("dns: %v", err)
			return
		}
		q := append([]byte(nil), buf[:n]...)
		go func() {
			up, err := net.DialTimeout("udp", upstream, 2*time.Second)
			if err != nil {
				return
			}
			defer up.Close()
			_ = up.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := up.Write(q); err != nil {
				return
			}
			r := make([]byte, 65535)
			m, err := up.Read(r)
			if err == nil {
				_, _ = pc.WriteTo(r[:m], from)
			}
		}()
	}
}

func serveTCP(ln net.Listener, upstream string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("dns: %v", err)
			return
		}
		go func() {
			defer c.Close()
			up, err := net.DialTimeout("tcp", upstream, 2*time.Second)
			if err != nil {
				return
			}
			defer up.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			_ = up.SetDeadline(time.Now().Add(30 * time.Second))
			go func() { _, _ = io.Copy(up, c) }()
			_, _ = io.Copy(c, up)
		}()
	}
}
