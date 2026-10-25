//go:build docker

package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The edge proxy on real Docker with real Caddy (ADR-015, L2-005 DOM-01 – DOM-09, L5-002 §6).

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// edgeDriver runs the edge on free high ports. It skips when an edge already runs on this machine, so a
// developer's real edge is never touched, and removes everything it made.
func edgeDriver(t *testing.T) (*Driver, int, int) {
	t.Helper()
	d := fastDriver(t)
	if c, _ := d.inspect(context.Background(), engine.EdgeName); c != nil {
		t.Skip("an edge proxy already exists on this machine")
	}
	httpPort, httpsPort := freePort(t), freePort(t)
	d.opts.EdgeHTTPPort, d.opts.EdgeHTTPSPort = httpPort, httpsPort
	t.Cleanup(func() {
		ctx := context.Background()
		_ = d.remove(ctx, engine.EdgeName)
		_ = d.cli.VolumeRemove(ctx, edgeVolume, true)
	})
	return d, httpPort, httpsPort
}

// client resolves every hostname to the local edge, like DNS pointing at this machine would.
func edgeClient(t *testing.T, d *Driver, port int, trust bool) *http.Client {
	t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if trust {
		pem, err := d.RootCA(context.Background())
		if err != nil {
			t.Fatalf("root CA: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			t.Fatalf("root CA is not PEM: %q", pem)
		}
		cfg.RootCAs = pool
	}
	dial := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{TLSClientConfig: cfg, MaxIdleConnsPerHost: 16,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, p, _ := net.SplitHostPort(addr)
				if p == "443" || p == "80" {
					p = strconv.Itoa(port)
				}
				return dial.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", p))
			}}}
}

func TestEdgeServesDomainsWithInternalTLSAndDropsNothingDuringADeploy(t *testing.T) {
	d, httpPort, httpsPort := edgeDriver(t)
	ctx := context.Background()
	app := resource(api.KindApp, "it-edge-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	spec := api.AppSpec{Image: "traefik/whoami:v1.10", Port: 80, MemoryMiB: 64,
		HealthCheck: &api.HealthCheck{Type: api.HealthHTTP, Path: "/health", IntervalSeconds: 1, TimeoutSeconds: 1, Retries: 30}}
	if _, _, err := deploy(t, d, app, spec, 1); err != nil {
		t.Fatal(err)
	}
	app.ServingRevision = 1
	up := engine.ContainerName(app) + ":80"
	host, plain := app.Name+".localhost", app.Name+"-plain.localhost"
	cfg := engine.EdgeConfig{Routes: []engine.Route{
		{Hostname: host, Upstream: up, TLS: api.TLSInternal, RedirectHTTP: true},
		{Hostname: plain, Upstream: up, TLS: api.TLSOff},
	}}
	if err := d.ConfigureEdge(ctx, cfg); err != nil {
		t.Fatalf("configure edge: %v", err)
	}
	if st := d.EdgeStatus(ctx); !st.Available || st.HTTPSPort != httpsPort {
		t.Fatalf("edge status %+v", st)
	}

	// The internal certificate appears without any DNS.
	var cert engine.Certificate
	deadline := time.Now().Add(60 * time.Second)
	for cert.State != engine.CertIssued && time.Now().Before(deadline) {
		cert, _ = d.Certificate(ctx, host)
		time.Sleep(500 * time.Millisecond)
	}
	if cert.State != engine.CertIssued || !strings.Contains(cert.Issuer, "EnclaveStack Local CA") || time.Until(cert.NotAfter) <= 0 {
		t.Fatalf("certificate %+v", cert)
	}

	secure := edgeClient(t, d, httpsPort, true)
	resp, err := secure.Get("https://" + host + "/api")
	if err != nil {
		t.Fatalf("https through the edge with the EnclaveStack CA: %v", err)
	}
	var echo struct {
		Host    string              `json:"host"`
		Headers map[string][]string `json:"headers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&echo)
	resp.Body.Close()
	if resp.StatusCode != 200 || echo.Host != host || echo.Headers["X-Forwarded-Proto"][0] != "https" || len(echo.Headers["X-Forwarded-For"]) == 0 {
		t.Errorf("forwarding (DOM-09): status %d host %q headers %v", resp.StatusCode, echo.Host, echo.Headers)
	}

	plainClient := edgeClient(t, d, httpPort, false)
	resp, err = plainClient.Get("http://" + host + "/path?q=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 308 || resp.Header.Get("Location") != fmt.Sprintf("https://%s:%d/path?q=1", host, httpsPort) {
		t.Errorf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = plainClient.Get("http://" + plain + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("tls off route: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	resp, err = plainClient.Get("http://unknown.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 || !strings.Contains(string(body), "No site is configured") {
		t.Errorf("unknown host: %d %q", resp.StatusCode, body)
	}

	// A rolling deploy under continuous HTTPS load: no request may fail (DEP-06).
	loadCtx, stop := context.WithCancel(ctx)
	var ok, failed atomic.Int64
	var lastErr atomic.Value
	done := make(chan struct{})
	for w := 0; w < 4; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for loadCtx.Err() == nil {
				r, err := secure.Get("https://" + host + "/")
				if err == nil {
					_, _ = io.Copy(io.Discard, r.Body)
					r.Body.Close()
				}
				switch {
				case loadCtx.Err() != nil:
				case err == nil && r.StatusCode == 200:
					ok.Add(1)
				default:
					failed.Add(1)
					lastErr.Store(fmt.Sprint(err, r))
				}
			}
		}()
	}
	time.Sleep(time.Second)
	app.Generation = 2
	spec.Image = "traefik/whoami:v1.11"
	_, n, err := deploy(t, d, app, spec, 2)
	time.Sleep(time.Second)
	stop()
	for w := 0; w < 4; w++ {
		<-done
	}
	if err != nil {
		t.Fatalf("deploy under load: %v (%v)", err, n.events)
	}
	t.Logf("requests through the edge during the deploy: %d ok, %d failed", ok.Load(), failed.Load())
	if failed.Load() != 0 || ok.Load() == 0 {
		t.Errorf("domain traffic dropped during a rolling deploy: %d failed (last: %v)", failed.Load(), lastErr.Load())
	}

	// Root CA download, and the edge stops with the last route.
	if pem, err := d.RootCA(ctx); err != nil || !strings.Contains(string(pem), "BEGIN CERTIFICATE") {
		t.Errorf("root CA: %v", err)
	}
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{}); err != nil {
		t.Fatal(err)
	}
	if st := d.EdgeStatus(ctx); st.Available {
		t.Errorf("edge still running with no routes: %+v", st)
	}
}

func TestEdgePortConflictIsAnActionablePermanentError(t *testing.T) {
	d, _, _ := edgeDriver(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	d.opts.EdgeHTTPPort = busy.Addr().(*net.TCPAddr).Port
	err = d.ConfigureEdge(context.Background(), engine.EdgeConfig{Routes: []engine.Route{{Hostname: "x.localhost", Upstream: "nowhere:80", TLS: api.TLSOff}}})
	if err == nil || !engine.IsPermanent(err) || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("port conflict: permanent=%v %v", engine.IsPermanent(err), err)
	}
	if st := d.EdgeStatus(context.Background()); st.Available || !strings.Contains(st.Message, "already in use") {
		t.Errorf("edge status should explain the conflict: %+v", st)
	}
}

// TestEdgeSpreadsRequestsAcrossReplicas: domain routes resolve the app's alias dynamically, so every
// replica of a network-placed app serves (ADR-022, L2-006 LB-02).
func TestEdgeSpreadsRequestsAcrossReplicas(t *testing.T) {
	d, httpPort, _ := edgeDriver(t)
	ctx := context.Background()
	cidr, _, _ := randomCIDR()
	n, _ := newNetwork(t, d, api.NetworkSpec{CIDR: cidr})
	app := resource(api.KindApp, "it-spread-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	ensure(t, d, engine.EnsureInput{Resource: app, Revision: 1, Placement: engine.Placement{Network: engine.DockerNetwork("", n.Name)},
		Spec: api.AppSpec{Image: "traefik/whoami:v1.11", Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 3}})
	host := app.Name + ".localhost"
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{Routes: []engine.Route{{Hostname: host, Upstream: engine.ContainerName(app) + ":80", TLS: api.TLSOff}}}); err != nil {
		t.Fatal(err)
	}
	c := edgeClient(t, d, httpPort, false)
	seen := map[string]bool{}
	deadline := time.Now().Add(20 * time.Second)
	for len(seen) < 3 && time.Now().Before(deadline) {
		resp, err := c.Get("http://" + host + "/")
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "Hostname:") {
				seen[line] = true
			}
		}
	}
	if len(seen) != 3 {
		t.Errorf("the edge reached %d replicas, want 3: %v", len(seen), seen)
	}
}

// TestAStoppedEdgeOnADeletedNetworkComesBack: a stopped edge still names the networks it had joined;
// when one of them is deleted meanwhile, Docker refuses to start it. The edge is made anew instead.
func TestAStoppedEdgeOnADeletedNetworkComesBack(t *testing.T) {
	d, _, _ := edgeDriver(t)
	ctx := context.Background()
	cfg := engine.EdgeConfig{Routes: []engine.Route{{Hostname: "x.localhost", Upstream: "nowhere:80", TLS: api.TLSOff}}}
	if err := d.ConfigureEdge(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	gone := "es-it-edge-gone-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := d.cli.NetworkCreate(ctx, gone, network.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := d.connectIfMissing(ctx, gone, engine.EdgeName); err != nil {
		t.Fatal(err)
	}
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{}); err != nil { // no routes: the edge stops
		t.Fatal(err)
	}
	if err := d.cli.NetworkRemove(ctx, gone); err != nil {
		t.Fatalf("remove the network: %v", err)
	}
	if err := d.ConfigureEdge(ctx, cfg); err != nil {
		t.Fatalf("the edge didn't come back: %v", err)
	}
	if st := d.EdgeStatus(ctx); !st.Available {
		t.Errorf("edge status: %+v", st)
	}
}
