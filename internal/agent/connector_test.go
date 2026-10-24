package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/tunnel"
)

// A relay runs on its edge machine through the agent (ADR-052): every connector call crosses the wire with
// the connector whole, files included, and comes back with its state.
func TestConnectorCallsCrossTheWire(t *testing.T) {
	p, ctx := newPair(t, "mch_edge"), context.Background()
	relay := tunnel.Relay("tun_relay0000001", "mch_edge", "10.90.0.2", 11010, 11011)
	st, err := p.remote.EnsureConnector(ctx, relay)
	if err != nil || !st.Exists || !st.Running || st.Address == "" {
		t.Fatalf("ensure: %+v %v", st, err)
	}
	got, ok := p.drv.Connectors()[relay.Name]
	if !ok || got.Hash != relay.Hash || string(got.Files["haproxy.cfg"]) != string(relay.Files["haproxy.cfg"]) || len(got.Publish) != 2 || got.Machine != "mch_edge" {
		t.Errorf("the agent's engine got %+v", got)
	}
	if again, err := p.remote.ConnectorState(ctx, relay); err != nil || again.Address != st.Address {
		t.Errorf("state: %+v %v", again, err)
	}

	// Exec and fetch carry their results and their failures.
	ts := tunnel.Tailscale("tun_ts00000000001", "tskey-auth-x", "galiaraa-stage", false, 80)
	if _, err := p.remote.EnsureConnector(ctx, ts); err != nil {
		t.Fatal(err)
	}
	out, code, err := p.remote.ConnectorExec(ctx, ts, []string{"tailscale", "status", "--json"})
	if err != nil || code != 0 || !strings.Contains(out, "galiaraa-stage") {
		t.Errorf("exec: %q %d %v", out, code, err)
	}
	if out, code, err := p.remote.ConnectorExec(ctx, ts, []string{"sh"}); err != nil || code != 127 || out != "unknown command" {
		t.Errorf("an exit code crosses: %q %d %v", out, code, err)
	}
	quick := tunnel.Quick("tun_quick0000001", 11000)
	if _, err := p.remote.EnsureConnector(ctx, quick); err != nil {
		t.Fatal(err)
	}
	if body, err := p.remote.ConnectorFetch(ctx, quick, tunnel.CloudflaredMetricsPort, "/ready"); err != nil || !strings.Contains(string(body), "readyConnections") {
		t.Errorf("fetch: %q %v", body, err)
	}
	if _, err := p.remote.ConnectorFetch(ctx, quick, tunnel.CloudflaredMetricsPort, "/nope"); err == nil {
		t.Error("a failed fetch came back as success")
	}

	if err := p.remote.RemoveConnector(ctx, relay); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.drv.Connectors()[relay.Name]; ok {
		t.Error("the relay survived removal")
	}
	if st, err := p.remote.ConnectorState(ctx, relay); err != nil || st.Exists {
		t.Errorf("state after removal: %+v %v", st, err)
	}
}
