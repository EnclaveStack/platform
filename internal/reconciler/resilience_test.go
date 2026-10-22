package reconciler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/reconciler"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

func TestAttemptsRunOutThenFailed(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	env.Fake.SetFault("doomed", fake.Fault{TransientFailures: 1000})
	res, _ := c.CreateResource(ctx, api.KindCache, "doomed", api.CacheSpec{})
	// Wait for the first failed attempt: the next one is then scheduled ~2 s out, so nothing is in
	// flight while the test fast-forwards the counter to the last allowed attempt.
	testenv.Eventually(t, 10*time.Second, "first attempt to fail", func() bool {
		got, _ := c.GetResource(ctx, res.ID)
		return got.Status.Attempts == 1
	})
	r, err := env.Store.GetResource(ctx, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Attempts = reconciler.MaxAttempts - 1
	r.NextAttemptAt = nil
	if err := env.Store.SaveResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "failed after max attempts", func() bool {
		got, _ := c.GetResource(ctx, res.ID)
		return got.Status.Phase == api.PhaseFailed && got.Status.Attempts == reconciler.MaxAttempts
	})
}

func TestWorkerPanicIsRecoveredAsTransient(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	env.Fake.SetFault("crashy", fake.Fault{Panic: true})
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindApp, "crashy", api.AppSpec{Image: "nginx"})
	testenv.Eventually(t, 10*time.Second, "panic recorded as a retry", func() bool {
		got, _ := c.GetResource(ctx, res.ID)
		return got.Status.Attempts >= 1 && strings.Contains(got.Status.Message, "internal error while reconciling")
	})
	if !strings.Contains(env.Logs.String(), "reconcile panicked") {
		t.Error("panic not logged")
	}
	env.Fake.SetFault("crashy", fake.Fault{})
	// The server is still serving and recovers the resource after the backoff.
	waitReadyWithin(t, c, res.ID, 15*time.Second)
}

func TestDeleteAndRestartWaitForTheEngine(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	app, _ := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx"})
	cache, _ := c.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{})
	waitReadyWithin(t, c, app.ID, 5*time.Second)
	waitReadyWithin(t, c, cache.ID, 5*time.Second)

	env.Fake.SetAvailable(false)
	if _, err := c.Action(ctx, cache.ID, api.ActionRestart); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteResource(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "delete waiting for engine", func() bool {
		got, err := c.GetResource(ctx, app.ID)
		return err == nil && got.Status.Phase == api.PhaseDeleting && strings.Contains(got.Status.Message, "container engine")
	})
	env.Fake.SetAvailable(true)
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.WaitGone(wctx, app.ID, 50*time.Millisecond); err != nil {
		t.Fatalf("delete did not finish after the engine came back: %v", err)
	}
	testenv.Eventually(t, 15*time.Second, "restart carried out", func() bool {
		evs, _ := c.Events(ctx, cache.ID, 10)
		return len(evs) > 0 && evs[0].Type == "restarted"
	})
}

func TestMetricsReflectReconcileOutcomes(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindCache, "metered", api.CacheSpec{})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
	body := httpGet(t, env.URL+"/metrics")
	for _, want := range []string{`enclave_reconcile_total{kind="cache",result="ready"}`, `enclave_resources{kind="cache",phase="ready"}`, "enclave_reconcile_duration_seconds_bucket"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}
