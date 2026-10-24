//go:build e2e && machines

package e2e

import (
	"bytes"
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

// Two machines for real (L5-001 §2f, L5-002 §10): the control plane in a container on this Docker, and a
// second machine as Docker-in-Docker running `enclave agent`. Between them: the join, placement, a
// private network across machines over WireGuard, a move with its data, and an app rescheduled off a
// machine that went offline.
//
//	make e2e-machines   (go test -tags "e2e machines" -run TestTwoMachines ./tests/e2e/)

const dindImage = "docker:28.5.1-dind"

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// linuxBinary builds enclave for the Docker engine's Linux.
func linuxBinary(t *testing.T) string {
	t.Helper()
	arch := map[string]string{"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}[docker(t, "info", "--format", "{{.Architecture}}")]
	if arch == "" {
		t.Skip("unknown Docker architecture")
	}
	out := filepath.Join(t.TempDir(), "enclave")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/enclave")
	cmd.Dir = repoRoot()
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	return out
}

// explain, when set, describes the state behind a timeout.
var explain func() string

func until(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			extra := ""
			if explain != nil {
				extra = "\n" + explain()
			}
			t.Fatalf("timed out waiting for %s%s", what, extra)
		}
		time.Sleep(time.Second)
	}
}

// runJob runs a job once and returns its output.
func runJob(t *testing.T, c *client.Client, id string) api.RunDetail {
	t.Helper()
	ctx := context.Background()
	run, err := c.RunNow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var d api.RunDetail
	until(t, 3*time.Minute, "the run", func() bool {
		d, err = c.GetRun(ctx, id, run.ID)
		return err == nil && d.Status != "running"
	})
	return d
}

// keepOnFailure leaves the containers for a look when ENCLAVE_E2E_KEEP=1 and the test failed.
func keepOnFailure(t *testing.T, f func()) {
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("ENCLAVE_E2E_KEEP") == "1" {
			return
		}
		f()
	})
}

func TestTwoMachines(t *testing.T) {
	if exec.Command("docker", "inspect", "es-system-mesh").Run() == nil {
		t.Skip("a real EnclaveStack mesh runs on this Docker; this test would replace it")
	}
	ctx := context.Background()
	bin := linuxBinary(t)
	gw := docker(t, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	sfx := fmt.Sprintf("%06d", rand.IntN(1_000_000))
	httpPort, agentPort := 47000+rand.IntN(1000), 48000+rand.IntN(1000)
	tunnelMain, tunnel2 := 52000+rand.IntN(500), 52600+rand.IntN(500)

	// The control plane, driving this Docker as the main machine.
	srv, m2 := "es-e2e-server-"+sfx, "es-e2e-machine2-"+sfx
	data := "es-e2e-data-" + sfx
	keepOnFailure(t, func() {
		_ = exec.Command("docker", "rm", "-f", srv).Run()
		_ = exec.Command("docker", "volume", "rm", data).Run()
	})
	docker(t, "run", "-d", "--name", srv, "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", bin+":/usr/local/bin/enclave:ro", "-v", data+":/data",
		"-p", fmt.Sprintf("%d:7070", httpPort), dindImage, "enclave", "server", "--listen", "0.0.0.0:7070", "--data-dir", "/data", "--log-format", "json",
		"--console-url", fmt.Sprintf("http://%s:%d", gw, httpPort), "--tunnel-address", gw, "--tunnel-port", fmt.Sprint(tunnelMain),
		"--publish-address", "0.0.0.0", "--poll-interval", "500ms", "--machine-check-interval", "2s", "--machine-offline-after", "6s", "--reschedule-after", "6s")
	s := &server{t: t, url: fmt.Sprintf("http://127.0.0.1:%d", httpPort), logs: &bytes.Buffer{}}
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
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", srv).CombinedOutput()
			var keep []string
			for _, l := range strings.Split(string(out), "\n") {
				if !strings.Contains(l, `"msg":"request"`) {
					keep = append(keep, l)
				}
			}
			t.Logf("server log (without requests):\n%s", tail(strings.Join(keep, "\n"), 80))
		}
	})
	_, c := setupAndToken(t, s)
	// The server runs in a container, so the main machine's name comes from the container's host name, not
	// this computer's (MCH-01): ask the server.
	mainName := mainMachineName(t, c)
	explain = func() string {
		var b strings.Builder
		if ms, err := c.ListMachines(ctx); err == nil {
			for _, m := range ms {
				fmt.Fprintf(&b, "machine %s: %s schedulable=%v labels=%v memory %d/%d %s\n", m.Name, m.State, m.Schedulable, m.Labels, m.MemoryReservedBytes, m.MemoryTotalBytes, m.Message)
			}
		}
		if rs, err := c.ListAll(ctx, client.ListOptions{}); err == nil {
			for _, r := range rs {
				fmt.Fprintf(&b, "%s/%s on %v: %s %q\n", r.Kind, r.Name, r.Outputs["machine"], r.Status.Phase, r.Status.Message)
				if ev, err := c.Events(ctx, r.ID, 5); err == nil {
					for _, e := range ev {
						fmt.Fprintf(&b, "    %s %s\n", e.Type, e.Message)
					}
				}
			}
		}
		for _, where := range [][]string{{"network", "inspect", "enclave"}, {"exec", m2, "docker", "network", "inspect", "enclave"}} {
			out, _ := exec.Command("docker", where...).CombinedOutput()
			fmt.Fprintf(&b, "docker %s:\n%s\n", strings.Join(where, " "), tail(string(out), 60))
		}
		return b.String()
	}
	t.Cleanup(func() { explain = nil })
	// What the server made on this Docker goes before the server does: resources through the API, then the
	// mesh and its stand-ins, which only this run made (it skips when a mesh was here before).
	keepOnFailure(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if rs, err := c.ListAll(ctx, client.ListOptions{}); err == nil {
			for _, r := range rs {
				if r.Stack == nil && r.OwnerID == nil {
					_, _ = c.DeleteResource(ctx, r.ID, nil)
				}
			}
			for _, r := range rs {
				if r.Outputs["machine"] == nil || r.Outputs["machine"] == mainName {
					_ = c.WaitGone(ctx, r.ID, time.Second)
				}
			}
		}
		// The server stops first, or it puts back the mesh as it goes.
		_ = exec.Command("docker", "rm", "-f", srv).Run()
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=enclave.standin-of").Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
		_ = exec.Command("docker", "rm", "-f", "es-system-mesh").Run()
		_ = exec.Command("docker", "volume", "rm", "es-system-mesh-data").Run()
		// The mesh and the health-check helper stay attached to the networks until they go, so the test's
		// network goes after the mesh, with the helper detached (TestMain removes the helper at the end).
		_ = exec.Command("docker", "network", "disconnect", "-f", "es-net-shop", "es-system-probe").Run()
		_ = exec.Command("docker", "network", "rm", "es-net-shop").Run()
	})

	// The second machine: a Docker of its own, reached through published ports.
	keepOnFailure(t, func() { _ = exec.Command("docker", "rm", "-f", "-v", m2).Run() })
	docker(t, "run", "-d", "--privileged", "--name", m2, "-e", "DOCKER_TLS_CERTDIR=", "-v", bin+":/usr/local/bin/enclave:ro",
		"-p", fmt.Sprintf("%d:%d", agentPort, agentPort), "-p", fmt.Sprintf("%d:%d/udp", tunnel2, tunnel2), dindImage,
		"--bip=192.168.209.1/24") // its own bridge would take 172.18/16, the main machine's default range on a fresh Docker
	until(t, time.Minute, "the second Docker", func() bool { return exec.Command("docker", "exec", m2, "docker", "info").Run() == nil })
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "exec", m2, "cat", "/tmp/agent.log").CombinedOutput()
			t.Logf("agent log:\n%s", tail(string(out), 60))
		}
	})

	added, err := c.AddMachine(ctx, api.AddMachine{Name: "rack-1", Labels: map[string]string{"zone": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(added.JoinCommand, fmt.Sprintf("http://%s:%d", gw, httpPort)) {
		t.Errorf("join command %q", added.JoinCommand)
	}
	agent := fmt.Sprintf("echo $$ > /tmp/agent.pid; exec enclave agent join http://%s:%d --code %s --advertise %s --listen :%d --tunnel-port %d >/tmp/agent.log 2>&1",
		gw, httpPort, added.JoinCode, gw, agentPort, tunnel2)
	docker(t, "exec", "-d", m2, "sh", "-c", agent)
	until(t, 2*time.Minute, "rack-1 online", func() bool {
		m, err := c.GetMachineByName(ctx, "rack-1")
		return err == nil && m.State == api.MachineOnline
	})

	// A network across both machines: a cache on rack-1, reached by name from a job on main (XNET-*).
	create := func(kind api.Kind, name string, spec any) api.Resource {
		t.Helper()
		r, err := c.CreateResource(ctx, kind, name, spec)
		if err != nil {
			t.Fatal(err)
		}
		r, err = c.WaitReady(ctx, r.ID, time.Second, nil)
		if err != nil {
			t.Fatalf("%s %s: %v", kind, name, err)
		}
		return r
	}
	create(api.KindNetwork, "shop", api.NetworkSpec{CIDR: "10.77.0.0/24"})
	cache := create(api.KindCache, "sessions", api.CacheSpec{Engine: api.CacheEngine, Version: api.CacheVersion, Network: "shop", Placed: api.Placed{MachineLabels: map[string]string{"zone": "b"}}})
	if cache.Outputs["machine"] != "rack-1" {
		t.Fatalf("the cache runs on %v", cache.Outputs["machine"])
	}
	host := fmt.Sprint(cache.Outputs["internalHost"])
	job := func(name, cmd string) api.Resource {
		return create(api.KindJob, name, api.JobSpec{Schedule: "0 0 1 1 *", Image: "redis:7", Command: []string{"sh", "-c", cmd}, Network: "shop", Placed: api.Placed{Machine: mainName}})
	}
	set := job("set-key", fmt.Sprintf("redis-cli -h %s set greeting hello-from-main", host))
	if d := runJob(t, c, set.ID); d.Status != "succeeded" || !strings.Contains(d.OutputTail, "OK") {
		t.Fatalf("across machines: %s %s %s", d.Status, d.Message, d.OutputTail)
	}

	// Move the cache to main, with its data (MOVE-*).
	if _, err := c.UpdateResource(ctx, cache.ID, map[string]any{"machine": mainName}, nil); err != nil {
		t.Fatal(err)
	}
	until(t, 5*time.Minute, "the move", func() bool {
		r, err := c.GetResource(ctx, cache.ID)
		return err == nil && r.Status.Phase == api.PhaseReady && r.Outputs["machine"] == mainName
	})
	get := job("get-key", fmt.Sprintf("redis-cli -h %s get greeting", host))
	if d := runJob(t, c, get.ID); d.Status != "succeeded" || !strings.Contains(d.OutputTail, "hello-from-main") {
		t.Fatalf("after the move the data is gone: %s %s %s", d.Status, d.Message, d.OutputTail)
	}

	// An app on rack-1 starts on main once rack-1 has been offline long enough (FAIL-02); main only gets
	// the label the app asks for once it runs, so it starts on rack-1.
	drifter := create(api.KindApp, "drifter", api.AppSpec{Image: "nginx:1.27", Port: 80, Placed: api.Placed{MachineLabels: map[string]string{"zone": "b"}}})
	if drifter.Outputs["machine"] != "rack-1" {
		t.Fatalf("drifter runs on %v", drifter.Outputs["machine"])
	}
	if _, err := c.UpdateMachineByName(ctx, mainName, api.UpdateMachine{Labels: map[string]string{"zone": "b"}}); err != nil {
		t.Fatal(err)
	}
	docker(t, "exec", m2, "sh", "-c", "kill $(cat /tmp/agent.pid)")
	until(t, time.Minute, "rack-1 offline", func() bool {
		m, err := c.GetMachineByName(ctx, "rack-1")
		return err == nil && m.State == api.MachineOffline
	})
	until(t, 3*time.Minute, "drifter on main", func() bool {
		r, err := c.GetResource(ctx, drifter.ID)
		return err == nil && r.Status.Phase == api.PhaseReady && r.Outputs["machine"] == mainName
	})
	if out := mustCLI(t, s, tokenOf(c), "", "machine", "ls"); !regexp.MustCompile(`rack-1\s.*offline`).MatchString(out) {
		t.Errorf("machine ls: %s", out)
	}
	events, err := c.MachineEvents(ctx, "rack-1")
	if err != nil || !strings.Contains(fmt.Sprint(events), "offline") {
		t.Errorf("rack-1 events: %v %v", events, err)
	}
}

func tokenOf(c *client.Client) string { return c.Token }

// mainMachineName is the name the server gave its main machine.
func mainMachineName(t *testing.T, c *client.Client) string {
	t.Helper()
	ms, err := c.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("list machines: %v", err)
	}
	for _, m := range ms {
		if m.Main {
			return m.Name
		}
	}
	t.Fatalf("no main machine in %+v", ms)
	return ""
}
