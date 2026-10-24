package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Public access (L2-014, L3-002 §16.6): what tunnels and public domains reference, and the rules that need
// the store to check.

// tunnelRefs checks a tunnel's credentials, app, gate secret, custom secrets and edge machine.
func (s *Resources) tunnelRefs(ctx context.Context, q *store.Queries, r *domain.Resource, t api.TunnelSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	var refs []store.Ref
	if t.CredentialsSecret != "" {
		sec, err := live(ctx, q, project, api.KindSecret, t.CredentialsSecret, "spec.credentialsSecret")
		if err != nil {
			return nil, err
		}
		refs = append(refs, store.Ref{ToID: sec.ID, Field: "spec.credentialsSecret"})
	}
	if api.IsAppLink(t.Provider) {
		app, err := live(ctx, q, project, api.KindApp, t.App, "spec.app")
		if err != nil {
			return nil, err
		}
		if t.Port == 0 {
			var as api.AppSpec
			if json.Unmarshal(app.Spec, &as) != nil || as.Port == 0 {
				return nil, api.Validation([]api.FieldError{{Field: "spec.port", Code: api.FieldRequired,
					Message: "The app " + t.App + " has no port. Give the app a port, or set the port here."}})
			}
		}
		refs = append(refs, store.Ref{ToID: app.ID, Field: "spec.app"})
		gate, err := s.gateRefs(ctx, q, project, t.GateOrDefault())
		if err != nil {
			return nil, err
		}
		refs = append(refs, gate...)
	}
	if t.Provider == api.TunnelCustom {
		secrets, err := s.secretRefs(ctx, q, project, t.SecretEnv)
		if err != nil {
			return nil, err
		}
		refs = append(refs, secrets...)
	}
	if t.Provider == api.TunnelRelay {
		if err := s.relayMachineFree(ctx, q, r, t.Machine); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// relayMachineFree checks the machine is an edge machine that carries no other relay: a relay takes its
// ports 80 and 443 (L3-002 §16.5).
func (s *Resources) relayMachineFree(ctx context.Context, q *store.Queries, r *domain.Resource, machine string) error {
	m, err := q.GetMachineByName(ctx, machine)
	if errors.Is(err, store.ErrNotFound) {
		return api.Validation([]api.FieldError{{Field: "spec.machine", Code: api.FieldNotFound, Message: "There's no machine named " + machine + "."}})
	}
	if err != nil {
		return err
	}
	if !m.IsEdge() {
		return api.Validation([]api.FieldError{{Field: "spec.machine", Code: api.FieldNotAllowed,
			Message: "A relay runs on an edge machine. Add one with Machines › Add an edge machine."}})
	}
	tunnels, err := q.ResourcesOfKind(ctx, api.KindTunnel)
	if err != nil {
		return err
	}
	for _, o := range tunnels {
		var os api.TunnelSpec
		if o.ID != r.ID && o.Phase != api.PhaseDeleting && json.Unmarshal(o.Spec, &os) == nil && os.Provider == api.TunnelRelay && os.Machine == machine {
			msg := fmt.Sprintf("The edge machine %s already carries a relay. One edge machine relays one tunnel, because the relay takes its ports 80 and 443.", machine)
			return api.Validation([]api.FieldError{{Field: "spec.machine", Code: api.FieldNotAllowed, Message: msg}})
		}
	}
	return nil
}

// gateRefs references a password gate's secret.
func (s *Resources) gateRefs(ctx context.Context, q *store.Queries, project string, g api.Gate) ([]store.Ref, error) {
	if g.Mode != api.GatePassword {
		return nil, nil
	}
	sec, err := live(ctx, q, project, api.KindSecret, g.PasswordSecret, "spec.gate.passwordSecret")
	if err != nil {
		return nil, err
	}
	return []store.Ref{{ToID: sec.ID, Field: "spec.gate.passwordSecret"}}, nil
}

// publicDomainRefs checks a public domain's tunnel is a domain tunnel of the project, that a Cloudflare
// tunnel has a Cloudflare zone, and references the tunnel and the gate's secret (PUB-01 – PUB-03).
func (s *Resources) publicDomainRefs(ctx context.Context, q *store.Queries, project string, d api.DomainSpec) ([]store.Ref, error) {
	if !d.IsPublic() {
		return nil, nil
	}
	tun, err := live(ctx, q, project, api.KindTunnel, d.Tunnel, "spec.tunnel")
	if err != nil {
		return nil, err
	}
	var ts api.TunnelSpec
	if err := json.Unmarshal(tun.Spec, &ts); err != nil {
		return nil, err
	}
	if api.IsAppLink(ts.Provider) {
		return nil, api.Validation([]api.FieldError{{Field: "spec.tunnel", Code: api.FieldNotAllowed,
			Message: "This way in gives one app its own address; your own domains go through Cloudflare or your own server."}})
	}
	if ts.Provider == api.TunnelCloudflare {
		if d.Zone == "" {
			return nil, api.Validation([]api.FieldError{{Field: "spec.zone", Code: api.FieldRequired,
				Message: "A Cloudflare tunnel needs the domain's zone at Cloudflare, so EnclaveStack can point the name at the tunnel."}})
		}
		z, err := live(ctx, q, project, api.KindZone, d.Zone, "spec.zone")
		if err != nil {
			return nil, err
		}
		var zs api.ZoneSpec
		if err := json.Unmarshal(z.Spec, &zs); err != nil {
			return nil, err
		}
		if zs.Provider != api.ProviderCloudflare {
			return nil, api.Validation([]api.FieldError{{Field: "spec.zone", Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("Cloudflare tunnels need the domain's DNS at Cloudflare. Move %s to Cloudflare, or use a relay tunnel.", zs.Zone)}})
		}
	} else if d.CloudflareAccess != nil {
		return nil, api.Validation([]api.FieldError{{Field: "spec.cloudflareAccess", Code: api.FieldNotAllowed, Message: "Cloudflare Access needs a Cloudflare tunnel."}})
	}
	refs := []store.Ref{{ToID: tun.ID, Field: "spec.tunnel"}}
	gate, err := s.gateRefs(ctx, q, project, d.GateOrDefault())
	if err != nil {
		return nil, err
	}
	return append(refs, gate...), nil
}
