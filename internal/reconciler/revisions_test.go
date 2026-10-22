package reconciler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Zero-downtime deploys (ADR-016, L2-005 DEP-01 – DEP-06) through the real API on the fake engine.

func TestEachDeployIsANewRevisionAndAFailedOneLeavesTheServingRevision(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, err := c.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx:1.27-alpine", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	first := waitReadyWithin(t, c, res.ID, 10*time.Second)
	if first.Outputs["servingRevision"] != float64(1) || first.Outputs["lastDeploy"] != "succeeded" {
		t.Fatalf("first deploy outputs %v", first.Outputs)
	}

	upd, err := c.UpdateResource(ctx, res.ID, map[string]any{"image": "nginx:1.28-alpine"}, &first.Generation)
	if err != nil {
		t.Fatal(err)
	}
	second := waitReadyWithin(t, c, upd.ID, 10*time.Second)
	if second.Outputs["servingRevision"] != float64(2) {
		t.Fatalf("update should serve revision 2: %v", second.Outputs)
	}

	env.Fake.SetFault("web", fake.Fault{Unhealthy: true})
	upd, err = c.UpdateResource(ctx, res.ID, map[string]any{"image": "nginx:broken"}, &second.Generation)
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	failed, err := c.WaitReady(wctx, upd.ID, 20*time.Millisecond, nil)
	var fe *client.FailedError
	if !asFailed(err, &fe) {
		t.Fatalf("want failed deploy, got %v", err)
	}
	if !strings.Contains(failed.Status.Message, "Revision 3 failed its health check") || !strings.Contains(failed.Status.Message, "Revision 2 is still serving") {
		t.Errorf("message should name both revisions: %q", failed.Status.Message)
	}
	if failed.Outputs["servingRevision"] != float64(2) || failed.Outputs["lastDeploy"] != "failed" {
		t.Errorf("the serving revision must survive a failed deploy: %v", failed.Outputs)
	}
	ev := eventTypes(t, c, res.ID)
	for _, typ := range []string{"revision_started", "traffic_switched", "health_check_failed"} {
		if ev[typ] == 0 {
			t.Errorf("missing %s event: %v", typ, ev)
		}
	}

	// Retrying the failed deploy spends a fresh number: numbers are never reused (ADR-016).
	if _, err := c.Action(ctx, res.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, err := c.GetResource(ctx, res.ID)
		return err == nil && got.Status.Phase == api.PhaseFailed && strings.Contains(got.Status.Message, "Revision 4 failed")
	})

	// Fixing the spec deploys again from the serving revision.
	env.Fake.SetFault("web", fake.Fault{})
	upd, err = c.UpdateResource(ctx, res.ID, map[string]any{"image": "nginx:1.29-alpine"}, &failed.Generation)
	if err != nil {
		t.Fatal(err)
	}
	fixed := waitReadyWithin(t, c, upd.ID, 10*time.Second)
	if fixed.Outputs["servingRevision"] != float64(5) || fixed.Outputs["latestRevision"] != float64(5) || fixed.Outputs["lastDeploy"] != "succeeded" {
		t.Errorf("fixed deploy outputs %v", fixed.Outputs)
	}

	// Restart keeps the serving revision; redeploy makes a new one.
	if _, err := c.Action(ctx, res.ID, api.ActionRedeploy); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := c.GetResource(ctx, res.ID)
		if err == nil && got.Status.Phase == api.PhaseReady && got.Outputs["servingRevision"] == float64(6) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("redeploy did not produce revision 6: %+v", got.Outputs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStrategyAndHealthCheckRoundTrip(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	spec := api.AppSpec{Image: "nginx:1.27-alpine", Port: 80, Strategy: api.StrategyRecreate,
		HealthCheck: &api.HealthCheck{Type: api.HealthHTTP, Path: "/healthz", Retries: 5}}
	res, err := c.CreateResource(ctx, api.KindApp, "api", spec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GetResource(ctx, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	var s api.AppSpec
	if err := api.DecodeStrict(got.Spec, &s); err != nil {
		t.Fatal(err)
	}
	if s.Strategy != api.StrategyRecreate || s.HealthCheck == nil || s.HealthCheck.Path != "/healthz" || s.HealthCheck.Retries != 5 || s.HealthCheck.IntervalSeconds != api.HealthIntervalDefault {
		t.Errorf("spec did not round-trip with defaults: %+v %+v", s, s.HealthCheck)
	}
	_, err = c.CreateResource(ctx, api.KindApp, "bad", map[string]any{"image": "nginx", "healthCheck": map[string]any{"type": "tcp"}})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeValidationFailed || len(ae.Details) == 0 || ae.Details[0].Field != "spec.healthCheck.port" {
		t.Errorf("tcp check without a port: %v", err)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
