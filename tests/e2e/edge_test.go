//go:build e2e && machines

package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// An edge machine for real (L2-014 EDG-*, L5-001 §2h, L5-002 §11): the control plane in a container on this
// Docker, given no address of its own, and a Docker-in-Docker "rented server" joined from the main side.
// A relay on it carries a public domain whose certificate comes from Pebble through the relay; a visitor
// reaches the app through it with their own address, and the edge machine never holds a key.
//
//	make e2e-machines EDGE=1   (go test -tags "e2e machines" -run TestEdgeMachine ./tests/e2e/)

const (
	pebbleImage = "ghcr.io/letsencrypt/pebble@sha256:d9080f68f6cb6af8d82134ab26de0aaaf312ac9cba42aecc6d3aede6cb63007b"
	challImage  = "ghcr.io/letsencrypt/pebble-challtestsrv@sha256:c6ea5f6193517b3c7a02431b928234caf9db29a7a8abca4c0b337403468b650d"
	curlImage   = "curlimages/curl:8.10.1"
)

func TestEdgeMachine(t *testing.T) {
	if exec.Command("docker", "inspect", "es-system-mesh").Run() == nil {
		t.Skip("a real EnclaveStack mesh runs on this Docker; this test would replace it")
	}
	ctx := context.Background()
	bin := linuxBinary(t)
	gw := docker(t, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	sfx := fmt.Sprintf("%06d", rand.IntN(1_000_000))
	httpPort, agentPort := 47000+rand.IntN(1000), 48000+rand.IntN(1000)
	tunnelEdge := 52600 + rand.IntN(500)
	edgeHTTP, edgeHTTPS := 49000+rand.IntN(500), 49500+rand.IntN(500)
	host := "stage-" + sfx + ".example.test"

	// Pebble's root, which the main machine's edge proxy trusts for the ACME directory.
	dir := t.TempDir()
	root := filepath.Join(dir, "pebble-root.pem")
	for _, image := range []string{pebbleImage, challImage, curlImage} {
		if exec.Command("docker", "image", "inspect", image).Run() != nil {
			docker(t, "pull", "-q", image)
		}
	}
	tmp := docker(t, "create", pebbleImage)
	docker(t, "cp", tmp+":/test/certs/pebble.minica.pem", root)
	docker(t, "rm", tmp)
	if err := os.Chmod(root, 0o644); err != nil {
		t.Fatal(err)
	}

	// The control plane, with no tunnel address: the main machine is behind NAT, as at home.
	srv, edge, data := "es-e2e-server-"+sfx, "es-e2e-edge-"+sfx, "es-e2e-data-"+sfx
	keepOnFailure(t, func() {
		_ = exec.Command("docker", "rm", "-f", srv).Run()
		_ = exec.Command("docker", "volume", "rm", data).Run()
	})
	docker(t, "run", "-d", "--name", srv, "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", bin+":/usr/local/bin/enclave:ro", "-v", data+":/data",
		"-v", root+":/etc/es-test/pebble-root.pem:ro", "-p", fmt.Sprintf("%d:7070", httpPort), dindImage,
		"enclave", "server", "--listen", "0.0.0.0:7070", "--data-dir", "/data", "--log-format", "json",
		"--console-url", fmt.Sprintf("http://%s:%d", gw, httpPort), "--publish-address", "0.0.0.0", "--poll-interval", "500ms",
		"--machine-check-interval", "2s", "--tunnel-check-interval", "2s",
		"--acme-directory", "https://pebble:14000/dir", "--acme-root", "/etc/es-test/pebble-root.pem")
	s := &server{t: t, url: fmt.Sprintf("http://127.0.0.1:%d", httpPort)}
	codeRe := regexp.MustCompile(`(?m)^\s+([A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4})\s*$`)
	until(t, time.Minute, "the server's setup code", func() bool {
		out, _ := exec.Command("docker", "logs", srv).CombinedOutput()
		if m := codeRe.FindSubmatch(out); m != nil {
			s.code = string(m[1])
		}
		res, err := http.Get(s.url + "/healthz")
		if err == nil {
			res.Body.Close()
		}
		return s.code != "" && err == nil && res.StatusCode == http.StatusOK
	})
	serverLog := func() string {
		out, _ := exec.Command("docker", "logs", srv).CombinedOutput()
		var keep []string
		for _, l := range strings.Split(string(out), "\n") {
			if !strings.Contains(l, `"msg":"request"`) {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server log (without requests):\n%s", tail(serverLog(), 80))
		}
	})
	_, c := setupAndToken(t, s)
	explain = func() string {
		var b strings.Builder
		if ms, err := c.ListMachines(ctx); err == nil {
			for _, m := range ms {
				fmt.Fprintf(&b, "machine %s (%s): %s %s\n", m.Name, m.Role, m.State, m.Message)
			}
		}
		if rs, err := c.ListAll(ctx, client.ListOptions{}); err == nil {
			for _, r := range rs {
				fmt.Fprintf(&b, "%s/%s: %s %q %v\n", r.Kind, r.Name, r.Status.Phase, r.Status.Message, r.Outputs)
				if ev, err := c.Events(ctx, r.ID, 5); err == nil {
					for _, e := range ev {
						fmt.Fprintf(&b, "    %s %s\n", e.Type, e.Message)
					}
				}
			}
		}
		out, _ := exec.Command("docker", "exec", edge, "docker", "ps", "-a").CombinedOutput()
		fmt.Fprintf(&b, "edge machine's containers:\n%s\n", out)
		return b.String()
	}
	t.Cleanup(func() { explain = nil })
	// The edge machine goes last: deleting the relay needs its agent.
	keepOnFailure(t, func() { _ = exec.Command("docker", "rm", "-f", "-v", edge).Run() })
	// What the server made on this Docker goes before the server does: resources through the API, then the
	// edge proxy, the mesh and the test's helpers, which only this run made (it skips when a mesh was here).
	keepOnFailure(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		// A resource in use (the app under a domain) is refused until its users are gone: go round again.
		for pass := 0; pass < 4; pass++ {
			rs, err := c.ListAll(ctx, client.ListOptions{})
			if err != nil || len(rs) == 0 {
				break
			}
			for _, r := range rs {
				if r.Stack == nil && r.OwnerID == nil {
					_, _ = c.DeleteResource(ctx, r.ID, nil)
				}
			}
			for _, r := range rs {
				wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = c.WaitGone(wctx, r.ID, time.Second)
				cancel()
			}
		}
		// The server stops first, or it puts back the mesh and the edge proxy as they go.
		_ = exec.Command("docker", "rm", "-f", srv).Run()
		for _, name := range []string{"es-e2e-pebble-" + sfx, "es-e2e-chall-" + sfx, "es-system-edge", "es-system-mesh"} {
			_ = exec.Command("docker", "rm", "-f", name).Run()
		}
		for _, v := range []string{"es-system-mesh-data", "es-system-edge-data"} {
			_ = exec.Command("docker", "volume", "rm", v).Run()
		}
	})

	// The edge machine: a Docker of its own, its agent and its 80/443 reached through published ports.
	docker(t, "run", "-d", "--privileged", "--name", edge, "-e", "DOCKER_TLS_CERTDIR=", "-v", bin+":/usr/local/bin/enclave:ro",
		"-p", fmt.Sprintf("%d:%d", agentPort, agentPort), "-p", fmt.Sprintf("%d:%d/udp", tunnelEdge, tunnelEdge),
		"-p", fmt.Sprintf("%d:80", edgeHTTP), "-p", fmt.Sprintf("%d:443", edgeHTTPS), dindImage, "--bip=192.168.209.1/24")
	until(t, time.Minute, "the edge machine's Docker", func() bool { return exec.Command("docker", "exec", edge, "docker", "info").Run() == nil })
	edgeIP := docker(t, "inspect", edge, "--format", "{{.NetworkSettings.Networks.bridge.IPAddress}}")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "exec", edge, "cat", "/tmp/agent.log").CombinedOutput()
			t.Logf("edge agent log:\n%s", tail(string(out), 60))
		}
	})

	// EDG-02: add it on the main side, run its join command there, and the main machine comes to it.
	added, err := c.AddMachine(ctx, api.AddMachine{Name: "vps1", Role: api.MachineEdge, Address: gw, AgentPort: agentPort})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(added.JoinCommand, "--edge") {
		t.Errorf("join command %q", added.JoinCommand)
	}
	docker(t, "exec", "-d", edge, "sh", "-c", fmt.Sprintf("exec enclave agent join --edge --code %s --listen :%d --tunnel-port %d >/tmp/agent.log 2>&1",
		added.JoinCode, agentPort, tunnelEdge))
	until(t, 2*time.Minute, "vps1 online", func() bool {
		m, err := c.GetMachineByName(ctx, "vps1")
		return err == nil && m.State == api.MachineOnline
	})
	if m, _ := c.GetMachineByName(ctx, "vps1"); m.Role != api.MachineEdge || m.Schedulable {
		t.Fatalf("vps1: role %q schedulable %v", m.Role, m.Schedulable)
	}

	create := func(kind api.Kind, name string, spec any) api.Resource {
		t.Helper()
		r, err := c.CreateResource(ctx, kind, name, spec)
		if err != nil {
			t.Fatal(err)
		}
		if r, err = c.WaitReady(ctx, r.ID, time.Second, nil); err != nil {
			t.Fatalf("%s %s: %v", kind, name, err)
		}
		return r
	}
	app := create(api.KindApp, "web", api.AppSpec{Image: "traefik/whoami:v1.10", Port: 80, MemoryMiB: 64})
	// EDG-03: nothing is ever placed on an edge machine.
	if app.Outputs["machine"] == "vps1" {
		t.Fatal("the app was placed on the edge machine")
	}

	// Pebble on the main machine's network, as the CA the edge proxy asks; its DNS points every name at
	// the edge machine's published port 80, so the HTTP challenge travels through the relay.
	docker(t, "run", "-d", "--name", "es-e2e-chall-"+sfx, "--network", "enclave", "--label", "enclave.managed=true", challImage,
		"-defaultIPv4", gw, "-defaultIPv6", "", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")
	chall := docker(t, "inspect", "es-e2e-chall-"+sfx, "--format", "{{.NetworkSettings.Networks.enclave.IPAddress}}")
	cfg := fmt.Sprintf(`{"pebble":{"listenAddress":"0.0.0.0:14000","managementListenAddress":"0.0.0.0:15000",
"certificate":"test/certs/localhost/cert.pem","privateKey":"test/certs/localhost/key.pem","httpPort":%d,"tlsPort":%d,
"ocspResponderURL":"","externalAccountBindingRequired":false}}`, edgeHTTP, edgeHTTPS)
	cfgFile := filepath.Join(dir, "pebble.json")
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	docker(t, "run", "-d", "--name", "es-e2e-pebble-"+sfx, "--network", "enclave", "--network-alias", "pebble", "--label", "enclave.managed=true",
		"-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "-v", cfgFile+":/etc/es-test/pebble.json:ro",
		pebbleImage, "-config", "/etc/es-test/pebble.json", "-dnsserver", chall+":8053")

	// A relay on vps1 and a public domain through it, open to everyone so the visitor needs no sign-in.
	relay := create(api.KindTunnel, "edge", api.TunnelSpec{Provider: api.TunnelRelay, Machine: "vps1"})
	if relay.Outputs["publicAddress"] != gw {
		t.Errorf("relay public address %v, want %s", relay.Outputs["publicAddress"], gw)
	}
	dom, err := c.CreateResource(ctx, api.KindDomain, "stage", api.DomainSpec{Hostname: host, App: "web", Tunnel: "edge", TLS: api.TLSAuto,
		Gate: &api.Gate{Mode: api.GateNone}})
	if err != nil {
		t.Fatal(err)
	}
	until(t, 3*time.Minute, "the domain's certificate through the relay", func() bool {
		d, err := c.GetResource(ctx, dom.ID)
		return err == nil && d.Status.Phase == api.PhaseReady && d.Outputs["certificateState"] == "issued"
	})

	// A visitor on another network reaches the app at the edge machine's address, over HTTPS with Pebble's
	// certificate; the app sees the visitor's address, carried by PROXY protocol (EDG-04).
	visit := func(args ...string) string {
		t.Helper()
		out, _ := exec.Command("docker", append([]string{"run", "--rm", "--network", "bridge", curlImage, "-sk", "-v", "--max-time", "10",
			"--resolve", host + ":443:" + edgeIP, "--resolve", host + ":80:" + edgeIP}, args...)...).CombinedOutput()
		return string(out)
	}
	var page string
	until(t, time.Minute, "the visitor's page", func() bool {
		page = visit("https://" + host + "/")
		return strings.Contains(page, "Hostname:")
	})
	if !regexp.MustCompile(`issuer: .*Pebble`).MatchString(page) {
		t.Errorf("certificate not from Pebble:\n%s", tail(page, 40))
	}
	visitor := regexp.MustCompile(`X-Forwarded-For: ([0-9.]+)`).FindStringSubmatch(page)
	if visitor == nil || !strings.HasPrefix(visitor[1], strings.TrimSuffix(gw, ".1")) || visitor[1] == gw {
		t.Errorf("the app didn't see the visitor's own address: %v\n%s", visitor, tail(page, 30))
	}
	if out := visit("http://" + host + "/x"); !strings.Contains(out, "< HTTP/1.1 308") {
		t.Errorf("plain HTTP isn't sent to HTTPS:\n%s", tail(out, 20))
	}
	if out := visit("-H", "Host: other.example.test", "http://"+edgeIP+"/"); !strings.Contains(out, "< HTTP/1.1 404") {
		t.Errorf("another hostname through the relay:\n%s", tail(out, 20))
	}

	// EDG-04: the edge machine holds no certificate key, secret or EnclaveStack data besides its relay.
	if out, _ := exec.Command("docker", "exec", edge, "sh", "-c",
		"find /var/lib/docker/volumes -type f -exec grep -l 'PRIVATE KEY' {} + 2>/dev/null; docker ps --format '{{.Names}}'").CombinedOutput(); strings.Contains(string(out), "/volumes/") ||
		strings.Contains(string(out), "es-system-edge") {
		t.Errorf("the edge machine holds keys or runs the edge proxy:\n%s", out)
	}

	// EDG-05: the edge machine can't be removed while its relay exists.
	if err := c.RemoveMachine(ctx, "vps1", false); err == nil {
		t.Error("removing vps1 with its relay was allowed")
	}
	if strings.Contains(serverLog(), `"level":"ERROR"`) {
		t.Errorf("errors in the server log:\n%s", tail(serverLog(), 40))
	}
}
