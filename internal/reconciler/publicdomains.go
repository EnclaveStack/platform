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
	"github.com/enclavestack/platform/internal/tunnel/cloudflare"
	"github.com/enclavestack/platform/pkg/api"
)

// Public domains (L2-014 PUB-01 – PUB-10, GATE-09): a domain served to the internet through a domain tunnel,
// with the DNS record and, for Cloudflare, the routing rule and the Access application it needs.

// Domain outputs of public access (L3-002 §16.3).
const (
	OutputPublic        = "public"
	OutputDomainTunnel  = "tunnel"
	OutputDNSRecord     = "dnsRecord"
	OutputRecordOwned   = "dnsRecordOwned"
	OutputRecordZone    = "dnsZone"
	OutputAccessApp     = "accessApplicationId"
	OutputTunnelState   = "tunnelState"
	RecordCreated       = "created"
	RecordAdopted       = "adopted"
	RecordManual        = "manual"
	certificateEdge     = "edge"
	cloudflareIssuer    = "Cloudflare"
	recordTTLForTunnels = 300
)

// publicTunnel returns a public domain's tunnel and its spec, waiting while the tunnel isn't ready.
func (r *Reconciler) publicTunnel(ctx context.Context, res *domain.Resource, spec api.DomainSpec) (*domain.Resource, api.TunnelSpec, error) {
	t, err := r.Store.GetResourceByName(ctx, res.Project, api.KindTunnel, spec.Tunnel)
	if errors.Is(err, store.ErrNotFound) {
		return nil, api.TunnelSpec{}, engine.Permanent(fmt.Errorf("the tunnel %s no longer exists", spec.Tunnel))
	}
	if err != nil {
		return nil, api.TunnelSpec{}, err
	}
	ts, err := tunnelSpecOf(t)
	if err != nil {
		return nil, ts, err
	}
	if t.Phase != api.PhaseReady {
		// Not an attempt: the domain waits for its tunnel, as a resource waits for a machine.
		return nil, ts, fmt.Errorf("%w: Waiting for the tunnel %s to connect", errNotPlaced, spec.Tunnel)
	}
	return t, ts, nil
}

// ensurePublicDomain serves a public domain through its tunnel (PUB-01 – PUB-08).
func (r *Reconciler) ensurePublicDomain(ctx context.Context, res *domain.Resource, spec api.DomainSpec, report engine.Reporter) (engine.Outputs, error) {
	t, ts, err := r.publicTunnel(ctx, res, spec)
	if err != nil {
		return nil, err
	}
	out := engine.Outputs{OutputPublic: true, OutputDomainTunnel: spec.Tunnel, "url": "https://" + spec.Hostname,
		OutputTunnelState: ConnectionUp, OutputGate: spec.GateOrDefault().Mode}
	relay := ts.Provider == api.TunnelRelay
	if relay && spec.Zone != "" {
		// The certificate comes first, so the relay listener's first configuration already serves it (ADR-024).
		report.Step("certificate", api.StepActive)
		if err := r.obtainDNS01(ctx, res, spec, report); err != nil {
			return nil, err
		}
	}
	report.Step("edge", api.StepActive)
	if err := r.syncEdge(ctx); err != nil {
		return nil, err
	}
	if _, _, err := r.syncTargets(ctx, res.ProjectName(), []string{"app/" + spec.App}); err != nil {
		return nil, err
	}
	report.Step("edge", api.StepDone)
	report.Step("route", api.StepActive)
	var rec dns.Record
	switch ts.Provider {
	case api.TunnelCloudflare:
		id, _ := t.Outputs[OutputTunnelID].(string)
		if id == "" {
			return nil, fmt.Errorf("%w: Waiting for the tunnel %s to be made at Cloudflare", errNotPlaced, spec.Tunnel)
		}
		client, err := r.cloudflareClient(ctx, t.Project, ts)
		if err != nil {
			return nil, err
		}
		if err := r.checkZoneAccount(ctx, res, spec, client); err != nil {
			return nil, err
		}
		ports, err := r.Store.EnsureTunnelPorts(ctx, t.ID, false)
		if err != nil {
			return nil, err
		}
		if _, err := r.syncIngress(ctx, t, ts, id, ports); err != nil {
			return nil, err
		}
		rec = dns.Record{FQDN: spec.Hostname, Type: api.RecordCNAME, Values: []string{cloudflare.TunnelTarget(id)}, TTL: recordTTLForTunnels, Proxied: true}
		if accessID, err := r.syncAccess(ctx, res, spec, client); err != nil {
			return nil, err
		} else if accessID != "" {
			out[OutputAccessApp] = accessID
		}
	case api.TunnelRelay:
		m, err := r.edgeMachine(ctx, ts.Machine)
		if err != nil {
			return nil, err
		}
		rec = dns.Record{FQDN: spec.Hostname, Type: api.RecordA, Values: []string{m.Address}, TTL: recordTTLForTunnels}
		if _, err := netip.ParseAddr(m.Address); err != nil {
			rec.Type = api.RecordCNAME // an edge machine added by its host name
		}
	}
	state := RecordManual
	if spec.Zone != "" {
		if state, err = r.putDomainRecord(ctx, res, spec.Zone, rec); err != nil {
			return nil, err
		}
		out[OutputRecordOwned], out[OutputRecordZone] = true, spec.Zone
	}
	out[OutputDNSRecord] = map[string]any{"type": rec.Type, "name": rec.FQDN, "value": strings.Join(rec.Values, ","), "state": state}
	if err := r.cleanupPublic(ctx, res, spec.Tunnel, rec.Type); err != nil { // the old tunnel's leftovers, once the new route works
		return nil, err
	}
	report.Step("route", api.StepDone)
	report.Step("certificate", api.StepActive)
	if !relay {
		out["certificateState"], out["certificateIssuer"] = certificateEdge, cloudflareIssuer
		report.Step("certificate", api.StepDone)
		return out, nil
	}
	cert, err := r.relayCertificate(ctx, res, spec, report)
	if err != nil {
		if state == RecordManual {
			return nil, fmt.Errorf("no certificate for %s yet. Point its %s record to %s; EnclaveStack can't write it without a zone", spec.Hostname, rec.Type, rec.Values[0])
		}
		return nil, err
	}
	out["certificateState"], out["certificateIssuer"] = engine.CertIssued, cert.Issuer
	out["certificateExpiresAt"] = cert.NotAfter.UTC().Format(time.RFC3339)
	report.Step("certificate", api.StepDone)
	return out, nil
}

// relayCertificate waits for the certificate the edge serves a relay domain with: loaded (DNS-01) or
// obtained through the relay (HTTP-01, PUB-03).
func (r *Reconciler) relayCertificate(ctx context.Context, res *domain.Resource, spec api.DomainSpec, report engine.Reporter) (engine.Certificate, error) {
	if spec.Zone != "" {
		c, err := r.Store.GetCertificate(ctx, spec.Hostname)
		if err != nil {
			return engine.Certificate{}, err
		}
		return engine.Certificate{State: engine.CertIssued, Issuer: c.Issuer, NotBefore: c.NotBefore, NotAfter: c.NotAfter}, nil
	}
	wait := r.CertWait
	if wait == 0 {
		wait = DefaultCertWait
	}
	deadline := r.Now().Add(wait)
	for {
		cert, err := r.Driver.Certificate(ctx, checkName(spec.Hostname))
		if err != nil {
			return cert, err
		}
		if cert.State == engine.CertIssued {
			return cert, nil
		}
		if !r.Now().Before(deadline) {
			return cert, fmt.Errorf("the edge doesn't serve the certificate for %s yet: Let's Encrypt checks it through the relay", spec.Hostname)
		}
		engine.Note(report, "Waiting for the certificate for "+spec.Hostname+", obtained through the relay.")
		select {
		case <-ctx.Done():
			return cert, ctx.Err()
		case <-time.After(certPollInterval):
		}
	}
}

// checkZoneAccount refuses a zone in another Cloudflare account than the tunnel's (PUB-02).
func (r *Reconciler) checkZoneAccount(ctx context.Context, res *domain.Resource, spec api.DomainSpec, client *cloudflare.Client) error {
	_, zs, err := r.zoneProvider(ctx, res.Project, spec.Zone)
	if err != nil {
		return err
	}
	account, err := client.ZoneAccount(ctx, zs.Zone)
	if err := r.tunnelError(api.TunnelCloudflare, err); err != nil {
		return err
	}
	if account != "" && account != client.Account {
		return engine.Permanent(fmt.Errorf("the zone %s is in another Cloudflare account than the tunnel %s. Use a tunnel in that account", zs.Zone, spec.Tunnel))
	}
	return nil
}

// syncAccess makes the domain's Access application match its list, or removes it (GATE-09).
func (r *Reconciler) syncAccess(ctx context.Context, res *domain.Resource, spec api.DomainSpec, client *cloudflare.Client) (string, error) {
	id, _ := res.Outputs[OutputAccessApp].(string)
	if prev, _ := res.Outputs[OutputDomainTunnel].(string); prev != spec.Tunnel {
		id = "" // the old tunnel's application goes with it (cleanupPublic); this tunnel's account gets its own
	}
	if spec.CloudflareAccess == nil {
		if id != "" {
			if err := r.tunnelError(api.TunnelCloudflare, client.DeleteAccessApp(ctx, id)); err != nil {
				return "", err
			}
			r.event(ctx, res, "access_removed", "Cloudflare Access removed.")
		}
		return "", nil
	}
	newID, err := client.SetAccessApp(ctx, cloudflare.AccessApp{ID: id, Domain: spec.Hostname, Emails: spec.CloudflareAccess.Emails, EmailDomains: spec.CloudflareAccess.EmailDomains})
	if err := r.tunnelError(api.TunnelCloudflare, err); err != nil {
		return "", err
	}
	if newID != id {
		n := len(spec.CloudflareAccess.Emails) + len(spec.CloudflareAccess.EmailDomains)
		r.event(ctx, res, "access_set", fmt.Sprintf("Cloudflare Access lets %d email address or domain entries through.", n))
	}
	return newID, nil
}

// putDomainRecord makes the zone hold a public domain's record. One with the same value is adopted; one
// with another value that EnclaveStack didn't write is never overwritten (PUB-04, DNS-04).
func (r *Reconciler) putDomainRecord(ctx context.Context, res *domain.Resource, zone string, rec dns.Record) (string, error) {
	p, zs, err := r.zoneProvider(ctx, res.Project, zone)
	if err != nil {
		return "", err
	}
	cur, err := p.Get(ctx, rec.FQDN, rec.Type)
	if err != nil {
		return "", dnsError(err)
	}
	state := RecordCreated
	switch {
	case cur == nil:
	case cur.Owner == res.ID || res.Outputs[OutputRecordOwned] == true && recordTypeOf(res) == rec.Type:
		if dns.SameValues(rec.Type, cur.Values, rec.Values) && cur.Proxied == rec.Proxied && cur.Owner == res.ID {
			return RecordCreated, nil
		}
	case dns.SameValues(rec.Type, cur.Values, rec.Values):
		state = RecordAdopted
	default:
		return "", engine.Permanent(fmt.Errorf("%s already has a %s record at %s pointing to %s, which EnclaveStack didn't write. Change or delete it there; EnclaveStack won't overwrite it",
			rec.FQDN, rec.Type, zs.Provider, strings.Join(dns.Normalize(rec.Type, cur.Values), ", ")))
	}
	if err := p.Set(ctx, rec, res.ID); err != nil {
		return "", dnsError(err)
	}
	if state == RecordCreated && cur == nil {
		r.event(ctx, res, "record_created", fmt.Sprintf("DNS record %s %s → %s created.", rec.Type, rec.FQDN, strings.Join(rec.Values, ", ")))
	}
	return state, nil
}

// recordTypeOf is the type of the record a public domain wrote, from its outputs.
func recordTypeOf(res *domain.Resource) string {
	if m, ok := res.Outputs[OutputDNSRecord].(map[string]any); ok {
		t, _ := m["type"].(string)
		return t
	}
	return ""
}

// cleanupPublic removes what a domain's previous way out left behind once it is served another way:
// its hostname from the old tunnel's routing rules, the old Access application, and a record of another
// type. keepTunnel is the tunnel it uses now ("" when it is local again or deleted); keepType the type of
// the record it holds now (PUB-06).
func (r *Reconciler) cleanupPublic(ctx context.Context, res *domain.Resource, keepTunnel, keepType string) error {
	prev, _ := res.Outputs[OutputDomainTunnel].(string)
	if prev != "" && prev != keepTunnel {
		if old, err := r.Store.GetResourceByName(ctx, res.Project, api.KindTunnel, prev); err == nil {
			if os, err := tunnelSpecOf(old); err == nil && os.Provider == api.TunnelCloudflare {
				id, _ := old.Outputs[OutputTunnelID].(string)
				if ports, err := r.Store.GetTunnelPorts(ctx, old.ID); err == nil && id != "" && old.Phase != api.PhaseDeleting {
					if _, err := r.syncIngress(ctx, old, os, id, ports); err != nil {
						return err
					}
				}
				if accessID, _ := res.Outputs[OutputAccessApp].(string); accessID != "" {
					client, err := r.cloudflareClient(ctx, old.Project, os)
					if err != nil {
						return err
					}
					if err := r.tunnelError(api.TunnelCloudflare, client.DeleteAccessApp(ctx, accessID)); err != nil {
						return err
					}
				}
			}
		}
	}
	oldType := recordTypeOf(res)
	zone, _ := res.Outputs[OutputRecordZone].(string) // the zone it wrote into, which the spec may no longer name
	if res.Outputs[OutputRecordOwned] == true && oldType != "" && oldType != keepType && zone != "" {
		if spec, ok := hostnameSpec(res); ok {
			p, _, err := r.zoneProvider(ctx, res.Project, zone)
			if err != nil {
				return err
			}
			if err := p.Delete(ctx, spec.Hostname, oldType); err != nil {
				return dnsError(err)
			}
			r.event(ctx, res, "record_deleted", fmt.Sprintf("DNS record %s %s deleted.", oldType, spec.Hostname))
		}
	}
	return nil
}

// leavePublic removes everything a public domain made, when it goes local again or is deleted.
func (r *Reconciler) leavePublic(ctx context.Context, res *domain.Resource) error {
	if res.Outputs[OutputPublic] != true {
		return nil
	}
	if err := r.cleanupPublic(ctx, res, "", ""); err != nil {
		return err
	}
	for _, k := range []string{OutputPublic, OutputDomainTunnel, OutputDNSRecord, OutputRecordOwned, OutputRecordZone, OutputAccessApp, OutputTunnelState, OutputGate} {
		res.Outputs = withoutOutput(res.Outputs, k)
	}
	return nil
}
