package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.4 rules that need other resources (L3-002 §7.3, §7.4): placement, network overlaps,
// firewall targets and peers, load balancer targets, zones and records.

// live returns a resource that exists and isn't being deleted, or a NOT_FOUND field error.
func live(ctx context.Context, q *store.Queries, project string, kind api.Kind, name, field string) (*domain.Resource, error) {
	res, err := q.GetResourceByName(ctx, project, kind, name)
	if errors.Is(err, store.ErrNotFound) || (err == nil && res.Phase == api.PhaseDeleting) {
		return nil, api.Validation([]api.FieldError{{Field: field, Code: api.FieldNotFound, Message: fmt.Sprintf("There's no %s named %s.", kindWords(kind), name)}})
	}
	return res, err
}

func kindWords(k api.Kind) string {
	if k == api.KindLoadBalancer {
		return "load balancer"
	}
	return string(k)
}

// placementRefs checks a placed resource's network and subnet (ADR-020) and references the network.
func (s *Resources) placementRefs(ctx context.Context, q *store.Queries, project, network, subnet string, publish bool) ([]store.Ref, error) {
	if network == "" {
		return nil, nil
	}
	n, err := live(ctx, q, project, api.KindNetwork, network, "spec.network")
	if err != nil {
		return nil, err
	}
	var ns api.NetworkSpec
	if err := json.Unmarshal(n.Spec, &ns); err != nil {
		return nil, err
	}
	if subnet != "" && !slices.ContainsFunc(ns.EffectiveSubnets(), func(sub api.Subnet) bool { return sub.Name == subnet }) {
		return nil, api.Validation([]api.FieldError{{Field: "spec.subnet", Code: api.FieldNotFound, Message: fmt.Sprintf("The network %s has no subnet named %s.", network, subnet)}})
	}
	if publish && !ns.HasInternet() {
		return nil, api.Validation([]api.FieldError{{Field: "spec.publish", Code: api.FieldNotAllowed,
			Message: fmt.Sprintf("The network %s has no internet access, so nothing in it can publish a host port. Use a domain or a published load balancer.", network)}})
	}
	return []store.Ref{{ToID: n.ID, Field: "spec.network"}}, nil
}

// networkOverlaps refuses a network whose range overlaps another EnclaveStack network (NET-01).
func (s *Resources) networkOverlaps(ctx context.Context, q *store.Queries, r *domain.Resource, ns api.NetworkSpec) error {
	mine, _ := api.ParseCIDR(ns.CIDR)
	others, err := q.ResourcesOfKind(ctx, api.KindNetwork)
	if err != nil {
		return err
	}
	for _, o := range others {
		var os api.NetworkSpec
		if o.ID == r.ID || json.Unmarshal(o.Spec, &os) != nil {
			continue
		}
		if theirs, ok := api.ParseCIDR(os.CIDR); ok && api.Overlaps(mine, theirs) {
			return api.Validation([]api.FieldError{{Field: "spec.cidr", Code: api.FieldOverlaps, Message: fmt.Sprintf("Overlaps the network %s (%s). Choose a range that doesn't.", o.Name, os.CIDR)}})
		}
	}
	return nil
}

// firewallRefs checks a firewall's targets and the networks and resources its rules name (FW-08).
func (s *Resources) firewallRefs(ctx context.Context, q *store.Queries, project string, fw api.FirewallSpec) ([]store.Ref, error) {
	var refs []store.Ref
	var missing []api.FieldError
	add := func(kind api.Kind, name, field string) {
		res, err := live(ctx, q, project, kind, name, field)
		if err != nil {
			var ae *api.Error
			if errors.As(err, &ae) {
				missing = append(missing, ae.Details...)
			}
			return
		}
		refs = append(refs, store.Ref{ToID: res.ID, Field: field})
	}
	for i, t := range fw.Targets {
		kind, name, _ := api.ParseTarget(t)
		add(kind, name, fmt.Sprintf("spec.targets[%d]", i))
	}
	for field, rules := range map[string][]api.FirewallRule{"spec.ingress": fw.Ingress, "spec.egress": fw.Egress} {
		for i, r := range rules {
			peers, sub := r.From, "from"
			if field == "spec.egress" {
				peers, sub = r.To, "to"
			}
			for j, p := range peers {
				if peer, ok := api.ParsePeer(p); ok && peer.Kind != "" {
					add(peer.Kind, peer.Name, fmt.Sprintf("%s[%d].%s[%d]", field, i, sub, j))
				}
			}
		}
	}
	if len(missing) > 0 {
		slices.SortFunc(missing, func(a, b api.FieldError) int { return compare(a.Field, b.Field) })
		return nil, api.Validation(missing)
	}
	return dedupeRefs(refs), nil
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// dedupeRefs drops repeated (target, field) pairs; a resource named twice keeps one reference per field.
func dedupeRefs(refs []store.Ref) []store.Ref {
	seen := map[store.Ref]bool{}
	out := refs[:0]
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// loadBalancerRefs checks a load balancer's target apps: they exist, have a port and share one network
// (LB-03, LB-08).
func (s *Resources) loadBalancerRefs(ctx context.Context, q *store.Queries, project string, lb api.LoadBalancerSpec) ([]store.Ref, error) {
	var refs []store.Ref
	network, first := "", ""
	for i, t := range lb.Targets {
		f := fmt.Sprintf("spec.targets[%d]", i)
		app, err := live(ctx, q, project, api.KindApp, t.App, f+".app")
		if err != nil {
			return nil, err
		}
		var as api.AppSpec
		if err := json.Unmarshal(app.Spec, &as); err != nil {
			return nil, err
		}
		if t.Port == 0 && as.Port == 0 {
			return nil, api.Validation([]api.FieldError{{Field: f + ".port", Code: api.FieldRequired, Message: "The app " + t.App + " has no port. Give the app a port, or set the port here."}})
		}
		if i == 0 {
			network, first = as.Network, t.App
		} else if as.Network != network {
			return nil, api.Validation([]api.FieldError{{Field: f + ".app", Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("%s is in %s and %s in %s. A load balancer's apps share one network.", t.App, networkWords(as.Network), first, networkWords(network))}})
		}
		refs = append(refs, store.Ref{ToID: app.ID, Field: f + ".app"})
	}
	return refs, nil
}

func networkWords(n string) string {
	if n == "" {
		return "the default network"
	}
	return "the network " + n
}

// checkLoadBalancersFollow refuses moving an app away from the network of a load balancer's other
// targets (LB-03).
func (s *Resources) checkLoadBalancersFollow(ctx context.Context, q *store.Queries, app *domain.Resource, a api.AppSpec) error {
	project := app.ProjectName()
	deps, err := q.Dependants(ctx, app.ID)
	if err != nil {
		return err
	}
	for _, d := range deps {
		var lb api.LoadBalancerSpec
		if d.Kind != api.KindLoadBalancer || json.Unmarshal(d.Spec, &lb) != nil {
			continue
		}
		for _, t := range lb.Targets {
			if t.App == app.Name {
				continue
			}
			other, err := q.GetResourceByName(ctx, project, api.KindApp, t.App)
			if err != nil {
				continue
			}
			var os api.AppSpec
			if json.Unmarshal(other.Spec, &os) == nil && os.Network != a.Network {
				return api.Validation([]api.FieldError{{Field: "spec.network", Code: api.FieldNotAllowed,
					Message: fmt.Sprintf("The load balancer %s also sends traffic to %s in %s. Move them together, or take this app out of the load balancer first.", d.Name, t.App, networkWords(os.Network))}})
			}
		}
	}
	return nil
}

// zoneRefs checks a zone's credentials secret and that no other zone has the same provider and name.
func (s *Resources) zoneRefs(ctx context.Context, q *store.Queries, r *domain.Resource, z api.ZoneSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	sec, err := live(ctx, q, project, api.KindSecret, z.CredentialsSecret, "spec.credentialsSecret")
	if err != nil {
		return nil, err
	}
	others, err := q.ResourcesOfKind(ctx, api.KindZone)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		var os api.ZoneSpec
		if o.ID != r.ID && json.Unmarshal(o.Spec, &os) == nil && os.Provider == z.Provider && os.Zone == z.Zone {
			return nil, api.Validation([]api.FieldError{{Field: "spec.zone", Code: api.FieldNotAllowed, Message: fmt.Sprintf("The zone %s already manages %s.", o.Name, z.Zone)}})
		}
	}
	return []store.Ref{{ToID: sec.ID, Field: "spec.credentialsSecret"}}, nil
}

// recordRefs checks a record's zone and that no other record has the same name and type in it.
func (s *Resources) recordRefs(ctx context.Context, q *store.Queries, r *domain.Resource, rec api.RecordSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	z, err := live(ctx, q, project, api.KindZone, rec.Zone, "spec.zone")
	if err != nil {
		return nil, err
	}
	var zs api.ZoneSpec
	if err := json.Unmarshal(z.Spec, &zs); err != nil {
		return nil, err
	}
	if len(api.RecordFQDN(rec.Name, zs.Zone)) > api.HostnameMaxLen {
		return nil, api.Validation([]api.FieldError{{Field: "spec.name", Code: api.FieldTooLong, Message: "With the zone, the name can be at most 253 characters."}})
	}
	others, err := q.ResourcesOfKind(ctx, api.KindRecord)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		var os api.RecordSpec
		if o.ID != r.ID && o.ProjectName() == project && json.Unmarshal(o.Spec, &os) == nil && os.Zone == rec.Zone && os.Name == rec.Name && os.Type == rec.Type {
			return nil, api.Validation([]api.FieldError{{Field: "spec.name", Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("The record %s already manages the %s record %s.", o.Name, rec.Type, api.RecordFQDN(rec.Name, zs.Zone))}})
		}
	}
	return []store.Ref{{ToID: z.ID, Field: "spec.zone"}}, nil
}

// domainZoneRef checks a DNS-01 domain's zone covers its hostname (CRT-01).
func (s *Resources) domainZoneRef(ctx context.Context, q *store.Queries, project string, d api.DomainSpec) ([]store.Ref, error) {
	if d.Zone == "" {
		return nil, nil
	}
	z, err := live(ctx, q, project, api.KindZone, d.Zone, "spec.zone")
	if err != nil {
		return nil, err
	}
	var zs api.ZoneSpec
	if err := json.Unmarshal(z.Spec, &zs); err != nil {
		return nil, err
	}
	host := d.Hostname
	if api.IsWildcard(host) {
		host = host[2:]
	}
	if !api.InZone(host, zs.Zone) {
		return nil, api.Validation([]api.FieldError{{Field: "spec.hostname", Code: api.FieldNotAllowed, Message: fmt.Sprintf("%s isn't in the zone %s (%s).", d.Hostname, d.Zone, zs.Zone)}})
	}
	return []store.Ref{{ToID: z.ID, Field: "spec.zone"}}, nil
}
