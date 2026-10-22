package reconciler_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Certificates through DNS (ADR-024, L2-006 CRT-01 – CRT-05) through the real API on the fake engine,
// with the local issuer and the in-memory DNS provider.

func TestWildcardDomainGetsItsCertificateThroughDNS(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	if _, err := c.CreateResource(ctx, api.KindSecret, "cf-token", api.SecretSpec{Value: `{"apiToken":"t"}`}); err != nil {
		t.Fatal(err)
	}
	z, _ := c.CreateResource(ctx, api.KindZone, "example", api.ZoneSpec{Provider: api.ProviderCloudflare, Zone: "example.com", CredentialsSecret: "cf-token"})
	waitReadyWithin(t, c, z.ID, 10*time.Second)
	app, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	waitReadyWithin(t, c, app.ID, 10*time.Second)

	// The edge listens on loopback (the default): DNS-01 needs no inbound port (CRT-01).
	dom, err := c.CreateResource(ctx, api.KindDomain, "wild", api.DomainSpec{Hostname: "*.example.com", App: "web", Zone: "example"})
	if err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, dom.ID, 10*time.Second)
	if ready.Outputs["certificateState"] != "issued" || ready.Outputs["certificateIssuer"] != "EnclaveStack Local DNS-01 CA" || ready.Outputs["url"] != "https://*.example.com" {
		t.Errorf("domain outputs %v", ready.Outputs)
	}
	if env.ACME.Orders() != 1 || !slices.Equal(env.ACME.Challenges(), []string{"_acme-challenge.example.com"}) {
		t.Errorf("orders %d, challenges %v", env.ACME.Orders(), env.ACME.Challenges())
	}
	if env.DNS.Lookup("_acme-challenge.example.com", "TXT") != nil {
		t.Error("the challenge record was left at the provider (CRT-04)")
	}
	routes := env.Fake.EdgeRoutes()
	if len(routes) != 1 || !routes[0].DNS01 || routes[0].Hostname != "*.example.com" {
		t.Errorf("routes %+v", routes)
	}
	if certs := env.Fake.EdgeCertificates(); len(certs) != 1 || !strings.Contains(string(certs[0].KeyPEM), "PRIVATE KEY") {
		t.Errorf("edge certificates %+v", certs)
	}
	stored, err := env.Store.GetCertificate(ctx, "*.example.com")
	if err != nil || strings.Contains(string(stored.KeyCiphertext), "PRIVATE KEY") || stored.DomainID != dom.ID {
		t.Fatalf("stored certificate: %v (key must be encrypted, CRT-05)", err)
	}
	if hasEvent(t, c, dom.ID, "certificate_issued") == false {
		t.Error("no certificate_issued event")
	}

	// Renewal: a certificate with a third of its lifetime left is replaced; the old one served meanwhile.
	env.ACME.SetLifetime(3 * time.Second)
	if _, err := c.Action(ctx, dom.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	// The reconcile finds a valid certificate and orders nothing; a short-lived one forces renewals.
	_ = env.Store.DeleteCertificate(ctx, "*.example.com")
	waitFor(t, 10*time.Second, func() bool { return env.ACME.Orders() >= 2 })
	waitFor(t, 15*time.Second, func() bool { return env.ACME.Orders() >= 3 && hasEvent(t, c, dom.ID, "certificate_renewed") })

	// Rules (CRT-01, CRT-02).
	for name, spec := range map[string]api.DomainSpec{
		"outside": {Hostname: "api.other.org", App: "web", Zone: "example"},
		"no-zone": {Hostname: "*.example.org", App: "web"},
		"no-such": {Hostname: "api.example.com", App: "web", Zone: "nope"},
	} {
		if _, err := c.CreateResource(ctx, api.KindDomain, "bad-"+name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c.DeleteResource(ctx, z.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a zone a domain uses: %v", err)
	}

	// Deleting the domain removes its certificate and its route.
	if _, err := c.DeleteResource(ctx, dom.ID, nil); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.WaitGone(gone, dom.ID, 50*time.Millisecond); err != nil {
		t.Fatalf("the domain was never deleted: %v", err)
	}
	if _, err := env.Store.GetCertificate(ctx, "*.example.com"); err == nil {
		t.Error("certificate kept after the domain was deleted")
	}
}

func TestFailedDNSOrderRetriesWithTheReason(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	_, _ = c.CreateResource(ctx, api.KindSecret, "cf-token", api.SecretSpec{Value: `{"apiToken":"t"}`})
	z, _ := c.CreateResource(ctx, api.KindZone, "example", api.ZoneSpec{Provider: api.ProviderCloudflare, Zone: "example.com", CredentialsSecret: "cf-token"})
	waitReadyWithin(t, c, z.ID, 10*time.Second)
	app, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	waitReadyWithin(t, c, app.ID, 10*time.Second)
	env.ACME.SetFail(errors.New("the CA said: incorrect TXT record"))
	dom, _ := c.CreateResource(ctx, api.KindDomain, "api", api.DomainSpec{Hostname: "api.example.com", App: "web", Zone: "example"})
	waitFor(t, 10*time.Second, func() bool {
		r, _ := c.GetResource(ctx, dom.ID)
		return r.Status.Attempts >= 1 && strings.Contains(r.Status.Message, "incorrect TXT record")
	})
	if env.DNS.Lookup("_acme-challenge.api.example.com", "TXT") != nil {
		t.Error("a failed order left its challenge record (CRT-04)")
	}
	env.ACME.SetFail(nil)
	if _, err := c.Action(ctx, dom.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	if r := waitReadyWithin(t, c, dom.ID, 10*time.Second); r.Outputs["certificateState"] != "issued" {
		t.Errorf("after the CA recovered: %v", r.Outputs)
	}
}
