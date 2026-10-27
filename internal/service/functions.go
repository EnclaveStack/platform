package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Functions (ADR-055, L2-016 FN-01 – FN-12).

// functionRefs checks what a function names: its secrets, network, bucket grants and the queues and
// buckets its triggers watch, which can't be deleted while it does.
func (s *Resources) functionRefs(ctx context.Context, q *store.Queries, project string, f api.FunctionSpec) ([]store.Ref, error) {
	refs, err := s.secretRefs(ctx, q, project, f.SecretEnv)
	if err != nil {
		return nil, err
	}
	placed, err := s.placementRefs(ctx, q, project, f.Network, f.Subnet, false)
	if err != nil {
		return nil, err
	}
	buckets, err := s.bucketRefs(ctx, q, project, f.BucketAccess)
	if err != nil {
		return nil, err
	}
	refs = append(append(refs, placed...), buckets...)
	for i, t := range f.Triggers {
		field := fmt.Sprintf("spec.triggers[%d]", i)
		switch {
		case t.Queue != "":
			qu, err := live(ctx, q, project, api.KindQueue, t.Queue, field+".queue")
			if err != nil {
				return nil, err
			}
			var qs api.QueueSpec
			if raw, _, err := api.NormalizeSpec(api.KindQueue, qu.Spec); err == nil {
				qs = raw.(api.QueueSpec)
			}
			// A message must stay hidden while its run goes on, or a second run takes it (FN-09).
			if need := f.TimeoutSeconds + api.QueueVisibilityMargin; qs.VisibilityTimeoutSeconds < need {
				return nil, api.Validation([]api.FieldError{{Field: field + ".queue", Code: api.FieldNotAllowed,
					Message: fmt.Sprintf("The queue %s hides a received message for %d s. A function with a %d s timeout needs %d s or more: raise the queue's visibility timeout, or shorten the timeout.",
						t.Queue, qs.VisibilityTimeoutSeconds, f.TimeoutSeconds, need)}})
			}
			refs = append(refs, store.Ref{ToID: qu.ID, Field: field + ".queue"})
		case t.Bucket != "":
			b, err := live(ctx, q, project, api.KindBucket, t.Bucket, field+".bucket")
			if err != nil {
				return nil, err
			}
			refs = append(refs, store.Ref{ToID: b.ID, Field: field + ".bucket"})
		}
	}
	return dedupeRefs(refs), nil
}

// Invoke starts a run of a function with a JSON body as its event (FN-06). The handler has checked the
// body's size and that it is JSON.
func (s *Resources) Invoke(ctx context.Context, p Principal, id string, body json.RawMessage) (api.Run, error) {
	var run *domain.Run
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		r, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if r.Kind != api.KindFunction {
			return api.NewError(api.CodeActionNotSupported, fmt.Sprintf("Only functions can be invoked; this is a %s.", r.Kind))
		}
		if r.Phase != api.PhaseReady {
			return api.NewError(api.CodeResourceBusy, "Wait until the function is ready, or use Retry if it failed.")
		}
		if err := functionHasRoom(ctx, q, r); err != nil {
			return err
		}
		ev, err := json.Marshal(api.FunctionEvent{Source: api.SourceInvoke, Body: body})
		if err != nil {
			return err
		}
		run = &domain.Run{ID: domain.NewID("run"), ResourceID: id, Trigger: api.TriggerInvoke, Status: api.RunRunning, StartedAt: s.Now(), Event: ev}
		return q.InsertRun(ctx, run)
	})
	if err != nil {
		return api.Run{}, err
	}
	s.Log.Info("function invoked", "resource_id", id, "run_id", run.ID, "by", p.Key())
	return run.ToAPI(), nil
}

// WaitRun waits up to wait for a run to end, and returns it with its output when it did (FN-06).
func (s *Resources) WaitRun(ctx context.Context, id, runID string, wait time.Duration) (api.RunDetail, bool, error) {
	deadline := time.Now().Add(wait)
	for {
		d, err := s.Run(ctx, id, runID)
		if err != nil || d.Status != api.RunRunning {
			return d, err == nil, err
		}
		if !time.Now().Before(deadline) {
			return d, false, nil
		}
		select {
		case <-ctx.Done():
			return d, false, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// functionHasRoom refuses a run past the function's concurrency (FN-07).
func functionHasRoom(ctx context.Context, q *store.Queries, r *domain.Resource) error {
	raw, _, err := api.NormalizeSpec(api.KindFunction, r.Spec)
	if err != nil {
		return err
	}
	n, err := q.CountRunning(ctx, r.ID)
	if err != nil {
		return err
	}
	if c := raw.(api.FunctionSpec).Concurrency; n >= c {
		e := api.NewError(api.CodeFunctionBusy, fmt.Sprintf("The function already runs %d runs, its concurrency. Try again in a few seconds.", c))
		e.RetryAfterSeconds = api.FunctionBusyRetrySeconds
		return e
	}
	return nil
}
