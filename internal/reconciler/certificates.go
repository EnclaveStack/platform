package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/acme"
	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Certificates through DNS (ADR-024, L2-006 CRT-01 – CRT-05): the control plane orders them through the
// domain's zone, keeps them encrypted, and the edge serves them as loaded certificates.

// dns01RenewFraction: a DNS-01 certificate renews once a third of its lifetime is left (30 days of 90).
const dns01RenewFraction = 3

// checkName is the name the edge is asked for when verifying a domain's certificate: a wildcard is
// checked with a name below it.
func checkName(hostname string) string {
	if base, ok := strings.CutPrefix(hostname, "*."); ok {
		return "es-check." + base
	}
	return hostname
}

// renewalDue reports a certificate with less than a third of its lifetime left.
func renewalDue(notBefore, notAfter, now time.Time) bool {
	life := notAfter.Sub(notBefore)
	return life <= 0 || notAfter.Sub(now) < life/dns01RenewFraction
}

// acmeAccountKey is the settings key of the ACME account for the configured directory.
func (r *Reconciler) acmeAccountKey() string {
	dir := r.ACMEDirectory
	if dir == "" {
		dir = acme.LetsEncrypt
	}
	h := sha256.Sum256([]byte(dir))
	return "acme_account:" + hex.EncodeToString(h[:8])
}

type storedAccount struct {
	Email string `json:"email"`
	Key   []byte `json:"key"` // secretbox ciphertext, the settings key as additional data
	URI   string `json:"uri"`
}

func (r *Reconciler) loadAccount(ctx context.Context) (acme.Account, error) {
	key := r.acmeAccountKey()
	raw, err := r.Store.GetSetting(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		cfg, err := r.edgeConfig(ctx)
		return acme.Account{Email: cfg.ACMEEmail}, err
	}
	if err != nil {
		return acme.Account{}, err
	}
	var sa storedAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return acme.Account{}, engine.Permanent(fmt.Errorf("the stored ACME account is unreadable: %w", err))
	}
	pemKey, err := r.Box.Open(sa.Key, []byte(key))
	if err != nil {
		return acme.Account{}, engine.Permanent(errors.New("the stored ACME account can't be decrypted with this machine's key"))
	}
	return acme.Account{Email: sa.Email, KeyPEM: pemKey, URI: sa.URI}, nil
}

func (r *Reconciler) saveAccount(ctx context.Context, a acme.Account) error {
	key := r.acmeAccountKey()
	ct, err := r.Box.Seal(a.KeyPEM, []byte(key))
	if err != nil {
		return err
	}
	b, err := json.Marshal(storedAccount{Email: a.Email, Key: ct, URI: a.URI})
	if err != nil {
		return err
	}
	return r.Store.SetSetting(ctx, key, string(b))
}

// obtainDNS01 makes sure the domain has a stored certificate that isn't due for renewal, ordering one
// through its zone when needed. Orders run one at a time: a wildcard and its base share a challenge name.
func (r *Reconciler) obtainDNS01(ctx context.Context, res *domain.Resource, spec api.DomainSpec, report engine.Reporter) error {
	cur, err := r.Store.GetCertificate(ctx, spec.Hostname)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if cur != nil && cur.DomainID == res.ID && !renewalDue(cur.NotBefore, cur.NotAfter, r.Now()) {
		return nil
	}
	if r.ACME == nil {
		return engine.Permanent(errors.New("certificates through DNS aren't configured on this server"))
	}
	p, _, err := r.zoneProvider(ctx, res.Project, spec.Zone)
	if err != nil {
		return err
	}
	r.acmeMu.Lock()
	defer r.acmeMu.Unlock()
	acct, err := r.loadAccount(ctx)
	if err != nil {
		return err
	}
	engine.Note(report, "Creating the challenge record "+acme.ChallengeName(spec.Hostname)+" and waiting for the certificate authority to check it.")
	cert, acct2, err := r.ACME.Obtain(ctx, acme.Request{Hostname: spec.Hostname, Account: acct, Provider: p, Owner: res.ID})
	r.Metrics.ACMEOrders.WithLabelValues("dns-01", result(err)).Inc()
	if acct2.URI != "" && (acct2.URI != acct.URI || string(acct2.KeyPEM) != string(acct.KeyPEM)) {
		if serr := r.saveAccount(ctx, acct2); serr != nil {
			return serr
		}
	}
	if err != nil {
		if dns.Permanent(err) {
			return engine.Permanent(err)
		}
		return fmt.Errorf("%w. The current certificate, if any, keeps serving", err)
	}
	ct, err := r.Box.Seal(cert.KeyPEM, []byte(spec.Hostname))
	if err != nil {
		return err
	}
	if err := r.Store.PutCertificate(ctx, &store.Certificate{Hostname: spec.Hostname, DomainID: res.ID, ChainPEM: string(cert.ChainPEM), KeyCiphertext: ct,
		Issuer: cert.Issuer, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter, UpdatedAt: r.Now()}); err != nil {
		return err
	}
	verb := "issued"
	if cur != nil {
		verb = "renewed"
	}
	engine.Event(report, "certificate_"+verb, fmt.Sprintf("Certificate for %s %s through DNS by %s; it expires on %s.", spec.Hostname, verb, cert.Issuer, cert.NotAfter.Format("2 Jan 2006")))
	return nil
}

// loadedCertificates decrypts the stored certificates of live DNS-01 domains for the edge.
func (r *Reconciler) loadedCertificates(ctx context.Context, live map[string]bool) ([]engine.LoadedCertificate, error) {
	certs, err := r.Store.AllCertificates(ctx)
	if err != nil {
		return nil, err
	}
	var out []engine.LoadedCertificate
	for _, c := range certs {
		if !live[c.Hostname] {
			continue
		}
		key, err := r.Box.Open(c.KeyCiphertext, []byte(c.Hostname))
		if err != nil {
			r.Log.Error("stored certificate key can't be decrypted; skipping it", "hostname", c.Hostname)
			continue
		}
		out = append(out, engine.LoadedCertificate{Hostname: c.Hostname, ChainPEM: []byte(c.ChainPEM), KeyPEM: key})
	}
	return out, nil
}

// refreshDNS01 sends a ready DNS-01 domain back to provisioning once its stored certificate is due
// (CRT-03); otherwise it keeps the outputs current.
func (r *Reconciler) refreshDNS01(ctx context.Context, res *domain.Resource, spec api.DomainSpec) {
	cur, err := r.Store.GetCertificate(ctx, spec.Hostname)
	now := r.Now()
	if err == nil && !renewalDue(cur.NotBefore, cur.NotAfter, now) {
		return
	}
	why := "it has no certificate"
	if err == nil {
		why = "its certificate expires on " + cur.NotAfter.UTC().Format("2 Jan 2006")
	}
	if err := res.SetPhase(api.PhaseProvisioning, "Renewing: "+why+".", now); err != nil {
		return
	}
	res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), now
	if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
		r.event(ctx, res, "certificate_renewal", "Renewing through DNS: "+why+".")
		r.Enqueue(res.ID)
	}
}
