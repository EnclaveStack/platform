//go:build docker

package docker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"
	"golang.org/x/crypto/bcrypt"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/tunnel"
	"github.com/enclavestack/platform/pkg/api"
)

// Connectors and the edge's tunnel listeners on real Docker with real Caddy (ADR-051, ADR-053,
// L3-006 §14, L5-002 §6): a connector is hardened whatever runs in it, and reaches the app only through
// its listener and the gate.

func TestConnectorIsHardenedAndReachesTheAppOnlyThroughTheGate(t *testing.T) {
	d, _, _ := edgeDriver(t)
	ctx := context.Background()
	if _, err := d.cli.NetworkInspect(ctx, engine.TunnelNetwork, network.InspectOptions{}); err == nil {
		t.Skip("a tunnel network already exists on this machine")
	}
	t.Cleanup(func() { // after the connector, before edgeDriver's own cleanup: the network goes once nothing is on it
		ctx := context.Background()
		_ = d.remove(ctx, engine.EdgeName)
		_ = d.cli.NetworkDisconnect(ctx, engine.TunnelNetwork, engine.ProbeName, true)
		if err := d.cli.NetworkRemove(ctx, engine.TunnelNetwork); err != nil {
			t.Errorf("remove the tunnel network: %v", err)
		}
	})

	app := resource(api.KindApp, "it-gate-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	if _, _, err := deploy(t, d, app, api.AppSpec{Image: "traefik/whoami:v1.10", Port: 80, MemoryMiB: 64}, 1); err != nil {
		t.Fatal(err)
	}
	app.ServingRevision = 1

	id := "tun_it" + suffix()
	c := engine.Connector{Name: engine.ConnectorName(id), TunnelID: id, Image: "curlimages/curl:8.10.1", Cmd: []string{"sleep", "3600"},
		Network: engine.TunnelNetwork, Tmpfs: []string{"/tmp"}, Files: map[string][]byte{"serve.json": []byte(`{"ok":true}`)}, FilesDir: "/etc/connector", Hash: "h1"}
	t.Cleanup(func() { _ = d.RemoveConnector(context.Background(), c) })
	st, err := d.EnsureConnector(ctx, c)
	if err != nil {
		t.Fatalf("ensure connector: %v", err)
	}
	if !st.Exists || !st.Running || st.Address == "" {
		t.Fatalf("connector state %+v", st)
	}

	// Hardened (L3-006 §14): no capabilities, no privilege gain, a read-only root, bounded memory.
	in, err := d.inspect(ctx, c.Name)
	if err != nil || in == nil {
		t.Fatalf("inspect: %v", err)
	}
	h := in.HostConfig
	if !slices.Equal(h.CapDrop, []string{"ALL"}) || !h.ReadonlyRootfs || !slices.Contains(h.SecurityOpt, "no-new-privileges:true") || h.Memory != 128<<20 || h.Privileged {
		t.Errorf("hardening: capDrop %v readOnly %v securityOpt %v memory %d privileged %v", h.CapDrop, h.ReadonlyRootfs, h.SecurityOpt, h.Memory, h.Privileged)
	}
	if nets := in.NetworkSettings.Networks; len(nets) != 1 || nets[engine.TunnelNetwork] == nil {
		t.Errorf("a connector joins the tunnel network only: %v", keys(nets))
	}
	if out, code, err := d.ConnectorExec(ctx, c, []string{"cat", "/etc/connector/serve.json"}); err != nil || code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Errorf("files: %q %d %v", out, code, err)
	}
	if _, code, _ := d.ConnectorExec(ctx, c, []string{"touch", "/etc/written"}); code == 0 {
		t.Error("the root is writable")
	}
	// The same hash keeps the container; another one replaces it.
	if again, err := d.EnsureConnector(ctx, c); err != nil || !again.StartedAt.Equal(st.StartedAt) {
		t.Errorf("same hash restarted it: %v %v → %v", err, st.StartedAt, again.StartedAt)
	}

	// The listener: a password gate with an open path, trusting only this connector.
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	port := 11000 + int(time.Now().UnixNano()%1000)
	// A second listener with the sign-in gate, whose gate can't be reached: it must fail closed.
	cfg := engine.EdgeConfig{Gate: engine.GateConfig{Upstream: "10.255.255.1:7172", EdgeKey: "edge-key"}, Listeners: []engine.Listener{{
		TunnelID: id, Kind: engine.ListenerAppLink, Port: port, Network: engine.TunnelNetwork, Trusted: st.Address, ClientIPHeader: "X-Forwarded-For",
		Routes: []engine.GatedRoute{{Upstream: engine.ContainerName(app) + ":80", Target: id, PasswordUser: "team", PasswordHash: string(hash),
			Gate: api.Gate{Mode: api.GatePassword, OpenPaths: []string{"/webhooks/"}}}},
	}, {
		TunnelID: id + "b", Kind: engine.ListenerAppLink, Port: port + 1, Network: engine.TunnelNetwork, Trusted: st.Address, ClientIPHeader: "X-Forwarded-For",
		Routes: []engine.GatedRoute{{Upstream: engine.ContainerName(app) + ":80", Target: id + "b", Gate: api.Gate{Mode: api.GateSignin}}},
	}}}
	if err := d.ConfigureEdge(ctx, cfg); err != nil {
		t.Fatalf("configure edge: %v", err)
	}
	edge, err := d.EdgeAddress(ctx, engine.TunnelNetwork)
	if err != nil || edge == "" {
		t.Fatalf("edge address on the tunnel network: %q %v", edge, err)
	}
	url := "http://" + edge + ":" + strconv.Itoa(port)
	curl := func(args ...string) (string, string) {
		t.Helper()
		var out string
		var code int
		deadline := time.Now().Add(30 * time.Second)
		for {
			out, code, err = d.ConnectorExec(ctx, c, append([]string{"curl", "-s", "-m", "5", "-w", "\n%{http_code}"}, args...))
			if (err == nil && code == 0) || time.Now().After(deadline) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil || code != 0 {
			t.Fatalf("curl %v: %q %d %v", args, out, code, err)
		}
		i := strings.LastIndexByte(out, '\n') // an empty body leaves only the status
		return out[:max(i, 0)], strings.TrimSpace(out[i+1:])
	}
	if _, status := curl(url + "/"); status != "401" {
		t.Errorf("without the password: %s", status)
	}
	if _, status := curl("-u", "team:wrong", url+"/"); status != "401" {
		t.Errorf("a wrong password: %s", status)
	}
	body, status := curl("-u", "team:correct horse", "-H", "X-Enclave-User: admin@example.com", "-H", "X-Forwarded-For: 203.0.113.50", url+"/api")
	var echo struct {
		Headers map[string][]string `json:"headers"`
	}
	_ = json.Unmarshal([]byte(body), &echo)
	if status != "200" || echo.Headers["X-Forwarded-Proto"][0] != "https" {
		t.Errorf("with the password: %s %s", status, body)
	}
	if _, spoofed := echo.Headers["X-Enclave-User"]; spoofed {
		t.Errorf("a visitor's X-Enclave-* header reached the app: %v", echo.Headers)
	}
	if got := echo.Headers["X-Forwarded-For"]; len(got) == 0 || !strings.Contains(got[0], "203.0.113.50") {
		t.Errorf("the trusted connector's visitor address: %v", got)
	}
	if _, status := curl(url + "/webhooks/stripe"); status != "200" {
		t.Errorf("an open path skips the gate: %s", status)
	}
	start := time.Now()
	if body, status := curl("http://" + edge + ":" + strconv.Itoa(port+1) + "/"); (status != "502" && status != "504") || strings.Contains(body, "nginx") || strings.Contains(body, "Hostname") {
		t.Errorf("an unreachable gate let the request through or hung: %s %q", status, body)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("an unreachable gate took %s to fail closed", took)
	}

	// Removing it takes its files volume too.
	if err := d.RemoveConnector(ctx, c); err != nil {
		t.Fatal(err)
	}
	if in, _ := d.inspect(ctx, c.Name); in != nil {
		t.Error("the connector survived removal")
	}
	if _, err := d.cli.VolumeInspect(ctx, filesVolume(c)); err == nil {
		t.Error("the files volume survived removal")
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// A relay on real HAProxy passes visitors, still encrypted, to the edge's relay listener with a PROXY
// protocol header (ADR-052): the edge ends TLS with the domain's certificate and the app learns the
// visitor's address, not the relay's.
func TestRelayPassesEncryptedTrafficWithTheVisitorsAddress(t *testing.T) {
	d, _, _ := edgeDriver(t)
	ctx := context.Background()
	app := resource(api.KindApp, "it-relay-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	if _, _, err := deploy(t, d, app, api.AppSpec{Image: "traefik/whoami:v1.10", Port: 80, MemoryMiB: 64}, 1); err != nil {
		t.Fatal(err)
	}
	app.ServingRevision = 1
	up := engine.ContainerName(app) + ":80"
	// The edge first runs for something else, so it has its address for the relay to send to.
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{Routes: []engine.Route{{Hostname: app.Name + ".localhost", Upstream: up, TLS: api.TLSOff}}}); err != nil {
		t.Fatalf("configure edge: %v", err)
	}
	edge, err := d.EdgeAddress(ctx, engine.NetworkName)
	if err != nil || edge == "" {
		t.Fatalf("edge address: %q %v", edge, err)
	}

	id := "tun_it" + suffix()
	listenHTTP := 11000 + int(time.Now().UnixNano()%500)*2
	listenHTTPS := listenHTTP + 1
	relay := tunnel.Relay(id, "", edge, listenHTTP, listenHTTPS)
	hostHTTP, hostHTTPS := freePort(t), freePort(t)
	relay.Publish = []engine.PublishedPort{{Container: tunnel.RelayHTTPPort, Host: hostHTTP, HostIP: "127.0.0.1"}, {Container: tunnel.RelayHTTPSPort, Host: hostHTTPS, HostIP: "127.0.0.1"}}
	t.Cleanup(func() { _ = d.RemoveConnector(context.Background(), relay) })
	st, err := d.EnsureConnector(ctx, relay)
	if err != nil || !st.Running || st.Address == "" {
		t.Fatalf("relay: %+v %v", st, err)
	}

	host := "stage.example.test"
	chain, key := selfSigned(t, host)
	cfg := engine.EdgeConfig{
		Routes:       []engine.Route{{Hostname: app.Name + ".localhost", Upstream: up, TLS: api.TLSOff}},
		Certificates: []engine.LoadedCertificate{{Hostname: host, ChainPEM: chain, KeyPEM: key}},
		Listeners: []engine.Listener{{TunnelID: id, Kind: engine.ListenerRelay, Port: listenHTTP, HTTPSPort: listenHTTPS, Network: engine.NetworkName, Trusted: st.Address,
			Routes: []engine.GatedRoute{{Hostname: host, Upstream: up, Target: "dom_it", Gate: api.Gate{Mode: api.GateNone}, TLS: true, DNS01: true}}}},
	}
	if err := d.ConfigureEdge(ctx, cfg); err != nil {
		t.Fatalf("configure edge with the relay listener: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(chain)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				port := hostHTTP
				if strings.HasSuffix(addr, ":443") {
					port = hostHTTPS
				}
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			}}}
	// The relay started before the listener, so its health checks marked both ports down; one good
	// check (every 2 s) brings each back.
	var resp *http.Response
	deadline := time.Now().Add(6 * time.Second)
	for _, url := range []string{"http://" + host + "/", "https://" + host + "/api"} {
		for {
			resp, err = client.Get(url)
			if err == nil {
				resp.Body.Close()
			}
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if err == nil {
		resp, err = client.Get("https://" + host + "/api")
	}
	if err != nil {
		t.Fatalf("https through the relay: %v", err)
	}
	var echo struct {
		Host    string              `json:"host"`
		Headers map[string][]string `json:"headers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&echo)
	resp.Body.Close()
	if resp.StatusCode != 200 || echo.Host != host {
		t.Fatalf("through the relay: %d %+v", resp.StatusCode, echo)
	}
	if xff := strings.Join(echo.Headers["X-Forwarded-For"], ","); xff == "" || strings.Contains(xff, st.Address) {
		t.Errorf("the app sees the relay, not the visitor: X-Forwarded-For %q (relay %s)", xff, st.Address)
	}
	// Plain HTTP through the relay is sent to HTTPS.
	resp, err = client.Get("http://" + host + "/path")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 308 || resp.Header.Get("Location") != "https://"+host+"/path" {
		t.Errorf("http through the relay: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Another hostname the relay doesn't serve gets nothing.
	resp, err = client.Get("http://other.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("an unknown hostname: %d", resp.StatusCode)
	}
}

func selfSigned(t *testing.T, host string) (chain, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}
