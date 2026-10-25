package cloudflare

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The Cloudflare client against the fake API (TUN-02, TUN-09, TUN-10, GATE-09).

func TestTunnelLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake("tok", "acct")
	defer f.Close()
	c := New(f.URL(), Credentials{APIToken: "tok", AccountID: "acct"})

	tun, err := c.EnsureTunnel(ctx, "enclave-default-home")
	if err != nil || tun.ID == "" {
		t.Fatalf("create: %+v %v", tun, err)
	}
	again, err := c.EnsureTunnel(ctx, "enclave-default-home")
	if err != nil || again.ID != tun.ID || len(f.Tunnels()) != 1 {
		t.Fatalf("a second ensure must adopt, not duplicate: %+v %v %v", again, err, f.Tunnels())
	}
	token, err := c.Token(ctx, tun.ID)
	if err != nil || token == "" {
		t.Fatalf("token %q %v", token, err)
	}
	if err := c.SetIngress(ctx, tun.ID, []string{"stage.example.com", "dev.example.com", "dev.example.com"}, "http://es-system-edge:11000"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Ingress(ctx, tun.ID)
	want := IngressFor([]string{"dev.example.com", "stage.example.com"}, "http://es-system-edge:11000")
	if err != nil || !SameIngress(got, want) || got[len(got)-1].Service != "http_status:404" || got[0].Hostname != "dev.example.com" {
		t.Fatalf("ingress %+v %v", got, err)
	}
	if err := c.DeleteTunnel(ctx, tun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetTunnel(ctx, tun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted tunnel still there: %v", err)
	}
	if err := c.DeleteTunnel(ctx, tun.ID); err != nil {
		t.Fatalf("deleting twice is not an error: %v", err)
	}
}

func TestErrorsNameWhatToFix(t *testing.T) {
	ctx := context.Background()
	f := NewFake("tok", "acct")
	defer f.Close()
	f.Allow("access")
	_, err := New(f.URL(), Credentials{APIToken: "tok", AccountID: "acct"}).EnsureTunnel(ctx, "x")
	if !errors.Is(err, ErrPermission) || !Permanent(err) || !strings.Contains(err.Error(), "Cloudflare Tunnel › Edit") {
		t.Fatalf("a token without tunnel permission must say which permission: %v", err)
	}
	_, err = New(f.URL(), Credentials{APIToken: "wrong", AccountID: "acct"}).EnsureTunnel(ctx, "x")
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("a wrong token: %v", err)
	}
	f.Allow("tunnel", "access", "zone")
	f.FailNext(1)
	_, err = New(f.URL(), Credentials{APIToken: "tok", AccountID: "acct"}).EnsureTunnel(ctx, "x")
	if err == nil || Permanent(err) {
		t.Fatalf("an outage is retried: %v", err)
	}
}

func TestAccessApplications(t *testing.T) {
	ctx := context.Background()
	f := NewFake("tok", "acct")
	defer f.Close()
	c := New(f.URL(), Credentials{APIToken: "tok", AccountID: "acct"})
	id, err := c.SetAccessApp(ctx, AccessApp{Domain: "stage.example.com", Emails: []string{"a@example.com"}, EmailDomains: []string{"example.com"}})
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	app, err := c.GetAccessApp(ctx, id)
	if err != nil || !SameAccess(app, []string{"A@example.com"}, []string{"example.com"}) {
		t.Fatalf("read back %+v %v", app, err)
	}
	if id2, err := c.SetAccessApp(ctx, AccessApp{ID: id, Domain: "stage.example.com", Emails: []string{"b@example.com"}}); err != nil || id2 != id {
		t.Fatalf("update keeps the ID: %s %v", id2, err)
	}
	if app, _ := c.GetAccessApp(ctx, id); !SameAccess(app, []string{"b@example.com"}, nil) {
		t.Fatalf("update not applied: %+v", app)
	}
	if err := c.DeleteAccessApp(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAccessApp(ctx, id); err != nil {
		t.Fatalf("deleting twice is not an error: %v", err)
	}
	// An application deleted at Cloudflare is made again by the next set.
	if id3, err := c.SetAccessApp(ctx, AccessApp{ID: id, Domain: "stage.example.com", Emails: []string{"b@example.com"}}); err != nil || id3 == id {
		t.Fatalf("re-create: %s %v", id3, err)
	}
}

func TestZoneAccount(t *testing.T) {
	ctx := context.Background()
	f := NewFake("tok", "acct")
	defer f.Close()
	f.AddZone("example.com", "acct")
	c := New(f.URL(), Credentials{APIToken: "tok", AccountID: "acct"})
	if a, err := c.ZoneAccount(ctx, "example.com"); err != nil || a != "acct" {
		t.Fatalf("%q %v", a, err)
	}
	f.Allow("tunnel")
	if a, err := c.ZoneAccount(ctx, "example.com"); err != nil || a != "" {
		t.Fatalf("without zone read the check is skipped: %q %v", a, err)
	}
}
