package reconciler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Domains and the edge proxy (ADR-015, L2-005 DOM-01 – DOM-09) through the real API on the fake engine.

func apiCode(err error) api.ErrorCode {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestDomainRoutesToTheAppsAliasWithAnInternalCertificate(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	if _, err := c.RootCA(ctx); apiCode(err) != api.CodeNotFound {
		t.Errorf("CA before any internal domain: %v", err)
	}
	app, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	waitReadyWithin(t, c, app.ID, 10*time.Second)

	dom, err := c.CreateResource(ctx, api.KindDomain, "web-local", api.DomainSpec{Hostname: "Web.Localhost.", App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, dom.ID, 10*time.Second)
	var spec api.DomainSpec
	if err := api.DecodeStrict(ready.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Hostname != "web.localhost" || spec.TLS != api.TLSInternal || spec.RedirectHTTP == nil || !*spec.RedirectHTTP {
		t.Errorf("normalized spec %+v", spec)
	}
	if ready.Outputs["url"] != "https://web.localhost" || ready.Outputs["certificateState"] != "issued" || ready.Outputs["certificateExpiresAt"] == nil {
		t.Errorf("outputs %v", ready.Outputs)
	}
	routes := env.Fake.EdgeRoutes()
	if len(routes) != 1 || routes[0].Upstream != "es-app-web:80" || routes[0].TLS != api.TLSInternal || !routes[0].RedirectHTTP {
		t.Fatalf("edge routes %+v", routes)
	}
	if pem, err := c.RootCA(ctx); err != nil || !strings.Contains(string(pem), "BEGIN CERTIFICATE") {
		t.Errorf("root CA: %v %q", err, pem)
	}

	// An app deploy doesn't touch the edge (upstream is the alias), but a port change re-renders it,
	// before the app reports ready: a slow edge reload must not leave ready pointing at the old port.
	env.Fake.SetEdgeDelay(time.Second)
	upd, err := c.UpdateResource(ctx, app.ID, map[string]any{"port": 8080}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, upd.ID, 10*time.Second)
	if r := env.Fake.EdgeRoutes(); len(r) != 1 || r[0].Upstream != "es-app-web:8080" {
		t.Errorf("app port change not applied to the edge: %+v", r)
	}
	env.Fake.SetEdgeDelay(0)

	// Rules: unique hostnames, the app stays while a domain uses it, its port too, the hostname is fixed.
	if _, err := c.CreateResource(ctx, api.KindDomain, "web-dup", api.DomainSpec{Hostname: "web.localhost", App: "web"}); apiCode(err) != api.CodeHostnameTaken {
		t.Errorf("duplicate hostname: %v", err)
	}
	if _, err := c.DeleteResource(ctx, app.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting an app a domain uses: %v", err)
	}
	if _, err := c.UpdateResource(ctx, app.ID, map[string]any{"port": nil}, nil); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("removing the port a domain routes to: %v", err)
	}
	if _, err := c.UpdateResource(ctx, dom.ID, map[string]any{"hostname": "other.localhost"}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("changing the hostname: %v", err)
	}
	for name, spec := range map[string]api.DomainSpec{
		"no-app":    {Hostname: "x.localhost", App: "missing"},
		"ip":        {Hostname: "10.0.0.1", App: "web"},
		"wildcard":  {Hostname: "*.example.com", App: "web"},
		"one-label": {Hostname: "intranet", App: "web"},
		"acme-on-l": {Hostname: "y.localhost", App: "web", TLS: api.TLSAuto},
	} {
		if _, err := c.CreateResource(ctx, api.KindDomain, "bad-"+name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}

	// Deleting the last domain empties the edge configuration.
	if _, err := c.DeleteResource(ctx, dom.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return len(env.Fake.EdgeRoutes()) == 0 })
	m, err := c.Machine(ctx)
	if err != nil || m.Edge.Available {
		t.Errorf("edge should be idle with no domains: %+v %v", m.Edge, err)
	}
}

func TestPublicDomainNeedsAnOpenEdgeThenWaitsForDNS(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	app, _ := c.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx", Port: 80})
	waitReadyWithin(t, c, app.ID, 10*time.Second)

	// The edge listens on loopback by default: a public certificate can't work, and the message says why.
	dom, _ := c.CreateResource(ctx, api.KindDomain, "api-public", api.DomainSpec{Hostname: "api.example.com", App: "api"})
	failed := waitFailed(t, c, dom.ID)
	if !strings.Contains(failed.Status.Message, "--edge-address 0.0.0.0") {
		t.Errorf("loopback edge message: %q", failed.Status.Message)
	}

	// Opened up, but DNS doesn't point here yet: the domain keeps retrying with an actionable message.
	env.Fake.SetEdgeAddress("0.0.0.0")
	env.Fake.SetCertificatePending("api.example.com", true)
	addr := "203.0.113.7"
	padded := "  " + addr + " " // stray spaces are trimmed, not rejected
	if _, err := c.UpdateMachine(ctx, api.MachineSettings{PublicAddress: &padded}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		r, err := c.GetResource(ctx, dom.ID)
		return err == nil && r.Status.Phase == api.PhaseProvisioning && strings.Contains(r.Status.Message, "Point its A (or AAAA) record to 203.0.113.7")
	})
	env.Fake.SetCertificatePending("api.example.com", false)
	ready := waitReadyWithin(t, c, dom.ID, 20*time.Second)
	if ready.Outputs["certificateIssuer"] != "Fake ACME CA" || ready.Outputs["publicAddress"] != addr || ready.Outputs["url"] != "https://api.example.com" {
		t.Errorf("outputs %v", ready.Outputs)
	}
	m, _ := c.Machine(ctx)
	if m.PublicAddress == nil || *m.PublicAddress != addr || !m.Edge.Available {
		t.Errorf("machine %+v", m)
	}
	empty := ""
	if m, err := c.UpdateMachine(ctx, api.MachineSettings{PublicAddress: &empty}); err != nil || m.PublicAddress != nil {
		t.Errorf("clearing the public address: %+v %v", m.PublicAddress, err)
	}
	bad := "not an address"
	if _, err := c.UpdateMachine(ctx, api.MachineSettings{PublicAddress: &bad}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("invalid public address: %v", err)
	}
}

func TestEdgePortConflictIsReportedAndDriftIsRepaired(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	app, _ := c.CreateResource(ctx, api.KindApp, "site", api.AppSpec{Image: "nginx", Port: 80})
	waitReadyWithin(t, c, app.ID, 10*time.Second)

	msg := "Port 80 or 443 is already in use on this machine, so the edge proxy can't start. Free it, then retry."
	env.Fake.SetEdgeFailure(msg)
	dom, _ := c.CreateResource(ctx, api.KindDomain, "site", api.DomainSpec{Hostname: "site.localhost", App: "site", TLS: api.TLSOff})
	failed := waitFailed(t, c, dom.ID)
	if failed.Status.Message != msg {
		t.Errorf("message %q", failed.Status.Message)
	}
	if m, _ := c.Machine(ctx); m.Edge.Available || m.Edge.Message != msg {
		t.Errorf("machine edge %+v", m.Edge)
	}
	env.Fake.SetEdgeFailure("")
	if _, err := c.Action(ctx, dom.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, dom.ID, 10*time.Second)
	if ready.Outputs["url"] != "http://site.localhost" || ready.Outputs["certificateState"] != "none" {
		t.Errorf("tls off outputs %v", ready.Outputs)
	}

	env.Fake.StopEdge()
	waitFor(t, 10*time.Second, func() bool {
		ev := eventTypes(t, c, dom.ID)
		r, err := c.GetResource(ctx, dom.ID)
		return ev["drift_detected"] > 0 && err == nil && r.Status.Phase == api.PhaseReady
	})
	if m, _ := c.Machine(ctx); !m.Edge.Available {
		t.Errorf("edge not restarted after drift: %+v", m.Edge)
	}
}

func waitFailed(t *testing.T, c *client.Client, id string) api.Resource {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.WaitReady(ctx, id, 20*time.Millisecond, nil)
	var fe *client.FailedError
	if !asFailed(err, &fe) {
		t.Fatalf("want failed, got %v (%+v)", err, r.Status)
	}
	return r
}

// Internal certificates live 12 hours. A fixed "7 days before expiry" rule sent every internal domain
// back to provisioning on each drift pass (found in the live check); the rule is relative to the
// certificate's own lifetime.
func TestCertificateRefreshIsRelativeToTheCertificatesLifetime(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 50 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	app, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80})
	waitReadyWithin(t, c, app.ID, 10*time.Second)
	dom, _ := c.CreateResource(ctx, api.KindDomain, "web-local", api.DomainSpec{Hostname: "web.localhost", App: "web"})
	waitReadyWithin(t, c, dom.ID, 10*time.Second)

	time.Sleep(500 * time.Millisecond) // ~10 drift passes
	if ev := eventTypes(t, c, dom.ID); ev["certificate_renewal"] != 0 {
		t.Fatalf("a fresh 12-hour certificate was treated as due for renewal: %v", ev)
	}

	// A certificate with 30 minutes of 12 hours left means Caddy's renewal didn't happen.
	env.Fake.SetCertificateExpiry("web.localhost", time.Now().Add(30*time.Minute))
	waitFor(t, 10*time.Second, func() bool { return eventTypes(t, c, dom.ID)["certificate_renewal"] > 0 })
	env.Fake.SetCertificateExpiry("web.localhost", time.Time{})
	waitReadyWithin(t, c, dom.ID, 10*time.Second)
}
