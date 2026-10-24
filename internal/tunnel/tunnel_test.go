package tunnel

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/engine"
)

// Connectors and what their agents report (ADR-051, ADR-052). The log lines and answers are the real
// agents' output, captured from the pinned images on 2026-10-24.

func TestConnectorsAreHardenedNamedAndHashed(t *testing.T) {
	id := "tun_abcdefghijklmnop"
	cases := map[string]engine.Connector{
		"quick":      Quick(id, 11000),
		"cloudflare": Cloudflare(id, "run-token"),
		"tailscale":  Tailscale(id, "tskey-auth-x", "example-stage-web", true, 11000),
		"ngrok":      Ngrok(id, "ngrok-token", "stage.ngrok.app", 11000),
		"custom":     Custom(id, "ghcr.io/acme/tunnel:1", []string{"run"}, map[string]string{"K": "v"}, 11000),
	}
	for name, c := range cases {
		if c.Name != "es-tunnel-abcdefghijkl" || c.TunnelID != id || c.Network != engine.TunnelNetwork || c.Hash == "" || c.Machine != "" {
			t.Errorf("%s: %+v", name, c)
		}
		if len(c.Publish) != 0 {
			t.Errorf("%s publishes ports: a connector only dials out (TUN-06)", name)
		}
	}
	if q := cases["quick"]; !slices.Contains(q.Cmd, "http://es-system-edge:11000") || q.Image != CloudflaredImage {
		t.Errorf("quick link must send to its own edge listener: %+v", q.Cmd)
	}
	if c := cases["cloudflare"]; c.Env["TUNNEL_TOKEN"] != "run-token" || slices.Contains(c.Cmd, "run-token") {
		t.Errorf("the run token goes in the environment, never the command line: %+v", c)
	}
	ts := cases["tailscale"]
	if ts.Env["TS_USERSPACE"] != "true" || ts.StateVolume != "es-tunnel-abcdefghijkl-state" || ts.Env["TS_HOSTNAME"] != "example-stage-web" {
		t.Errorf("tailscale: %+v", ts)
	}
	var serve map[string]any
	if err := json.Unmarshal(ts.Files["serve.json"], &serve); err != nil || serve["AllowFunnel"] == nil {
		t.Errorf("a public Tailscale link turns Funnel on: %s %v", ts.Files["serve.json"], err)
	}
	if private := Tailscale(id, "k", "h", false, 11000); strings.Contains(string(private.Files["serve.json"]), "AllowFunnel") || private.Hash == ts.Hash {
		t.Error("a private link must not use Funnel, and must differ from the public one")
	}
	ng := cases["ngrok"]
	if ng.Env["NGROK_AUTHTOKEN"] != "" || !strings.Contains(string(ng.Files["ngrok.yml"]), `authtoken: "ngrok-token"`) || !slices.Contains(ng.Cmd, "https://stage.ngrok.app") {
		t.Errorf("ngrok: the token goes into its own configuration file, so its start script writes nothing: %+v %s", ng, ng.Files["ngrok.yml"])
	}
	if cu := cases["custom"]; cu.Env["ENCLAVE_UPSTREAM"] != "http://es-system-edge:11000" || cu.Env["K"] != "v" {
		t.Errorf("custom: %+v", cu.Env)
	}
	if a, b := Quick(id, 11000), Quick(id, 11000); a.Hash != b.Hash || a.Hash == Quick(id, 11001).Hash {
		t.Error("the hash names the configuration")
	}
}

func TestRelay(t *testing.T) {
	r := Relay("tun_relayrelayrelayr", "mch_edge", "10.89.0.5", 12000, 12001)
	if r.Name != "es-relay-relayrelayre" || r.Machine != "mch_edge" || r.Network != engine.NetworkName {
		t.Errorf("relay %+v", r)
	}
	if !slices.Equal(r.Publish, []engine.PublishedPort{{Container: 8080, Host: 80, HostIP: "0.0.0.0"}, {Container: 8443, Host: 443, HostIP: "0.0.0.0"}}) {
		t.Errorf("a relay publishes 80 and 443 to unprivileged ports: %+v", r.Publish)
	}
	cfg := string(r.Files["haproxy.cfg"])
	for _, want := range []string{"mode tcp", "bind :8080", "bind :8443", "server edge 10.89.0.5:12000 send-proxy-v2 check", "server edge 10.89.0.5:12001 send-proxy-v2 check"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("relay configuration lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "mode http") || strings.Contains(cfg, "ssl") {
		t.Error("a relay must never decrypt (EDG-04)")
	}
}

func TestReadingWhatAgentsReport(t *testing.T) {
	logs := []string{"2026-10-24T11:52:52Z INF Requesting new quick Tunnel on trycloudflare.com...",
		"2026-10-24T11:52:58Z INF |  https://attractions-million-rates-divine.trycloudflare.com                                |",
		"2026-10-24T11:52:59Z INF Registered tunnel connection connIndex=0"}
	if u := QuickURL(logs); u != "https://attractions-million-rates-divine.trycloudflare.com" {
		t.Errorf("quick URL %q", u)
	}
	if u := QuickHostname([]byte(`{"hostname":"attractions-million-rates-divine.trycloudflare.com"}`)); u != "https://attractions-million-rates-divine.trycloudflare.com" {
		t.Errorf("quick hostname %q", u)
	}
	if n := ReadyConnections([]byte(`{"status":200,"readyConnections":1,"connectorId":"86dd"}`)); n != 1 {
		t.Errorf("ready connections %d", n)
	}
	if n := ReadyConnections([]byte(`not json`)); n != 0 {
		t.Error("unreadable answers mean no connection")
	}
	bad := []string{`{"t":"2026-10-24T11:53:44+0000","lvl":"eror","msg":"failed to reconnect session","obj":"tunnels.session","err":"authentication failed: The authtoken you specified does not look like a proper ngrok authtoken.\nInstructions to install your authtoken are on your ngrok dashboard:\nhttps://dashboard.ngrok.com/get-started/your-authtoken\r\n\r\nERR_NGROK_105\r\n"}`}
	if u, why := NgrokURL(bad); u != "" || why != "The authtoken you specified does not look like a proper ngrok authtoken." {
		t.Errorf("ngrok failure: %q %q", u, why)
	}
	good := []string{`{"lvl":"info","msg":"started tunnel","name":"command_line","addr":"http://es-system-edge:11000","url":"https://abc.ngrok-free.app"}`}
	if u, why := NgrokURL(good); u != "https://abc.ngrok-free.app" || why != "" {
		t.Errorf("ngrok url: %q %q", u, why)
	}
	if u := NgrokTunnels([]byte(`{"tunnels":[{"public_url":"https://abc.ngrok-free.app","proto":"https"}]}`)); u != "https://abc.ngrok-free.app" {
		t.Errorf("ngrok tunnels %q", u)
	}
	st, err := ParseTailscaleStatus(`{"BackendState":"Running","CertDomains":["web.tail1234.ts.net"],"Self":{"DNSName":"web.tail1234.ts.net.","Online":true,"CapMap":{"funnel":null,"https":null}}}`)
	if err != nil || !st.Running || !st.Online || !st.HTTPS || !st.Funnel || st.DNSName != "web.tail1234.ts.net" {
		t.Errorf("tailscale status %+v %v", st, err)
	}
	st, _ = ParseTailscaleStatus(`{"BackendState":"NeedsLogin","Self":{"DNSName":"","Online":false}}`)
	if st.Running || st.HTTPS || st.Funnel {
		t.Errorf("logged-out status %+v", st)
	}
	if why := TailscaleFailure([]string{"boot: Running 'tailscale up'", "backend error: invalid key: API key does not exist"}); !strings.Contains(why, "refused the auth key") {
		t.Errorf("tailscale failure %q", why)
	}
}
