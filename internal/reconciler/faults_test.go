package reconciler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Error branches of the reconciler (L3-007 §1), driven by fake-driver fault injection.

func TestDeleteFailureIsRetriedUntilGone(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindCache, "sticky", api.CacheSpec{})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
	env.Fake.SetFault("sticky", fake.Fault{DeleteFailures: 1})
	if _, err := c.DeleteResource(ctx, res.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "delete retry message", func() bool {
		got, err := c.GetResource(ctx, res.ID)
		return err == nil && strings.HasPrefix(got.Status.Message, "Delete failed, retrying")
	})
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.WaitGone(wctx, res.ID, 50*time.Millisecond); err != nil {
		t.Fatalf("not deleted after the retry: %v", err)
	}
}

func TestPermanentRestartFailureIsReportedAndLeavesTheResourceReady(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindApp, "stubborn", api.AppSpec{Image: "nginx"})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
	env.Fake.SetFault("stubborn", fake.Fault{RestartPermanent: true})
	if _, err := c.Action(ctx, res.ID, api.ActionRestart); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "restart failure event", func() bool {
		evs, _ := c.Events(ctx, res.ID, 5)
		return len(evs) > 0 && evs[0].Type == "failed" && strings.HasPrefix(evs[0].Message, "Restart failed")
	})
	got, _ := c.GetResource(ctx, res.ID)
	if got.Status.Phase != api.PhaseReady {
		t.Errorf("a failed restart changed the phase to %s", got.Status.Phase)
	}
	// The request was cleared: a new restart is accepted.
	env.Fake.SetFault("stubborn", fake.Fault{})
	if _, err := c.Action(ctx, res.ID, api.ActionRestart); err != nil {
		t.Errorf("second restart refused: %v", err)
	}
}

func TestVeryLongEngineMessagesAreTruncated(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	env.Fake.SetFault("noisy", fake.Fault{Permanent: true, Message: strings.Repeat("x", 2000)})
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindApp, "noisy", api.AppSpec{Image: "nginx"})
	testenv.Eventually(t, 5*time.Second, "failed", func() bool {
		got, _ := c.GetResource(ctx, res.ID)
		return got.Status.Phase == api.PhaseFailed
	})
	got, _ := c.GetResource(ctx, res.ID)
	if n := len([]rune(got.Status.Message)); n > 501 || !strings.HasSuffix(got.Status.Message, "…") {
		t.Errorf("message length %d, want truncated to 500 + …", n)
	}
}

func TestDriftOfBucketsAndEngineOutageInDriftChecks(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	res, _ := c.CreateResource(ctx, api.KindBucket, "media", api.BucketSpec{})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
	env.Fake.KillBucket("media")
	testenv.Eventually(t, 5*time.Second, "bucket drift repaired", func() bool {
		evs, _ := c.Events(ctx, res.ID, 20)
		for _, e := range evs {
			if e.Type == "drift_detected" {
				return true
			}
		}
		return false
	})
	waitReadyWithin(t, c, res.ID, 5*time.Second)
	env.Fake.SetAvailable(false)
	testenv.Eventually(t, 5*time.Second, "engine_up gauge 0", func() bool {
		return strings.Contains(httpGet(t, env.URL+"/metrics"), "enclave_engine_up 0")
	})
	if got, _ := c.GetResource(ctx, res.ID); got.Status.Phase != api.PhaseReady {
		t.Errorf("engine outage alone changed the phase to %s", got.Status.Phase)
	}
}

func TestUndecryptableSecretFailsTheAppPermanently(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	sec, _ := c.CreateResource(ctx, api.KindSecret, "garbled", api.SecretSpec{Value: "v"})
	waitReadyWithin(t, c, sec.ID, 5*time.Second)
	stored, err := env.Store.GetResource(ctx, sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.SecretCiphertext = []byte("not a ciphertext at all, definitely too long to be empty")
	if err := env.Store.SaveResource(ctx, stored); err != nil {
		t.Fatal(err)
	}
	app, _ := c.CreateResource(ctx, api.KindApp, "user", api.AppSpec{Image: "nginx", SecretEnv: map[string]string{"K": "garbled"}})
	testenv.Eventually(t, 5*time.Second, "permanent failure", func() bool {
		got, _ := c.GetResource(ctx, app.ID)
		return got.Status.Phase == api.PhaseFailed && strings.Contains(got.Status.Message, "can't be decrypted")
	})
	if env.Fake.EnsureCalls("user") != 0 {
		t.Error("the driver ran without the secret")
	}
}
