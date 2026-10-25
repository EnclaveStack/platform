package reconciler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// DNS zones and records (ADR-023, L2-006 DNS-01 – DNS-08) through the real API on the fake engine, with
// the in-memory DNS provider.

func TestZonesAndRecordsAtTheProvider(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) {
		c.DriftEvery, c.RecordCheck = 100*time.Millisecond, 200*time.Millisecond
	}))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	env.DNS.AddZone("example.com")
	if _, err := c.CreateResource(ctx, api.KindSecret, "cf-token", api.SecretSpec{Value: `{"apiToken":"t"}`}); err != nil {
		t.Fatal(err)
	}
	z, err := c.CreateResource(ctx, api.KindZone, "example", api.ZoneSpec{Provider: api.ProviderCloudflare, Zone: "Example.com", CredentialsSecret: "cf-token"})
	if err != nil {
		t.Fatal(err)
	}
	zr := waitReadyWithin(t, c, z.ID, 10*time.Second)
	if zr.Outputs["zoneId"] != "mem-example.com" || !strings.Contains(zr.Outputs["nameServers"].(string), "ns1.") {
		t.Errorf("zone outputs %v", zr.Outputs)
	}

	// A machine record needs the public address, and follows it (DNS-02, DNS-03).
	rec, err := c.CreateResource(ctx, api.KindRecord, "api", api.RecordSpec{Zone: "example", Name: "api", Type: "A", Machine: true})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitFailed(t, c, rec.ID)
	if !strings.Contains(failed.Status.Message, "no public address") {
		t.Errorf("machine record without an address: %q", failed.Status.Message)
	}
	addr := "203.0.113.7"
	if _, err := c.UpdateMachine(ctx, api.MachineSettings{PublicAddress: &addr}); err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, rec.ID, 10*time.Second)
	if ready.Outputs["fqdn"] != "api.example.com" || ready.Outputs["values"] != addr || ready.Outputs["owned"] != true {
		t.Errorf("record outputs %v", ready.Outputs)
	}
	if got := env.DNS.Lookup("api.example.com", "A"); got == nil || got.Values[0] != addr || got.Owner != rec.ID || got.TTL != 300 {
		t.Fatalf("at the provider: %+v", got)
	}
	addr2 := "203.0.113.9"
	if _, err := c.UpdateMachine(ctx, api.MachineSettings{PublicAddress: &addr2}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got := env.DNS.Lookup("api.example.com", "A")
		return got != nil && got.Values[0] == addr2
	})

	// Adoption of an identical record; refusal of a different one (DNS-04).
	env.DNS.Put(dns.Record{FQDN: "www.example.com", Type: "CNAME", Values: []string{"api.example.com"}, TTL: 300})
	env.DNS.Put(dns.Record{FQDN: "mail.example.com", Type: "A", Values: []string{"192.0.2.1"}, TTL: 300})
	www, _ := c.CreateResource(ctx, api.KindRecord, "www", api.RecordSpec{Zone: "example", Name: "www", Type: "CNAME", Values: []string{"api.example.com"}})
	if r := waitReadyWithin(t, c, www.ID, 10*time.Second); r.Outputs["owned"] != true {
		t.Errorf("identical record not adopted: %v", r.Outputs)
	}
	mail, _ := c.CreateResource(ctx, api.KindRecord, "mail", api.RecordSpec{Zone: "example", Name: "mail", Type: "A", Values: []string{"198.51.100.4"}})
	mf := waitFailed(t, c, mail.ID)
	if !strings.Contains(mf.Status.Message, "192.0.2.1") || !strings.Contains(mf.Status.Message, "won't overwrite") {
		t.Errorf("conflict message %q", mf.Status.Message)
	}
	if got := env.DNS.Lookup("mail.example.com", "A"); got.Values[0] != "192.0.2.1" {
		t.Errorf("a record EnclaveStack didn't write was overwritten: %+v", got)
	}

	// Drift: a record changed at the provider is put back (DNS-05).
	env.DNS.Put(dns.Record{FQDN: "www.example.com", Type: "CNAME", Values: []string{"evil.example.net"}, TTL: 300})
	waitFor(t, 10*time.Second, func() bool {
		got := env.DNS.Lookup("www.example.com", "CNAME")
		return got != nil && got.Values[0] == "api.example.com" && eventTypes(t, c, www.ID)["record_repaired"] > 0
	})

	// Rules: unique name and type per zone, immutable name, zone and secret in use (DNS-07).
	if _, err := c.CreateResource(ctx, api.KindRecord, "api-again", api.RecordSpec{Zone: "example", Name: "api", Type: "A", Values: []string{"192.0.2.9"}}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("duplicate record: %v", err)
	}
	if _, err := c.UpdateResource(ctx, rec.ID, map[string]any{"name": "api2"}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("renaming a record: %v", err)
	}
	for _, id := range []string{z.ID} {
		if _, err := c.DeleteResource(ctx, id, nil); apiCode(err) != api.CodeResourceInUse {
			t.Errorf("deleting a zone in use: %v", err)
		}
	}
	sec, _ := c.FindResource(ctx, api.KindSecret, "cf-token")
	if _, err := c.DeleteResource(ctx, sec.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting the zone's credentials: %v", err)
	}

	if m := httpGet(t, env.URL+"/metrics"); !strings.Contains(m, `enclave_dns_calls_total{provider="cloudflare",result="ok"}`) {
		t.Error("DNS call metrics not exported")
	}

	// Deleting removes owned records only.
	for _, id := range []string{rec.ID, mail.ID} {
		if _, err := c.DeleteResource(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitGone(ctx, id, 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if env.DNS.Lookup("api.example.com", "A") != nil {
		t.Error("an owned record was left at the provider")
	}
	if env.DNS.Lookup("mail.example.com", "A") == nil {
		t.Error("a record EnclaveStack never owned was deleted")
	}
}

func TestZoneCredentialsAndMissingZonesAreClear(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	env.DNS.AddZone("example.com")
	if _, err := c.CreateResource(ctx, api.KindSecret, "empty", api.SecretSpec{Value: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateResource(ctx, api.KindSecret, "good", api.SecretSpec{Value: `{"apiToken":"t"}`}); err != nil {
		t.Fatal(err)
	}
	bad, _ := c.CreateResource(ctx, api.KindZone, "denied", api.ZoneSpec{Provider: api.ProviderRoute53, Zone: "example.com", CredentialsSecret: "empty"})
	if f := waitFailed(t, c, bad.ID); !strings.Contains(f.Status.Message, "permission") || f.Status.Attempts != 1 {
		t.Errorf("rejected credentials are permanent and named: %q (attempts %d)", f.Status.Message, f.Status.Attempts)
	}
	missing, _ := c.CreateResource(ctx, api.KindZone, "missing", api.ZoneSpec{Provider: api.ProviderCloudflare, Zone: "other.org", CredentialsSecret: "good"})
	if f := waitFailed(t, c, missing.ID); !strings.Contains(f.Status.Message, "doesn't exist") {
		t.Errorf("missing zone: %q", f.Status.Message)
	}
	if _, err := c.CreateResource(ctx, api.KindZone, "dup", api.ZoneSpec{Provider: api.ProviderRoute53, Zone: "example.com", CredentialsSecret: "good"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a second zone for the same provider and name: %v", err)
	}
}
