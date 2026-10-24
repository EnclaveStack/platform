package engine

import (
	"strings"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Public access (L2-014, ADR-051 – ADR-053): connectors that carry traffic from the internet, and the edge
// proxy's listeners that receive it.

// Names on the engine.
const (
	// TunnelNetwork holds the connectors and the edge proxy, nothing else (ADR-051).
	TunnelNetwork = "es-system-tunnel"
	// ConnectorPrefix and RelayPrefix start connector and relay container names.
	ConnectorPrefix = "es-tunnel-"
	RelayPrefix     = "es-relay-"
	// GatePort is the control plane's gate listener (ADR-053); GateHost is how the edge reaches it.
	GatePort = 7172
	GateHost = "host.docker.internal"
)

// Listener kinds: what sends traffic to a listener, which decides how the visitor's address is learned.
const (
	// ListenerAppLink receives one app link's traffic: every request goes to its one route.
	ListenerAppLink = "app_link"
	// ListenerCloudflare receives a Cloudflare tunnel's public domains, plain HTTP (TLS ends at Cloudflare).
	ListenerCloudflare = "cloudflare"
	// ListenerRelay receives a relay's ports 80 and 443 with a PROXY protocol header; TLS ends here.
	ListenerRelay = "relay"
)

// Connector is a container that carries public traffic: a tunnel's connector on the main machine, or a
// relay on an edge machine (ADR-051, ADR-052). Ensure is idempotent: a connector whose Hash changed is
// made anew.
type Connector struct {
	Name     string
	TunnelID string
	// Machine is where it runs: "" for the main machine, an edge machine's ID for a relay.
	Machine string
	Image   string
	Cmd     []string
	Env     map[string]string
	// Files are written into FilesDir before it starts (Tailscale's serve configuration, a relay's HAProxy file).
	Files    map[string][]byte
	FilesDir string
	// StateVolume, mounted at StatePath, keeps what must outlive the container (a Tailscale device's identity).
	StateVolume string
	StatePath   string
	// Network is the engine network it joins: TunnelNetwork, or NetworkName for a relay.
	Network string
	// Publish binds container ports on the machine (a relay's 80 and 443).
	Publish []PublishedPort
	// Tmpfs are writable directories on an otherwise read-only root.
	Tmpfs     []string
	MemoryMiB int
	// Hash identifies the configuration; a running connector with another Hash is replaced.
	Hash string
}

// PublishedPort binds a container port on the machine.
type PublishedPort struct {
	Container int
	Host      int
	HostIP    string
}

// ConnectorState is what the engine sees of a connector.
type ConnectorState struct {
	Exists    bool
	Running   bool
	StartedAt time.Time
	Restarts  int
	// Address is its address on its network, which the edge trusts for the visitor's address.
	Address string
	// Logs are its last lines, oldest first (where providers announce the address they gave).
	Logs []string
}

// Listener is one tunnel's server on the edge proxy (ADR-051): it receives only that tunnel's traffic.
type Listener struct {
	TunnelID string
	Kind     string
	// Port is the HTTP listener; HTTPSPort the relay's TLS listener.
	Port      int
	HTTPSPort int
	// Network is the engine network whose address it binds: TunnelNetwork, or NetworkName for a relay.
	Network string
	// Trusted is the connector's or relay's address: the only source whose visitor address is believed.
	Trusted string
	// ClientIPHeader carries the visitor's address from a trusted connector ("" for a relay: PROXY protocol).
	ClientIPHeader string
	Routes         []GatedRoute
}

// GatedRoute is a public hostname (or, for an app link, every hostname) with its gate (GATE-01 – GATE-07).
type GatedRoute struct {
	// Hostname matches the request's host; "" matches any (an app link).
	Hostname string
	Upstream string
	// Target is the domain's or app link's resource ID, which the gate checks access against.
	Target string
	Gate   api.Gate
	// PasswordUser and PasswordHash (bcrypt) are a password gate's account.
	PasswordUser string
	PasswordHash string
	// TLS and DNS01 apply to relay routes, whose certificates the edge serves (ADR-052).
	TLS   bool
	DNS01 bool
}

// GateConfig is how the edge reaches the gate listener (ADR-053).
type GateConfig struct {
	// Upstream is host:port of the gate listener as the edge container reaches it.
	Upstream string
	// EdgeKey proves a request comes from the edge proxy.
	EdgeKey string
}

// ConnectorName is the container of a tunnel's connector; RelayName of its relay.
func ConnectorName(tunnelID string) string { return ConnectorPrefix + shortHash(tunnelID) }

// RelayName is the container of a relay tunnel on its edge machine.
func RelayName(tunnelID string) string { return RelayPrefix + shortHash(tunnelID) }

// ConnectorVolume keeps a connector's state across restarts (Tailscale's device identity).
func ConnectorVolume(tunnelID string) string { return ConnectorName(tunnelID) + "-state" }

// shortHash is the random part of an ID, at most 12 characters: names stay short and unique.
func shortHash(id string) string {
	if i := strings.LastIndexByte(id, '_'); i >= 0 {
		id = id[i+1:]
	}
	if len(id) > 12 {
		id = id[:12]
	}
	return strings.ToLower(id)
}
