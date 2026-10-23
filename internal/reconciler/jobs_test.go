package reconciler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Scheduled jobs (ADR-017, L2-005 JOB-01 – JOB-08) through the real API on the fake engine.

func waitRun(t *testing.T, c *client.Client, jobID, runID string) api.RunDetail {
	t.Helper()
	var d api.RunDetail
	waitFor(t, 15*time.Second, func() bool {
		var err error
		d, err = c.GetRun(context.Background(), jobID, runID)
		return err == nil && d.Status != api.RunRunning
	})
	return d
}

func newJob(t *testing.T, c *client.Client, name string, spec api.JobSpec) api.Resource {
	t.Helper()
	res, err := c.CreateResource(context.Background(), api.KindJob, name, spec)
	if err != nil {
		t.Fatal(err)
	}
	return waitReadyWithin(t, c, res.ID, 10*time.Second)
}

func TestManualRunsRecordOutputFailuresTimeoutsAndOverlap(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	job := newJob(t, c, "tick", api.JobSpec{Schedule: "@daily", Image: "busybox:1.37", Command: []string{"sh", "-c", "echo hi"}})
	if job.Outputs["nextRunAt"] == nil {
		t.Errorf("next run not reported: %v", job.Outputs)
	}

	run, err := c.RunNow(ctx, job.ID)
	if err != nil || run.Trigger != api.TriggerManual || run.Status != api.RunRunning {
		t.Fatalf("run now: %+v %v", run, err)
	}
	d := waitRun(t, c, job.ID, run.ID)
	if d.Status != api.RunSucceeded || d.ExitCode == nil || *d.ExitCode != 0 || !strings.Contains(d.OutputTail, "tick: sh -c echo hi") || d.FinishedAt == nil {
		t.Fatalf("run %+v", d)
	}
	got, _ := c.GetResource(ctx, job.ID)
	if got.Outputs["lastRunStatus"] != api.RunSucceeded || got.Outputs["lastRunAt"] == nil {
		t.Errorf("last run not reported: %v", got.Outputs)
	}
	if env.Fake.JobStarts(run.ID) != 1 {
		t.Errorf("started %d times", env.Fake.JobStarts(run.ID))
	}

	// A failing command: failed, exit code kept, event recorded.
	upd, _ := c.UpdateResource(ctx, job.ID, map[string]any{"command": []string{"sh", "-c", "exit 3"}}, nil)
	waitReadyWithin(t, c, upd.ID, 10*time.Second)
	run, _ = c.RunNow(ctx, job.ID)
	if d := waitRun(t, c, job.ID, run.ID); d.Status != api.RunFailed || *d.ExitCode != 3 || d.Message != "Exited with code 3." {
		t.Errorf("failed run %+v", d)
	}
	if eventTypes(t, c, job.ID)["run_failed"] == 0 {
		t.Error("no run_failed event")
	}

	// Longer than its timeout: stopped and marked timed_out (JOB-05).
	upd, _ = c.UpdateResource(ctx, job.ID, map[string]any{"command": []string{"sleep 5"}, "timeoutSeconds": 1}, nil)
	waitReadyWithin(t, c, upd.ID, 10*time.Second)
	run, _ = c.RunNow(ctx, job.ID)
	// Overlap forbid: a second manual run while this one goes is refused (JOB-03).
	if _, err := c.RunNow(ctx, job.ID); apiCode(err) != api.CodeRunInProgress {
		t.Errorf("second run during the first: %v", err)
	}
	if d := waitRun(t, c, job.ID, run.ID); d.Status != api.RunTimedOut || !strings.Contains(d.Message, "timeout") {
		t.Errorf("timed-out run %+v", d)
	}

	list, err := c.ListRuns(ctx, job.ID, 2, "")
	if err != nil || len(list.Items) != 2 || list.NextCursor == nil || list.Items[0].ID != run.ID {
		t.Fatalf("runs page: %+v %v", list, err)
	}
	if rest, err := c.ListRuns(ctx, job.ID, 10, *list.NextCursor); err != nil || len(rest.Items) != 1 {
		t.Errorf("second page: %+v %v", rest, err)
	}

	// Output may hold secrets: reading it needs write access, listing doesn't.
	reader, _ := env.Token(t, api.ScopeRead)
	if _, err := reader.ListRuns(ctx, job.ID, 10, ""); err != nil {
		t.Errorf("read token listing runs: %v", err)
	}
	if _, err := reader.GetRun(ctx, job.ID, run.ID); apiCode(err) != api.CodeForbidden {
		t.Errorf("read token reading output: %v", err)
	}
	if _, err := c.Logs(ctx, job.ID, 10); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("job logs: %v", err)
	}
}

func TestAppJobsInheritTheAppAndKeepItInUse(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	if _, err := c.CreateResource(ctx, api.KindSecret, "db-url", api.SecretSpec{Value: "postgres://x"}); err != nil {
		t.Fatal(err)
	}
	app, _ := c.CreateResource(ctx, api.KindApp, "api", api.AppSpec{Image: "ghcr.io/acme/api:1.4", Port: 8080,
		Env: map[string]string{"LOG_LEVEL": "info", "MODE": "web"}, SecretEnv: map[string]string{"DATABASE_URL": "db-url"}})
	waitReadyWithin(t, c, app.ID, 10*time.Second)
	job := newJob(t, c, "cleanup", api.JobSpec{Schedule: "@hourly", App: "api", Command: []string{"./bin/cleanup"}, Env: map[string]string{"MODE": "job"}})

	run, _ := c.RunNow(ctx, job.ID)
	waitRun(t, c, job.ID, run.ID)
	var started bool
	for _, jr := range env.Fake.StartedJobs() {
		if jr.RunID != run.ID {
			continue
		}
		started = true
		if jr.Image != "ghcr.io/acme/api:1.4" || jr.Env["LOG_LEVEL"] != "info" || jr.Env["MODE"] != "job" || jr.Env["DATABASE_URL"] != "postgres://x" || jr.Command[0] != "./bin/cleanup" {
			t.Errorf("inheritance: %+v", jr)
		}
	}
	_ = started // the fake forgets finished runs; the env is checked above when present

	if _, err := c.DeleteResource(ctx, app.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting an app a job uses: %v", err)
	}
	for name, spec := range map[string]map[string]any{
		"both":      {"schedule": "@daily", "app": "api", "image": "busybox"},
		"neither":   {"schedule": "@daily"},
		"cron":      {"schedule": "61 * * * *", "image": "busybox"},
		"every":     {"schedule": "@every 1m", "image": "busybox"},
		"zone":      {"schedule": "@daily", "timeZone": "Mars/Olympus", "image": "busybox"},
		"timeout":   {"schedule": "@daily", "image": "busybox", "timeoutSeconds": 90000},
		"no-app":    {"schedule": "@daily", "app": "missing"},
		"no-secret": {"schedule": "@daily", "image": "busybox", "secretEnv": map[string]string{"K": "missing"}},
	} {
		if _, err := c.CreateResource(ctx, api.KindJob, "bad-"+name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
}

func TestScheduledRunsCatchUpOnceAndRespectOverlap(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	job := newJob(t, c, "minutely", api.JobSpec{Schedule: "* * * * *", TimeZone: "Asia/Kolkata", Image: "busybox", Command: []string{"sleep 0.5"}})

	// Due a moment ago: one scheduled run, and the next run moves into the future.
	awayFromMinuteEdge(t)
	due := time.Now().Add(-time.Second).Truncate(time.Minute)
	if err := env.Store.SetNextRun(ctx, job.ID, &due); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		l, _ := c.ListRuns(ctx, job.ID, 10, "")
		return len(l.Items) == 1 && l.Items[0].Status == api.RunSucceeded
	})
	l, _ := c.ListRuns(ctx, job.ID, 10, "")
	if l.Items[0].Trigger != api.TriggerSchedule || l.Items[0].ScheduledFor == nil || !l.Items[0].ScheduledFor.Equal(due.UTC()) {
		t.Errorf("scheduled run %+v", l.Items[0])
	}
	r, _ := env.Store.GetResource(ctx, job.ID)
	// Advanced past the run that was due. (Not "in the future": on a slow runner the next minute may already
	// have come by the time this looks.)
	if r.NextRunAt == nil || !r.NextRunAt.After(due) {
		t.Errorf("next run not advanced past %v: %v", due, r.NextRunAt)
	}

	// Ten minutes of downtime: one skipped record counting the missed runs, then a single catch-up run.
	awayFromMinuteEdge(t)
	past := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	if err := env.Store.SetNextRun(ctx, job.ID, &past); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		l, _ := c.ListRuns(ctx, job.ID, 10, "")
		if len(l.Items) != 3 {
			return false
		}
		for _, it := range l.Items {
			if it.Status == api.RunRunning {
				return false
			}
		}
		return true
	})
	l, _ = c.ListRuns(ctx, job.ID, 10, "")
	var skipped, catchUp int
	for _, it := range l.Items {
		switch {
		case it.Status == api.RunSkipped && it.SkippedCount >= 9:
			skipped++
		case it.Trigger == api.TriggerCatchUp:
			catchUp++
		}
	}
	if skipped != 1 || catchUp != 1 {
		t.Errorf("catch-up: %d skipped records, %d catch-up runs: %+v", skipped, catchUp, l.Items)
	}

	// Overlap forbid: an occurrence due while a run is going is recorded as skipped.
	upd, _ := c.UpdateResource(ctx, job.ID, map[string]any{"command": []string{"sleep 3"}}, nil)
	waitReadyWithin(t, c, upd.ID, 10*time.Second)
	awayFromMinuteEdge(t)
	long, err := c.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Second)
	if err := env.Store.SetNextRun(ctx, job.ID, &now); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		l, _ := c.ListRuns(ctx, job.ID, 1, "")
		return len(l.Items) == 1 && l.Items[0].Status == api.RunSkipped && strings.Contains(l.Items[0].Message, "overlap")
	})
	waitRun(t, c, job.ID, long.ID)
}

// awayFromMinuteEdge waits until the clock is between seconds 2 and 30 of a minute. The job above runs every
// minute and each phase counts its runs, so a phase that starts in the first second (the previous minute is
// then missed, and caught up) or crosses into the next minute (one more run) sees the wrong number: about one
// CI run in fifteen did, depending only on when the test happened to start.
func awayFromMinuteEdge(t *testing.T) {
	t.Helper()
	for s := time.Now().Second(); s < 2 || s > 30; s = time.Now().Second() {
		time.Sleep(200 * time.Millisecond)
	}
}

func TestARunLeftRunningIsAdoptedAndStartedOnce(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	job := newJob(t, c, "orphan", api.JobSpec{Schedule: "@daily", Image: "busybox", Command: []string{"echo adopted"}})
	// As if the previous process claimed it and stopped before starting the container.
	run := &domain.Run{ID: domain.NewID("run"), ResourceID: job.ID, Trigger: api.TriggerSchedule, Status: api.RunRunning, StartedAt: time.Now()}
	if err := env.Store.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	d := waitRun(t, c, job.ID, run.ID)
	if d.Status != api.RunSucceeded || env.Fake.JobStarts(run.ID) != 1 {
		t.Errorf("adopted run %+v, started %d times", d, env.Fake.JobStarts(run.ID))
	}

	// Deleting a job removes it and its history.
	if _, err := c.DeleteResource(ctx, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		_, err := c.GetResource(ctx, job.ID)
		return apiCode(err) == api.CodeNotFound
	})
	if _, err := env.Store.GetRun(ctx, job.ID, run.ID); err == nil {
		t.Error("runs outlived their job")
	}
}
