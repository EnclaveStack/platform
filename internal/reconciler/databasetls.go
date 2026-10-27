package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Database TLS (ADR-058, L2-016 DBX-01 – DBX-03): EnclaveStack's database CA signs each TLS database a
// certificate for a year. It is kept, so the container is only recreated when a new one is issued: when
// the database's names change, or 30 days before it ends.

const (
	databaseCertValidity = 365 * 24 * time.Hour
	databaseCertRenew    = 30 * 24 * time.Hour
	databaseCAName       = "EnclaveStack databases CA"
	databaseCAAAD        = "database_ca"
)

// databaseCA returns the database CA, making it on first use.
func (r *Reconciler) databaseCA(ctx context.Context) (*agent.CA, error) {
	stored, err := r.Store.GetDatabaseCA(ctx)
	if errors.Is(err, store.ErrNotFound) {
		certPEM, keyPEM, err := agent.NewNamedCA(r.Now(), databaseCAName)
		if err != nil {
			return nil, err
		}
		sealed, err := r.Box.Seal(keyPEM, []byte(databaseCAAAD))
		if err != nil {
			return nil, err
		}
		stored = store.DatabaseCA{CertPEM: string(certPEM), KeyCiphertext: sealed, CreatedAt: r.Now()}
		if err := r.Store.PutDatabaseCA(ctx, stored); errors.Is(err, store.ErrConflict) {
			return r.databaseCA(ctx) // made at the same time elsewhere
		} else if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	keyPEM, err := r.Box.Open(stored.KeyCiphertext, []byte(databaseCAAAD))
	if err != nil {
		return nil, fmt.Errorf("the database CA's key can't be decrypted: %w", err)
	}
	return agent.ParseCA([]byte(stored.CertPEM), keyPEM)
}

// databaseNames are the names and addresses a database's certificate covers.
func (r *Reconciler) databaseNames(ctx context.Context, res *domain.Resource, s api.DatabaseSpec) ([]string, []net.IP) {
	names := []string{engine.ContainerName(res), engine.PrivateDNS(res), "localhost"}
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	if s.Exposed {
		ips = append(ips, r.machineAddresses(ctx, res)...)
	}
	return names, ips
}

// machineAddresses are the non-loopback IPv4 addresses of the machine res runs on: this computer's
// interfaces for the main machine, the address it joined with for another (ADR-058).
func (r *Reconciler) machineAddresses(ctx context.Context, res *domain.Resource) []net.IP {
	mainID := r.Store.MainID()
	if id := res.MachineOn(mainID); id != mainID {
		m, err := r.Store.GetMachine(ctx, id)
		if err != nil || m.Address == "" {
			return nil
		}
		if ip := net.ParseIP(m.Address); ip != nil {
			return []net.IP{ip}
		}
		if addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", m.Address); err == nil {
			return addrs
		}
		return nil
	}
	return localIPv4()
}

func localIPv4() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP.To4())
		}
	}
	return out
}

func joinNames(names []string, ips []net.IP) string {
	all := slices.Clone(names)
	for _, ip := range ips {
		all = append(all, ip.String())
	}
	slices.Sort(all)
	return strings.Join(all, ",")
}

// databaseTLS returns a TLS database's files, issuing a certificate when it has none, its names changed
// or it ends within 30 days.
func (r *Reconciler) databaseTLS(ctx context.Context, res *domain.Resource, s api.DatabaseSpec) (*engine.TLSFiles, error) {
	ca, err := r.databaseCA(ctx)
	if err != nil {
		return nil, err
	}
	names, ips := r.databaseNames(ctx, res, s)
	want := joinNames(names, ips)
	cur, err := r.Store.GetDatabaseCert(ctx, res.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	now := r.Now()
	if err == nil && cur.Names == want && cur.NotAfter.Sub(now) > databaseCertRenew {
		key, err := r.Box.Open(cur.KeyCiphertext, []byte(res.ID))
		if err != nil {
			return nil, fmt.Errorf("the database's TLS key can't be decrypted: %w", err)
		}
		return &engine.TLSFiles{CertPEM: []byte(cur.CertPEM), KeyPEM: key, CAPEM: ca.PEM, Serial: cur.Serial, NotAfter: cur.NotAfter}, nil
	}
	certPEM, keyPEM, serial, notAfter, err := ca.IssueServer(engine.ContainerName(res), names, ips, now, databaseCertValidity)
	if err != nil {
		return nil, err
	}
	sealed, err := r.Box.Seal(keyPEM, []byte(res.ID))
	if err != nil {
		return nil, err
	}
	if err := r.Store.PutDatabaseCert(ctx, store.DatabaseCert{ResourceID: res.ID, CertPEM: string(certPEM), KeyCiphertext: sealed, Names: want,
		Serial: serial, NotAfter: notAfter}); err != nil {
		return nil, err
	}
	return &engine.TLSFiles{CertPEM: certPEM, KeyPEM: keyPEM, CAPEM: ca.PEM, Serial: serial, NotAfter: notAfter}, nil
}

// withDatabaseOptions adds a database's TLS and setup-script outputs (DBX-02, DBX-04).
func withDatabaseOptions(out engine.Outputs, s api.DatabaseSpec, t *engine.TLSFiles) engine.Outputs {
	if out == nil {
		out = engine.Outputs{}
	}
	if t != nil {
		out["tls"] = true
		out["caCertificate"] = string(t.CAPEM)
		out["certificateExpiresAt"] = t.NotAfter.UTC().Format(time.RFC3339)
	}
	if s.InitScript != "" {
		out["initScript"] = "ran"
	}
	return out
}

// renewDatabaseCert re-ensures a TLS database whose certificate ends within 30 days (drift pass).
func (r *Reconciler) renewDatabaseCert(ctx context.Context, res *domain.Resource) bool {
	c, err := r.Store.GetDatabaseCert(ctx, res.ID)
	if err != nil || c.NotAfter.Sub(r.Now()) > databaseCertRenew {
		return false
	}
	if err := res.SetPhase(api.PhaseUpdating, "Renewing the TLS certificate.", r.Now()); err != nil {
		return false
	}
	res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), r.Now()
	if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err != nil || !ok {
		return false
	}
	r.event(ctx, res, "certificate_renewed", fmt.Sprintf("The TLS certificate ends %s; issuing a new one.", c.NotAfter.UTC().Format(time.DateOnly)))
	r.Enqueue(res.ID)
	return true
}
