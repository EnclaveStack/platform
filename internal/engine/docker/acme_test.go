//go:build docker

package docker

import (
	"archive/tar"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Automatic (ACME HTTP-01) certificates against Pebble, Let's Encrypt's test CA, without the public
// internet: challtestsrv answers every DNS query with the edge's address (L2-005 DOM-03, L5-002 §6).

const (
	pebbleImage = "ghcr.io/letsencrypt/pebble@sha256:d9080f68f6cb6af8d82134ab26de0aaaf312ac9cba42aecc6d3aede6cb63007b"
	challImage  = "ghcr.io/letsencrypt/pebble-challtestsrv@sha256:c6ea5f6193517b3c7a02431b928234caf9db29a7a8abca4c0b337403468b650d"
	pebbleCfg   = `{"pebble":{"listenAddress":"0.0.0.0:14000","managementListenAddress":"0.0.0.0:15000",
"certificate":"test/certs/localhost/cert.pem","privateKey":"test/certs/localhost/key.pem","httpPort":80,"tlsPort":443,
"ocspResponderURL":"","externalAccountBindingRequired":false}}`
)

func runHelper(t *testing.T, d *Driver, name, image, alias string, cmd []string, env []string, files map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	if err := d.ensureImage(ctx, image, false); err != nil {
		t.Fatal(err)
	}
	_ = d.remove(ctx, name)
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{engine.NetworkName: {Aliases: []string{alias}}}}
	if _, err := d.cli.ContainerCreate(ctx, &container.Config{Image: image, Cmd: cmd, Env: env, Labels: map[string]string{LabelManaged: "true"}},
		&container.HostConfig{}, netCfg, nil, name); err != nil {
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
}

func readFile(t *testing.T, d *Driver, name, path string) []byte {
	t.Helper()
	rc, _, err := d.cli.CopyFromContainer(context.Background(), name, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(tr)
	return b
}

func TestAutomaticCertificateFromAnACMECA(t *testing.T) {
	d, _, httpsPort := edgeDriver(t)
	ctx := context.Background()
	if err := d.ensureNetwork(ctx); err != nil {
		t.Fatal(err)
	}
	app := resource(api.KindApp, "it-acme-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	if _, _, err := deploy(t, d, app, api.AppSpec{Image: "traefik/whoami:v1.10", Port: 80, MemoryMiB: 64}, 1); err != nil {
		t.Fatal(err)
	}
	up := engine.ContainerName(app) + ":80"

	// Start the edge (internal TLS only) to learn its address on the network.
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{Routes: []engine.Route{{Hostname: "boot.localhost", Upstream: up, TLS: api.TLSInternal}}}); err != nil {
		t.Fatal(err)
	}
	edge, err := d.inspect(ctx, engine.EdgeName)
	if err != nil || edge == nil {
		t.Fatal(err)
	}
	edgeIP := edge.NetworkSettings.Networks[engine.NetworkName].IPAddress

	runHelper(t, d, "es-test-chall", challImage, "challtestsrv", []string{"-defaultIPv4", edgeIP, "-defaultIPv6", "", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", ""}, nil, nil)
	runHelper(t, d, "es-test-pebble", pebbleImage, "pebble", []string{"-config", "/etc/es-test/pebble.json", "-dnsserver", "challtestsrv:8053"},
		[]string{"PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0"}, map[string][]byte{"pebble.json": []byte(pebbleCfg)})
	d.opts.ACMEDirectory = "https://pebble:14000/dir"
	d.opts.ACMERootPEM = readFile(t, d, "es-test-pebble", "/test/certs/pebble.minica.pem")

	host := "acme-" + suffix() + ".example.test"
	if err := d.ConfigureEdge(ctx, engine.EdgeConfig{ACMEEmail: "ops@example.test", Routes: []engine.Route{
		{Hostname: "boot.localhost", Upstream: up, TLS: api.TLSInternal},
		{Hostname: host, Upstream: up, TLS: api.TLSAuto, RedirectHTTP: true},
	}}); err != nil {
		t.Fatal(err)
	}
	var cert engine.Certificate
	deadline := time.Now().Add(2 * time.Minute)
	for cert.State != engine.CertIssued && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		cert, _ = d.Certificate(ctx, host)
	}
	if cert.State != engine.CertIssued || !strings.Contains(strings.ToLower(cert.Issuer), "pebble") {
		t.Fatalf("no ACME certificate for %s: %+v\nedge log: %s", host, cert, d.tail(ctx, engine.EdgeName, 15))
	}
	t.Logf("issued by %q on port %d, expires %s", cert.Issuer, httpsPort, cert.NotAfter.Format(time.RFC3339))
}
