package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Stacks (ADR-048): a stack has no runtime of its own. Its phase follows its members: ready when every one
// is, failed when one is (naming it), in progress otherwise (CAT-05). The service creates, changes and
// deletes the members; the reconciler only watches them.

// errMembers means a stack waits for its members; it isn't an attempt.
var errMembers = errors.New("waiting for members")

// StackRetry is how often a stack in progress looks at its members again.
const StackRetry = 2 * time.Second

// ensureStack aggregates a stack's members into its outputs and phase.
func (r *Reconciler) ensureStack(ctx context.Context, res *domain.Resource, rep engine.Reporter) (engine.Outputs, error) {
	rep.Step("members", api.StepActive)
	out, ready, total, failed, err := r.stackOutputs(ctx, res)
	if err != nil {
		return nil, err
	}
	if failed != nil {
		rep.Step("members", api.StepFailed)
		return nil, engine.Permanent(fmt.Errorf("the %s %s failed: %s", failed.Kind, failed.Name, failed.Message))
	}
	if ready < total {
		res.Outputs = out
		return nil, fmt.Errorf("%w: %d of %d are ready", errMembers, ready, total)
	}
	rep.Step("members", api.StepDone)
	return out, nil
}

// stackOutputs lists the members with their phases, keeping the template's facts.
func (r *Reconciler) stackOutputs(ctx context.Context, res *domain.Resource) (engine.Outputs, int, int, *domain.Resource, error) {
	members, err := r.Store.StackMembers(ctx, res.ID)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	out := engine.Outputs{}
	for _, k := range []string{api.OutputTemplate, api.OutputTemplateVersion, api.OutputStackOutputs} {
		if v, ok := res.Outputs[k]; ok {
			out[k] = v
		}
	}
	list := make([]api.StackMember, 0, len(members))
	ready, total := 0, 0
	var failed *domain.Resource
	for _, m := range members {
		if m.Phase == api.PhaseDeleting {
			continue
		}
		total++
		list = append(list, api.StackMember{Kind: m.Kind, Name: m.Name, ID: m.ID, Phase: m.Phase})
		switch m.Phase {
		case api.PhaseReady:
			ready++
		case api.PhaseFailed:
			if failed == nil {
				failed = m
			}
		}
	}
	out[api.OutputMembers], out[api.OutputReady], out[api.OutputTotal] = list, ready, total
	return out, ready, total, failed, nil
}

// refreshStack looks at a member's stack again after the member changed phase.
func (r *Reconciler) refreshStack(ctx context.Context, member *domain.Resource) {
	if member.Stack == "" {
		return
	}
	st, err := r.Store.GetResource(ctx, member.Stack)
	if err != nil {
		return
	}
	switch st.Phase {
	case api.PhaseDeleting:
		r.Enqueue(st.ID) // it is removed after its last member
	case api.PhaseReady, api.PhaseFailed:
		out, ready, total, failed, err := r.stackOutputs(ctx, st)
		if err != nil {
			return
		}
		now := r.Now()
		switch {
		case failed != nil && st.Phase == api.PhaseReady:
			if st.SetPhase(api.PhaseFailed, fmt.Sprintf("the %s %s failed: %s", failed.Kind, failed.Name, failed.Message), now) == nil {
				r.event(ctx, st, "failed", st.Message)
			}
		case failed == nil && (st.Phase == api.PhaseFailed || ready < total):
			_ = st.SetPhase(api.PhaseProvisioning, "", now) // and its ensure decides
			r.Enqueue(st.ID)
		}
		st.Outputs, st.UpdatedAt = out, now
		_, _ = r.Store.SaveStatus(ctx, st, st.Generation)
	default:
		r.Enqueue(st.ID)
	}
}

// deleteStack waits for the stack's members to be gone.
func (r *Reconciler) deleteStack(ctx context.Context, res *domain.Resource) error {
	members, err := r.Store.StackMembers(ctx, res.ID)
	if err != nil {
		return err
	}
	if len(members) > 0 {
		return fmt.Errorf("%w: %d left to delete", errMembers, len(members))
	}
	return nil
}
