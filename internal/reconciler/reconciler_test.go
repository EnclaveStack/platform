package reconciler_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Resilience behaviour from L3-007, driven through the real API against the fake engine.

func eventTypes(t *testing.T, c *client.Client, id string) map[string]int {
	t.Helper()
	evs, err := c.Events(context.Background(), id, 200)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range evs {
		out[e.Type]++
	}
	return out
}

func TestTransientFailuresAreRetriedWithBackoffThenSucceed(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	env.Fake.SetFault("flaky", fake.Fault{TransientFailures: 2})
	ctx := context.Background()
	res, err := c.CreateResource(ctx, api.KindCache, "flaky", api.CacheSpec{})
	if err != nil {
		t.Fatal(err)
	}
	// First retry waits ~2 s, second ~4 s (±20 %).
	ready := waitReadyWithin(t, c, res.ID, 15*time.Second)
	if ready.Status.Attempts != 0 || ready.Status.Message != "" {
		t.Errorf("attempts/message not reset on success: %+v", ready.Status)
	}
	ev := eventTypes(t, c, res.ID)
	if ev["retry_scheduled"] != 2 || ev["ready"] != 1 {
		t.Errorf("events %v", ev)
	}
	if env.Fake.EnsureCalls("flaky") != 3 {
		t.Errorf("driver called %d times, want 3", env.Fake.EnsureCalls("flaky"))
	}
}

func waitReadyWithin(t *testing.T, c *client.Client, id string, d time.Duration) api.Resource {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	r, err := c.WaitReady(ctx, id, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("wait ready: %v (status %+v)", err, r.Status)
	}
	return r
}

func TestPermanentFailureStopsAndRetryActionRecovers(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	env.Fake.SetFault("broken", fake.Fault{Permanent: true})
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindApp, "broken", api.AppSpec{Image: "acme/missing:1"})
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failed, err := c.WaitReady(wctx, res.ID, 20*time.Millisecond, nil)
	var fe *client.FailedError
	if err == nil || !asFailed(err, &fe) {
		t.Fatalf("want failed, got %v", err)
	}
	if failed.Status.Phase != api.PhaseFailed || failed.Status.Message == "" || env.Fake.EnsureCalls("broken") != 1 {
		t.Errorf("permanent failure retried or silent: %+v calls=%d", failed.Status, env.Fake.EnsureCalls("broken"))
	}
	var sawFailedStep bool
	for _, s := range failed.Status.Steps {
		sawFailedStep = sawFailedStep || s.State == api.StepFailed
	}
	if !sawFailedStep {
		t.Errorf("no failed step recorded: %+v", failed.Status.Steps)
	}
	env.Fake.SetFault("broken", fake.Fault{})
	if _, err := c.Action(ctx, res.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, res.ID, 10*time.Second)
}

func asFailed(err error, target **client.FailedError) bool {
	f, ok := err.(*client.FailedError)
	*target = f
	return ok
}

func TestRecoveryAfterRestartMidProvisioning(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.FakeStepDelay = 400 * time.Millisecond }))
	c, tok := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{})
	testenv.Eventually(t, 5*time.Second, "provisioning to start", func() bool {
		r, _ := c.GetResource(ctx, res.ID)
		return r.Status.Phase == api.PhaseProvisioning
	})
	env = env.Restart(t) // the in-flight reconcile is interrupted
	c = client.New(env.URL, tok)
	r := waitReadyWithin(t, c, res.ID, 15*time.Second)
	if r.Status.ObservedGeneration != 1 {
		t.Errorf("recovered at generation %d", r.Status.ObservedGeneration)
	}
	if env.SetupCode != "" {
		t.Error("restart re-opened setup")
	}
}

func TestEngineOutageQueuesWorkWithoutExhaustingAttempts(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	env.Fake.SetAvailable(false)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindCache, "later", api.CacheSpec{})
	testenv.Eventually(t, 5*time.Second, "engine wait message", func() bool {
		r, _ := c.GetResource(ctx, res.ID)
		return r.Status.Message != "" && r.Status.Attempts == 0
	})
	r, _ := c.GetResource(ctx, res.ID)
	if r.Status.Phase == api.PhaseFailed {
		t.Fatal("engine outage failed the resource")
	}
	env.Fake.SetAvailable(true)
	waitReadyWithin(t, c, res.ID, 15*time.Second)
}

func TestDriftIsDetectedAndRepaired(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
	calls := env.Fake.EnsureCalls("sessions")
	env.Fake.Kill(res.ID)
	testenv.Eventually(t, 10*time.Second, "drift repair", func() bool {
		return env.Fake.EnsureCalls("sessions") > calls && eventTypes(t, c, res.ID)["drift_detected"] >= 1
	})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
}

func TestSpecChangeDuringReconcileEndsAtTheNewGeneration(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.FakeStepDelay = 200 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1"})
	testenv.Eventually(t, 5*time.Second, "provisioning", func() bool {
		r, _ := c.GetResource(ctx, res.ID)
		return r.Status.Phase == api.PhaseProvisioning
	})
	if _, err := c.UpdateResource(ctx, res.ID, map[string]string{"image": "nginx:2"}, nil); err != nil {
		t.Fatal(err)
	}
	r := waitReadyWithin(t, c, res.ID, 15*time.Second)
	if r.Generation != 2 || r.Status.ObservedGeneration != 2 {
		t.Errorf("generation %d observed %d", r.Generation, r.Status.ObservedGeneration)
	}
}

func TestReconcileThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput test")
	}
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := c.CreateResource(ctx, api.KindCache, fmt.Sprintf("cache-%03d", i), api.CacheSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	testenv.Eventually(t, 30*time.Second, "all 200 ready", func() bool {
		all, err := c.ListAllResources(ctx, api.KindCache)
		if err != nil {
			return false
		}
		ready := 0
		for _, r := range all {
			if r.Status.Phase == api.PhaseReady {
				ready++
			}
		}
		return ready == n
	})
	t.Logf("200 resources created and ready in %s (L5-003 target < 30 s)", time.Since(start).Round(time.Millisecond))
}
