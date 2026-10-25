//go:build docker

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/acme"
	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// DNS-01 certificates against Pebble without the public internet (ADR-024, L5-002 §7): the control
// plane's ACME client writes challenge records through a DNS provider backed by challtestsrv, which
// Pebble queries; the edge then serves the certificate as loaded, with a wildcard route behind an
// exact one.

// challProvider is a dns.Provider on challtestsrv's management API (set-txt / clear-txt).
type challProvider struct{ base string }

func (c challProvider) post(path string, body map[string]string) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(c.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("challtestsrv %s: %s %s", path, resp.Status, msg)
	}
	return nil
}

func (c challProvider) Zone(context.Context) (dns.Zone, error) { return dns.Zone{ID: "chall"}, nil }
func (c challProvider) Get(context.Context, string, string) (*dns.Record, error) {
	return nil, nil
}
func (c challProvider) Set(_ context.Context, r dns.Record, _ string) error {
	return c.post("/set-txt", map[string]string{"host": r.FQDN + ".", "value": r.Values[0]})
}
func (c challProvider) Delete(_ context.Context, fqdn, _ string) error {
	return c.post("/clear-txt", map[string]string{"host": fqdn + "."})
}

// publishedHelper starts a helper on the enclave network with container ports published on loopback,
// and returns the host ports.
func publishedHelper(t *testing.T, d *Driver, name, image, alias string, cmd, env []string, files map[string][]byte, ports ...int) map[int]int {
	t.Helper()
	ctx := context.Background()
	if err := d.ensureNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.ensureImage(ctx, image, false); err != nil {
		t.Fatal(err)
	}
	_ = d.remove(ctx, name)
	exposed, bindings := nat.PortSet{}, nat.PortMap{}
	for _, p := range ports {
		np := nat.Port(fmt.Sprintf("%d/tcp", p))
		exposed[np] = struct{}{}
		bindings[np] = []nat.PortBinding{{HostIP: "127.0.0.1"}}
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: {Aliases: []string{alias}}}}
	if _, err := d.cli.ContainerCreate(ctx, &container.Config{Image: image, Cmd: cmd, Env: env, ExposedPorts: exposed, Labels: map[string]string{LabelManaged: "true"}},
		&container.HostConfig{PortBindings: bindings}, netCfg, nil, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.remove(context.Background(), name) })
	if files != nil {
		if err := d.copyFiles(ctx, name, "/etc/es-test", files, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.start(ctx, name); err != nil {
		t.Fatal(err)
	}
	c, _ := d.inspect(ctx, name)
	out := map[int]int{}
	for _, p := range ports {
		out[p] = hostPort(c, p)
	}
	return out
}

func TestWildcardCertificateThroughDNS01(t *testing.T) {
	d, _, httpsPort := edgeDriver(t)
	ctx := context.Background()
	chall := publishedHelper(t, d, "es-test-chall-dns", challImage, "challtestsrv",
		[]string{"-defaultIPv4", "127.0.0.1", "-defaultIPv6", "", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", ""}, nil, nil, 8055)
	pebble := publishedHelper(t, d, "es-test-pebble-dns", pebbleImage, "pebble", []string{"-config", "/etc/es-test/pebble.json", "-dnsserver", "challtestsrv:8053"},
		[]string{"PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0"}, map[string][]byte{"pebble.json": []byte(pebbleCfg)}, 14000)
	root := readFile(t, d, "es-test-pebble-dns", "/test/certs/pebble.minica.pem")
	provider := challProvider{base: fmt.Sprintf("http://127.0.0.1:%d", chall[8055])}

	issuer := &acme.Lego{Directory: fmt.Sprintf("https://localhost:%d/dir", pebble[14000]), RootPEM: root, SkipPropagationCheck: true}
	base := "dns-" + suffix() + ".example.test"
	var cert acme.Certificate
	var acct acme.Account
	var err error
	deadline := time.Now().Add(time.Minute)
	for { // Pebble takes a moment to listen
		cert, acct, err = issuer.Obtain(ctx, acme.Request{Hostname: "*." + base, Account: acme.Account{Email: "ops@example.test"}, Provider: provider, Owner: "dom_test"})
		if err == nil || time.Now().After(deadline) || !strings.Contains(err.Error(), "reach the ACME directory") {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("obtain: %v", err)
	}
	if acct.URI == "" || len(acct.KeyPEM) == 0 || !strings.Contains(strings.ToLower(cert.Issuer), "pebble") || !strings.Contains(string(cert.KeyPEM), "PRIVATE KEY") {
		t.Fatalf("certificate %+v account %+v", cert.Issuer, acct.URI)
	}
	// A second order reuses the registered account.
	exactCert, acct2, err := issuer.Obtain(ctx, acme.Request{Hostname: "api." + base, Account: acct, Provider: provider, Owner: "dom_test"})
	if err != nil || acct2.URI != acct.URI {
		t.Fatalf("second order: %v (account %s → %s)", err, acct.URI, acct2.URI)
	}

	// The edge serves the loaded wildcard certificate; an exact route wins over the wildcard.
	wild := resource(api.KindApp, "it-wild-"+suffix())
	exact := resource(api.KindApp, "it-exact-"+suffix())
	for name, a := range map[string]*domain.Resource{"wild": wild, "exact": exact} {
		cleanup(t, d, a, engine.ObjectStore{})
		ensure(t, d, engine.EnsureInput{Resource: a, Revision: 1, Spec: api.AppSpec{Image: "traefik/whoami:v1.11", Command: []string{"--name", name},
			Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 1}})
	}
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{
		Routes: []engine.Route{
			{Hostname: "*." + base, Upstream: engine.ContainerName(wild) + ":80", TLS: api.TLSAuto, DNS01: true},
			{Hostname: "api." + base, Upstream: engine.ContainerName(exact) + ":80", TLS: api.TLSAuto, DNS01: true},
		},
		Certificates: []engine.LoadedCertificate{{Hostname: "*." + base, ChainPEM: cert.ChainPEM, KeyPEM: cert.KeyPEM},
			{Hostname: "api." + base, ChainPEM: exactCert.ChainPEM, KeyPEM: exactCert.KeyPEM}},
	}); err != nil {
		t.Fatal(err)
	}
	served, err := d.Certificate(ctx, "anything."+base)
	if err != nil || served.State != engine.CertIssued || served.NotAfter != cert.NotAfter {
		t.Fatalf("edge certificate %+v %v\nedge log: %s", served, err, d.tail(ctx, engine.EdgeName, 15))
	}
	c := edgeClient(t, d, httpsPort, false)
	c.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // Pebble's chain isn't in the system pool
	for host, want := range map[string]string{"anything." + base: "Name: wild", "api." + base: "Name: exact"} {
		resp, err := c.Get("https://" + host + "/")
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", host, err, resp)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), want) {
			t.Errorf("%s reached the wrong app (want %q): %q", host, want, b)
		}
	}
	conf, _ := d.exec(ctx, engine.EdgeName, []string{"stat", "-c", "%a", edgeConfigFile}, nil)
	if strings.TrimSpace(conf.Output) != "600" {
		t.Errorf("edge configuration holds keys but has mode %q", conf.Output)
	}
}
