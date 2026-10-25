package reconciler_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Networks, placement and private DNS (ADR-020, L2-006 NET-01 – NET-08) through the real API on the
// fake engine.

func inRange(t *testing.T, ip any, cidr string) bool {
	t.Helper()
	s, _ := ip.(string)
	a, err := netip.ParseAddr(s)
	return err == nil && netip.MustParsePrefix(cidr).Contains(a)
}

func TestNetworksPlaceResourcesAndGuardTheirLifecycle(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	net, err := c.CreateResource(ctx, api.KindNetwork, "backend", api.NetworkSpec{CIDR: "10.40.0.0/16",
		Subnets: []api.Subnet{{Name: "apps", CIDR: "10.40.2.0/24"}, {Name: "data", CIDR: "10.40.1.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	ready := waitReadyWithin(t, c, net.ID, 10*time.Second)
	if ready.Outputs["dockerNetwork"] != "es-net-backend" || ready.Outputs["gateway"] != "10.40.0.1" || !env.Fake.HasNetwork("backend") {
		t.Fatalf("network outputs %v", ready.Outputs)
	}

	// Placement: the first subnet is the automatic range, another subnet is a fixed range.
	db, err := c.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{Network: "backend", Subnet: "data"})
	if err != nil {
		t.Fatal(err)
	}
	dbReady := waitReadyWithin(t, c, db.ID, 10*time.Second)
	if !inRange(t, dbReady.Outputs[engine.OutputPrivateIP], "10.40.1.0/24") || dbReady.Outputs[engine.OutputPrivateDNS] != "orders.database.internal" {
		t.Errorf("database placement outputs %v", dbReady.Outputs)
	}
	if p := env.Fake.PlacementOf(db.ID); p.Network != "es-net-backend" || p.Subnet != "10.40.1.0/24" {
		t.Errorf("database placement %+v", p)
	}
	app, err := c.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Network: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	appReady := waitReadyWithin(t, c, app.ID, 10*time.Second)
	if !inRange(t, appReady.Outputs[engine.OutputPrivateIP], "10.40.2.0/24") || appReady.Outputs[engine.OutputPrivateDNS] != "api.app.internal" {
		t.Errorf("app placement outputs %v", appReady.Outputs)
	}
	if p := env.Fake.PlacementOf(app.ID); p.Network != "es-net-backend" || p.Subnet != "" {
		t.Errorf("an app in the first subnet takes the automatic range: %+v", p)
	}

	// An image job runs in its own network; an app job in its app's.
	job := newJob(t, c, "migrate", api.JobSpec{Schedule: "@daily", App: "api"})
	run, err := c.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, job.ID, run.ID)
	for _, jr := range env.Fake.StartedJobs() {
		if jr.RunID == run.ID && jr.Placement.Network != "es-net-backend" {
			t.Errorf("app job placement %+v", jr.Placement)
		}
	}

	// Rules: overlaps, unknown names, immutable addresses, publishing without internet, deletion in use.
	for name, spec := range map[string]api.NetworkSpec{
		"overlap": {CIDR: "10.40.128.0/17"},
		"public":  {CIDR: "8.8.8.0/24"},
	} {
		if _, err := c.CreateResource(ctx, api.KindNetwork, name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
	if _, err := c.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{Network: "backend", Subnet: "nope"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("unknown subnet: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{Network: "missing"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("unknown network: %v", err)
	}
	if _, err := c.UpdateResource(ctx, net.ID, map[string]any{"internet": false}, nil); apiCode(err) != api.CodeImmutableField {
		t.Errorf("changing internet access: %v", err)
	}
	f := false
	priv, err := c.CreateResource(ctx, api.KindNetwork, "private", api.NetworkSpec{CIDR: "10.41.0.0/24", Internet: &f})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, priv.ID, 10*time.Second)
	if _, err := c.CreateResource(ctx, api.KindApp, "public-web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Publish: true, Network: "private"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("publishing from a network without internet: %v", err)
	}
	cache, err := c.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{Network: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitReadyWithin(t, c, cache.ID, 10*time.Second); got.Outputs["port"] != nil || got.Outputs["internalPort"] == nil {
		t.Errorf("a cache without internet has no host port: %v", got.Outputs)
	}
	if _, err := c.DeleteResource(ctx, net.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a network in use: %v", err)
	}

	// Moving: the app goes back to the default network with a new revision; then the network can go.
	moved, err := c.UpdateResource(ctx, app.ID, map[string]any{"network": nil}, nil)
	if err != nil {
		t.Fatal(err)
	}
	movedReady := waitReadyWithin(t, c, moved.ID, 10*time.Second)
	if env.Fake.PlacementOf(app.ID).Network != "" || movedReady.Outputs[engine.OutputServingRevision] == appReady.Outputs[engine.OutputServingRevision] {
		t.Errorf("app not moved with a new revision: %+v %v", env.Fake.PlacementOf(app.ID), movedReady.Outputs)
	}
	for _, id := range []string{job.ID, db.ID} {
		if _, err := c.DeleteResource(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitGone(ctx, id, 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.DeleteResource(ctx, net.ID, nil); err != nil {
		t.Fatalf("deleting the unused network: %v", err)
	}
	if err := c.WaitGone(ctx, net.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if env.Fake.HasNetwork("backend") {
		t.Error("engine network left behind")
	}
}

func TestNetworkRemovedOutsideEnclaveStackIsRestored(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	net, err := c.CreateResource(ctx, api.KindNetwork, "backend", api.NetworkSpec{CIDR: "10.40.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, net.ID, 10*time.Second)
	env.Fake.KillNetwork("backend")
	waitFor(t, 15*time.Second, func() bool {
		r, err := c.GetResource(ctx, net.ID)
		return err == nil && r.Status.Phase == api.PhaseReady && env.Fake.HasNetwork("backend") && eventTypes(t, c, net.ID)["drift_detected"] > 0
	})
}
