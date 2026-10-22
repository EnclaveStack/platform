package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// DNS zones and records (ADR-023, L2-006 DNS-01 – DNS-08). Records are reconciled against the provider
// directly; the engine isn't involved.

// DefaultRecordCheck is how often ready records are compared with the provider (DNS-05): not every
// drift pass, to stay within provider rate limits.
const DefaultRecordCheck = 10 * time.Minute

// OutputOwned records that EnclaveStack wrote a record and may change or delete it (DNS-04).
const OutputOwned = "owned"

// dnsError maps provider errors to retry behaviour: rejected credentials and refused requests are
// permanent, rate limits and outages transient (L3-007 §9).
func dnsError(err error) error {
	if err == nil {
		return nil
	}
	if dns.Permanent(err) {
		return engine.Permanent(err)
	}
	return err
}

// zoneProvider opens the provider of a zone resource with its decrypted credentials.
func (r *Reconciler) zoneProvider(ctx context.Context, project, zoneName string) (dns.Provider, api.ZoneSpec, error) {
	z, err := r.Store.GetResourceByName(ctx, project, api.KindZone, zoneName)
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.ZoneSpec{}, engine.Permanent(fmt.Errorf("the zone %s no longer exists", zoneName))
	}
	if err != nil {
		return nil, api.ZoneSpec{}, err
	}
	raw, _, err := api.NormalizeSpec(api.KindZone, z.Spec)
	if err != nil {
		return nil, api.ZoneSpec{}, engine.Permanent(fmt.Errorf("the zone %s has an unreadable spec", zoneName))
	}
	spec := raw.(api.ZoneSpec)
	creds, err := r.secretValue(ctx, project, spec.CredentialsSecret)
	if err != nil {
		return nil, spec, err
	}
	if r.DNS == nil {
		return nil, spec, engine.Permanent(errors.New("DNS providers aren't configured on this server"))
	}
	p, err := r.DNS(spec.Provider, spec.Zone, []byte(creds))
	if err != nil {
		return nil, spec, dnsError(err)
	}
	return &countedProvider{p: p, name: spec.Provider, r: r}, spec, nil
}

// countedProvider counts every provider call by result (enclave_dns_calls_total).
type countedProvider struct {
	p    dns.Provider
	name string
	r    *Reconciler
}

func (c *countedProvider) count(err error) {
	c.r.Metrics.DNSCalls.WithLabelValues(c.name, result(err)).Inc()
}

func (c *countedProvider) Zone(ctx context.Context) (dns.Zone, error) {
	z, err := c.p.Zone(ctx)
	c.count(err)
	return z, err
}

func (c *countedProvider) Get(ctx context.Context, fqdn, typ string) (*dns.Record, error) {
	rec, err := c.p.Get(ctx, fqdn, typ)
	c.count(err)
	return rec, err
}

func (c *countedProvider) Set(ctx context.Context, rec dns.Record, owner string) error {
	err := c.p.Set(ctx, rec, owner)
	c.count(err)
	return err
}

func (c *countedProvider) Delete(ctx context.Context, fqdn, typ string) error {
	err := c.p.Delete(ctx, fqdn, typ)
	c.count(err)
	return err
}

// ensureZone checks the credentials can read the zone and reports it (DNS-01).
func (r *Reconciler) ensureZone(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	report.Step("credentials", api.StepActive)
	p, spec, err := r.zoneProvider(ctx, res.Project, res.Name)
	if err != nil {
		return nil, err
	}
	report.Step("credentials", api.StepDone)
	report.Step("zone", api.StepActive)
	z, err := p.Zone(ctx)
	if err != nil {
		return nil, dnsError(err)
	}
	report.Step("zone", api.StepDone)
	return engine.Outputs{"zoneId": z.ID, "nameServers": strings.Join(z.NameServers, ","), "provider": spec.Provider}, nil
}

// recordValues resolves a record's values: its own, or the machine's public address (DNS-02).
func (r *Reconciler) recordValues(ctx context.Context, spec api.RecordSpec) ([]string, error) {
	if !spec.Machine {
		return spec.Values, nil
	}
	public, err := r.Store.GetSetting(ctx, store.SettingPublicAddress)
	if errors.Is(err, store.ErrNotFound) || public == "" {
		return nil, engine.Permanent(errors.New("this record points at the machine, but the machine has no public address. Set it in the domain settings, then retry"))
	}
	if err != nil {
		return nil, err
	}
	a, err := netip.ParseAddr(public)
	switch {
	case err != nil:
		return nil, engine.Permanent(fmt.Errorf("the machine's public address %s is a hostname; an %s record needs an IP address. Use a CNAME record instead", public, spec.Type))
	case spec.Type == api.RecordA && !a.Is4():
		return nil, engine.Permanent(fmt.Errorf("the machine's public address %s is IPv6; use an AAAA record", public))
	case spec.Type == api.RecordAAAA && a.Is4():
		return nil, engine.Permanent(fmt.Errorf("the machine's public address %s is IPv4; use an A record", public))
	}
	return []string{a.String()}, nil
}

// ensureRecord makes the provider hold the record, adopting an identical one and never overwriting
// one with other values that EnclaveStack didn't write (DNS-03, DNS-04).
func (r *Reconciler) ensureRecord(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindRecord, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	spec := raw.(api.RecordSpec)
	report.Step("zone", api.StepActive)
	p, zs, err := r.zoneProvider(ctx, res.Project, spec.Zone)
	if err != nil {
		return nil, err
	}
	want, err := r.recordValues(ctx, spec)
	if err != nil {
		return nil, err
	}
	report.Step("zone", api.StepDone)
	report.Step("record", api.StepActive)
	fqdn := api.RecordFQDN(spec.Name, zs.Zone)
	cur, err := p.Get(ctx, fqdn, spec.Type)
	if err != nil {
		return nil, dnsError(err)
	}
	owned := res.Outputs[OutputOwned] == true
	switch {
	case cur == nil:
	case owned || cur.Owner == res.ID || dns.SameValues(spec.Type, cur.Values, want):
		if dns.SameValues(spec.Type, cur.Values, want) && cur.TTL == spec.TTL && (cur.Owner == res.ID || owned) {
			report.Step("record", api.StepDone)
			return recordOutputs(fqdn, want), nil
		}
	default:
		return nil, engine.Permanent(fmt.Errorf("%s already has a %s record at %s with %s, which EnclaveStack didn't write. Change or delete it there, or use another name; EnclaveStack won't overwrite it",
			fqdn, spec.Type, zs.Provider, strings.Join(dns.Normalize(spec.Type, cur.Values), ", ")))
	}
	if err := p.Set(ctx, dns.Record{FQDN: fqdn, Type: spec.Type, Values: want, TTL: spec.TTL}, res.ID); err != nil {
		return nil, dnsError(err)
	}
	report.Step("record", api.StepDone)
	return recordOutputs(fqdn, want), nil
}

func recordOutputs(fqdn string, values []string) engine.Outputs {
	return engine.Outputs{"fqdn": fqdn, "values": strings.Join(values, ","), OutputOwned: true}
}

// deleteRecord removes a record EnclaveStack owns; one it never wrote is left alone.
func (r *Reconciler) deleteRecord(ctx context.Context, res *domain.Resource) error {
	if res.Outputs[OutputOwned] != true {
		return nil
	}
	raw, _, err := api.NormalizeSpec(api.KindRecord, res.Spec)
	if err != nil {
		return nil
	}
	spec := raw.(api.RecordSpec)
	p, zs, err := r.zoneProvider(ctx, res.Project, spec.Zone)
	if err != nil {
		return err
	}
	return dnsError(p.Delete(ctx, api.RecordFQDN(spec.Name, zs.Zone), spec.Type))
}

// checkRecords compares ready records with their provider at most every RecordCheckEvery and puts back
// what changed outside EnclaveStack (DNS-05).
func (r *Reconciler) checkRecords(ctx context.Context, ready []*domain.Resource) {
	every := r.RecordCheckEvery
	if every == 0 {
		every = DefaultRecordCheck
	}
	now := r.Now()
	for _, res := range ready {
		if res.Kind != api.KindRecord || res.RequestedAction != "" {
			continue
		}
		r.mu.Lock()
		last, seen := r.recordChecked[res.ID]
		if seen && now.Sub(last) < every {
			r.mu.Unlock()
			continue
		}
		r.recordChecked[res.ID] = now
		r.mu.Unlock()
		if !seen {
			continue // just reconciled, or this process just started: check on the next round
		}
		raw, _, err := api.NormalizeSpec(api.KindRecord, res.Spec)
		if err != nil {
			continue
		}
		spec := raw.(api.RecordSpec)
		p, zs, err := r.zoneProvider(ctx, res.Project, spec.Zone)
		if err != nil {
			continue
		}
		want, err := r.recordValues(ctx, spec)
		if err != nil {
			continue
		}
		cur, err := p.Get(ctx, api.RecordFQDN(spec.Name, zs.Zone), spec.Type)
		if err != nil || (cur != nil && dns.SameValues(spec.Type, cur.Values, want) && cur.TTL == spec.TTL) {
			continue
		}
		what := "was deleted"
		if cur != nil {
			what = "was changed to " + strings.Join(dns.Normalize(spec.Type, cur.Values), ", ")
		}
		if err := res.SetPhase(api.PhaseProvisioning, "Restoring: the record "+what+" at the provider.", now); err != nil {
			continue
		}
		res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), now
		if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
			r.event(ctx, res, "record_repaired", "The record "+what+" outside EnclaveStack; putting it back.")
			r.Enqueue(res.ID)
		}
	}
}
