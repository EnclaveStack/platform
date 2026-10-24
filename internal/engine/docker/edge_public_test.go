package docker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The tunnels' listeners as the edge renders them (ADR-051 – ADR-053): each bound to its network, trusting
// only its connector, with the gate in front of every route and visitors' X-Enclave-* headers dropped.

func proxyTo(up string) obj { return obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": up}}} }

func TestPublicServersRenderEachTunnelsListener(t *testing.T) {
	cfg := engine.EdgeConfig{Gate: engine.GateConfig{Upstream: "host.docker.internal:7172", EdgeKey: "k"}, Listeners: []engine.Listener{
		{TunnelID: "tun_relay", Kind: engine.ListenerRelay, Port: 11010, HTTPSPort: 11011, Network: engine.NetworkName, Trusted: "10.90.0.5",
			Routes: []engine.GatedRoute{
				{Hostname: "b.example.com", Upstream: "app-b:80", Target: "dom_b", Gate: api.Gate{Mode: api.GateSignin}, TLS: true},
				{Hostname: "a.example.com", Upstream: "app-a:80", Target: "dom_a", Gate: api.Gate{Mode: api.GateNone}, TLS: true, DNS01: true},
			}},
		{TunnelID: "tun_link", Kind: engine.ListenerAppLink, Port: 11000, Network: engine.TunnelNetwork, Trusted: "172.30.0.7", ClientIPHeader: "Cf-Connecting-IP",
			Routes: []engine.GatedRoute{{Upstream: "web:80", Target: "tun_link", Gate: api.Gate{Mode: api.GatePassword, OpenPaths: []string{"/webhooks/"}}, PasswordUser: "team", PasswordHash: "$2a$hash"}}},
	}}
	addrs := map[string]string{engine.NetworkName: "10.90.0.2", engine.TunnelNetwork: "172.30.0.2"}
	servers, acme, loaded, err := publicServers(cfg, addrs, proxyTo)
	if err != nil {
		t.Fatal(err)
	}
	// Certificates: the relay's DNS-01 route is loaded, the other comes over HTTP-01 on the relay's port.
	if len(acme[11010]) != 1 || acme[11010][0] != "b.example.com" || len(loaded) != 1 || loaded[0] != "a.example.com" {
		t.Errorf("acme %v loaded %v", acme, loaded)
	}
	raw, _ := json.Marshal(servers)
	var got map[string]struct {
		Listen          []string          `json:"listen"`
		Wrappers        []map[string]any  `json:"listener_wrappers"`
		Trusted         map[string]any    `json:"trusted_proxies"`
		ClientIPHeaders []string          `json:"client_ip_headers"`
		Routes          []json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	link, relay, relayTLS := got["tunnel-11000"], got["tunnel-11010"], got["tunnel-11010-tls"]
	if link.Listen[0] != "172.30.0.2:11000" || relay.Listen[0] != "10.90.0.2:11010" || relayTLS.Listen[0] != "10.90.0.2:11011" {
		t.Errorf("binds: %v %v %v", link.Listen, relay.Listen, relayTLS.Listen)
	}
	// An app link believes only its connector's visitor header; a relay only its relay's PROXY header.
	if link.ClientIPHeaders[0] != "Cf-Connecting-IP" || !strings.Contains(string(mustJSON(link.Trusted)), "172.30.0.7/32") {
		t.Errorf("app link trust: %v %v", link.ClientIPHeaders, link.Trusted)
	}
	if w := mustJSON(relayTLS.Wrappers); !strings.Contains(w, `"proxy_protocol"`) || !strings.Contains(w, "10.90.0.5/32") || !strings.Contains(w, `"tls"`) ||
		strings.Index(w, "proxy_protocol") > strings.Index(w, `"tls"`) {
		t.Errorf("relay wrappers: %s", w)
	}
	// Exact hostnames route in order, then nothing else is served.
	routes := mustJSON(relayTLS.Routes)
	if strings.Index(routes, "a.example.com") > strings.Index(routes, "b.example.com") || !strings.Contains(routes, "No site is configured") {
		t.Errorf("relay routes: %s", routes)
	}
	// The sign-in gate checks with the gate; every route first drops visitors' X-Enclave-* headers.
	if !strings.Contains(routes, `"uri":"/check"`) || !strings.Contains(routes, `"X-Enclave-Edge-Key":["k"]`) || strings.Count(routes, `"delete":["X-Enclave-*"]`) != 2 ||
		!strings.Contains(routes, `"response_header_timeout":"2s"`) {
		t.Errorf("relay gate: %s", routes)
	}
	// The password gate: an open path straight to the app, then basic auth.
	lr := mustJSON(link.Routes)
	if !strings.Contains(lr, `"path":["/webhooks/*"]`) || !strings.Contains(lr, `"http_basic"`) || !strings.Contains(lr, `"username":"team"`) || strings.Contains(lr, "/check") {
		t.Errorf("app link routes: %s", lr)
	}
	// The plain relay port only redirects to HTTPS.
	if r := mustJSON(relay.Routes); !strings.Contains(r, `"status_code":308`) || strings.Contains(r, "reverse_proxy") {
		t.Errorf("relay http: %s", r)
	}
}

func TestPublicServersRefuseAListenerOffTheEdgesNetworks(t *testing.T) {
	cfg := engine.EdgeConfig{Listeners: []engine.Listener{{TunnelID: "tun_x", Kind: engine.ListenerCloudflare, Port: 11000, Network: engine.TunnelNetwork, Trusted: "172.30.0.7"}}}
	if _, _, _, err := publicServers(cfg, map[string]string{}, proxyTo); err == nil || !strings.Contains(err.Error(), "isn't on the network") {
		t.Errorf("error %v", err)
	}
}

func TestAnUnknownGateModeFailsClosed(t *testing.T) {
	h := mustJSON(gatedHandle(engine.GatedRoute{Upstream: "web:80", Target: "t", Gate: api.Gate{Mode: "bogus"}}, engine.GateConfig{Upstream: "g:1", EdgeKey: "k"}, proxyTo))
	if !strings.Contains(h, `"uri":"/check"`) {
		t.Errorf("an unknown mode went straight to the app: %s", h)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAListenerWaitsForItsConnectorsAddress(t *testing.T) {
	// A relay not started yet: no address to believe, so no listener (an empty one would trust anyone, or
	// be refused by the proxy and take every other site down with it).
	cfg := engine.EdgeConfig{Listeners: []engine.Listener{{TunnelID: "tun_relay", Kind: engine.ListenerRelay, Port: 12000, HTTPSPort: 12001, Network: engine.NetworkName,
		Routes: []engine.GatedRoute{{Hostname: "a.example.com", Upstream: "app-a:80", Target: "dom_a", Gate: api.Gate{Mode: api.GateSignin}, TLS: true}}}}}
	servers, acme, loaded, err := publicServers(cfg, map[string]string{engine.NetworkName: "10.90.0.2"}, proxyTo)
	if err != nil || len(servers) != 0 || len(acme) != 0 || len(loaded) != 0 {
		t.Fatalf("servers %v acme %v loaded %v err %v", servers, acme, loaded, err)
	}
}
