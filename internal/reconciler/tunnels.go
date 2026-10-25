package reconciler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/tunnel"
	"github.com/enclavestack/platform/internal/tunnel/cloudflare"
	"github.com/enclavestack/platform/pkg/api"
)

// Public access (L2-014, ADR-051 – ADR-053): tunnels, the edge's listeners for them, and keeping them
// connected. Public domains are in publicdomains.go.

// Tunnel timing (L3-007 §14).
const (
	// DefaultTunnelConnectWait bounds one wait for a new connector to connect and get its address.
	DefaultTunnelConnectWait = 90 * time.Second
	// DefaultTunnelCheck is how often ready tunnels are looked at.
	DefaultTunnelCheck = 10 * time.Second
	// TunnelDownRestart is how long a connector may stay without a connection before it is restarted.
	TunnelDownRestart = 30 * time.Second
	// DefaultTunnelDrift is how often a Cloudflare tunnel's routing rules, records and Access applications
	// are compared with Cloudflare (TUN-10).
	DefaultTunnelDrift = 10 * time.Minute
	tunnelPoll         = time.Second
)

// Tunnel outputs (L3-002 §16.3).
const (
	OutputTunnelURL     = "url"
	OutputConnections   = "connections"
	OutputConnection    = "connection" // connected, reconnecting
	OutputTunnelID      = "tunnelId"
	OutputPublicAddress = "publicAddress"
	OutputServes        = "serves"
	OutputGate          = "gate" // an app link's or a public domain's gate mode, with its default applied
	ConnectionUp        = "connected"
	ConnectionDown      = "reconnecting"
)

// tunnelState is what the tunnel loop remembers between passes.
type tunnelState struct {
	mu        sync.Mutex
	downSince map[string]time.Time
	drifted   map[string]time.Time
	trusted   map[string]string // tunnel ID → its connector's or relay's last known address
}

func (t *tunnelState) init() {
	if t.downSince == nil {
		t.downSince, t.drifted, t.trusted = map[string]time.Time{}, map[string]time.Time{}, map[string]string{}
	}
}

func (r *Reconciler) rememberTrusted(id, addr string) {
	r.tun.mu.Lock()
	defer r.tun.mu.Unlock()
	r.tun.init()
	if addr != "" {
		r.tun.trusted[id] = addr
	}
}

func tunnelSpecOf(res *domain.Resource) (api.TunnelSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindTunnel, res.Spec)
	if err != nil {
		return api.TunnelSpec{}, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	return raw.(api.TunnelSpec), nil
}

// cloudflareName is the tunnel's name at Cloudflare (TUN-02).
func cloudflareName(res *domain.Resource) string {
	return "enclave-" + res.ProjectName() + "-" + res.Name
}

// tunnelError maps provider errors to retry behaviour (TUN-09).
func (r *Reconciler) tunnelError(provider string, err error) error {
	if err == nil {
		r.Metrics.TunnelProviderCalls.WithLabelValues(provider, "ok").Inc()
		return nil
	}
	r.Metrics.TunnelProviderCalls.WithLabelValues(provider, "error").Inc()
	if cloudflare.Permanent(err) {
		return engine.Permanent(err)
	}
	return err
}

// cloudflareClient opens a Cloudflare tunnel's account with its token (TUN-02).
func (r *Reconciler) cloudflareClient(ctx context.Context, project string, s api.TunnelSpec) (*cloudflare.Client, error) {
	token, err := r.secretToken(ctx, project, s.CredentialsSecret, "apiToken")
	if err != nil {
		return nil, err
	}
	return cloudflare.New(r.CloudflareAPI, cloudflare.Credentials{APIToken: token, AccountID: s.AccountID}), nil
}

// secretToken reads a provider's token from a secret: the value itself, or JSON with key, so one
// {"apiToken"} secret serves a zone and a tunnel alike.
func (r *Reconciler) secretToken(ctx context.Context, project, secret, key string) (string, error) {
	raw, err := r.secretValue(ctx, project, secret)
	if err != nil {
		return "", err
	}
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return "", engine.Permanent(fmt.Errorf("the secret %s isn't valid JSON; give the token itself, or {%q:\"…\"}", secret, key))
		}
		v, _ := m[key].(string)
		if strings.TrimSpace(v) == "" {
			return "", engine.Permanent(fmt.Errorf("the secret %s has no %q", secret, key))
		}
		return strings.TrimSpace(v), nil
	}
	if raw == "" {
		return "", engine.Permanent(fmt.Errorf("the secret %s is empty", secret))
	}
	return raw, nil
}

// tailscaleHostname is a Tailscale link's device name: as given, or "<project>-<app>" (TUN-12).
func tailscaleHostname(res *domain.Resource, s api.TunnelSpec) string {
	if s.Hostname != "" {
		return s.Hostname
	}
	h := res.ProjectName() + "-" + s.App
	if len(h) > 63 {
		h = strings.TrimRight(h[:63], "-")
	}
	return h
}

// appUpstream is host:port of the app a link or domain reaches, on the enclave network.
func (r *Reconciler) appUpstream(ctx context.Context, project, app string, port int) (string, error) {
	a, err := r.Store.GetResourceByName(ctx, project, api.KindApp, app)
	if errors.Is(err, store.ErrNotFound) {
		return "", engine.Permanent(fmt.Errorf("the app %s no longer exists", app))
	}
	if err != nil {
		return "", err
	}
	if port == 0 {
		if as, _, err := api.NormalizeSpec(api.KindApp, a.Spec); err == nil {
			port = as.(api.AppSpec).Port
		}
	}
	if port == 0 {
		return "", engine.Permanent(fmt.Errorf("the app %s has no port", app))
	}
	return net.JoinHostPort(engine.ContainerName(a), strconv.Itoa(port)), nil
}

// edgeMachine returns the edge machine a relay runs on.
func (r *Reconciler) edgeMachine(ctx context.Context, name string) (store.Machine, error) {
	m, err := r.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return m, engine.Permanent(fmt.Errorf("the edge machine %s no longer exists", name))
	}
	if err != nil {
		return m, err
	}
	if !m.IsEdge() {
		return m, engine.Permanent(fmt.Errorf("%s isn't an edge machine", name))
	}
	if m.State == api.MachineWaiting {
		return m, fmt.Errorf("%w: the edge machine %s is waiting to join; run its join command on that server (enclave machine join-code %s makes a new one)", engine.ErrMachineOffline, name, name)
	}
	if m.State != api.MachineOnline {
		return m, fmt.Errorf("%w: the edge machine %s is %s", engine.ErrMachineOffline, name, m.State)
	}
	return m, nil
}

// connectorFor builds a tunnel's connector from its spec, resolving secrets. ports are its edge listeners.
func (r *Reconciler) connectorFor(ctx context.Context, res *domain.Resource, s api.TunnelSpec, ports store.TunnelPorts) (engine.Connector, engine.Outputs, error) {
	out := engine.Outputs{}
	switch s.Provider {
	case api.TunnelQuick:
		return tunnel.Quick(res.ID, ports.HTTPPort), out, nil
	case api.TunnelCloudflare:
		client, err := r.cloudflareClient(ctx, res.Project, s)
		if err != nil {
			return engine.Connector{}, nil, err
		}
		t, err := client.EnsureTunnel(ctx, cloudflareName(res))
		if err := r.tunnelError(s.Provider, err); err != nil {
			return engine.Connector{}, nil, err
		}
		token, err := client.Token(ctx, t.ID)
		if err := r.tunnelError(s.Provider, err); err != nil {
			return engine.Connector{}, nil, err
		}
		out[OutputTunnelID] = t.ID
		return tunnel.Cloudflare(res.ID, token), out, nil
	case api.TunnelTailscale:
		key, err := r.secretToken(ctx, res.Project, s.CredentialsSecret, "authKey")
		if err != nil {
			return engine.Connector{}, nil, err
		}
		out["device"] = tailscaleHostname(res, s)
		return tunnel.Tailscale(res.ID, key, tailscaleHostname(res, s), s.Mode == api.TailscalePublic, ports.HTTPPort), out, nil
	case api.TunnelNgrok:
		token, err := r.secretToken(ctx, res.Project, s.CredentialsSecret, "authToken")
		if err != nil {
			return engine.Connector{}, nil, err
		}
		return tunnel.Ngrok(res.ID, token, s.Domain, ports.HTTPPort), out, nil
	case api.TunnelCustom:
		env := map[string]string{}
		for k, v := range s.Env {
			env[k] = v
		}
		for k, name := range s.SecretEnv {
			v, err := r.secretValue(ctx, res.Project, name)
			if err != nil {
				return engine.Connector{}, nil, err
			}
			env[k] = v
		}
		return tunnel.Custom(res.ID, s.Image, s.Command, env, ports.HTTPPort), out, nil
	case api.TunnelRelay:
		m, err := r.edgeMachine(ctx, s.Machine)
		if err != nil {
			return engine.Connector{}, nil, err
		}
		edge, err := r.Driver.EdgeAddress(ctx, engine.NetworkName)
		if err != nil {
			return engine.Connector{}, nil, err
		}
		if edge == "" {
			return engine.Connector{}, nil, errors.New("the edge proxy isn't running yet")
		}
		out[OutputPublicAddress] = m.Address
		return tunnel.Relay(res.ID, m.ID, edge, ports.HTTPPort, ports.HTTPSPort), out, nil
	}
	return engine.Connector{}, nil, engine.Permanent(fmt.Errorf("unknown provider %q", s.Provider))
}

// connectorIdentity names a tunnel's connector without resolving secrets: enough to read, exec in or
// remove it.
func (r *Reconciler) connectorIdentity(ctx context.Context, res *domain.Resource, s api.TunnelSpec) engine.Connector {
	if s.Provider == api.TunnelRelay {
		c := engine.Connector{Name: engine.RelayName(res.ID), TunnelID: res.ID, Network: engine.NetworkName}
		if m, err := r.Store.GetMachineByName(ctx, s.Machine); err == nil {
			c.Machine = m.ID
		}
		return c
	}
	c := engine.Connector{Name: engine.ConnectorName(res.ID), TunnelID: res.ID, Network: engine.TunnelNetwork}
	if s.Provider == api.TunnelTailscale {
		c.StateVolume = engine.ConnectorVolume(res.ID)
	}
	return c
}

// ensureTunnel makes a tunnel's connector run, routes its listener on the edge and waits for its address
// or connection (TUN-01 – TUN-14).
func (r *Reconciler) ensureTunnel(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	s, err := tunnelSpecOf(res)
	if err != nil {
		return nil, err
	}
	report.Step("check", api.StepActive)
	ports, err := r.Store.EnsureTunnelPorts(ctx, res.ID, s.Provider == api.TunnelRelay)
	if err != nil {
		return nil, err
	}
	if s.Provider == api.TunnelRelay {
		// The relay points at the edge proxy, so the edge must run first; its listener waits for the relay.
		if err := r.syncEdge(ctx); err != nil {
			return nil, err
		}
	}
	c, out, err := r.connectorFor(ctx, res, s, ports)
	if err != nil {
		return nil, err
	}
	report.Step("check", api.StepDone)
	report.Step("connector", api.StepActive)
	if res.RequestedAction == api.ActionRedeploy {
		// A restart keeps a Tailscale device's identity: only the container goes (TUN-05).
		keep := c
		keep.StateVolume = ""
		if err := r.Driver.RemoveConnector(ctx, keep); err != nil {
			return nil, err
		}
	}
	st, err := r.Driver.EnsureConnector(ctx, c)
	if err != nil {
		return nil, err
	}
	r.rememberTrusted(res.ID, st.Address)
	if err := r.syncEdge(ctx); err != nil { // the listener now trusts the connector's address
		return nil, err
	}
	if s.Provider == api.TunnelCloudflare {
		hosts, err := r.syncIngress(ctx, res, s, out[OutputTunnelID].(string), ports)
		if err != nil {
			return nil, err
		}
		out[OutputServes] = hosts
	}
	report.Step("connector", api.StepDone)
	report.Step("address", api.StepActive)
	obs, err := r.waitConnected(ctx, res, s, c, report)
	if err != nil {
		return nil, err
	}
	for k, v := range obs {
		out[k] = v
	}
	if s.Provider == api.TunnelCustom && s.URL != "" { // the address it gives, if the user knows it
		out[OutputTunnelURL] = s.URL
	}
	if api.IsAppLink(s.Provider) {
		out[OutputGate] = s.GateOrDefault().Mode
	}
	if prev, _ := res.Outputs[OutputTunnelURL].(string); prev != "" && out[OutputTunnelURL] != nil && out[OutputTunnelURL] != prev {
		r.event(ctx, res, "address_changed", fmt.Sprintf("The public address is now %s.", out[OutputTunnelURL]))
	}
	out[OutputConnection] = ConnectionUp
	report.Step("address", api.StepDone)
	r.Metrics.TunnelConnections.WithLabelValues(res.ID).Set(float64(asInt(out[OutputConnections])))
	return out, nil
}

// syncIngress writes a Cloudflare tunnel's routing rules: each public domain through it to its listener.
func (r *Reconciler) syncIngress(ctx context.Context, res *domain.Resource, s api.TunnelSpec, tunnelID string, ports store.TunnelPorts) ([]string, error) {
	hosts, err := r.publicHostnames(ctx, res)
	if err != nil {
		return nil, err
	}
	client, err := r.cloudflareClient(ctx, res.Project, s)
	if err != nil {
		return nil, err
	}
	if err := r.tunnelError(s.Provider, client.SetIngress(ctx, tunnelID, hosts, tunnel.Upstream(ports.HTTPPort))); err != nil {
		return nil, err
	}
	return hosts, nil
}

// publicHostnames lists the hostnames of the public domains through a tunnel that aren't being deleted.
func (r *Reconciler) publicHostnames(ctx context.Context, tun *domain.Resource) ([]string, error) {
	domains, err := r.Store.ResourcesOfKind(ctx, api.KindDomain)
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, d := range domains {
		if d.Phase == api.PhaseDeleting || d.ProjectName() != tun.ProjectName() {
			continue
		}
		if ds, ok := hostnameSpec(d); ok && ds.Tunnel == tun.Name {
			hosts = append(hosts, ds.Hostname)
		}
	}
	slices.Sort(hosts)
	return hosts, nil
}

// observation is one look at a connector: whether it is connected, its address, and a failure to report.
type observation struct {
	connected   bool
	url         string
	connections int
	failure     string // permanent: retrying won't help until the user changes something
	waiting     string // why it isn't connected yet
}

// observe looks at a connector once, the way its provider tells.
func (r *Reconciler) observe(ctx context.Context, res *domain.Resource, s api.TunnelSpec, c engine.Connector) observation {
	st, err := r.Driver.ConnectorState(ctx, c)
	if err != nil {
		return observation{waiting: err.Error()}
	}
	if !st.Exists {
		return observation{waiting: "The connector doesn't exist."}
	}
	last := ""
	if len(st.Logs) > 0 {
		last = st.Logs[len(st.Logs)-1]
	}
	switch s.Provider {
	case api.TunnelQuick, api.TunnelCloudflare:
		var o observation
		if body, err := r.Driver.ConnectorFetch(ctx, c, tunnel.CloudflaredMetricsPort, "/ready"); err == nil {
			o.connections = tunnel.ReadyConnections(body)
		}
		if s.Provider == api.TunnelQuick {
			if body, err := r.Driver.ConnectorFetch(ctx, c, tunnel.CloudflaredMetricsPort, "/quicktunnel"); err == nil {
				o.url = tunnel.QuickHostname(body)
			}
			if o.url == "" {
				o.url = tunnel.QuickURL(st.Logs)
			}
		}
		o.connected = st.Running && o.connections > 0 && (s.Provider != api.TunnelQuick || o.url != "")
		if !o.connected {
			o.waiting = "No connection to Cloudflare yet. " + last
		}
		return o
	case api.TunnelTailscale:
		if !st.Running {
			if why := tunnel.TailscaleFailure(st.Logs); why != "" {
				return observation{failure: why}
			}
			return observation{waiting: "Tailscale isn't running. " + last}
		}
		out, code, err := r.Driver.ConnectorExec(ctx, c, tunnel.TailscaleStatusCmd)
		if err != nil || code != 0 {
			return observation{waiting: "Tailscale is starting."}
		}
		ts, err := tunnel.ParseTailscaleStatus(out)
		if err != nil || !ts.Running || ts.DNSName == "" {
			if why := tunnel.TailscaleFailure(st.Logs); why != "" {
				return observation{failure: why}
			}
			return observation{waiting: "Tailscale is logging in (" + ts.State + ")."}
		}
		if !ts.HTTPS {
			return observation{failure: "Turn on HTTPS Certificates in your Tailscale admin console (DNS › HTTPS Certificates), then Retry."}
		}
		if s.Mode == api.TailscalePublic && !ts.Funnel {
			return observation{failure: "Funnel isn't allowed for this device. Add the funnel attribute to your tailnet policy in the Tailscale admin console (Access controls), then Retry."}
		}
		o := observation{url: "https://" + ts.DNSName, connected: ts.Online}
		if o.connected {
			o.connections = 1
		} else {
			o.waiting = "The Tailscale device is offline."
		}
		return o
	case api.TunnelNgrok:
		url, failure := tunnel.NgrokURL(st.Logs)
		if body, err := r.Driver.ConnectorFetch(ctx, c, tunnel.NgrokAPIPort, "/api/tunnels"); err == nil {
			if u := tunnel.NgrokTunnels(body); u != "" {
				url = u
			}
		}
		if failure != "" && url == "" {
			return observation{failure: "ngrok refused the token: " + failure}
		}
		o := observation{url: url, connected: st.Running && url != ""}
		if o.connected {
			o.connections = 1
		} else {
			o.waiting = "ngrok hasn't connected yet. " + last
		}
		return o
	default: // custom and relay: running is all the engine can tell
		o := observation{connected: st.Running}
		if o.connected {
			o.connections = 1
		} else {
			o.waiting = "The connector isn't running. " + last
		}
		return o
	}
}

// waitConnected waits for a new connector to connect and get its address.
func (r *Reconciler) waitConnected(ctx context.Context, res *domain.Resource, s api.TunnelSpec, c engine.Connector, report engine.Reporter) (engine.Outputs, error) {
	wait := r.TunnelConnectWait
	if wait == 0 {
		wait = DefaultTunnelConnectWait
	}
	deadline := r.Now().Add(wait)
	for {
		o := r.observe(ctx, res, s, c)
		if o.failure != "" {
			return nil, engine.Permanent(errors.New(o.failure))
		}
		if o.connected {
			out := engine.Outputs{OutputConnections: o.connections}
			if o.url != "" {
				out[OutputTunnelURL] = o.url
			}
			if s.Provider == api.TunnelTailscale {
				out["funnel"] = s.Mode == api.TailscalePublic
			}
			return out, nil
		}
		if !r.Now().Before(deadline) {
			return nil, errors.New(strings.TrimSpace(o.waiting))
		}
		engine.Note(report, strings.TrimSpace(o.waiting))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(tunnelPoll):
		}
	}
}

func asInt(v any) int {
	n, _ := asInt64(v)
	return int(n)
}

// deleteTunnel removes a tunnel's connector, and what the provider holds: the tunnel at Cloudflare, the
// device in the tailnet. Its listener leaves the edge with it (TUN-02, TUN-12).
func (r *Reconciler) deleteTunnel(ctx context.Context, res *domain.Resource) error {
	s, err := tunnelSpecOf(res)
	if err != nil {
		return err
	}
	c := r.connectorIdentity(ctx, res, s)
	switch s.Provider {
	case api.TunnelTailscale:
		if st, err := r.Driver.ConnectorState(ctx, c); err == nil && st.Running {
			if _, _, err := r.Driver.ConnectorExec(ctx, c, tunnel.TailscaleLogoutCmd); err != nil {
				r.Log.Warn("tailscale logout failed; removing the device anyway", "resource_id", res.ID, "error", err)
			}
		}
	}
	// A relay whose edge machine was removed has nothing left to remove there.
	if s.Provider != api.TunnelRelay || c.Machine != "" {
		if err := r.Driver.RemoveConnector(ctx, c); err != nil {
			return err
		}
	}
	r.Metrics.RelayUp.DeleteLabelValues(res.ID)
	if s.Provider == api.TunnelCloudflare {
		if id, _ := res.Outputs[OutputTunnelID].(string); id != "" {
			client, err := r.cloudflareClient(ctx, res.Project, s)
			if err != nil {
				return err
			}
			if err := r.tunnelError(s.Provider, client.DeleteTunnel(ctx, id)); err != nil {
				return err
			}
		}
	}
	r.Metrics.TunnelConnections.DeleteLabelValues(res.ID)
	r.tun.mu.Lock()
	r.tun.init()
	delete(r.tun.trusted, res.ID)
	delete(r.tun.downSince, res.ID)
	delete(r.tun.drifted, res.ID)
	r.tun.mu.Unlock()
	return r.syncEdge(ctx)
}

// clientIPHeader is the header a trusted connector carries the visitor's address in (PUB-07).
func clientIPHeader(provider string) string {
	switch provider {
	case api.TunnelQuick, api.TunnelCloudflare:
		return "Cf-Connecting-IP"
	case api.TunnelRelay:
		return "" // PROXY protocol
	}
	return "X-Forwarded-For"
}

// trustedAddress is a connector's address, from the engine or, when it can't answer, as last seen.
func (r *Reconciler) trustedAddress(ctx context.Context, res *domain.Resource, s api.TunnelSpec) string {
	if st, err := r.Driver.ConnectorState(ctx, r.connectorIdentity(ctx, res, s)); err == nil && st.Address != "" {
		r.rememberTrusted(res.ID, st.Address)
		return st.Address
	}
	r.tun.mu.Lock()
	defer r.tun.mu.Unlock()
	r.tun.init()
	return r.tun.trusted[res.ID]
}

// tunnelListeners renders every tunnel's listener, with its routes, for the edge (ADR-051, ADR-052). It
// also returns the relay hostnames whose certificates come through DNS.
func (r *Reconciler) tunnelListeners(ctx context.Context) ([]engine.Listener, map[string]bool, error) {
	tunnels, err := r.Store.ResourcesOfKind(ctx, api.KindTunnel)
	if err != nil {
		return nil, nil, err
	}
	domains, err := r.Store.ResourcesOfKind(ctx, api.KindDomain)
	if err != nil {
		return nil, nil, err
	}
	dns01 := map[string]bool{}
	var out []engine.Listener
	for _, t := range tunnels {
		if t.Phase == api.PhaseDeleting {
			continue
		}
		s, err := tunnelSpecOf(t)
		if err != nil {
			continue
		}
		ports, err := r.Store.GetTunnelPorts(ctx, t.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue // not reconciled yet
		}
		if err != nil {
			return nil, nil, err
		}
		l := engine.Listener{TunnelID: t.ID, Port: ports.HTTPPort, HTTPSPort: ports.HTTPSPort, Network: engine.TunnelNetwork,
			Trusted: r.trustedAddress(ctx, t, s), ClientIPHeader: clientIPHeader(s.Provider)}
		switch {
		case api.IsAppLink(s.Provider):
			l.Kind = engine.ListenerAppLink
			up, err := r.appUpstream(ctx, t.Project, s.App, s.Port)
			if err != nil {
				continue
			}
			l.Routes = []engine.GatedRoute{r.gatedRoute(ctx, t.Project, "", up, t.ID, s.GateOrDefault(), false, false)}
		case s.Provider == api.TunnelCloudflare:
			l.Kind = engine.ListenerCloudflare
		case s.Provider == api.TunnelRelay:
			l.Kind, l.Network = engine.ListenerRelay, engine.NetworkName
		}
		if !api.IsAppLink(s.Provider) {
			for _, d := range domains {
				if d.Phase == api.PhaseDeleting || d.ProjectName() != t.ProjectName() {
					continue
				}
				ds, ok := hostnameSpec(d)
				if !ok || ds.Tunnel != t.Name {
					continue
				}
				up, err := r.appUpstream(ctx, d.Project, ds.App, ds.Port)
				if err != nil {
					continue
				}
				relay := s.Provider == api.TunnelRelay
				l.Routes = append(l.Routes, r.gatedRoute(ctx, d.Project, ds.Hostname, up, d.ID, ds.GateOrDefault(), relay, relay && ds.Zone != ""))
				if relay && ds.Zone != "" {
					dns01[ds.Hostname] = true
				}
			}
		}
		out = append(out, l)
	}
	return out, dns01, nil
}

// gatedRoute resolves a gate into a route. A password gate whose secret can't be read lets nobody in: the
// gate fails closed (GATE-04).
func (r *Reconciler) gatedRoute(ctx context.Context, project, host, upstream, target string, g api.Gate, tls, dns01 bool) engine.GatedRoute {
	route := engine.GatedRoute{Hostname: host, Upstream: upstream, Target: target, Gate: g, TLS: tls, DNS01: dns01}
	if g.Mode != api.GatePassword {
		return route
	}
	user, pass := "", ""
	if raw, err := r.secretValue(ctx, project, g.PasswordSecret); err == nil {
		var c struct{ Username, Password string }
		if json.Unmarshal([]byte(raw), &c) == nil {
			user, pass = c.Username, c.Password
		}
	}
	if user == "" || pass == "" {
		user, pass = "enclave-closed", randomToken() // nobody knows it
		r.Log.Warn("a password gate's secret isn't readable as {\"username\",\"password\"}; the gate lets nobody in", "secret", g.PasswordSecret, "target", target)
	}
	hash, err := r.hashPassword(pass)
	if err != nil {
		hash, _ = r.hashPassword(randomToken())
	}
	route.PasswordUser, route.PasswordHash = user, hash
	return route
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
