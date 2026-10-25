package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/pkg/api"
)

// Offline machines (ADR-046): once a machine has been offline for RescheduleAfter, every app and image job
// on it that keeps no data and isn't pinned starts on another machine (FAIL-02). Everything else waits for
// its machine (FAIL-03). A rescheduled resource stays where it went, so a flapping machine moves its apps at
// most once per outage.

// DefaultRescheduleAfter is how long a machine is offline before its apps start elsewhere.
const DefaultRescheduleAfter = 60 * time.Second

// rescheduleOffline moves what may move off machines that have been offline long enough.
func (r *Reconciler) rescheduleOffline(ctx context.Context) {
	if r.Machines == nil {
		return
	}
	after := r.RescheduleAfter
	if after <= 0 {
		after = DefaultRescheduleAfter
	}
	machines, err := r.Store.ListMachines(ctx)
	if err != nil {
		return
	}
	for _, mc := range machines {
		if mc.State != api.MachineOffline || r.Now().Sub(mc.StateSince) < after {
			continue
		}
		on, err := r.Store.ResourcesOnMachine(ctx, mc.ID)
		if err != nil {
			continue
		}
		for _, res := range on {
			if movable(res) {
				r.reschedule(ctx, res, mc.Name, mc.StateSince)
			}
		}
	}
}

// movable reports whether a resource may start elsewhere by itself: an app or an image job, with no
// volume, no machine of its own, and not being deleted.
func movable(res *domain.Resource) bool {
	if res.Phase == api.PhaseDeleting || (res.Kind != api.KindApp && res.Kind != api.KindJob) {
		return false
	}
	spec, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return false
	}
	switch s := spec.(type) {
	case api.AppSpec:
		return s.Machine == "" && len(s.Volumes) == 0
	case api.JobSpec:
		return s.Machine == "" && s.App == "" && len(s.Volumes) == 0
	}
	return false
}

func (r *Reconciler) reschedule(ctx context.Context, res *domain.Resource, from string, since time.Time) {
	need, err := r.need(ctx, res)
	if err != nil {
		return
	}
	cands, err := r.Machines.Candidates(ctx)
	if err != nil {
		return
	}
	need.Machine = ""
	to, _ := domain.ChooseMachine(cands, need)
	if to == "" || to == res.Machine {
		return // nowhere to go yet; tried again next pass
	}
	if err := r.setMachine(ctx, res, to); err != nil {
		return
	}
	msg := fmt.Sprintf("%s has been offline since %s; started on %s instead.", from, since.Local().Format("15:04"), r.Machines.Name(ctx, to))
	if res.Kind == api.KindApp {
		now := r.Now()
		if err := res.SetPhase(api.PhaseUpdating, "", now); err == nil {
			res.Steps, res.Attempts, res.NextAttemptAt, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), 0, nil, now
			_, _ = r.Store.SaveStatus(ctx, res, res.Generation)
		}
		r.followApp(ctx, res, to)
	}
	r.event(ctx, res, "rescheduled", msg)
	r.Log.Warn("rescheduled off an offline machine", "resource_id", res.ID, "from", from, "to", to)
	r.Metrics.ReconcileTotal.WithLabelValues(string(res.Kind), "rescheduled").Inc()
	r.Enqueue(res.ID)
}

// followApp moves an app's jobs with it: they run where the app runs (PLC-04).
func (r *Reconciler) followApp(ctx context.Context, app *domain.Resource, machine string) {
	jobs, err := r.Store.ProjectResourcesOfKind(ctx, app.ProjectName(), api.KindJob)
	if err != nil {
		return
	}
	for _, j := range jobs {
		var s api.JobSpec
		if spec, _, err := api.NormalizeSpec(api.KindJob, j.Spec); err == nil {
			s = spec.(api.JobSpec)
		}
		if s.App == app.Name && j.Machine != machine {
			_ = r.setMachine(ctx, j, machine)
		}
	}
}
