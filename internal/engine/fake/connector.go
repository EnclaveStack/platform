package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/engine"
)

// Public access connectors (ADR-051, ADR-052). The fake behaves like each provider's agent as far as the
// reconciler reads it: cloudflared's log line and /ready, `tailscale status --json`, ngrok's log line.

type connector struct {
	cfg      engine.Connector
	running  bool
	started  time.Time
	restarts int
	address  string
	// generation counts starts, so a quick link gets a new address after each one, like the real one.
	generation int
	loggedOut  bool
}

// LoggedOut reports whether `tailscale logout` ran in the connector named name.
func (d *Driver) LoggedOut(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.connectors[name]
	return ok && c.loggedOut
}

// TailscaleTailnet is the fake tailnet's DNS suffix.
const TailscaleTailnet = "fake-tailnet.ts.net"

// ConnectorFaults change what fake connectors show.
type ConnectorFaults struct {
	// Down keeps a connector from connecting (it runs, but reports no connection).
	Down bool
	// NoHTTPS and NoFunnel are a tailnet without HTTPS certificates, or without Funnel for the device.
	NoHTTPS, NoFunnel bool
}

// SetConnectorFaults sets the faults of the connector named name ("" clears them).
func (d *Driver) SetConnectorFaults(name string, f ConnectorFaults) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.connectorFaults == nil {
		d.connectorFaults = map[string]ConnectorFaults{}
	}
	d.connectorFaults[name] = f
}

// Connectors returns the connectors that exist, by name.
func (d *Driver) Connectors() map[string]engine.Connector {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]engine.Connector{}
	for n, c := range d.connectors {
		out[n] = c.cfg
	}
	return out
}

// RestartConnector simulates the connector's container restarting (Docker's restart policy, or a crash).
func (d *Driver) RestartConnector(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.connectors[name]; ok {
		c.restarts++
		c.generation++
		c.started = time.Now()
	}
}

// StopConnector simulates a connector stopped outside EnclaveStack.
func (d *Driver) StopConnector(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.connectors[name]; ok {
		c.running = false
	}
}

// EnsureConnector implements engine.Driver.
func (d *Driver) EnsureConnector(ctx context.Context, c engine.Connector) (engine.ConnectorState, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.ConnectorState{}, err
	}
	d.mu.Lock()
	if d.connectors == nil {
		d.connectors = map[string]*connector{}
	}
	existing, ok := d.connectors[c.Name]
	switch {
	case ok && existing.cfg.Hash == c.Hash:
		if !existing.running {
			existing.running, existing.started = true, time.Now()
			existing.generation++
		}
	default:
		d.nextConnector++
		d.connectors[c.Name] = &connector{cfg: c, running: true, started: time.Now(), generation: 1,
			address: fmt.Sprintf("172.31.%d.%d", d.nextConnector/250, 2+d.nextConnector%250)}
	}
	d.mu.Unlock()
	return d.ConnectorState(ctx, c)
}

// ConnectorState implements engine.Driver.
func (d *Driver) ConnectorState(ctx context.Context, c engine.Connector) (engine.ConnectorState, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.ConnectorState{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	k, ok := d.connectors[c.Name]
	if !ok {
		return engine.ConnectorState{}, nil
	}
	st := engine.ConnectorState{Exists: true, Running: k.running, StartedAt: k.started, Restarts: k.restarts, Address: k.address}
	if k.running && !d.connectorFaults[c.Name].Down {
		st.Logs = fakeLogs(k)
	}
	return st, nil
}

// fakeLogs is what the provider's agent prints once connected.
func fakeLogs(k *connector) []string {
	switch {
	case strings.Contains(k.cfg.Image, "cloudflared") && slices.Contains(k.cfg.Cmd, "--url"):
		return []string{"INF Requesting new quick Tunnel on trycloudflare.com...",
			fmt.Sprintf("INF |  https://fake-%s-%d.trycloudflare.com  |", strings.TrimPrefix(k.cfg.Name, engine.ConnectorPrefix), k.generation),
			"INF Registered tunnel connection connIndex=0"}
	case strings.Contains(k.cfg.Image, "cloudflared"):
		return []string{"INF Registered tunnel connection connIndex=0", "INF Registered tunnel connection connIndex=1"}
	case strings.Contains(k.cfg.Image, "ngrok"):
		domain := "fake-" + strings.TrimPrefix(k.cfg.Name, engine.ConnectorPrefix) + ".ngrok-free.app"
		for i, a := range k.cfg.Cmd {
			if a == "--url" && i+1 < len(k.cfg.Cmd) {
				domain = strings.TrimPrefix(k.cfg.Cmd[i+1], "https://")
			}
		}
		line, _ := json.Marshal(map[string]string{"lvl": "info", "msg": "started tunnel", "name": "command_line", "addr": "http://es-system-edge", "url": "https://" + domain})
		return []string{string(line)}
	}
	return []string{"connector running"}
}

// RemoveConnector implements engine.Driver.
func (d *Driver) RemoveConnector(ctx context.Context, c engine.Connector) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.connectors, c.Name)
	return nil
}

// ConnectorExec implements engine.Driver: `tailscale status --json` and `tailscale logout`.
func (d *Driver) ConnectorExec(ctx context.Context, c engine.Connector, cmd []string) (string, int, error) {
	if err := d.Ping(ctx); err != nil {
		return "", 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	k, ok := d.connectors[c.Name]
	if !ok || !k.running {
		return "", 0, fmt.Errorf("the connector %s isn't running", c.Name)
	}
	if len(cmd) == 0 || cmd[0] != "tailscale" {
		return "unknown command", 127, nil
	}
	if slices.Contains(cmd, "logout") {
		k.loggedOut = true
		return "", 0, nil
	}
	if !slices.Contains(cmd, "status") {
		return "unknown command", 127, nil
	}
	faults := d.connectorFaults[c.Name]
	host := k.cfg.Env["TS_HOSTNAME"]
	self := map[string]any{"DNSName": host + "." + TailscaleTailnet + ".", "HostName": host, "Online": !faults.Down}
	caps := map[string]any{}
	if !faults.NoHTTPS {
		caps["https"] = nil
	}
	if !faults.NoFunnel {
		caps["funnel"] = nil
		caps["https://tailscale.com/cap/funnel"] = nil
	}
	self["CapMap"] = caps
	status := map[string]any{"BackendState": "Running", "Self": self}
	if !faults.NoHTTPS {
		status["CertDomains"] = []string{host + "." + TailscaleTailnet}
	}
	if faults.Down {
		status["BackendState"] = "NeedsLogin"
	}
	b, _ := json.Marshal(status)
	return string(b), 0, nil
}

// ConnectorFetch implements engine.Driver: cloudflared's /ready.
func (d *Driver) ConnectorFetch(ctx context.Context, c engine.Connector, port int, path string) ([]byte, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	k, ok := d.connectors[c.Name]
	if !ok || !k.running {
		return nil, errors.New("connection refused")
	}
	if path == "/ready" {
		if d.connectorFaults[c.Name].Down {
			return []byte(`{"status":503,"readyConnections":0}`), nil
		}
		return []byte(`{"status":200,"readyConnections":4}`), nil
	}
	return nil, fmt.Errorf("404 for %s", path)
}

// GateBindAddress implements engine.Driver.
func (d *Driver) GateBindAddress(context.Context) (string, error) { return "127.0.0.1", nil }

// EdgeAddress implements engine.Driver.
func (d *Driver) EdgeAddress(_ context.Context, network string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.edge.running {
		return "", nil
	}
	if network == engine.TunnelNetwork {
		return "172.31.255.2", nil
	}
	return fakeEdgeAddress, nil
}

// fakeEdgeAddress is the fake edge proxy's address on the default network.
const fakeEdgeAddress = "10.89.0.2"

// EdgeListeners returns the listeners of the last applied configuration.
func (d *Driver) EdgeListeners() []engine.Listener {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]engine.Listener(nil), d.edge.cfg.Listeners...)
}

// EdgeGate returns the gate settings of the last applied configuration.
func (d *Driver) EdgeGate() engine.GateConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.edge.cfg.Gate
}
