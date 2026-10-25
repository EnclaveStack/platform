//go:build docker

package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// Replicas and load balancers on real Docker (ADR-022, L5-002 §7).

func TestReplicasBehindALoadBalancer(t *testing.T) {
	d := fastDriver(t)
	ctx := context.Background()
	cidr, _, _ := randomCIDR()
	n, _ := newNetwork(t, d, api.NetworkSpec{CIDR: cidr})
	placement := engine.Placement{Network: engine.DockerNetwork("", n.Name)}

	app := resource(api.KindApp, "it-rep-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	spec := api.AppSpec{Image: "traefik/whoami:v1.11", Port: 80, MemoryMiB: 64, Strategy: api.StrategyRolling, Replicas: 3}
	out := ensure(t, d, engine.EnsureInput{Resource: app, Spec: spec, Revision: 1, Placement: placement})
	if out[engine.OutputRunningReplicas] != 3 || len(revisionsOf(t, d, app)) != 3 {
		t.Fatalf("replicas: %v %v", out, revisionsOf(t, d, app))
	}
	app.Spec = []byte(`{"replicas":3}`)
	if h, err := d.Health(ctx, app, engine.ObjectStore{}); err != nil || h != engine.Healthy {
		t.Errorf("health %v %v", h, err)
	}

	r := resource(api.KindLoadBalancer, "it-lb-"+suffix())
	cleanup(t, d, r, engine.ObjectStore{})
	lbSpec := api.LoadBalancerSpec{Port: 80, Protocol: api.LBProtocolHTTP, Publish: true, Algorithm: api.AlgorithmRoundRobin}
	cfg := &lb.Config{HTTP: true, Port: 80, Algorithm: api.AlgorithmRoundRobin, Check: lb.Check{HTTP: true, Path: "/", IntervalSeconds: 1, TimeoutSeconds: 1, HealthyThreshold: 1, UnhealthyThreshold: 2},
		Targets: []lb.Target{{Host: engine.ContainerName(app), Port: 80, Weight: 1}}}
	lbOut := ensure(t, d, engine.EnsureInput{Resource: r, Spec: lbSpec, LoadBalancer: cfg, Placement: placement})
	url := fmt.Sprintf("http://127.0.0.1:%v/", lbOut["port"])

	status := func() lb.Status {
		st, err := d.LoadBalancerStatus(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	waitFor(t, 15*time.Second, func() bool { return status() == lb.Status{Targets: 3, Healthy: 3} })
	seen := map[string]bool{}
	for range 30 {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
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
		t.Errorf("requests reached %d replicas, want 3: %v", len(seen), seen)
	}

	// A stopped replica gets no traffic and is reported; the app's health says so too.
	revs := revisionsOf(t, d, app)
	timeout := 1
	if err := d.cli.ContainerStop(ctx, revs[1], container.StopOptions{Timeout: &timeout}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, func() bool { st := status(); return st.Healthy == 2 })
	for range 10 {
		resp, err := http.Get(url)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("a request failed with one replica stopped: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	if h, _ := d.Health(ctx, app, engine.ObjectStore{}); h != engine.Stopped {
		t.Errorf("app health with a stopped replica: %v", h)
	}

	// Reconfiguring reloads in place: the same container, still serving.
	before, _ := d.inspect(ctx, engine.ContainerName(r))
	cfg.Algorithm = api.AlgorithmLeastConn
	ensure(t, d, engine.EnsureInput{Resource: r, Spec: lbSpec, LoadBalancer: cfg, Placement: placement})
	after, _ := d.inspect(ctx, engine.ContainerName(r))
	if before.ID != after.ID || hostPort(after, 80) != hostPort(before, 80) {
		t.Errorf("reconfiguring recreated the container or moved its port")
	}
	if resp, err := http.Get(url); err != nil || resp.StatusCode != 200 {
		t.Errorf("after reload: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}

	// A configuration HAProxy rejects is permanent, and the running one keeps serving.
	bad := *cfg
	bad.Targets = []lb.Target{{Host: "bad host", Port: 80, Weight: 1}}
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := d.Ensure(sctx, engine.EnsureInput{Resource: r, Spec: lbSpec, LoadBalancer: &bad, Placement: placement}, &steps{}); err == nil || !engine.IsPermanent(err) {
		t.Errorf("rejected configuration: %v", err)
	}
	if resp, err := http.Get(url); err != nil || resp.StatusCode != 200 {
		t.Errorf("after a rejected configuration: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
