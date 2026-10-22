package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/go-connections/nat"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// DefaultEdgeImage is the edge proxy (ADR-015, DAR-008): Caddy 2.10.2, pinned by index digest.
const DefaultEdgeImage = "caddy@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d"

const (
	edgeVolume     = "es-system-edge-data"
	edgeConfigDir  = "/etc/enclave"
	edgeConfigFile = edgeConfigDir + "/caddy.json"
	edgeACMERoot   = edgeConfigDir + "/acme-root.pem"
	edgeCARoot     = "/data/caddy/pki/authorities/local/root.crt"
	reloadTimeout  = 30 * time.Second
	certDialWait   = 3 * time.Second
	// Requests that hit an app revision as it stops are retried for this long (ADR-016: zero drops for
	// domain traffic without the edge knowing about revisions).
	proxyTryDuration = "10s"
	proxyTryInterval = "250ms"
	upstreamRefresh  = "1s"
)

func (o Options) edgePorts() (string, int, int) {
	addr, httpPort, httpsPort := o.EdgeAddress, o.EdgeHTTPPort, o.EdgeHTTPSPort
	if addr == "" {
		addr = "127.0.0.1"
	}
	if httpPort == 0 {
		httpPort = 80
	}
	if httpsPort == 0 {
		httpsPort = 443
	}
	return addr, httpPort, httpsPort
}

type obj = map[string]any

// renderCaddy builds Caddy's JSON configuration from the routes. Values are JSON, never templated
// text, and hostnames are validated by pkg/api, so a hostname cannot inject configuration (L3-006 §5).
// The output is deterministic for a given input.
func renderCaddy(cfg engine.EdgeConfig, o Options) ([]byte, error) {
	_, _, httpsPort := o.edgePorts()
	routes := slices.Clone(cfg.Routes)
	// Exact hostnames match before wildcards (ADR-024): routes are tried in order.
	slices.SortFunc(routes, func(a, b engine.Route) int {
		if aw, bw := strings.HasPrefix(a.Hostname, "*."), strings.HasPrefix(b.Hostname, "*."); aw != bw {
			if aw {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Hostname, b.Hostname)
	})
	proxy := func(up string) obj {
		// Dynamic A-record upstreams on the app's stable alias spread requests across every replica and
		// follow deploys without an edge change (ADR-022); failed dials are retried (ADR-015).
		host, port, err := net.SplitHostPort(up)
		if err != nil {
			return obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": up}},
				"load_balancing": obj{"try_duration": proxyTryDuration, "try_interval": proxyTryInterval}}
		}
		return obj{"handler": "reverse_proxy",
			"dynamic_upstreams": obj{"source": "a", "name": host, "port": port, "refresh": upstreamRefresh, "versions": obj{"ipv4": true}},
			"load_balancing":    obj{"selection_policy": obj{"policy": "round_robin"}, "try_duration": proxyTryDuration, "try_interval": proxyTryInterval}}
	}
	portSuffix := ""
	if httpsPort != 443 {
		portSuffix = ":" + strconv.Itoa(httpsPort)
	}
	var httpsRoutes, httpRoutes []obj
	var internalHosts, acmeHosts, loadedHosts []string
	for _, r := range routes {
		match := []obj{{"host": []string{r.Hostname}}}
		if r.TLS != api.TLSOff {
			httpsRoutes = append(httpsRoutes, obj{"match": match, "handle": []obj{proxy(r.Upstream)}, "terminal": true})
			if r.DNS01 {
				loadedHosts = append(loadedHosts, r.Hostname)
			} else if r.TLS == api.TLSInternal {
				internalHosts = append(internalHosts, r.Hostname)
			} else {
				acmeHosts = append(acmeHosts, r.Hostname)
			}
		}
		if r.TLS != api.TLSOff && r.RedirectHTTP {
			httpRoutes = append(httpRoutes, obj{"match": match, "terminal": true, "handle": []obj{{"handler": "static_response", "status_code": 308,
				"headers": obj{"Location": []string{"https://{http.request.host}" + portSuffix + "{http.request.uri}"}}}}})
		} else {
			httpRoutes = append(httpRoutes, obj{"match": match, "handle": []obj{proxy(r.Upstream)}, "terminal": true})
		}
	}
	httpRoutes = append(httpRoutes, obj{"handle": []obj{{"handler": "static_response", "status_code": 404,
		"body": "No site is configured for this hostname on this EnclaveStack machine.\n"}}})
	servers := obj{"http": obj{"listen": []string{":80"}, "routes": httpRoutes}}
	if len(httpsRoutes) > 0 {
		auto := obj{"disable_redirects": true}
		if len(loadedHosts) > 0 {
			auto["skip_certificates"] = loadedHosts
		}
		servers["https"] = obj{"listen": []string{":443"}, "routes": httpsRoutes, "automatic_https": auto}
	}
	var policies []obj
	if len(internalHosts) > 0 {
		policies = append(policies, obj{"subjects": internalHosts, "issuers": []obj{{"module": "internal"}}})
	}
	if len(acmeHosts) > 0 {
		issuer := obj{"module": "acme"}
		if cfg.ACMEEmail != "" {
			issuer["email"] = cfg.ACMEEmail
		}
		if o.ACMEDirectory != "" {
			issuer["ca"] = o.ACMEDirectory
		}
		if len(o.ACMERootPEM) > 0 {
			issuer["trusted_roots_pem_files"] = []string{edgeACMERoot}
		}
		policies = append(policies, obj{"subjects": acmeHosts, "issuers": []obj{issuer}})
	}
	conf := obj{
		"admin": obj{"listen": "localhost:2019"},
		"apps": obj{
			// Per-host request metrics feed the request graphs and alerts (ADR-037).
			"http": obj{"servers": servers, "metrics": obj{"per_host": true}},
			"pki":  obj{"certificate_authorities": obj{"local": obj{"name": "EnclaveStack Local CA", "install_trust": false}}},
		},
	}
	tlsApp := obj{}
	if len(policies) > 0 {
		tlsApp["automation"] = obj{"policies": policies}
	}
	if len(cfg.Certificates) > 0 {
		certs := slices.Clone(cfg.Certificates)
		slices.SortFunc(certs, func(a, b engine.LoadedCertificate) int { return strings.Compare(a.Hostname, b.Hostname) })
		var pems []obj
		for _, c := range certs {
			pems = append(pems, obj{"certificate": string(c.ChainPEM), "key": string(c.KeyPEM), "tags": []string{"enclave-dns01"}})
		}
		tlsApp["certificates"] = obj{"load_pem": pems}
	}
	if len(tlsApp) == 0 {
		delete(conf["apps"].(obj), "tls")
	} else {
		conf["apps"].(obj)["tls"] = tlsApp
	}
	return json.MarshalIndent(conf, "", "  ")
}

// ConfigureEdge implements engine.Driver (ADR-015).
func (d *Driver) ConfigureEdge(ctx context.Context, cfg engine.EdgeConfig) error {
	d.edgeMu.Lock()
	defer d.edgeMu.Unlock()
	c, err := d.inspect(ctx, engine.EdgeName)
	if err != nil {
		return err
	}
	if len(cfg.Routes) == 0 {
		d.edgeErr = ""
		if c != nil && c.State != nil && c.State.Running {
			sctx, cancel := context.WithTimeout(ctx, stepTimeout)
			defer cancel()
			if err := d.cli.ContainerStop(sctx, engine.EdgeName, container.StopOptions{}); err != nil && !isNotFound(err) {
				return d.classify(fmt.Errorf("stop edge proxy: %w", err))
			}
		}
		return nil
	}
	conf, err := renderCaddy(cfg, d.opts)
	if err != nil {
		return engine.Permanent(fmt.Errorf("render edge configuration: %w", err))
	}
	// A stopped edge is made anew: it may still name networks deleted while it was down, which Docker
	// refuses to start, and everything it keeps (certificates) is in its volume.
	if c != nil && (!d.edgeMatches(c) || c.State == nil || !c.State.Running) {
		if err := d.remove(ctx, engine.EdgeName); err != nil {
			return err
		}
		c = nil
	}
	if c == nil {
		if err := d.createEdge(ctx); err != nil {
			return d.edgeFailed(err)
		}
	}
	if err := d.copyFiles(ctx, engine.EdgeName, edgeConfigDir, map[string][]byte{"caddy.json": conf, "acme-root.pem": d.opts.ACMERootPEM}, 0o600); err != nil { // holds loaded keys (ADR-024)
		return err
	}
	if c == nil || c.State == nil || !c.State.Running {
		if err := d.start(ctx, engine.EdgeName); err != nil {
			return d.edgeFailed(err)
		}
		if err := d.waitStable(ctx, engine.EdgeName, time.Second); err != nil {
			return d.edgeFailed(fmt.Errorf("the edge proxy stopped right after starting: %w. Last output: %s", err, d.tail(ctx, engine.EdgeName, 5)))
		}
		d.edgeErr = ""
		return nil // a fresh start reads the configuration file
	}
	rctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	res, err := d.exec(rctx, engine.EdgeName, []string{"caddy", "reload", "--config", edgeConfigFile}, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return engine.Permanent(fmt.Errorf("the edge proxy rejected its configuration: %s", res.Output))
	}
	d.edgeErr = ""
	return nil
}

// edgeMatches reports whether the existing edge container has the wanted image and port bindings.
func (d *Driver) edgeMatches(c *container.InspectResponse) bool {
	addr, httpPort, httpsPort := d.opts.edgePorts()
	if c.Config == nil || c.Config.Image != d.opts.EdgeImage || c.HostConfig == nil {
		return false
	}
	want := map[string]string{"80/tcp": strconv.Itoa(httpPort), "443/tcp": strconv.Itoa(httpsPort)}
	for port, hp := range want {
		b := c.HostConfig.PortBindings[nat.Port(port)]
		if len(b) != 1 || b[0].HostPort != hp || b[0].HostIP != addr {
			return false
		}
	}
	return true
}

func (d *Driver) createEdge(ctx context.Context) error {
	if err := d.ensureNetwork(ctx); err != nil {
		return err
	}
	if err := d.ensureImage(ctx, d.opts.EdgeImage, false); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, stepTimeout)
	_, err := d.cli.VolumeCreate(vctx, volume.CreateOptions{Name: edgeVolume, Labels: map[string]string{LabelManaged: "true"}})
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("create edge volume: %w", err))
	}
	addr, httpPort, httpsPort := d.opts.edgePorts()
	cfg := &container.Config{Image: d.opts.EdgeImage, Cmd: []string{"caddy", "run", "--config", edgeConfigFile},
		Labels:       map[string]string{LabelManaged: "true", LabelKind: "system"},
		ExposedPorts: nat.PortSet{"80/tcp": {}, "443/tcp": {}}}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{Memory: 256 << 20},
		Binds:         []string{edgeVolume + ":/data"},
		PortBindings: nat.PortMap{
			"80/tcp":  {{HostIP: addr, HostPort: strconv.Itoa(httpPort)}},
			"443/tcp": {{HostIP: addr, HostPort: strconv.Itoa(httpsPort)}},
		},
	}
	netCfg, err := d.systemNetworking(ctx)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.ContainerCreate(cctx, cfg, host, netCfg, nil, engine.EdgeName); err != nil {
		return d.classify(fmt.Errorf("create edge proxy: %w", err))
	}
	return d.joinManagedNetworks(ctx, engine.EdgeName) // domains route to apps in every network (ADR-020)
}

// edgeFailed turns bind conflicts into an actionable, permanent error and remembers it for EdgeStatus.
func (d *Driver) edgeFailed(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind:") {
		_, httpPort, httpsPort := d.opts.edgePorts()
		d.edgeErr = fmt.Sprintf("Port %d or %d is already in use on this machine, so the edge proxy can't start. Free it, then retry.", httpPort, httpsPort)
		return engine.Permanent(errors.New(d.edgeErr))
	}
	if errors.Is(err, engine.ErrUnavailable) {
		return err
	}
	d.edgeErr = msg
	return err
}

// copyFiles writes files with mode into a directory of a container (running or not) through the engine
// API. The directory is created if it doesn't exist.
func (d *Driver) copyFiles(ctx context.Context, name, dir string, files map[string][]byte, mode int64) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	base := strings.Trim(dir, "/")
	epoch := time.Unix(0, 0)
	if err := tw.WriteHeader(&tar.Header{Name: base + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: epoch}); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: base + "/" + n, Mode: mode, Size: int64(len(files[n])), ModTime: epoch}); err != nil {
			return err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.CopyToContainer(cctx, name, "/", &buf, container.CopyToContainerOptions{}); err != nil {
		return d.classify(fmt.Errorf("copy files into %s: %w", name, err))
	}
	return nil
}

// Certificate implements engine.Driver: a TLS handshake to the edge with the hostname as SNI.
func (d *Driver) Certificate(ctx context.Context, hostname string) (engine.Certificate, error) {
	_, _, httpsPort := d.opts.edgePorts()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: certDialWait},
		Config: &tls.Config{ServerName: hostname, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec // we read the certificate, we don't trust it
	dctx, cancel := context.WithTimeout(ctx, certDialWait)
	defer cancel()
	conn, err := dialer.DialContext(dctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(httpsPort)))
	if err != nil {
		return engine.Certificate{State: engine.CertPending}, nil
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 || certs[0].VerifyHostname(hostname) != nil {
		return engine.Certificate{State: engine.CertPending}, nil
	}
	issuer := certs[0].Issuer.CommonName
	if issuer == "" && len(certs[0].Issuer.Organization) > 0 {
		issuer = certs[0].Issuer.Organization[0]
	}
	return engine.Certificate{State: engine.CertIssued, Issuer: issuer, NotBefore: certs[0].NotBefore.UTC(), NotAfter: certs[0].NotAfter.UTC()}, nil
}

// EdgeStatus implements engine.Driver.
func (d *Driver) EdgeStatus(ctx context.Context) engine.EdgeStatus {
	addr, httpPort, httpsPort := d.opts.edgePorts()
	st := engine.EdgeStatus{Address: addr, HTTPPort: httpPort, HTTPSPort: httpsPort}
	c, err := d.inspect(ctx, engine.EdgeName)
	d.edgeMu.Lock()
	lastErr := d.edgeErr
	d.edgeMu.Unlock()
	switch {
	case err != nil:
		st.Message = "The container engine isn't reachable."
	case lastErr != "":
		st.Message = lastErr
	case c == nil || c.State == nil || !c.State.Running:
		st.Message = "The edge proxy starts with the first domain."
	default:
		st.Available = true
	}
	return st
}

// RootCA implements engine.Driver.
func (d *Driver) RootCA(ctx context.Context) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	rc, _, err := d.cli.CopyFromContainer(cctx, engine.EdgeName, edgeCARoot)
	if isNotFound(err) {
		return nil, engine.ErrNoCA
	}
	if err != nil {
		return nil, d.classify(fmt.Errorf("read the internal CA: %w", err))
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	if _, err := tr.Next(); err != nil {
		return nil, engine.ErrNoCA
	}
	pem, err := io.ReadAll(io.LimitReader(tr, 64<<10))
	if err != nil {
		return nil, err
	}
	return pem, nil
}
