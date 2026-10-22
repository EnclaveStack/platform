// Package acme obtains certificates with the ACME DNS-01 challenge through EnclaveStack's DNS provider
// plug-ins (ADR-024). It uses lego's core packages only; challenge records go through internal/dns, so
// provider credentials never leave the control plane. It imports nothing else of the control plane.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/registration"

	"github.com/enclavestack/platform/internal/dns"
)

func init() { legolog.Logger = log.New(io.Discard, "", 0) } // errors come back as values

// LetsEncrypt is the production directory.
const LetsEncrypt = lego.LEDirectoryProduction

// OrderTimeout bounds one order, the propagation wait included (L3-007 §9).
const OrderTimeout = 10 * time.Minute

// Account is an ACME account: its key and, once registered, its URL.
type Account struct {
	Email  string `json:"email"`
	KeyPEM []byte `json:"key"`
	URI    string `json:"uri"`
}

// Request is one certificate for one hostname (a wildcard or an exact name).
type Request struct {
	Hostname string
	Account  Account
	Provider dns.Provider
	// Owner marks the challenge records as EnclaveStack's (the domain's ID).
	Owner string
}

// Certificate is what an order produced.
type Certificate struct {
	ChainPEM  []byte
	KeyPEM    []byte
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
}

// Issuer obtains certificates. Obtain returns the account too, registered if it wasn't.
type Issuer interface {
	Obtain(ctx context.Context, req Request) (Certificate, Account, error)
}

// Lego issues through an ACME directory.
type Lego struct {
	Directory string
	// RootPEM is trusted for the directory's HTTPS (tests use Pebble's).
	RootPEM []byte
	// SkipPropagationCheck trusts that a TXT record is visible once the provider accepted it (tests);
	// otherwise lego waits until the zone's authoritative name servers serve it.
	SkipPropagationCheck bool
}

type user struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *user) GetEmail() string                        { return u.email }
func (u *user) GetRegistration() *registration.Resource { return u.reg }
func (u *user) GetPrivateKey() crypto.PrivateKey        { return u.key }

// Obtain implements Issuer.
func (l *Lego) Obtain(ctx context.Context, req Request) (Certificate, Account, error) {
	acct := req.Account
	key, err := accountKey(&acct)
	if err != nil {
		return Certificate{}, acct, err
	}
	u := &user{email: acct.Email, key: key}
	if acct.URI != "" {
		u.reg = &registration.Resource{URI: acct.URI}
	}
	cfg := lego.NewConfig(u)
	cfg.CADirURL = l.Directory
	cfg.Certificate.KeyType = "P256"
	if len(l.RootPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(l.RootPEM) {
			return Certificate{}, acct, errors.New("the ACME root certificate isn't PEM")
		}
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}

	type result struct {
		cert Certificate
		acct Account
		err  error
	}
	octx, cancel := context.WithTimeout(ctx, OrderTimeout)
	defer cancel()
	done := make(chan result, 1)
	go func() {
		// lego takes no context; the order is bounded by its own timeouts and abandoned here on ours.
		client, err := lego.NewClient(cfg)
		if err != nil {
			done <- result{err: fmt.Errorf("reach the ACME directory: %w", err)}
			return
		}
		if u.reg == nil {
			reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
			if err != nil {
				done <- result{err: fmt.Errorf("register the ACME account: %w", err)}
				return
			}
			u.reg, acct.URI = reg, reg.URI
		}
		opts := []dns01.ChallengeOption{}
		if l.SkipPropagationCheck {
			opts = append(opts, dns01.WrapPreCheck(func(string, string, string, dns01.PreCheckFunc) (bool, error) { return true, nil }))
		}
		if err := client.Challenge.SetDNS01Provider(&challengeProvider{ctx: octx, p: req.Provider, owner: req.Owner}, opts...); err != nil {
			done <- result{err: err}
			return
		}
		res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: []string{req.Hostname}, Bundle: true})
		if err != nil {
			done <- result{err: fmt.Errorf("obtain a certificate for %s: %w", req.Hostname, err)}
			return
		}
		cert, err := describe(res.Certificate, res.PrivateKey)
		done <- result{cert: cert, acct: acct, err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return Certificate{}, acct, r.err
		}
		return r.cert, r.acct, nil
	case <-octx.Done():
		return Certificate{}, acct, fmt.Errorf("the certificate order for %s didn't finish within %s", req.Hostname, OrderTimeout)
	}
}

// accountKey decodes the account's key, creating one (P-256) for a new account.
func accountKey(a *Account) (crypto.PrivateKey, error) {
	if len(a.KeyPEM) == 0 {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		a.KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		return k, nil
	}
	b, _ := pem.Decode(a.KeyPEM)
	if b == nil {
		return nil, errors.New("the ACME account key isn't PEM")
	}
	return x509.ParseECPrivateKey(b.Bytes)
}

// describe reads a chain's leaf for its issuer and validity.
func describe(chain, key []byte) (Certificate, error) {
	b, _ := pem.Decode(chain)
	if b == nil {
		return Certificate{}, errors.New("the certificate isn't PEM")
	}
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return Certificate{}, err
	}
	issuer := leaf.Issuer.CommonName
	if issuer == "" && len(leaf.Issuer.Organization) > 0 {
		issuer = leaf.Issuer.Organization[0]
	}
	return Certificate{ChainPEM: chain, KeyPEM: key, Issuer: issuer, NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC()}, nil
}

// ChallengeName is the TXT record a DNS-01 challenge for hostname uses (a wildcard shares its base's).
func ChallengeName(hostname string) string {
	return "_acme-challenge." + strings.TrimPrefix(strings.TrimSuffix(hostname, "."), "*.")
}

// challengeProvider solves DNS-01 through a EnclaveStack DNS provider (lego's challenge.Provider).
type challengeProvider struct {
	ctx   context.Context
	p     dns.Provider
	owner string
}

// keyAuthDigest is the TXT value: base64url(SHA-256(key authorisation)), RFC 8555 §8.4.
func keyAuthDigest(keyAuth string) string {
	sum := sha256.Sum256([]byte(keyAuth))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *challengeProvider) Present(domain, _, keyAuth string) error {
	return c.p.Set(c.ctx, dns.Record{FQDN: ChallengeName(domain), Type: "TXT", Values: []string{keyAuthDigest(keyAuth)}, TTL: 60}, c.owner)
}

// CleanUp runs whatever the outcome, so nothing stays at the provider (CRT-04).
func (c *challengeProvider) CleanUp(domain, _, _ string) error {
	return c.p.Delete(context.WithoutCancel(c.ctx), ChallengeName(domain), "TXT")
}

// Timeout is how long lego waits for the record to propagate, and how often it looks.
func (c *challengeProvider) Timeout() (time.Duration, time.Duration) {
	return 5 * time.Minute, 5 * time.Second
}
