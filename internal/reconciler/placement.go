package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Directory is what the reconciler knows of machines (ADR-044): candidates for placement and their names.
type Directory interface {
	Candidates(ctx context.Context) ([]domain.Candidate, error)
	Name(ctx context.Context, machineID string) string
	Count(ctx context.Context) int
	// Members lists every machine that joined, with its engine when it is online and its tunnel's facts
	// (ADR-045); RecordTunnelKey keeps the key a machine's mesh reported.
	Members(ctx context.Context) ([]engine.MeshMember, error)
	RecordTunnelKey(ctx context.Context, machineID, key string) error
	// Engines reaches one machine's engine and hypervisor (a move addresses both machines, ADR-047).
	Engines(machineID string) (engine.Driver, engine.Hypervisor, error)
}

// errNotPlaced means no machine fits yet; the resource waits with the reason, as a VM waits for QEMU
// (PLC-02).
var errNotPlaced = errors.New("not placed")

// NotPlacedRetry is how often an unplaced resource tries again.
const NotPlacedRetry = 30 * time.Second

// OutputMachine is the name of the machine a placed resource runs on (PLC-05).
const OutputMachine = "machine"

// place chooses a machine for a placed kind that has none yet, and records it.
func (r *Reconciler) place(ctx context.Context, res *domain.Resource) error {
	if !domain.Placed(res.Kind) || res.Machine != "" {
		return nil
	}
	if r.Machines == nil || r.Machines.Count(ctx) <= 1 {
		// One machine: everything runs on it, as before v0.8 (PLC-08).
		return r.setMachine(ctx, res, r.Store.MainID())
	}
	need, err := r.need(ctx, res)
	if err != nil {
		return err
	}
	cands, err := r.Machines.Candidates(ctx)
	if err != nil {
		return err
	}
	id, why := domain.ChooseMachine(cands, need)
	if id == "" {
		return fmt.Errorf("%w: %s", errNotPlaced, why)
	}
	if err := r.setMachine(ctx, res, id); err != nil {
		return err
	}
	r.event(ctx, res, "placed", "Placed on "+r.Machines.Name(ctx, id)+".")
	return nil
}

func (r *Reconciler) setMachine(ctx context.Context, res *domain.Resource, id string) error {
	if err := r.Store.SetMachine(ctx, res.ID, id); err != nil {
		return err
	}
	res.Machine = id
	return nil
}

// need is what a resource asks of its machine: its memory and labels, VM support, and the machine its
// volumes or its app are on (PLC-04).
func (r *Reconciler) need(ctx context.Context, res *domain.Resource) (domain.Need, error) {
	spec, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return domain.Need{}, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	p, _ := api.PlacementOf(spec)
	need := domain.Need{MemoryMiB: domain.PlacementMemoryMiB(res), Labels: p.MachineLabels, VMs: res.Kind == api.KindVM, Machine: p.Machine}
	type ref struct {
		kind api.Kind
		name string
	}
	var follow []ref
	switch s := spec.(type) {
	case api.AppSpec:
		for _, m := range s.Volumes {
			follow = append(follow, ref{api.KindVolume, m.Volume})
		}
	case api.JobSpec:
		if s.App != "" {
			follow = append(follow, ref{api.KindApp, s.App})
		}
		for _, m := range s.Volumes {
			follow = append(follow, ref{api.KindVolume, m.Volume})
		}
	case api.VMSpec:
		for _, d := range s.Disks {
			follow = append(follow, ref{api.KindVolume, d})
		}
	}
	for _, f := range follow {
		o, err := r.Store.GetResourceByName(ctx, res.ProjectName(), f.kind, f.name)
		if errors.Is(err, store.ErrNotFound) {
			return need, engine.Permanent(fmt.Errorf("the %s %s no longer exists", f.kind, f.name))
		}
		if err != nil {
			return need, err
		}
		if o.Machine == "" {
			return need, fmt.Errorf("%w: waiting for the %s %s to be placed", errNotPlaced, f.kind, f.name)
		}
		name := r.Machines.Name(ctx, o.Machine)
		if need.Machine != "" && need.Machine != name && need.Machine != o.Machine {
			return need, engine.Permanent(fmt.Errorf("the %s %s is on %s, and what uses it must run there too", f.kind, f.name, name))
		}
		need.Machine = o.Machine
	}
	return need, nil
}

// withMachine adds the machine's name to a placed resource's outputs (PLC-05).
func (r *Reconciler) withMachine(ctx context.Context, res *domain.Resource, out engine.Outputs) engine.Outputs {
	if !domain.Placed(res.Kind) || out == nil {
		return out
	}
	id := res.MachineOn(r.Store.MainID())
	name := id
	if r.Machines != nil {
		name = r.Machines.Name(ctx, id)
	} else if mc, err := r.Store.GetMachine(ctx, id); err == nil {
		name = mc.Name
	}
	out[OutputMachine] = name
	return out
}

// RetryWaiting looks at every waiting resource now, instead of at its next try: a machine came online,
// so what waited for it, or for room, may go ahead (ADR-046).
func (r *Reconciler) RetryWaiting(ctx context.Context) {
	waiting, err := r.Store.Waiting(ctx, r.Now())
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Error("list waiting resources failed", "error", err)
		}
		return
	}
	for _, res := range waiting {
		res.NextAttemptAt = nil
		if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
			r.Enqueue(res.ID)
		}
	}
}
