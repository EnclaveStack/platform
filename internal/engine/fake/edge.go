package fake

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// fakeCA is a stand-in root certificate for the internal CA (not a real key pair).
const fakeCA = "-----BEGIN CERTIFICATE-----\nZmFrZSBob21lY2xvdWQgcm9vdA==\n-----END CERTIFICATE-----\n"

// edge is the fake edge proxy: it remembers the last configuration and serves certificates by mode.
type edge struct {
	cfg     engine.EdgeConfig
	running bool
	failure string
	pending map[string]bool
	everTLS bool
	reloads int
	delay   time.Duration
	address string
	expiry  map[string]time.Time
}

// Certificate lifetimes like the real ones: Caddy's internal CA issues 12-hour leaves, ACME 90 days.
const (
	internalCertLifetime = 12 * time.Hour
	acmeCertLifetime     = 90 * 24 * time.Hour
)

// SetCertificateExpiry makes the edge serve a certificate for hostname that expires at notAfter
// (simulates a renewal that didn't happen). The zero time clears it.
func (d *Driver) SetCertificateExpiry(hostname string, notAfter time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.edge.expiry == nil {
		d.edge.expiry = map[string]time.Time{}
	}
	if notAfter.IsZero() {
		delete(d.edge.expiry, hostname)
		return
	}
	d.edge.expiry[hostname] = notAfter
}

// SetCertificatePending makes the edge withhold a certificate for hostname (simulates DNS not pointing here).
func (d *Driver) SetCertificatePending(hostname string, pending bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.edge.pending == nil {
		d.edge.pending = map[string]bool{}
	}
	d.edge.pending[hostname] = pending
}

// SetEdgeFailure makes ConfigureEdge fail permanently with msg ("" clears it) — e.g. a port conflict.
func (d *Driver) SetEdgeFailure(msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edge.failure = msg
}

// SetEdgeDelay makes each ConfigureEdge take d before it applies (a slow edge reload).
func (d *Driver) SetEdgeDelay(delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edge.delay = delay
}

// SetEdgeAddress sets where the fake edge says it listens (default 127.0.0.1).
func (d *Driver) SetEdgeAddress(addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edge.address = addr
}

// EdgeCertificates returns the loaded certificates of the last applied configuration.
func (d *Driver) EdgeCertificates() []engine.LoadedCertificate {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]engine.LoadedCertificate(nil), d.edge.cfg.Certificates...)
}

// EdgeRoutes returns the routes of the last applied configuration.
func (d *Driver) EdgeRoutes() []engine.Route {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]engine.Route(nil), d.edge.cfg.Routes...)
}

// StopEdge simulates the edge container stopped outside EnclaveStack (drift).
func (d *Driver) StopEdge() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edge.running = false
}

// ConfigureEdge implements engine.Driver.
func (d *Driver) ConfigureEdge(ctx context.Context, cfg engine.EdgeConfig) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	delay := d.edge.delay
	d.mu.Unlock()
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.edge.failure != "" {
		d.edge.running = false
		return engine.Permanent(errors.New(d.edge.failure))
	}
	d.edge.cfg, d.edge.running = cfg, len(cfg.Routes) > 0
	d.edge.reloads++
	for _, r := range cfg.Routes {
		if r.TLS == api.TLSInternal {
			d.edge.everTLS = true
		}
	}
	return nil
}

// Certificate implements engine.Driver.
func (d *Driver) Certificate(ctx context.Context, hostname string) (engine.Certificate, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.Certificate{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.edge.running {
		return engine.Certificate{State: engine.CertPending}, nil
	}
	for _, r := range d.edge.cfg.Routes {
		if !routeMatches(r.Hostname, hostname) {
			continue
		}
		switch {
		case r.DNS01:
			// The edge serves exactly what the control plane loaded (ADR-024).
			for _, c := range d.edge.cfg.Certificates {
				if leaf := leafOf(c.ChainPEM); leaf != nil && leaf.VerifyHostname(hostname) == nil {
					issuer := leaf.Issuer.CommonName
					return engine.Certificate{State: engine.CertIssued, Issuer: issuer, NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC()}, nil
				}
			}
			return engine.Certificate{State: engine.CertPending}, nil
		case r.TLS == api.TLSOff:
			return engine.Certificate{State: engine.CertNone}, nil
		case d.edge.pending[hostname]:
			return engine.Certificate{State: engine.CertPending}, nil
		default:
			issuer, life := "Fake ACME CA", acmeCertLifetime
			if r.TLS == api.TLSInternal {
				issuer, life = "EnclaveStack Local CA (fake)", internalCertLifetime
			}
			now := time.Now().UTC()
			notBefore, notAfter := now.Add(-time.Minute), now.Add(life-time.Minute)
			if exp, ok := d.edge.expiry[hostname]; ok {
				notAfter, notBefore = exp.UTC(), exp.UTC().Add(-life)
			}
			return engine.Certificate{State: engine.CertIssued, Issuer: issuer, NotBefore: notBefore, NotAfter: notAfter}, nil
		}
	}
	return engine.Certificate{State: engine.CertPending}, nil
}

// EdgeStatus implements engine.Driver.
func (d *Driver) EdgeStatus(context.Context) engine.EdgeStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	addr := d.edge.address
	if addr == "" {
		addr = "127.0.0.1"
	}
	st := engine.EdgeStatus{Available: d.edge.running, Address: addr, HTTPPort: 80, HTTPSPort: 443}
	switch {
	case d.edge.failure != "":
		st.Message = d.edge.failure
	case !d.edge.running:
		st.Message = "The edge proxy starts with the first domain."
	}
	return st
}

// RootCA implements engine.Driver.
func (d *Driver) RootCA(context.Context) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.edge.everTLS {
		return nil, engine.ErrNoCA
	}
	return []byte(fakeCA), nil
}

// routeMatches applies Caddy's host matching: exact, or one label under a wildcard.
func routeMatches(route, hostname string) bool {
	if route == hostname {
		return true
	}
	base, ok := strings.CutPrefix(route, "*.")
	if !ok {
		return false
	}
	label, rest, found := strings.Cut(hostname, ".")
	return found && label != "" && rest == base
}

func leafOf(chain []byte) *x509.Certificate {
	b, _ := pem.Decode(chain)
	if b == nil {
		return nil
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil
	}
	return c
}
