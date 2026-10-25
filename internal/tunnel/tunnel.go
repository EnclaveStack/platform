// Package tunnel builds the connectors of public access and reads what they report (L2-014, ADR-051,
// ADR-052). It decides nothing about the store: the reconciler hands it what a tunnel needs and gets back
// the container to run, then hands it the container's logs and status to learn the address and state.
package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
)

// Images of the providers' agents, pinned by their multi-arch index digests (ADR-013, ADR-051).
const (
	// CloudflaredImage is cloudflared 2026.10.3: quick links and Cloudflare tunnels.
	CloudflaredImage = "cloudflare/cloudflared@sha256:faa8e0a1475f137c910be6025f43c8e4092fdd35461b31e19fd1390657a1ff39"
	// TailscaleImage is Tailscale 1.104.2.
	TailscaleImage = "tailscale/tailscale@sha256:b11583ad2e93e4671128538c88cf11f460444a5d7e449311dd1933a400ab849d"
	// NgrokImage is the ngrok agent 3.39.13.
	NgrokImage = "ngrok/ngrok@sha256:a51a7637a70be573615b01852af0419c4ab9dc857601b3d61ea2a082be3ec0d3"
	// RelayImage is the load balancers' HAProxy (ADR-022), in TCP mode.
	RelayImage = lb.Image
)

// Ports inside connectors.
const (
	CloudflaredMetricsPort = 2000
	NgrokAPIPort           = 4040
	// RelayHTTPPort and RelayHTTPSPort are where a relay listens inside its container; the machine's 80
	// and 443 are published to them, so it needs no capability to bind low ports.
	RelayHTTPPort  = 8080
	RelayHTTPSPort = 8443
	// TailscaleSocket is where the Tailscale container's daemon listens.
	TailscaleSocket = "/tmp/tailscaled.sock"
)

// Connector settings shared by every provider.
const (
	filesDir         = "/etc/enclave"
	connectorMemory  = 128
	tailscaleMemory  = 192
	tailscaleState   = "/var/lib/tailscale"
	tailscaleServeJS = "serve.json"
)

// Upstream is the edge listener a connector sends traffic to: the edge's name on the tunnel network and
// the tunnel's own port (ADR-051).
func Upstream(port int) string {
	return "http://" + net.JoinHostPort(engine.EdgeName, strconv.Itoa(port))
}

func connector(tunnelID string) engine.Connector {
	return engine.Connector{Name: engine.ConnectorName(tunnelID), TunnelID: tunnelID, Network: engine.TunnelNetwork, MemoryMiB: connectorMemory}
}

// Quick is a quick link's connector: cloudflared asks trycloudflare.com for a random address (TUN-04).
func Quick(tunnelID string, port int) engine.Connector {
	c := connector(tunnelID)
	c.Image = CloudflaredImage
	c.Cmd = []string{"tunnel", "--no-autoupdate", "--metrics", "0.0.0.0:" + strconv.Itoa(CloudflaredMetricsPort), "--url", Upstream(port)}
	return withHash(c)
}

// Cloudflare is a Cloudflare tunnel's connector, run with the tunnel's token (TUN-02). The routing rules
// live at Cloudflare.
func Cloudflare(tunnelID, token string) engine.Connector {
	c := connector(tunnelID)
	c.Image = CloudflaredImage
	c.Cmd = []string{"tunnel", "--no-autoupdate", "--metrics", "0.0.0.0:" + strconv.Itoa(CloudflaredMetricsPort), "run"}
	c.Env = map[string]string{"TUNNEL_TOKEN": token}
	return withHash(c)
}

// Tailscale is a Tailscale link's connector: a device of the user's tailnet in userspace networking (no
// capability needed), serving the edge listener over HTTPS on 443, to the tailnet or, public, to the
// internet with Funnel (TUN-12). Its state volume keeps the device's identity and name.
func Tailscale(tunnelID, authKey, hostname string, public bool, port int) engine.Connector {
	c := connector(tunnelID)
	c.Image, c.MemoryMiB = TailscaleImage, tailscaleMemory
	c.Env = map[string]string{"TS_AUTHKEY": authKey, "TS_HOSTNAME": hostname, "TS_USERSPACE": "true", "TS_STATE_DIR": tailscaleState,
		"TS_SERVE_CONFIG": filesDir + "/" + tailscaleServeJS, "TS_SOCKET": TailscaleSocket, "TS_AUTH_ONCE": "true"}
	c.Files, c.FilesDir = map[string][]byte{tailscaleServeJS: ServeConfig(port, public)}, filesDir
	c.StateVolume, c.StatePath = engine.ConnectorVolume(tunnelID), tailscaleState
	c.Tmpfs = []string{"/tmp", "/var/run/tailscale"}
	return withHash(c)
}

// ServeConfig is Tailscale's serve configuration: HTTPS on 443 for the device's own name, proxied to the
// edge listener, and Funnel on it when public.
func ServeConfig(port int, public bool) []byte {
	host := "${TS_CERT_DOMAIN}:443"
	cfg := map[string]any{
		"TCP": map[string]any{"443": map[string]any{"HTTPS": true}},
		"Web": map[string]any{host: map[string]any{"Handlers": map[string]any{"/": map[string]any{"Proxy": Upstream(port)}}}},
	}
	if public {
		cfg["AllowFunnel"] = map[string]any{host: true}
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return b
}

// Ngrok is an ngrok link's connector. Its own configuration file carries the token, so the image's start
// script writes nothing and the root stays read-only (TUN-13).
func Ngrok(tunnelID, token, domain string, port int) engine.Connector {
	c := connector(tunnelID)
	c.Image = NgrokImage
	cfg := fmt.Sprintf("version: 3\nagent:\n  authtoken: %s\n  web_addr: 0.0.0.0:%d\n", yamlQuote(token), NgrokAPIPort)
	c.Files, c.FilesDir = map[string][]byte{"ngrok.yml": []byte(cfg)}, filesDir
	c.Env = map[string]string{"NGROK_CONFIG": filesDir + "/ngrok.yml"}
	c.Cmd = []string{"http", Upstream(port), "--log", "stdout", "--log-format", "json"}
	if domain != "" {
		c.Cmd = append(c.Cmd, "--url", "https://"+domain)
	}
	return withHash(c)
}

// Custom runs the user's own connector image with ENCLAVE_UPSTREAM set (TUN-14). env holds the plain and
// resolved secret variables.
func Custom(tunnelID, image string, cmd []string, env map[string]string, port int) engine.Connector {
	c := connector(tunnelID)
	c.Image, c.Cmd = image, slices.Clone(cmd)
	c.Env = map[string]string{}
	for k, v := range env {
		c.Env[k] = v
	}
	c.Env["ENCLAVE_UPSTREAM"] = Upstream(port)
	c.Tmpfs = []string{"/tmp"}
	return withHash(c)
}

// Relay is a relay's HAProxy on its edge machine: the machine's 80 and 443 passed, still encrypted, to the
// edge proxy's relay listeners at edgeAddr, each connection with a PROXY protocol v2 header (ADR-052).
func Relay(tunnelID, machineID, edgeAddr string, httpPort, httpsPort int) engine.Connector {
	c := engine.Connector{Name: engine.RelayName(tunnelID), TunnelID: tunnelID, Machine: machineID, Image: RelayImage,
		Network: engine.NetworkName, MemoryMiB: connectorMemory, FilesDir: "/usr/local/etc/haproxy",
		Publish: []engine.PublishedPort{{Container: RelayHTTPPort, Host: 80, HostIP: "0.0.0.0"}, {Container: RelayHTTPSPort, Host: 443, HostIP: "0.0.0.0"}},
		Tmpfs:   []string{"/tmp", "/var/lib/haproxy"}}
	c.Files = map[string][]byte{"haproxy.cfg": RelayConfig(edgeAddr, httpPort, httpsPort)}
	return withHash(c)
}

// RelayConfig is the relay's HAProxy configuration: two TCP frontends and their backends, health-checked
// every 2 s; one good check brings a listener back, so a relay that started first serves within seconds.
func RelayConfig(edgeAddr string, httpPort, httpsPort int) []byte {
	var b strings.Builder
	b.WriteString("global\n  maxconn 4096\n\ndefaults\n  mode tcp\n  timeout connect 5s\n  timeout client 1h\n  timeout server 1h\n\n")
	for _, p := range []struct {
		name     string
		listen   int
		upstream int
	}{{"http", RelayHTTPPort, httpPort}, {"https", RelayHTTPSPort, httpsPort}} {
		fmt.Fprintf(&b, "frontend %s\n  bind :%d\n  default_backend edge_%s\n\n", p.name, p.listen, p.name)
		fmt.Fprintf(&b, "backend edge_%s\n  server edge %s send-proxy-v2 check inter 2s rise 1 fall 3\n\n", p.name, net.JoinHostPort(edgeAddr, strconv.Itoa(p.upstream)))
	}
	return []byte(b.String())
}

// withHash names the configuration, so a change replaces the container.
func withHash(c engine.Connector) engine.Connector {
	b, _ := json.Marshal(struct {
		Image, Network       string
		Cmd                  []string
		Env                  map[string]string
		Files                map[string][]byte
		Publish              []engine.PublishedPort
		Machine, StateVolume string
		Tmpfs                []string
		MemoryMiB            int
	}{c.Image, c.Network, c.Cmd, c.Env, c.Files, c.Publish, c.Machine, c.StateVolume, c.Tmpfs, c.MemoryMiB})
	sum := sha256.Sum256(b)
	c.Hash = hex.EncodeToString(sum[:8])
	return c
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid YAML double-quoted scalar
	return string(b)
}

var (
	quickURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
)

// QuickURL finds the address cloudflared announced, last one first (a restart announces a new one).
func QuickURL(logs []string) string {
	for i := len(logs) - 1; i >= 0; i-- {
		if u := quickURL.FindString(logs[i]); u != "" {
			return u
		}
	}
	return ""
}

// QuickHostname reads cloudflared's /quicktunnel answer.
func QuickHostname(body []byte) string {
	var r struct {
		Hostname string `json:"hostname"`
	}
	if json.Unmarshal(body, &r) != nil || r.Hostname == "" {
		return ""
	}
	return "https://" + r.Hostname
}

// ReadyConnections reads cloudflared's /ready answer: how many connections to Cloudflare are open.
func ReadyConnections(body []byte) int {
	var r struct {
		ReadyConnections int `json:"readyConnections"`
	}
	if json.Unmarshal(body, &r) != nil {
		return 0
	}
	return r.ReadyConnections
}

// NgrokURL finds the address ngrok announced in its JSON log, and an authentication failure if it had one.
func NgrokURL(logs []string) (url, failure string) {
	for i := len(logs) - 1; i >= 0; i-- {
		var l struct {
			Msg string `json:"msg"`
			URL string `json:"url"`
			Err string `json:"err"`
		}
		if json.Unmarshal([]byte(logs[i]), &l) != nil {
			continue
		}
		switch {
		case l.Msg == "started tunnel" && l.URL != "":
			return l.URL, ""
		case failure == "" && strings.Contains(l.Err, "authentication failed"):
			failure = firstLine(strings.TrimPrefix(l.Err, "authentication failed: "))
		case failure == "" && strings.Contains(l.Err, "ERR_NGROK"):
			failure = firstLine(l.Err)
		}
	}
	return "", failure
}

// NgrokTunnels reads the ngrok agent's /api/tunnels answer: the first public URL.
func NgrokTunnels(body []byte) string {
	var r struct {
		Tunnels []struct {
			PublicURL string `json:"public_url"`
		} `json:"tunnels"`
	}
	if json.Unmarshal(body, &r) != nil {
		return ""
	}
	for _, t := range r.Tunnels {
		if strings.HasPrefix(t.PublicURL, "https://") {
			return t.PublicURL
		}
	}
	return ""
}

// TailscaleStatus is what `tailscale status --json` says about the device.
type TailscaleStatus struct {
	// Running is the daemon up and logged in.
	Running bool
	// DNSName is the device's name in the tailnet, without the trailing dot.
	DNSName string
	Online  bool
	// HTTPS is whether the tailnet issues certificates; Funnel whether the device may use Funnel.
	HTTPS, Funnel bool
	// State is the daemon's state (Running, NeedsLogin, Starting…).
	State string
}

// ParseTailscaleStatus reads `tailscale status --json`.
func ParseTailscaleStatus(out string) (TailscaleStatus, error) {
	var s struct {
		BackendState string   `json:"BackendState"`
		CertDomains  []string `json:"CertDomains"`
		Self         *struct {
			DNSName string                     `json:"DNSName"`
			Online  bool                       `json:"Online"`
			CapMap  map[string]json.RawMessage `json:"CapMap"`
		} `json:"Self"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return TailscaleStatus{}, fmt.Errorf("read Tailscale's status: %w", err)
	}
	st := TailscaleStatus{State: s.BackendState, Running: s.BackendState == "Running", HTTPS: len(s.CertDomains) > 0}
	if s.Self != nil {
		st.DNSName, st.Online = strings.TrimSuffix(s.Self.DNSName, "."), s.Self.Online
		_, st.Funnel = s.Self.CapMap["funnel"]
		if _, ok := s.Self.CapMap["https://tailscale.com/cap/funnel"]; ok {
			st.Funnel = true
		}
	}
	return st, nil
}

// TailscaleStatusCmd and TailscaleLogoutCmd are the fixed commands run in a Tailscale connector.
var (
	TailscaleStatusCmd = []string{"tailscale", "--socket", TailscaleSocket, "status", "--json"}
	TailscaleLogoutCmd = []string{"tailscale", "--socket", TailscaleSocket, "logout"}
)

// TailscaleFailure finds why a Tailscale connector stopped, from its log.
func TailscaleFailure(logs []string) string {
	for i := len(logs) - 1; i >= 0; i-- {
		l := strings.ToLower(logs[i])
		switch {
		case strings.Contains(l, "invalid key"), strings.Contains(l, "auth key") && strings.Contains(l, "not valid"):
			return "Tailscale refused the auth key. Make a new reusable key in its admin console (Settings › Keys) and put it in the secret."
		case strings.Contains(l, "expired"):
			return "The Tailscale auth key has expired. Make a new reusable key in its admin console (Settings › Keys)."
		}
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(strings.TrimSuffix(s, "\r"))
}
