package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/internal/tunnel/cloudflare"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Public access through the real API and reconciler on the fake engine, with an in-memory Cloudflare
// and DNS (L2-014, L5-002 §11).

func publicEnv(t *testing.T) (*testenv.Env, *client.Client) {
	t.Helper()
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) {
		c.TunnelConnectWait, c.TunnelCheck, c.TunnelDrift, c.TunnelRestart = 3*time.Second, time.Hour, time.Hour, 200*time.Millisecond
	}))
	c, _ := env.Token(t, api.ScopeWrite)
	if _, err := c.CreateResource(context.Background(), api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80}); err != nil {
		t.Fatal(err)
	}
	return env, c
}

func listenerOf(env *testenv.Env, tunnelID string) (engine.Listener, bool) {
	for _, l := range env.Fake.EdgeListeners() {
		if l.TunnelID == tunnelID {
			return l, true
		}
	}
	return engine.Listener{}, false
}

func TestQuickLink(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	q, err := c.CreateResource(ctx, api.KindTunnel, "demo", api.TunnelSpec{Provider: api.TunnelQuick, App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, q.ID, 15*time.Second)
	url, _ := ready.Outputs["url"].(string)
	if !strings.HasPrefix(url, "https://fake-") || !strings.HasSuffix(url, ".trycloudflare.com") || ready.Outputs["connection"] != "connected" {
		t.Fatalf("quick link outputs %v", ready.Outputs)
	}
	l, ok := listenerOf(env, q.ID)
	if !ok || l.Kind != engine.ListenerAppLink || l.Network != engine.TunnelNetwork || l.Port < 11000 || l.Trusted == "" || l.ClientIPHeader != "Cf-Connecting-IP" {
		t.Fatalf("listener %+v", l)
	}
	if len(l.Routes) != 1 || l.Routes[0].Hostname != "" || l.Routes[0].Upstream != "es-app-web:80" || l.Routes[0].Target != q.ID || l.Routes[0].Gate.Mode != api.GateSignin {
		t.Fatalf("an app link has one route to its app, behind the sign-in gate: %+v", l.Routes)
	}
	if env.Fake.EdgeGate().EdgeKey == "" || !strings.HasPrefix(env.Fake.EdgeGate().Upstream, engine.GateHost+":") {
		t.Fatalf("gate config %+v", env.Fake.EdgeGate())
	}
	conns := env.Fake.Connectors()
	if c, ok := conns[engine.ConnectorName(q.ID)]; !ok || c.Network != engine.TunnelNetwork || len(c.Publish) != 0 {
		t.Fatalf("connector %+v", conns)
	}

	// A restart gives a new address; the next look shows it, with an event (TUN-04).
	env.Fake.RestartConnector(engine.ConnectorName(q.ID))
	env.Reconciler().CheckTunnels(ctx)
	again, _ := c.GetResource(ctx, q.ID)
	if again.Outputs["url"] == url || !strings.Contains(again.Outputs["url"].(string), "trycloudflare.com") {
		t.Fatalf("a restarted quick link must show its new address: %v → %v", url, again.Outputs["url"])
	}
	events, _ := c.Events(ctx, q.ID, 20)
	if !slices.ContainsFunc(events, func(e api.Event) bool { return e.Type == "address_changed" }) {
		t.Errorf("no address_changed event: %+v", events)
	}

	// The app can't go while a link reaches it (TUN-08); a domain can't use a link (L3-002 §16.5).
	if _, err := c.DeleteResource(ctx, mustFind(t, c, api.KindApp, "web").ID, nil); !isCode(err, api.CodeResourceInUse) {
		t.Fatalf("deleting the app a link reaches: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindDomain, "stage", api.DomainSpec{Hostname: "stage.example.com", App: "web", Tunnel: "demo"}); !isField(err, "spec.tunnel") {
		t.Fatalf("a domain through a quick link: %v", err)
	}

	// Deleting removes the connector and the listener.
	if _, err := c.DeleteResource(ctx, q.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "quick link gone", func() bool {
		_, err := c.GetResource(ctx, q.ID)
		_, inEdge := listenerOf(env, q.ID)
		_, running := env.Fake.Connectors()[engine.ConnectorName(q.ID)]
		return isCode(err, api.CodeNotFound) && !inEdge && !running
	})
}

func TestConnectorDownIsRestarted(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	q, err := c.CreateResource(ctx, api.KindTunnel, "demo", api.TunnelSpec{Provider: api.TunnelQuick, App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, q.ID, 15*time.Second)
	name := engine.ConnectorName(q.ID)
	env.Fake.SetConnectorFaults(name, fake.ConnectorFaults{Down: true})
	env.Reconciler().CheckTunnels(ctx)
	down, _ := c.GetResource(ctx, q.ID)
	if down.Outputs["connection"] != "reconnecting" {
		t.Fatalf("a connector without a connection shows reconnecting: %v", down.Outputs)
	}
	time.Sleep(250 * time.Millisecond)
	env.Reconciler().CheckTunnels(ctx) // still down after the restart threshold: restart it
	env.Fake.SetConnectorFaults(name, fake.ConnectorFaults{})
	testenv.Eventually(t, 15*time.Second, "restarted and connected", func() bool {
		r, _ := c.GetResource(ctx, q.ID)
		return r.Status.Phase == api.PhaseReady && r.Outputs["connection"] == "connected"
	})
	events, _ := c.Events(ctx, q.ID, 30)
	for _, want := range []string{"disconnected", "restarting"} {
		if !slices.ContainsFunc(events, func(e api.Event) bool { return e.Type == want }) {
			t.Errorf("no %s event: %+v", want, events)
		}
	}
}

func TestCloudflareTunnelAndPublicDomain(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	env.DNS.AddZone("galiaraa.com")
	env.Cloudflare.AddZone("galiaraa.com", "0123456789abcdef0123456789abcdef")
	for name, value := range map[string]string{"cf-token": `{"apiToken":"t"}`} {
		if _, err := c.CreateResource(ctx, api.KindSecret, name, api.SecretSpec{Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	z, err := c.CreateResource(ctx, api.KindZone, "galiaraa", api.ZoneSpec{Provider: api.ProviderCloudflare, Zone: "galiaraa.com", CredentialsSecret: "cf-token"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, z.ID, 10*time.Second)
	tun, err := c.CreateResource(ctx, api.KindTunnel, "home", api.TunnelSpec{Provider: api.TunnelCloudflare, CredentialsSecret: "cf-token", AccountID: "0123456789abcdef0123456789abcdef"}) // one {"apiToken"} secret serves the zone and the tunnel
	if err != nil {
		t.Fatal(err)
	}
	tr := waitReadyWithin(t, c, tun.ID, 15*time.Second)
	id, _ := tr.Outputs["tunnelId"].(string)
	if id == "" || env.Cloudflare.Tunnels()["enclave-default-home"].ID != id || tr.Outputs["connections"] == nil {
		t.Fatalf("tunnel at Cloudflare: %v %v", tr.Outputs, env.Cloudflare.Tunnels())
	}
	if c := env.Fake.Connectors()[engine.ConnectorName(tun.ID)]; c.Env["TUNNEL_TOKEN"] != "fake-run-token-"+id {
		t.Fatalf("the connector runs with the tunnel's token: %+v", c)
	}

	// A Cloudflare tunnel needs the zone at Cloudflare (PUB-02).
	if _, err := c.CreateResource(ctx, api.KindDomain, "stage", api.DomainSpec{Hostname: "stage.galiaraa.com", App: "web", Tunnel: "home"}); !isField(err, "spec.zone") {
		t.Fatalf("no zone: %v", err)
	}
	d, err := c.CreateResource(ctx, api.KindDomain, "stage", api.DomainSpec{Hostname: "stage.galiaraa.com", App: "web", Tunnel: "home", Zone: "galiaraa",
		CloudflareAccess: &api.CloudflareAccess{Emails: []string{"sunny@example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	dr := waitReadyWithin(t, c, d.ID, 15*time.Second)
	if dr.Outputs["public"] != true || dr.Outputs["url"] != "https://stage.galiaraa.com" || dr.Outputs["certificateState"] != "edge" || dr.Outputs["certificateIssuer"] != "Cloudflare" {
		t.Fatalf("domain outputs %v", dr.Outputs)
	}
	rec := env.DNS.Lookup("stage.galiaraa.com", "CNAME")
	if rec == nil || rec.Values[0] != cloudflare.TunnelTarget(id) || !rec.Proxied || rec.Owner != d.ID {
		t.Fatalf("CNAME at the provider: %+v", rec)
	}
	if got := env.Cloudflare.IngressOf(id); len(got) != 2 || got[0].Hostname != "stage.galiaraa.com" || got[1].Service != "http_status:404" {
		t.Fatalf("routing rules %+v", got)
	}
	if app, ok := env.Cloudflare.AccessApps()["stage.galiaraa.com"]; !ok || !slices.Contains(app.Emails, "sunny@example.com") {
		t.Fatalf("Access application %+v", env.Cloudflare.AccessApps())
	}
	l, _ := listenerOf(env, tun.ID)
	if l.Kind != engine.ListenerCloudflare || len(l.Routes) != 1 || l.Routes[0].Hostname != "stage.galiaraa.com" || l.Routes[0].Gate.Mode != api.GateSignin || l.Routes[0].Target != d.ID {
		t.Fatalf("listener %+v", l)
	}
	for _, r := range env.Fake.EdgeRoutes() {
		if r.Hostname == "stage.galiaraa.com" {
			t.Fatal("a public domain must not be served on the machine's own ports (PUB-01)")
		}
	}

	// The tunnel can't go while the domain uses it (TUN-08).
	if _, err := c.DeleteResource(ctx, tun.ID, nil); !isCode(err, api.CodeResourceInUse) {
		t.Fatalf("deleting a used tunnel: %v", err)
	}

	// Making it local again removes the record, the routing rule and the Access application (PUB-06).
	if _, err := c.UpdateResource(ctx, d.ID, map[string]any{"tunnel": "", "zone": "", "cloudflareAccess": nil, "tls": "internal"}, nil); err != nil {
		t.Fatal(err)
	}
	local := waitReadyWithin(t, c, d.ID, 15*time.Second)
	if local.Outputs["public"] != nil || env.DNS.Lookup("stage.galiaraa.com", "CNAME") != nil || len(env.Cloudflare.AccessApps()) != 0 {
		t.Fatalf("leftovers after going local: %v %+v %+v", local.Outputs, env.DNS.Lookup("stage.galiaraa.com", "CNAME"), env.Cloudflare.AccessApps())
	}
	if got := env.Cloudflare.IngressOf(id); len(got) != 1 {
		t.Fatalf("the routing rule should be gone: %+v", got)
	}
	if _, err := c.DeleteResource(ctx, d.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, d.ID, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteResource(ctx, tun.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "tunnel deleted at Cloudflare", func() bool { return len(env.Cloudflare.Tunnels()) == 0 })
}

func TestCloudflareErrorsAndDrift(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	if _, err := c.CreateResource(ctx, api.KindSecret, "cf-tunnel", api.SecretSpec{Value: `{"token":"t"}`}); err != nil {
		t.Fatal(err)
	}
	bad, err := c.CreateResource(ctx, api.KindTunnel, "home", api.TunnelSpec{Provider: api.TunnelCloudflare, CredentialsSecret: "cf-tunnel", AccountID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if f := waitFailed(t, c, bad.ID); !strings.Contains(f.Status.Message, `"apiToken"`) {
		t.Fatalf("a JSON secret without the token must say so: %q", f.Status.Message)
	}
	env.Cloudflare.Allow("access", "zone")
	if _, err := c.UpdateResource(ctx, mustFind(t, c, api.KindSecret, "cf-tunnel").ID, api.SecretSpec{Value: "plain-token"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Action(ctx, bad.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	if f := waitFailed(t, c, bad.ID); !strings.Contains(f.Status.Message, "Cloudflare Tunnel › Edit") {
		t.Fatalf("a token without tunnel permission must name it (TUN-09): %q", f.Status.Message)
	}
	env.Cloudflare.Allow("tunnel", "access", "zone")
	if _, err := c.Action(ctx, bad.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, bad.ID, 15*time.Second)
	id := ready.Outputs["tunnelId"].(string)

	// Deleted at Cloudflare: the next drift check fails the tunnel, and Retry makes it again (TUN-10).
	env.Cloudflare.DeleteOutside(id)
	env.Reconciler().TunnelDriftEvery = time.Nanosecond
	env.Reconciler().CheckTunnels(ctx) // first look only remembers
	env.Reconciler().CheckTunnels(ctx)
	if f := waitFailed(t, c, bad.ID); !strings.Contains(f.Status.Message, "deleted at Cloudflare") {
		t.Fatalf("%q", f.Status.Message)
	}
	if _, err := c.Action(ctx, bad.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	again := waitReadyWithin(t, c, bad.ID, 15*time.Second)
	if again.Outputs["tunnelId"] == id {
		t.Fatal("Retry must make a new tunnel")
	}
}

func TestTailscaleNgrokAndCustomLinks(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	for name, value := range map[string]string{"ts-key": "tskey-auth-x", "ngrok": `{"authToken":"tok"}`, "x-token": "abc"} {
		if _, err := c.CreateResource(ctx, api.KindSecret, name, api.SecretSpec{Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := c.CreateResource(ctx, api.KindTunnel, "stage-ts", api.TunnelSpec{Provider: api.TunnelTailscale, CredentialsSecret: "ts-key", App: "web", Mode: api.TailscalePublic})
	if err != nil {
		t.Fatal(err)
	}
	tr := waitReadyWithin(t, c, ts.ID, 15*time.Second)
	if tr.Outputs["url"] != "https://default-web."+fake.TailscaleTailnet || tr.Outputs["funnel"] != true || tr.Outputs["device"] != "default-web" {
		t.Fatalf("tailscale outputs %v", tr.Outputs)
	}
	if l, _ := listenerOf(env, ts.ID); l.ClientIPHeader != "X-Forwarded-For" || l.Routes[0].Gate.Mode != api.GateSignin {
		t.Fatalf("a public Tailscale link is gated: %+v", l)
	}
	// A tailnet without Funnel for the device: the link says what to switch on (TUN-09).
	env.Fake.SetConnectorFaults(engine.ConnectorName(ts.ID), fake.ConnectorFaults{NoFunnel: true})
	if _, err := c.Action(ctx, ts.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	if f := waitFailed(t, c, ts.ID); !strings.Contains(f.Status.Message, "funnel attribute") {
		t.Fatalf("%q", f.Status.Message)
	}
	// Private needs no Funnel, and its gate is open to the tailnet by default (GATE-01).
	if _, err := c.UpdateResource(ctx, ts.ID, map[string]any{"mode": "private"}, nil); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, ts.ID, 15*time.Second)
	if l, _ := listenerOf(env, ts.ID); l.Routes[0].Gate.Mode != api.GateNone {
		t.Fatalf("private link gate %+v", l.Routes[0].Gate)
	}
	// Deleting logs the device out of the tailnet (TUN-12).
	if _, err := c.DeleteResource(ctx, ts.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, ts.ID, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	ng, err := c.CreateResource(ctx, api.KindTunnel, "stage-ngrok", api.TunnelSpec{Provider: api.TunnelNgrok, CredentialsSecret: "ngrok", App: "web", Domain: "stage.ngrok.app"})
	if err != nil {
		t.Fatal(err)
	}
	if nr := waitReadyWithin(t, c, ng.ID, 15*time.Second); nr.Outputs["url"] != "https://stage.ngrok.app" {
		t.Fatalf("ngrok outputs %v", nr.Outputs)
	}

	cu, err := c.CreateResource(ctx, api.KindTunnel, "stage-x", api.TunnelSpec{Provider: api.TunnelCustom, App: "web", Image: "ghcr.io/acme/tunnel:1",
		Command: []string{"run", "--to", "$ENCLAVE_UPSTREAM"}, SecretEnv: map[string]string{"TOKEN": "x-token"}, URL: "https://stage.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	cr := waitReadyWithin(t, c, cu.ID, 15*time.Second)
	conn := env.Fake.Connectors()[engine.ConnectorName(cu.ID)]
	if cr.Outputs["url"] != "https://stage.example.net" || conn.Env["TOKEN"] != "abc" || !strings.HasPrefix(conn.Env["ENCLAVE_UPSTREAM"], "http://es-system-edge:") {
		t.Fatalf("custom: %v %+v", cr.Outputs, conn.Env)
	}
	// Without an address the connector gives none: no empty url output.
	bare, err := c.CreateResource(ctx, api.KindTunnel, "stage-y", api.TunnelSpec{Provider: api.TunnelCustom, App: "web", Image: "ghcr.io/acme/tunnel:1"})
	if err != nil {
		t.Fatal(err)
	}
	if br := waitReadyWithin(t, c, bare.ID, 15*time.Second); br.Outputs["url"] != nil {
		t.Errorf("custom without an address: %v", br.Outputs)
	}
	// Secrets a connector uses can't be deleted while it does (CP-12).
	if _, err := c.DeleteResource(ctx, mustFind(t, c, api.KindSecret, "x-token").ID, nil); !isCode(err, api.CodeResourceInUse) {
		t.Fatalf("deleting a custom connector's secret: %v", err)
	}
}

func TestGate(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	q, err := c.CreateResource(ctx, api.KindTunnel, "demo", api.TunnelSpec{Provider: api.TunnelQuick, App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, q.ID, 15*time.Second)
	host := "demo.example.test"
	g := newGateClient(t, env, q.ID, host)

	// No key: the gate answers nothing (ADR-053).
	if resp := g.do(t, http.MethodGet, "/check", "", nil, false); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a request without the edge key: %d", resp.StatusCode)
	}
	// No session: browsers go to the sign-in page, keeping where they were going; JSON clients get 401.
	resp := g.do(t, http.MethodGet, "/check", "", map[string]string{"X-Forwarded-Uri": "/orders?id=7"}, true)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/.enclave/gate/signin?next=%2Forders%3Fid%3D7" {
		t.Fatalf("check without a session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := g.do(t, http.MethodGet, "/check", "", map[string]string{"Accept": "application/json"}, true); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("JSON check: %d", resp.StatusCode)
	}
	page := g.do(t, http.MethodGet, "/.enclave/gate/signin?next=/orders", "", nil, true)
	body := readBody(page)
	if page.StatusCode != 200 || !strings.Contains(body, host) || !strings.Contains(page.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") || page.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("sign-in page %d %q", page.StatusCode, body)
	}

	// A form from elsewhere is refused (CSRF); a wrong password isn't let in.
	if r := g.signin(t, testenv.AdminEmail, testenv.AdminPassword, "/", "https://evil.example"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin sign-in: %d", r.StatusCode)
	}
	if r := g.signin(t, testenv.AdminEmail, "nope-nope-nope", "/", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	// The right password: a cookie for this host only, then through, with who it is (GATE-02, GATE-03, GATE-07).
	ok := g.signin(t, testenv.AdminEmail, testenv.AdminPassword, "//evil.example/x", "")
	if ok.StatusCode != http.StatusSeeOther || ok.Header.Get("Location") != "/" {
		t.Fatalf("sign-in must redirect only to a path on this host: %d %q", ok.StatusCode, ok.Header.Get("Location"))
	}
	ck := ok.Cookies()
	if len(ck) != 1 || ck[0].Name != "__Host-enclave_gate" || !ck[0].Secure || !ck[0].HttpOnly || ck[0].SameSite != http.SameSiteLaxMode || ck[0].Path != "/" {
		t.Fatalf("gate cookie %+v", ck)
	}
	cookie := ck[0].Value
	if r := g.do(t, http.MethodGet, "/check", cookie, nil, true); r.StatusCode != http.StatusNoContent || r.Header.Get("X-Enclave-User") != testenv.AdminEmail {
		t.Fatalf("check with the session: %d %q", r.StatusCode, r.Header.Get("X-Enclave-User"))
	}
	other := newGateClient(t, env, q.ID, "other.example.test")
	if r := other.do(t, http.MethodGet, "/check", cookie, nil, true); r.StatusCode != http.StatusFound {
		t.Fatalf("a cookie for another host must not pass: %d", r.StatusCode)
	}
	// The gate cookie never opens the console's API.
	req, _ := http.NewRequest(http.MethodGet, env.URL+"/api/v1/session", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-enclave_gate", Value: cookie})
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the API with a gate cookie: %v %v", r, err)
	}
	// Signing out ends it.
	if r := g.do(t, http.MethodPost, "/.enclave/gate/signout", cookie, map[string]string{"Origin": "https://" + host}, true); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign out: %d", r.StatusCode)
	}
	if r := g.do(t, http.MethodGet, "/check", cookie, nil, true); r.StatusCode != http.StatusFound {
		t.Fatalf("after sign-out: %d", r.StatusCode)
	}
	// Ten failures from one address, then a wait (GATE-08); all in the audit log.
	for i := 0; i < 10; i++ {
		g.signin(t, testenv.AdminEmail, "wrong-password-x", "/", "")
	}
	if r := g.signin(t, testenv.AdminEmail, testenv.AdminPassword, "/", ""); r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Fatalf("after ten failures: %d", r.StatusCode)
	}
	audit, err := c.Audit(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var signins, failures int
	for _, e := range audit.Items {
		if e.Action == "gate.signin" && e.Target == host {
			signins++
		}
		if e.Action == "gate.signin_failed" {
			failures++
		}
	}
	if signins != 1 || failures < 11 {
		t.Fatalf("audit: %d sign-ins, %d failures", signins, failures)
	}
}

func TestGateOnlyLetsProjectMembersIn(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	if _, err := c.CreateProject(ctx, api.CreateProject{Name: "galiaraa-stage"}); err != nil {
		t.Fatal(err)
	}
	stage := c.InProject("galiaraa-stage")
	if _, err := stage.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80}); err != nil {
		t.Fatal(err)
	}
	q, err := stage.CreateResource(ctx, api.KindTunnel, "demo", api.TunnelSpec{Provider: api.TunnelQuick, App: "web"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, q.ID, 15*time.Second)
	viewer := "viewer@example.com"
	inv, err := c.InviteUser(ctx, api.InviteUser{Email: viewer, ProjectRoles: map[string]string{"default": api.RoleViewer}})
	if err != nil {
		t.Fatal(err)
	}
	u0, _ := url.Parse(inv.URL)
	if _, err := client.New(env.URL, "").AcceptInvite(ctx, u0.Query().Get("code"), "viewer-password-1"); err != nil {
		t.Fatal(err)
	}
	g := newGateClient(t, env, q.ID, "demo.example.test")
	r := g.signin(t, viewer, "viewer-password-1", "/", "")
	if r.StatusCode != http.StatusForbidden || !strings.Contains(readBody(r), "This environment is private.") || len(r.Cookies()) != 0 {
		t.Fatalf("someone without a role in the project: %d", r.StatusCode)
	}
	// Given a role, they pass; removed again, their session ends at the next re-check (GATE-03).
	u, err := c.FindUser(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	role := api.RoleViewer
	if _, err := c.UpdateUser(ctx, u.ID, false, nil, map[string]*string{"galiaraa-stage": &role}); err != nil {
		t.Fatal(err)
	}
	r = g.signin(t, viewer, "viewer-password-1", "/", "")
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("a viewer of the project: %d", r.StatusCode)
	}
	cookie := r.Cookies()[0].Value
	if _, err := c.UpdateUser(ctx, u.ID, false, nil, map[string]*string{"galiaraa-stage": nil}); err != nil {
		t.Fatal(err)
	}
	env.Gate().Now = func() time.Time { return time.Now().Add(time.Minute) }
	if r := g.do(t, http.MethodGet, "/check", cookie, nil, true); r.StatusCode != http.StatusFound {
		t.Fatalf("a removed member must be signed out within a minute: %d", r.StatusCode)
	}
}

func mustFind(t *testing.T, c *client.Client, kind api.Kind, name string) api.Resource {
	t.Helper()
	r, err := c.FindResource(context.Background(), kind, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func isCode(err error, code api.ErrorCode) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Code == code
}

func isField(err error, field string) bool {
	var ae *api.Error
	if !errors.As(err, &ae) {
		return false
	}
	for _, d := range ae.Details {
		if d.Field == field {
			return true
		}
	}
	return false
}

// gateClient talks to the gate listener as the edge proxy does for one host and target.
type gateClient struct {
	env          *testenv.Env
	target, host string
}

func newGateClient(t *testing.T, env *testenv.Env, target, host string) *gateClient {
	t.Helper()
	if env.GateURL == "" {
		t.Fatal("the gate isn't listening")
	}
	return &gateClient{env: env, target: target, host: host}
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (g *gateClient) do(t *testing.T, method, path, cookie string, headers map[string]string, withKey bool) *http.Response {
	t.Helper()
	return g.send(t, method, path, cookie, headers, withKey, nil)
}

func (g *gateClient) send(t *testing.T, method, path, cookie string, headers map[string]string, withKey bool, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, g.env.GateURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if withKey {
		req.Header.Set("X-Enclave-Edge-Key", g.env.GateKey)
	}
	req.Header.Set("X-Enclave-Gate-Target", g.target)
	req.Header.Set("X-Forwarded-Host", g.host)
	req.Header.Set("X-Enclave-Client-IP", "203.0.113.50")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-enclave_gate", Value: cookie})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (g *gateClient) signin(t *testing.T, email, password, next, origin string) *http.Response {
	t.Helper()
	if origin == "" {
		origin = "https://" + g.host
	}
	return g.send(t, http.MethodPost, "/.enclave/gate/signin", "", map[string]string{"Origin": origin}, true,
		url.Values{"email": {email}, "password": {password}, "next": {next}})
}

func readBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestEdgeMachineAndRelay(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	edge := env.AddEdge(t, c, "vps1")
	m, err := c.GetMachineByName(ctx, "vps1")
	if err != nil || m.Role != api.MachineEdge || m.Schedulable {
		t.Fatalf("edge machine %+v %v", m, err)
	}
	// Nothing is placed on it (EDG-03).
	if _, err := c.UpdateMachineByName(ctx, "vps1", api.UpdateMachine{Schedulable: ptr(true)}); !isField(err, "schedulable") {
		t.Fatalf("making an edge machine schedulable: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindApp, "pinned", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Placed: api.Placed{Machine: "vps1"}}); !isField(err, "spec.machine") {
		t.Fatalf("pinning an app to an edge machine: %v", err)
	}
	for i := 0; i < 3; i++ {
		a, err := c.CreateResource(ctx, api.KindApp, fmt.Sprintf("app-%d", i), api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
		if err != nil {
			t.Fatal(err)
		}
		if r := waitReadyWithin(t, c, a.ID, 15*time.Second); r.Outputs["machine"] == "vps1" {
			t.Fatalf("an app was placed on the edge machine")
		}
	}

	// A relay runs on it and passes 80/443 to this machine's relay listeners (TUN-03, EDG-04).
	relay, err := c.CreateResource(ctx, api.KindTunnel, "edge", api.TunnelSpec{Provider: api.TunnelRelay, Machine: "vps1"})
	if err != nil {
		t.Fatal(err)
	}
	rr := waitReadyWithin(t, c, relay.ID, 15*time.Second)
	if rr.Outputs["publicAddress"] != "127.0.0.1" {
		t.Fatalf("relay outputs %v", rr.Outputs)
	}
	conn, ok := edge.Driver.Connectors()[engine.RelayName(relay.ID)]
	if !ok || conn.Network != engine.NetworkName || len(conn.Publish) != 2 || !strings.Contains(string(conn.Files["haproxy.cfg"]), "send-proxy-v2") {
		t.Fatalf("the relay must run on the edge machine: %+v", edge.Driver.Connectors())
	}
	if _, onMain := env.Fake.Connectors()[engine.RelayName(relay.ID)]; onMain {
		t.Fatal("the relay must not run on the main machine")
	}
	if _, err := c.CreateResource(ctx, api.KindTunnel, "edge2", api.TunnelSpec{Provider: api.TunnelRelay, Machine: "vps1"}); !isField(err, "spec.machine") {
		t.Fatalf("a second relay on one edge machine: %v", err)
	}

	// A public domain through it without a zone: the record to add is shown, the certificate is the edge's
	// own, obtained through the relay (PUB-03).
	d, err := c.CreateResource(ctx, api.KindDomain, "stage", api.DomainSpec{Hostname: "stage.example.com", App: "web", Tunnel: "edge", Gate: &api.Gate{Mode: api.GateNone}})
	if err != nil {
		t.Fatal(err)
	}
	dr := waitReadyWithin(t, c, d.ID, 15*time.Second)
	rec, _ := dr.Outputs["dnsRecord"].(map[string]any)
	if rec["type"] != "A" || rec["value"] != "127.0.0.1" || rec["state"] != "manual" || dr.Outputs["certificateState"] != "issued" {
		t.Fatalf("relay domain outputs %v", dr.Outputs)
	}
	l, _ := listenerOf(env, relay.ID)
	if l.Kind != engine.ListenerRelay || l.Network != engine.NetworkName || l.HTTPSPort != l.Port+1 || len(l.Routes) != 1 || !l.Routes[0].TLS || l.Routes[0].Gate.Mode != api.GateNone {
		t.Fatalf("relay listener %+v", l)
	}

	// The machine can't go while the relay names it.
	if err := c.RemoveMachine(ctx, "vps1", false); !isCode(err, api.CodeMachineNotEmpty) {
		t.Fatalf("removing the relay's edge machine: %v", err)
	}
	if _, err := c.DeleteResource(ctx, d.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, d.ID, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteResource(ctx, relay.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "relay removed from the edge machine", func() bool {
		_, ok := edge.Driver.Connectors()[engine.RelayName(relay.ID)]
		return !ok
	})
}

func ptr[T any](v T) *T { return &v }

// A way in installed from the catalogue is a stack of its secret and tunnel (L2-014 TUN-15).
func TestPublicAccessFromTheCatalogue(t *testing.T) {
	_, c := publicEnv(t)
	ctx := context.Background()
	st, err := c.CreateResource(ctx, api.KindStack, "stage-ts", api.StackSpec{Template: "tailscale",
		Parameters: map[string]any{"app": "web", "mode": "public"}, SecretParameters: map[string]string{"authKey": "tskey-auth-x"}})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, st.ID, 20*time.Second)
	tun := mustFind(t, c, api.KindTunnel, "stage-ts")
	tr := waitReadyWithin(t, c, tun.ID, 15*time.Second)
	if tr.Outputs["url"] != "https://default-web."+fake.TailscaleTailnet || tr.Outputs["funnel"] != true {
		t.Fatalf("tailscale link from the catalogue: %v", tr.Outputs)
	}
	if _, err := c.FindResource(ctx, api.KindSecret, "stage-ts-authkey"); err != nil {
		t.Fatalf("the auth key secret: %v", err)
	}
	entries, err := c.Catalogue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ways int
	for _, e := range entries {
		if e.Category == api.CategoryPublicAccess {
			ways++
		}
	}
	if ways != 6 {
		t.Fatalf("the catalogue lists %d ways in", ways)
	}
}

// A relay on an edge machine that hasn't joined waits, and says how to finish joining it (EDG-02).
func TestARelayWaitsForItsEdgeMachineToJoin(t *testing.T) {
	_, c := publicEnv(t)
	ctx := context.Background()
	if _, err := c.AddMachine(ctx, api.AddMachine{Name: "vps2", Role: api.MachineEdge, Address: "203.0.113.9"}); err != nil {
		t.Fatal(err)
	}
	relay, err := c.CreateResource(ctx, api.KindTunnel, "edge", api.TunnelSpec{Provider: api.TunnelRelay, Machine: "vps2"})
	if err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "the relay says what to do", func() bool {
		r, err := c.GetResource(ctx, relay.ID)
		return err == nil && r.Status.Phase != api.PhaseReady && r.Status.Phase != api.PhaseFailed &&
			strings.Contains(r.Status.Message, "vps2 is waiting to join") && strings.Contains(r.Status.Message, "enclave machine join-code vps2")
	})
}

// An edge machine is a stranger's computer (EDG-04): in the mesh it peers only with the main machine, learns
// only the edge proxy's address, and its relay may reach only its own listeners there; the other machines
// never hear of it.
func TestAnEdgeMachineReachesOnlyItsRelayListeners(t *testing.T) {
	env, c := publicEnv(t)
	ctx := context.Background()
	worker := env.AddAgent(t, c, "rack-1", nil)
	edge := env.AddEdge(t, c, "vps1")
	relay, err := c.CreateResource(ctx, api.KindTunnel, "edge", api.TunnelSpec{Provider: api.TunnelRelay, Machine: "vps1"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, relay.ID, 15*time.Second)
	l, _ := listenerOf(env, relay.ID)
	reach := []string{fmt.Sprintf("10.89.0.2:%d", l.Port), fmt.Sprintf("10.89.0.2:%d", l.HTTPSPort)}
	slices.Sort(reach)

	peerOf := func(cfg *engine.MeshConfig, id string) *engine.MeshPeer {
		for i := range cfg.Peers {
			if cfg.Peers[i].MachineID == id {
				return &cfg.Peers[i]
			}
		}
		return nil
	}
	var why string
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(why)
		}
	})
	testenv.Eventually(t, 15*time.Second, "the edge's mesh limited to its relay", func() bool {
		if err := env.Reconciler().SyncMesh(ctx); err != nil {
			why = err.Error()
			return false
		}
		main, onEdge, onWorker := env.Fake.Mesh(), edge.Driver.Mesh(), worker.Driver.Mesh()
		if main == nil || onEdge == nil || onWorker == nil {
			why = "no mesh yet"
			return false
		}
		// The main machine: the edge peer is limited to the relay's listeners, and only the relay's address.
		p := peerOf(main, edge.ID)
		if p == nil || !p.Limited || !slices.Equal(p.Reach, reach) || len(p.Addresses) != 1 {
			why = fmt.Sprintf("main's edge peer %+v, want reach %v", p, reach)
			return false
		}
		// The edge: the main machine only, the edge proxy's address only, the default network only.
		if len(onEdge.Peers) != 1 || onEdge.Peers[0].Addresses == nil || !slices.Equal(onEdge.Peers[0].Addresses, []string{"10.89.0.2"}) ||
			len(onEdge.Networks) != 1 || len(onEdge.StandIns) != 1 || onEdge.StandIns[0].Of != engine.EdgeName {
			why = fmt.Sprintf("edge's mesh %+v", onEdge)
			return false
		}
		// The worker never peers with the edge.
		if peerOf(onWorker, edge.ID) != nil || peerOf(main, worker.ID) == nil || peerOf(main, worker.ID).Limited {
			why = fmt.Sprintf("worker's mesh %+v", onWorker)
			return false
		}
		return true
	})
}
