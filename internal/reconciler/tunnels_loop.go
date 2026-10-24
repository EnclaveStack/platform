package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/tunnel"
	"github.com/enclavestack/platform/internal/tunnel/cloudflare"
	"github.com/enclavestack/platform/pkg/api"
)

// Keeping tunnels connected (TUN-05, TUN-10; L3-007 §14).

func (r *Reconciler) tunnelLoop(ctx context.Context) {
	every := r.TunnelCheckEvery
	if every == 0 {
		every = DefaultTunnelCheck
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.CheckTunnels(ctx)
		}
	}
}

// CheckTunnels runs one pass (exported for tests).
func (r *Reconciler) CheckTunnels(ctx context.Context) {
	r.applyEdgeIfChanged(ctx)
	ready, err := r.Store.ResourcesInPhase(ctx, api.PhaseReady)
	if err != nil {
		return
	}
	counts := map[[2]string]int{}
	for _, res := range ready {
		if res.Kind != api.KindTunnel || res.RequestedAction != "" {
			continue
		}
		s, err := tunnelSpecOf(res)
		if err != nil {
			continue
		}
		state := r.checkTunnel(ctx, res, s)
		counts[[2]string{s.Provider, state}]++
	}
	r.Metrics.Tunnels.Reset()
	for k, n := range counts {
		r.Metrics.Tunnels.WithLabelValues(k[0], k[1]).Set(float64(n))
	}
}

// applyEdgeIfChanged applies the edge configuration when it changed without a reconcile, for example a
// password gate's secret got a new value (GATE-04).
func (r *Reconciler) applyEdgeIfChanged(ctx context.Context) {
	r.edgeMu.Lock()
	cfg, err := r.edgeConfig(ctx)
	last := r.edgeHash
	r.edgeMu.Unlock()
	if err != nil || last == "" || edgeHash(cfg) == last {
		return
	}
	if err := r.syncEdge(ctx); err != nil {
		r.Log.Warn("apply the changed edge configuration failed", "error", err)
	}
}

// edgeHash identifies an edge configuration.
func edgeHash(cfg engine.EdgeConfig) string {
	b, _ := json.Marshal(cfg)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkTunnel looks at one ready tunnel and returns its connection state.
func (r *Reconciler) checkTunnel(ctx context.Context, res *domain.Resource, s api.TunnelSpec) string {
	c := r.connectorIdentity(ctx, res, s)
	o := r.observe(ctx, res, s, c)
	now := r.Now()
	r.Metrics.TunnelConnections.WithLabelValues(res.ID).Set(float64(o.connections))
	if s.Provider == api.TunnelRelay {
		up := 0.0
		if o.connected {
			up = 1
		}
		r.Metrics.RelayUp.WithLabelValues(res.ID).Set(up)
	}
	if o.failure != "" {
		if err := res.SetPhase(api.PhaseFailed, o.failure, now); err == nil {
			res.UpdatedAt = now
			if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
				r.event(ctx, res, "failed", o.failure)
				r.setDomainsTunnelState(ctx, res, "failed")
			}
		}
		return "failed"
	}
	r.tun.mu.Lock()
	r.tun.init()
	since, wasDown := r.tun.downSince[res.ID]
	if o.connected {
		delete(r.tun.downSince, res.ID)
	} else if !wasDown {
		r.tun.downSince[res.ID], since = now, now
	}
	r.tun.mu.Unlock()

	prev, _ := res.Outputs[OutputConnection].(string)
	changed := false
	if o.connected {
		if prev != ConnectionUp {
			res.Outputs, changed = withOutput(res.Outputs, OutputConnection, ConnectionUp), true
			r.event(ctx, res, "connected", fmt.Sprintf("Connected (%d connection%s).", o.connections, plural(o.connections)))
			r.setDomainsTunnelState(ctx, res, ConnectionUp)
		}
		if old, _ := res.Outputs[OutputTunnelURL].(string); o.url != "" && o.url != old && s.Provider != api.TunnelCustom {
			res.Outputs, changed = withOutput(res.Outputs, OutputTunnelURL, o.url), true
			r.event(ctx, res, "address_changed", "The public address is now "+o.url+".")
		}
		if n, _ := asInt64(res.Outputs[OutputConnections]); int(n) != o.connections {
			res.Outputs, changed = withOutput(res.Outputs, OutputConnections, o.connections), true
		}
	} else {
		if prev != ConnectionDown {
			res.Outputs, changed = withOutput(res.Outputs, OutputConnection, ConnectionDown), true
			r.event(ctx, res, "disconnected", "Lost the connection: "+strings.TrimSpace(o.waiting))
			r.setDomainsTunnelState(ctx, res, ConnectionDown)
		}
		after := r.TunnelRestartAfter
		if after == 0 {
			after = TunnelDownRestart
		}
		if now.Sub(since) >= after {
			r.restartTunnel(ctx, res, now.Sub(since))
			return "reconnecting"
		}
	}
	if changed {
		res.UpdatedAt = now
		_, _ = r.Store.SaveStatus(ctx, res, res.Generation)
	}
	r.tunnelDrift(ctx, res, s)
	if o.connected {
		return ConnectionUp
	}
	return "reconnecting"
}

// restartTunnel makes a connector without a connection for TunnelDownRestart again (TUN-05).
func (r *Reconciler) restartTunnel(ctx context.Context, res *domain.Resource, down time.Duration) {
	now := r.Now()
	msg := fmt.Sprintf("No connection for %d s: restarting the connector.", int(down.Seconds()))
	if err := res.SetPhase(api.PhaseProvisioning, msg, now); err != nil {
		return
	}
	res.RequestedAction, res.Steps, res.UpdatedAt = api.ActionRedeploy, domain.NewSteps(domain.StepNames[res.Kind]...), now
	if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
		r.tun.mu.Lock()
		delete(r.tun.downSince, res.ID)
		r.tun.mu.Unlock()
		r.event(ctx, res, "restarting", msg)
		r.Enqueue(res.ID)
	}
}

// setDomainsTunnelState shows a tunnel's state on the public domains through it (TUN-05).
func (r *Reconciler) setDomainsTunnelState(ctx context.Context, tun *domain.Resource, state string) {
	deps, err := r.Store.Dependants(ctx, tun.ID)
	if err != nil {
		return
	}
	for _, d := range deps {
		if d.Kind != api.KindDomain || d.Outputs[OutputTunnelState] == state {
			continue
		}
		d.Outputs, d.UpdatedAt = withOutput(d.Outputs, OutputTunnelState, state), r.Now()
		if ok, err := r.Store.SaveStatus(ctx, d, d.Generation); err == nil && ok {
			msg := map[string]string{ConnectionUp: "The tunnel " + tun.Name + " is connected again.",
				ConnectionDown: "The tunnel " + tun.Name + " lost its connection; the site is unreachable until it reconnects.",
				"failed":       "The tunnel " + tun.Name + " failed; the site is unreachable."}[state]
			r.event(ctx, d, "tunnel_"+state, msg)
		}
	}
}

// tunnelDrift compares what a domain tunnel needs at its provider with what is there, at most every
// TunnelDriftEvery, and puts back what changed outside EnclaveStack (TUN-10).
func (r *Reconciler) tunnelDrift(ctx context.Context, res *domain.Resource, s api.TunnelSpec) {
	if api.IsAppLink(s.Provider) {
		return
	}
	every := r.TunnelDriftEvery
	if every == 0 {
		every = DefaultTunnelDrift
	}
	now := r.Now()
	r.tun.mu.Lock()
	r.tun.init()
	last, seen := r.tun.drifted[res.ID]
	if seen && now.Sub(last) < every {
		r.tun.mu.Unlock()
		return
	}
	r.tun.drifted[res.ID] = now
	r.tun.mu.Unlock()
	if !seen {
		return // just made: check on the next round
	}
	if s.Provider == api.TunnelCloudflare {
		if !r.cloudflareDrift(ctx, res, s) {
			return
		}
	}
	deps, err := r.Store.Dependants(ctx, res.ID)
	if err != nil {
		return
	}
	for _, d := range deps {
		if d.Kind == api.KindDomain && d.Phase == api.PhaseReady {
			r.domainDrift(ctx, res, s, d)
		}
	}
}

// cloudflareDrift checks the tunnel and its routing rules; false when the tunnel is gone.
func (r *Reconciler) cloudflareDrift(ctx context.Context, res *domain.Resource, s api.TunnelSpec) bool {
	id, _ := res.Outputs[OutputTunnelID].(string)
	client, err := r.cloudflareClient(ctx, res.Project, s)
	if err != nil || id == "" {
		return true
	}
	if _, err := client.GetTunnel(ctx, id); errors.Is(err, cloudflare.ErrNotFound) {
		msg := "The tunnel was deleted at Cloudflare. Retry makes it again."
		if err := res.SetPhase(api.PhaseFailed, msg, r.Now()); err == nil {
			res.UpdatedAt = r.Now()
			if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
				r.event(ctx, res, "failed", msg)
				r.setDomainsTunnelState(ctx, res, "failed")
			}
		}
		return false
	} else if err != nil {
		return true
	}
	ports, err := r.Store.GetTunnelPorts(ctx, res.ID)
	if err != nil {
		return true
	}
	hosts, err := r.publicHostnames(ctx, res)
	if err != nil {
		return true
	}
	cur, err := client.Ingress(ctx, id)
	if err != nil || cloudflare.SameIngress(cur, cloudflare.IngressFor(hosts, tunnel.Upstream(ports.HTTPPort))) {
		return true
	}
	if _, err := r.syncIngress(ctx, res, s, id, ports); err == nil {
		r.event(ctx, res, "routing_repaired", "The routing rules were changed at Cloudflare; put them back.")
	}
	return true
}

// domainDrift puts back a public domain's record and Access application (TUN-10).
func (r *Reconciler) domainDrift(ctx context.Context, tun *domain.Resource, s api.TunnelSpec, d *domain.Resource) {
	spec, ok := hostnameSpec(d)
	if !ok || !spec.IsPublic() {
		return
	}
	if rec, ok := d.Outputs[OutputDNSRecord].(map[string]any); ok && spec.Zone != "" && d.Outputs[OutputRecordOwned] == true {
		typ, _ := rec["type"].(string)
		value, _ := rec["value"].(string)
		want := dns.Record{FQDN: spec.Hostname, Type: typ, Values: strings.Split(value, ","), TTL: recordTTLForTunnels, Proxied: s.Provider == api.TunnelCloudflare}
		if p, _, err := r.zoneProvider(ctx, d.Project, spec.Zone); err == nil {
			cur, err := p.Get(ctx, want.FQDN, want.Type)
			if err == nil && (cur == nil || !dns.SameValues(typ, cur.Values, want.Values) || cur.Proxied != want.Proxied) {
				if err := p.Set(ctx, want, d.ID); err == nil {
					r.event(ctx, d, "record_repaired", "The DNS record was changed outside EnclaveStack; put it back.")
				}
			}
		}
	}
	if s.Provider == api.TunnelCloudflare && spec.CloudflareAccess != nil {
		id, _ := d.Outputs[OutputAccessApp].(string)
		client, err := r.cloudflareClient(ctx, tun.Project, s)
		if err != nil || id == "" {
			return
		}
		app, err := client.GetAccessApp(ctx, id)
		if err == nil && cloudflare.SameAccess(app, spec.CloudflareAccess.Emails, spec.CloudflareAccess.EmailDomains) {
			return
		}
		if err != nil && !errors.Is(err, cloudflare.ErrNotFound) {
			return
		}
		newID, err := client.SetAccessApp(ctx, cloudflare.AccessApp{ID: id, Domain: spec.Hostname, Emails: spec.CloudflareAccess.Emails, EmailDomains: spec.CloudflareAccess.EmailDomains})
		if err != nil {
			return
		}
		if newID != id {
			d.Outputs, d.UpdatedAt = withOutput(d.Outputs, OutputAccessApp, newID), r.Now()
			_, _ = r.Store.SaveStatus(ctx, d, d.Generation)
		}
		r.event(ctx, d, "access_repaired", "Cloudflare Access was changed at Cloudflare; put it back.")
	}
}
