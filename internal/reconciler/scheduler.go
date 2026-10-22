package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The scheduler (ADR-017, L2-005 JOB-01 – JOB-08): it claims due occurrences in one transaction (so a run
// starts exactly once), and adopts every run still marked running that nobody is executing — a manual
// run, a run claimed just before a crash, or one that outlived the previous process.

const (
	DefaultSchedulerTick = time.Second
	maxConcurrentRuns    = 8
	// maxOccurrenceScan bounds counting missed occurrences after long downtime.
	maxOccurrenceScan = 100_000
	sweepEvery        = time.Hour
)

func (r *Reconciler) schedulerLoop(ctx context.Context) {
	tick := r.SchedulerTick
	if tick <= 0 {
		tick = DefaultSchedulerTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	var lastSweep time.Time
	for {
		r.SchedulerPass(ctx)
		if now := r.Now(); now.Sub(lastSweep) >= sweepEvery {
			if n, err := r.Store.SweepRuns(ctx, now); err != nil && ctx.Err() == nil {
				r.Log.Warn("sweep runs failed", "error", err)
			} else if n > 0 {
				r.Log.Info("old runs removed", "count", n)
			}
			lastSweep = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SchedulerPass claims due occurrences and adopts unattended runs (exported for tests). While the engine
// is down nothing is claimed; occurrences missed meanwhile follow the catch-up rule when it returns.
func (r *Reconciler) SchedulerPass(ctx context.Context) {
	// Synced secrets are read from their store over HTTPS, so they refresh while the engine is down;
	// runs and transcodes wait for it (ADR-041).
	engineUp := r.Driver.Ping(ctx) == nil
	due, err := r.Store.DueSchedules(ctx, r.Now())
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Error("list due schedules failed", "error", err)
		}
		return
	}
	for _, res := range due {
		if res.Kind == api.KindSecret { // a synced secret's refresh (ADR-041)
			r.refreshSecret(ctx, res)
			continue
		}
		if !engineUp {
			continue
		}
		if err := r.claimDue(ctx, res.ID); err != nil && ctx.Err() == nil {
			r.Log.Error("claim schedule failed", "resource_id", res.ID, "error", err)
		}
	}
	if !engineUp {
		return
	}
	r.watchTranscoders(ctx)
	running, err := r.Store.RunningRuns(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Error("list running runs failed", "error", err)
		}
		return
	}
	for _, run := range running {
		if r.adopt(run.ID) {
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer r.release("run:" + run.ID)
				r.execute(ctx, run)
			}()
		}
	}
}

// adopt marks a run as being executed by this process; false when it already is.
func (r *Reconciler) adopt(runID string) bool { return r.claimSlot("run:" + runID) }

func (r *Reconciler) claimSlot(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight[key] {
		return false
	}
	r.inflight[key] = true
	return true
}

// claimDue turns a resource's due occurrences into runs: missed ones become one skipped record, the
// latest runs (catch_up when some were missed), and the overlap policy may skip it (JOB-03, JOB-06).
func (r *Reconciler) claimDue(ctx context.Context, id string) error {
	return r.Store.WithTx(ctx, func(q *store.Queries) error {
		res, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := r.Now()
		if res.Phase != api.PhaseReady || res.NextRunAt == nil || res.NextRunAt.After(now) {
			return nil // claimed by someone else, or no longer due
		}
		sched, overlap, err := scheduleOf(res)
		if err != nil {
			return q.SetNextRun(ctx, id, nil)
		}
		first := *res.NextRunAt
		last, occ, n := first, first, 0
		for !occ.After(now) && n < maxOccurrenceScan {
			last, n = occ, n+1
			occ = sched.Next(occ)
		}
		if occ.Before(now) { // gave up counting: resume from now
			occ = sched.Next(now)
		}
		if n > 1 {
			if err := q.InsertRun(ctx, &domain.Run{ID: domain.NewID("run"), ResourceID: id, Trigger: api.TriggerSchedule, Status: api.RunSkipped,
				ScheduledFor: &first, StartedAt: now, FinishedAt: &now, SkippedCount: n - 1,
				Message: fmt.Sprintf("%d scheduled run%s missed while the machine or the engine was down.", n-1, plural(n-1))}); err != nil {
				return err
			}
		}
		trigger := api.TriggerSchedule
		if n > 1 {
			trigger = api.TriggerCatchUp
		}
		run := &domain.Run{ID: domain.NewID("run"), ResourceID: id, Trigger: trigger, Status: api.RunRunning, ScheduledFor: &last, StartedAt: now}
		if overlap == api.OverlapForbid {
			busy, err := q.HasRunningRun(ctx, id)
			if err != nil {
				return err
			}
			if busy {
				run.Status, run.FinishedAt = api.RunSkipped, &now
				run.Message = "Skipped: the previous run is still going (overlap: forbid)."
			}
		}
		if err := q.InsertRun(ctx, run); err != nil {
			return err
		}
		if err := q.SetNextRun(ctx, id, &occ); err != nil {
			return err
		}
		return q.MergeOutputs(ctx, id, map[string]any{"nextRunAt": occ.UTC().Format(time.RFC3339)})
	})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// scheduleOf parses a schedulable resource's schedule and overlap policy.
func scheduleOf(res *domain.Resource) (api.Schedule, string, error) {
	raw, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return api.Schedule{}, "", err
	}
	switch s := raw.(type) {
	case api.JobSpec:
		sched, err := api.ParseSchedule(s.Schedule, s.TimeZone)
		return sched, s.Overlap, err
	case api.BackupSpec:
		sched, err := api.ParseSchedule(s.Schedule, s.TimeZone)
		return sched, api.OverlapForbid, err // two dumps of one source at once help nobody
	}
	return api.Schedule{}, "", fmt.Errorf("a %s has no schedule", res.Kind)
}

// execute carries one run to its end, bounded by maxConcurrentRuns.
func (r *Reconciler) execute(ctx context.Context, run *domain.Run) {
	select {
	case r.runSlots <- struct{}{}:
		defer func() { <-r.runSlots }()
	case <-ctx.Done():
		return
	}
	res, err := r.Store.GetResource(ctx, run.ResourceID)
	if err != nil {
		return // deleted (its runs went with it) or shutting down
	}
	log := r.Log.With("resource_id", res.ID, "kind", res.Kind, "run_id", run.ID)
	switch res.Kind {
	case api.KindJob:
		r.runJob(ctx, res, run, log)
	case api.KindBackup:
		r.runBackup(ctx, res, run, log)
	case api.KindTranscoder:
		r.runTranscode(ctx, res, run, log)
	default:
		r.finish(context.WithoutCancel(ctx), res, run, api.RunFailed, nil, nil, fmt.Sprintf("A %s can't run.", res.Kind), log)
	}
}

// runJob starts (or re-attaches to) a job's container and records the outcome.
func (r *Reconciler) runJob(ctx context.Context, res *domain.Resource, run *domain.Run, log *slog.Logger) {
	done := context.WithoutCancel(ctx)
	jr, timeout, err := r.jobRun(ctx, res, run)
	if err != nil {
		r.finish(done, res, run, api.RunFailed, nil, nil, err.Error(), log)
		return
	}
	if err := r.Driver.StartJob(ctx, jr); err != nil {
		if errors.Is(err, engine.ErrUnavailable) || ctx.Err() != nil {
			return // stays running; adopted again when the engine (or the process) is back
		}
		r.finish(done, res, run, api.RunFailed, nil, nil, "The run couldn't start: "+truncate(err.Error()), log)
		return
	}
	remaining := run.StartedAt.Add(timeout).Sub(r.Now())
	if remaining < time.Second {
		remaining = time.Second
	}
	out, err := r.Driver.WaitJob(ctx, run.ID, remaining)
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, engine.ErrJobMissing):
		r.finish(done, res, run, api.RunFailed, nil, nil, "The run's container disappeared before it finished.", log)
		return
	case errors.Is(err, engine.ErrUnavailable):
		return
	case err != nil:
		r.finish(done, res, run, api.RunFailed, nil, nil, "The run couldn't be followed: "+truncate(err.Error()), log)
		return
	}
	exit := out.ExitCode
	switch {
	case out.TimedOut:
		r.finish(done, res, run, api.RunTimedOut, &exit, out.Output, fmt.Sprintf("Stopped after the timeout of %s.", timeout), log)
	case exit == 0:
		r.finish(done, res, run, api.RunSucceeded, &exit, out.Output, "Exited with code 0.", log)
	default:
		r.finish(done, res, run, api.RunFailed, &exit, out.Output, fmt.Sprintf("Exited with code %d.", exit), log)
	}
}

// jobRun resolves a job's image and environment: an app's, overridden by the job's own (JOB-02).
func (r *Reconciler) jobRun(ctx context.Context, res *domain.Resource, run *domain.Run) (engine.JobRun, time.Duration, error) {
	raw, _, err := api.NormalizeSpec(api.KindJob, res.Spec)
	if err != nil {
		return engine.JobRun{}, 0, fmt.Errorf("the job's spec is unreadable: %w", err)
	}
	s := raw.(api.JobSpec)
	jr := engine.JobRun{RunID: run.ID, ResourceID: res.ID, Name: res.Name, Image: s.Image, Command: s.Command, Env: map[string]string{}, MemoryMiB: s.MemoryMiB}
	if jr.Mounts, err = r.resolveMounts(ctx, res.Project, s.Volumes); err != nil {
		return jr, 0, err
	}
	secretEnv := map[string]string{}
	network, subnet := s.Network, s.Subnet
	if s.App != "" {
		app, err := r.Store.GetResourceByName(ctx, res.Project, api.KindApp, s.App)
		if err != nil {
			return jr, 0, fmt.Errorf("the app %s no longer exists", s.App)
		}
		araw, _, err := api.NormalizeSpec(api.KindApp, app.Spec)
		if err != nil {
			return jr, 0, fmt.Errorf("the app %s has an unreadable spec", s.App)
		}
		a := araw.(api.AppSpec)
		// An app job runs in its app's network (NET-03).
		jr.Image, network, subnet = a.Image, a.Network, a.Subnet
		if a.Source != nil { // a source app: its serving built image (ADR-019)
			img, _ := app.Outputs[OutputBuiltImage].(string)
			if img == "" {
				return jr, 0, fmt.Errorf("the app %s hasn't been built yet", s.App)
			}
			jr.Image = img
		}
		if len(jr.Command) == 0 {
			jr.Command = a.Command
		}
		for k, v := range a.Env {
			jr.Env[k] = v
		}
		for k, v := range a.SecretEnv {
			secretEnv[k] = v
		}
	}
	if jr.Placement, err = r.placement(ctx, res.Project, network, subnet); err != nil {
		return jr, 0, err
	}
	for k, v := range s.Env {
		jr.Env[k] = v
		delete(secretEnv, k)
	}
	for k, v := range s.SecretEnv {
		secretEnv[k] = v
	}
	for k, name := range secretEnv {
		v, err := r.secretValue(ctx, res.Project, name)
		if err != nil {
			return jr, 0, err
		}
		jr.Env[k] = v
	}
	return jr, time.Duration(s.TimeoutSeconds) * time.Second, nil
}

// finish records a run's end, the resource's last-run facts, metrics and (for failures) an event.
func (r *Reconciler) finish(ctx context.Context, res *domain.Resource, run *domain.Run, status string, exit *int, output []byte, msg string, log *slog.Logger) {
	now := r.Now()
	run.Status, run.FinishedAt, run.ExitCode, run.OutputTail, run.Message = status, &now, exit, output, msg
	ok, err := r.Store.FinishRun(ctx, run)
	if err != nil || !ok {
		if err != nil {
			log.Error("record run result failed", "error", err)
		}
		return
	}
	outputs := map[string]any{"lastRunStatus": status, "lastRunAt": now.UTC().Format(time.RFC3339)}
	failures, err := r.Store.RecordRunOutcome(ctx, res.ID, status != api.RunSucceeded, outputs)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Warn("record run outcome failed", "error", err)
	}
	if res.Kind == api.KindBackup && err == nil {
		r.afterBackupRun(ctx, res, status, failures, log)
	}
	r.Metrics.RunsTotal.WithLabelValues(string(res.Kind), status).Inc()
	r.Metrics.RunDuration.WithLabelValues(string(res.Kind)).Observe(now.Sub(run.StartedAt).Seconds())
	if status == api.RunSucceeded {
		log.Info("run succeeded", "duration_ms", now.Sub(run.StartedAt).Milliseconds())
		return
	}
	r.event(ctx, res, "run_"+status, fmt.Sprintf("Run %s: %s", run.ID, msg))
	log.Warn("run did not succeed", "status", status, "message", msg)
}

// ensureJob validates a job's schedule and sets its next run (ADR-017). A spec change reschedules from
// now; otherwise the stored next run is kept, so a reconcile never drops a due occurrence.
func (r *Reconciler) ensureJob(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	report.Step("schedule", api.StepActive)
	sched, _, err := scheduleOf(res)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("the schedule can't be used: %w", err))
	}
	raw, _, _ := api.NormalizeSpec(api.KindJob, res.Spec)
	if s := raw.(api.JobSpec); s.App != "" {
		if _, err := r.Store.GetResourceByName(ctx, res.Project, api.KindApp, s.App); err != nil {
			return nil, engine.Permanent(fmt.Errorf("the app %s no longer exists", s.App))
		}
	}
	if res.NextRunAt == nil || res.ObservedGeneration != res.Generation {
		next := sched.Next(r.Now())
		res.NextRunAt = &next
	}
	out := engine.Outputs{"nextRunAt": res.NextRunAt.UTC().Format(time.RFC3339)}
	for _, k := range []string{"lastRunStatus", "lastRunAt"} {
		if v, ok := res.Outputs[k]; ok {
			out[k] = v
		}
	}
	report.Step("schedule", api.StepDone)
	return out, nil
}
