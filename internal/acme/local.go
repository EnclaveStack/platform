package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/dns"
)

// Local issues certificates from an in-process CA after writing and removing the challenge record,
// so the whole DNS-01 path runs with the fake engine (tests, demos) without a CA. Never used with the
// Docker engine.
type Local struct {
	// Lifetime of issued certificates (default 90 days, like Let's Encrypt).
	Lifetime time.Duration
	// Fail makes orders fail with this error.
	Fail error

	mu      sync.Mutex
	caKey   *ecdsa.PrivateKey
	caCert  *x509.Certificate
	orders  int
	present []string // challenge names seen
}

// Orders returns how many certificates were issued.
func (l *Local) Orders() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.orders
}

// Challenges returns the TXT names written during orders.
func (l *Local) Challenges() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.present...)
}

// SetLifetime sets the lifetime of certificates issued from now on.
func (l *Local) SetLifetime(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Lifetime = d
}

// SetFail makes orders fail with err (nil restores them).
func (l *Local) SetFail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Fail = err
}

// Obtain implements Issuer.
func (l *Local) Obtain(ctx context.Context, req Request) (Certificate, Account, error) {
	acct := req.Account
	if _, err := accountKey(&acct); err != nil {
		return Certificate{}, acct, err
	}
	if acct.URI == "" {
		acct.URI = "local:account"
	}
	name := ChallengeName(req.Hostname)
	if err := req.Provider.Set(ctx, dns.Record{FQDN: name, Type: "TXT", Values: []string{"local-challenge"}, TTL: 60}, req.Owner); err != nil {
		return Certificate{}, acct, err
	}
	defer func() { _ = req.Provider.Delete(context.WithoutCancel(ctx), name, "TXT") }()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.present = append(l.present, name)
	if l.Fail != nil {
		return Certificate{}, acct, l.Fail
	}
	if l.caKey == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return Certificate{}, acct, err
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "EnclaveStack Local DNS-01 CA"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
			KeyUsage: x509.KeyUsageCertSign}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
		if err != nil {
			return Certificate{}, acct, err
		}
		l.caKey = k
		l.caCert, _ = x509.ParseCertificate(der)
	}
	life := l.Lifetime
	if life == 0 {
		life = 90 * 24 * time.Hour
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, acct, err
	}
	l.orders++
	now := time.Now().Truncate(time.Second)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(int64(l.orders) + 1), Subject: pkix.Name{CommonName: req.Hostname}, DNSNames: []string{req.Hostname},
		NotBefore: now, NotAfter: now.Add(life), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, l.caCert, &k.PublicKey, l.caKey)
	if err != nil {
		return Certificate{}, acct, err
	}
	keyDER, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return Certificate{}, acct, err
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: l.caCert.Raw})...)
	cert, err := describe(chain, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return cert, acct, err
}
