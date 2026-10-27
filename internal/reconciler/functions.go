package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Functions (ADR-055, L2-016 FN-01 – FN-12): a container per event, on the job engine. Each event is a run
// with its event JSON; the scheduler adopts and executes it as a job's run. The sources are the function's
// schedule (claimDue), its queues and bucket folders (watchFunctions), topics (the delivery loop) and
// invocations (the service).

// overlapConcurrency is a function's overlap rule: runs overlap up to its concurrency.
const overlapConcurrency = "concurrency"

var errNoSchedule = errors.New("no schedule trigger")

// progressEvery is how often a topic delivery waiting for its function's run tells the broker it's alive.
const progressEvery = 20 * time.Second

func functionSpecOf(res *domain.Resource) (api.FunctionSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindFunction, res.Spec)
	if err != nil {
		return api.FunctionSpec{}, err
	}
	return raw.(api.FunctionSpec), nil
}

// functionFull reports whether a function already runs as many runs as its concurrency allows (FN-07).
func functionFull(ctx context.Context, q *store.Queries, res *domain.Resource) (bool, error) {
	s, err := functionSpecOf(res)
	if err != nil {
		return true, err
	}
	n, err := q.CountRunning(ctx, res.ID)
	return n >= s.Concurrency, err
}

// bucketSeenKey and bucketBaselineKey are a bucket trigger's rows in transcoder_objects (L3-005 §12).
func bucketSeenKey(bucket, key string) string        { return bucket + "/" + key }
func bucketBaselineKey(bucket, prefix string) string { return "\x00" + bucket + "/" + prefix }

// ensureFunction checks what the triggers name, gives the function its bucket access and sets its next
// scheduled run (FN-01, FN-02, FN-09).
func (r *Reconciler) ensureFunction(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	s, err := functionSpecOf(res)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	report.Step("check", api.StepActive)
	for _, t := range s.Triggers {
		switch {
		case t.Queue != "":
			q, err := r.Store.GetResourceByName(ctx, res.Project, api.KindQueue, t.Queue)
			if errors.Is(err, store.ErrNotFound) {
				return nil, engine.Permanent(fmt.Errorf("the queue %s no longer exists", t.Queue))
			}
			if err != nil {
				return nil, err
			}
			raw, _, err := api.NormalizeSpec(api.KindQueue, q.Spec)
			if err != nil {
				return nil, engine.Permanent(fmt.Errorf("the queue %s has an unreadable spec", t.Queue))
			}
			if need := s.TimeoutSeconds + api.QueueVisibilityMargin; raw.(api.QueueSpec).VisibilityTimeoutSeconds < need {
				return nil, engine.Permanent(fmt.Errorf("the queue %s hides a received message for %d s, less than the function's timeout plus %d s. Set its visibility timeout to %d s or more, or shorten the timeout",
					t.Queue, raw.(api.QueueSpec).VisibilityTimeoutSeconds, api.QueueVisibilityMargin, need))
			}
		case t.Bucket != "":
			if _, err := r.bucketTarget(ctx, res.Project, t.Bucket); err != nil {
				return nil, err
			}
		}
	}
	if _, err := r.placement(ctx, res.Project, s.Network, s.Subnet); err != nil {
		return nil, err
	}
	for _, name := range s.SecretEnv {
		if _, err := r.secretValue(ctx, res.Project, name); err != nil {
			return nil, err
		}
	}
	// Given or taken away now, so a change shows at once (BKA-02, BKA-06).
	if _, err := r.bucketAccessEnv(ctx, res, s); err != nil {
		return nil, err
	}
	report.Step("check", api.StepDone)
	report.Step("ready", api.StepActive)
	if err := r.forgetFunctionBuckets(ctx, res, s); err != nil {
		return nil, err
	}
	out := engine.Outputs{"triggers": len(s.Triggers)}
	var folders []string
	for _, t := range s.Triggers {
		if t.Bucket != "" {
			folders = append(folders, t.Bucket+"/"+t.Prefix)
		}
	}
	if len(folders) > 0 {
		out["watching"] = folders
	}
	if t, ok := s.Schedule(); ok {
		sched, err := api.ParseSchedule(t.Schedule, t.TimeZone)
		if err != nil {
			return nil, engine.Permanent(fmt.Errorf("the schedule can't be used: %w", err))
		}
		if res.NextRunAt == nil || res.ObservedGeneration != res.Generation {
			next := sched.Next(r.Now())
			res.NextRunAt = &next
		}
		out["nextRunAt"] = res.NextRunAt.UTC().Format(time.RFC3339)
	} else {
		res.NextRunAt = nil
	}
	for _, k := range []string{"lastRunStatus", "lastRunAt"} {
		if v, ok := res.Outputs[k]; ok {
			out[k] = v
		}
	}
	report.Step("ready", api.StepDone)
	return out, nil
}

// functionRun is a function's container for one run: its image and environment, then the event file and
// the ENCLAVE_ variables, which the function's own environment can't use (FN-05).
func (r *Reconciler) functionRun(ctx context.Context, res *domain.Resource, run *domain.Run, s api.FunctionSpec) (engine.JobRun, error) {
	jr := engine.JobRun{RunID: run.ID, ResourceID: res.ID, Name: res.Name, Image: s.Image, Command: s.Command, MemoryMiB: s.MemoryMiB, Env: maps.Clone(s.Env)}
	if jr.Env == nil {
		jr.Env = map[string]string{}
	}
	var err error
	if jr.Placement, err = r.placement(ctx, res.Project, s.Network, s.Subnet); err != nil {
		return jr, err
	}
	for k, name := range s.SecretEnv {
		v, err := r.secretValue(ctx, res.Project, name)
		if err != nil {
			return jr, err
		}
		jr.Env[k] = v
	}
	access, err := r.bucketAccessEnv(ctx, res, s)
	if err != nil {
		return jr, err
	}
	maps.Copy(jr.Env, access)
	event := run.Event
	if len(event) == 0 {
		event, _ = json.Marshal(api.FunctionEvent{Source: api.SourceManual})
	}
	var src struct{ Source string }
	_ = json.Unmarshal(event, &src)
	jr.Files = map[string][]byte{api.EventPath: event}
	maps.Copy(jr.Env, map[string]string{api.EnvEventFile: api.EventPath, api.EnvEventSource: src.Source, api.EnvRunID: run.ID, api.EnvFunction: res.Name})
	return jr, nil
}

// runFunction runs one event. A queue message is deleted when its run succeeds; otherwise it comes back
// after the queue's visibility timeout (FN-08).
func (r *Reconciler) runFunction(ctx context.Context, res *domain.Resource, run *domain.Run, log *slog.Logger) {
	done := context.WithoutCancel(ctx)
	s, err := functionSpecOf(res)
	if err != nil {
		r.finish(done, res, run, api.RunFailed, nil, nil, "The function's spec is unreadable.", log)
		return
	}
	jr, err := r.functionRun(ctx, res, run, s)
	if err != nil {
		r.finish(done, res, run, api.RunFailed, nil, nil, err.Error(), log)
		return
	}
	if !r.runContainer(ctx, res, run, jr, time.Duration(s.TimeoutSeconds)*time.Second, log) || run.Receipt == "" || run.Status != api.RunSucceeded {
		return
	}
	var ev api.FunctionEvent
	if json.Unmarshal(run.Event, &ev) != nil || ev.Queue == "" {
		return
	}
	b, err := r.broker(done)
	if err == nil {
		err = b.Delete(done, domain.BrokerKey(res.Project, ev.Queue), run.Receipt)
	}
	if err != nil && !errors.Is(err, messaging.ErrNotFound) {
		log.Warn("delete a handled queue message failed; it comes back after the visibility timeout", "queue", ev.Queue, "error", err)
	}
}

// watchFunctions takes queue messages for every ready function's queue triggers on each pass, and lists
// its bucket folders at most every TranscodeWatch, starting a run per message or new object within the
// function's concurrency (FN-07, FN-08, FN-10).
func (r *Reconciler) watchFunctions(ctx context.Context) {
	list, err := r.Store.ResourcesOfKind(ctx, api.KindFunction)
	if err != nil {
		return
	}
	every := r.TranscodeWatch
	if every <= 0 {
		every = DefaultTranscodeWatch
	}
	now := r.Now()
	for _, res := range list {
		if res.Phase != api.PhaseReady {
			continue
		}
		s, err := functionSpecOf(res)
		if err != nil || len(s.Triggers) == 0 {
			continue
		}
		running, err := r.Store.CountRunning(ctx, res.ID)
		if err != nil {
			continue
		}
		free := s.Concurrency - running
		list := now.Sub(r.watchedAt[res.ID]) >= every
		if list {
			r.watchedAt[res.ID] = now
		}
		for _, t := range s.Triggers {
			if ctx.Err() != nil {
				return
			}
			switch {
			case t.Queue != "" && free > 0:
				n, err := r.receiveForFunction(ctx, res, t.Queue, free)
				free -= n
				if err != nil && ctx.Err() == nil {
					r.Log.Debug("receive for a function failed", "resource_id", res.ID, "queue", t.Queue, "error", err)
				}
			case t.Bucket != "" && list:
				n, err := r.watchFunctionBucket(ctx, res, t, free)
				free -= n
				if err != nil && ctx.Err() == nil {
					r.Log.Warn("listing a function's bucket failed", "resource_id", res.ID, "bucket", t.Bucket, "error", err)
				}
			}
		}
	}
}

// receiveForFunction takes up to max messages from a queue and starts a run for each.
func (r *Reconciler) receiveForFunction(ctx context.Context, res *domain.Resource, queue string, max int) (int, error) {
	b, err := r.broker(ctx)
	if err != nil {
		return 0, err
	}
	msgs, err := b.Receive(ctx, domain.BrokerKey(res.Project, queue), max, 0)
	if err != nil {
		return 0, err
	}
	now := r.Now()
	for i, m := range msgs {
		ev, err := json.Marshal(api.FunctionEvent{Source: api.SourceQueue, Queue: queue, Message: api.QueueMessage{ID: m.ID, Body: m.Body,
			Attributes: m.Attributes, SentAt: m.SentAt, ReceiveCount: m.ReceiveCount}})
		if err != nil {
			return i, err
		}
		run := &domain.Run{ID: domain.NewID("run"), ResourceID: res.ID, Trigger: api.TriggerQueue, Status: api.RunRunning, StartedAt: now, Event: ev, Receipt: m.Receipt}
		if err := r.Store.InsertRun(ctx, run); err != nil {
			return i, err // the message comes back after the visibility timeout
		}
	}
	return len(msgs), nil
}

// watchFunctionBucket lists one bucket folder and starts a run per object it hasn't seen. The first
// listing of a folder is a baseline: objects already there don't start runs (FN-10).
func (r *Reconciler) watchFunctionBucket(ctx context.Context, res *domain.Resource, t api.FunctionTrigger, free int) (int, error) {
	target, err := r.bucketTarget(ctx, res.Project, t.Bucket)
	if err != nil {
		return 0, err
	}
	st, err := r.Opener.Open(ctx, target)
	if err != nil {
		return 0, err
	}
	objects, err := st.List(ctx, t.Prefix)
	if err != nil {
		return 0, err
	}
	slices.SortFunc(objects, func(a, b backup.Object) int { return strings.Compare(a.Key, b.Key) })
	seen, err := r.Store.SeenObjects(ctx, res.ID)
	if err != nil {
		return 0, err
	}
	now := r.Now()
	if _, baselined := seen[bucketBaselineKey(t.Bucket, t.Prefix)]; !baselined {
		for _, o := range objects {
			if err := r.Store.MarkSeen(ctx, res.ID, bucketSeenKey(t.Bucket, o.Key), objectTag(o), "", now); err != nil {
				return 0, err
			}
		}
		return 0, r.Store.MarkSeen(ctx, res.ID, bucketBaselineKey(t.Bucket, t.Prefix), "", "", now)
	}
	started := 0
	for _, o := range objects {
		if started >= free {
			break // the rest wait for the next listing
		}
		key := bucketSeenKey(t.Bucket, o.Key)
		if strings.HasSuffix(o.Key, "/") || seen[key] == objectTag(o) {
			continue
		}
		size, modified := o.Size, o.Modified.UTC()
		ev, err := json.Marshal(api.FunctionEvent{Source: api.SourceBucket, Bucket: t.Bucket, Key: o.Key, Size: &size, ModifiedAt: &modified})
		if err != nil {
			return started, err
		}
		run := &domain.Run{ID: domain.NewID("run"), ResourceID: res.ID, Trigger: api.TriggerBucket, Status: api.RunRunning, StartedAt: now, Key: o.Key, Event: ev}
		if err := r.Store.InsertRun(ctx, run); err != nil {
			return started, err
		}
		if err := r.Store.MarkSeen(ctx, res.ID, key, objectTag(o), run.ID, now); err != nil {
			return started, err
		}
		started++
	}
	return started, nil
}

// forgetFunctionBuckets drops what a function saw in folders it no longer watches, so watching one again
// starts with a new baseline.
func (r *Reconciler) forgetFunctionBuckets(ctx context.Context, res *domain.Resource, s api.FunctionSpec) error {
	watched := map[string]bool{}
	for _, t := range s.Triggers {
		if t.Bucket != "" {
			watched[t.Bucket+"/"+t.Prefix] = true
		}
	}
	if prev, _ := res.Outputs["watching"].([]any); len(prev) > 0 {
		for _, p := range prev {
			if folder, _ := p.(string); folder != "" && !watched[folder] {
				return r.Store.ForgetSeen(ctx, res.ID)
			}
		}
	}
	return nil
}

// sendToFunction starts a run for a topic message and waits for it: the delivery is acknowledged when the
// run succeeds and retried with the topic's backoff when it doesn't (FN-03). While the run goes on, the
// broker is told the delivery is in progress, so it isn't handed out again.
func (r *Reconciler) sendToFunction(ctx context.Context, w subscriptionWork, d messaging.Delivery) error {
	fn, err := r.Store.GetResourceByName(ctx, w.project, api.KindFunction, w.sub.Function)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("the function %s doesn't exist", w.sub.Function)
	}
	if err != nil {
		return err
	}
	if fn.Phase != api.PhaseReady {
		return fmt.Errorf("the function %s isn't ready", w.sub.Function)
	}
	ev, err := json.Marshal(api.FunctionEvent{Source: api.SourceTopic, Topic: w.name, Message: api.TopicMessage{Sequence: d.Sequence, Body: d.Body,
		Attributes: d.Attributes, PublishedAt: d.Published, Attempt: d.Attempt}})
	if err != nil {
		return err
	}
	run := &domain.Run{ID: domain.NewID("run"), ResourceID: fn.ID, Trigger: api.TriggerTopic, Status: api.RunRunning, StartedAt: r.Now(), Event: ev}
	err = r.Store.WithTx(ctx, func(q *store.Queries) error {
		full, err := functionFull(ctx, q, fn)
		if err != nil {
			return err
		}
		if full {
			return fmt.Errorf("the function %s is at its concurrency", w.sub.Function)
		}
		return q.InsertRun(ctx, run)
	})
	if err != nil {
		return err
	}
	tick := time.NewTicker(time.Second / 2)
	defer tick.Stop()
	lastProgress := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		got, err := r.Store.GetRun(ctx, fn.ID, run.ID)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("the function %s was deleted", w.sub.Function)
		}
		if err != nil {
			continue
		}
		switch got.Status {
		case api.RunRunning:
			if d.Progress != nil && time.Since(lastProgress) >= progressEvery {
				_ = d.Progress(ctx)
				lastProgress = time.Now()
			}
			continue
		case api.RunSucceeded:
			return nil
		}
		return fmt.Errorf("the function's run %s ended %s: %s", run.ID, got.Status, got.Message)
	}
}
