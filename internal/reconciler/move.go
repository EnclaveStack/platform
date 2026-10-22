package reconciler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Moving (ADR-047): copy, then switch. A resource whose spec names another machine (or labels its machine
// lacks) is moved by its next ensure. Apps and load balancers start on the new machine and the old
// containers go once it is ready; resources with data stop, their data is copied, and they start on the new
// machine with the same address; the old copy is removed only once the new one is ready. If anything fails
// before that, the resource carries on where it was.

// moving is one move in progress: where from and to, what to keep, and how to undo it.
type moving struct {
	res     *domain.Resource
	from    string // machine ID
	to      string
	address string // the private address it keeps (data kinds)
	network string // the engine network of that address
	started time.Time
	stopped time.Time // when it stopped serving (data kinds)
	copied  int64
	disks   []string // disk volumes that move with a VM
	users   []*domain.Resource
}

// ensureWithMove runs an ensure, moving the resource first when its machine changed: if the new copy
// doesn't become ready, the old one runs again; if it does, the old one is removed.
func (r *Reconciler) ensureWithMove(ctx context.Context, res *domain.Resource, log *slog.Logger) error {
	mv, err := r.startMove(ctx, res, &progress{r: r, ctx: ctx, res: res, gen: res.Generation})
	if err != nil {
		return err
	}
	if mv == nil {
		return r.ensure(ctx, res, log)
	}
	if mv.address != "" {
		r.keep.Store(res.ID, mv.address)
		defer r.keep.Delete(res.ID)
	}
	if err := r.ensure(ctx, res, log); err != nil {
		r.abortMove(ctx, mv, err)
		return err
	}
	r.finishMove(ctx, mv)
	return nil
}

// kept is the address a move keeps for a resource.
func (r *Reconciler) kept(id string) (string, bool) {
	v, ok := r.keep.Load(id)
	if !ok {
		return "", false
	}
	return v.(string), v.(string) != ""
}

// moveTarget is the machine a placed resource must move to, or "" when it stays.
func (r *Reconciler) moveTarget(ctx context.Context, res *domain.Resource) (string, error) {
	if r.Machines == nil || !domain.Placed(res.Kind) || res.Machine == "" {
		return "", nil
	}
	spec, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return "", nil
	}
	p, _ := api.PlacementOf(spec)
	switch {
	case res.RequestedAction == domain.ActionMove && p.Machine == "":
		// A drain: any other machine that fits (MCH-08).
		need, err := r.need(ctx, res)
		if err != nil {
			return "", err
		}
		cands, err := r.Machines.Candidates(ctx)
		if err != nil {
			return "", err
		}
		var others []domain.Candidate
		for _, c := range cands {
			if c.ID != res.Machine {
				others = append(others, c)
			}
		}
		need.Machine = ""
		id, why := domain.ChooseMachine(others, need)
		if id == "" {
			return "", fmt.Errorf("%w: %s", errNotPlaced, why)
		}
		return id, nil
	case p.Machine != "":
		m, err := r.Store.GetMachineByName(ctx, p.Machine)
		if errors.Is(err, store.ErrNotFound) {
			return "", engine.Permanent(fmt.Errorf("there's no machine named %s", p.Machine))
		}
		if err != nil || m.ID == res.Machine {
			return "", err
		}
		return m.ID, nil
	case len(p.MachineLabels) > 0:
		cur, err := r.Store.GetMachine(ctx, res.Machine)
		if err != nil || api.HasLabels(cur.Labels, p.MachineLabels) {
			return "", err
		}
		need, err := r.need(ctx, res)
		if err != nil {
			return "", err
		}
		cands, err := r.Machines.Candidates(ctx)
		if err != nil {
			return "", err
		}
		id, why := domain.ChooseMachine(cands, need)
		if id == "" {
			return "", fmt.Errorf("%w: %s", errNotPlaced, why)
		}
		return id, nil
	}
	return "", nil
}

// startMove begins moving res to its new machine; nil when it stays. For data it stops the resource and
// copies the data; for everything it records the new machine, so the ensure that follows runs there.
func (r *Reconciler) startMove(ctx context.Context, res *domain.Resource, rep engine.Reporter) (*moving, error) {
	to, err := r.moveTarget(ctx, res)
	if err != nil || to == "" {
		return nil, err
	}
	if res.Kind == api.KindVolume && r.volumeUsedByVM(ctx, res) != "" {
		return nil, engine.Permanent(fmt.Errorf("the disk %s is attached to the VM %s; move the VM, and its disks move with it", res.Name, r.volumeUsedByVM(ctx, res)))
	}
	if (res.Kind == api.KindApp || res.Kind == api.KindJob) && r.mountsVolumes(res) {
		return nil, engine.Permanent(fmt.Errorf("%s uses volumes, which stay where they are; move the volume, and what uses it moves with it", res.Name))
	}
	mv := &moving{res: res, from: res.Machine, to: to, started: r.Now()}
	fromName, toName := r.Machines.Name(ctx, mv.from), r.Machines.Name(ctx, to)
	r.event(ctx, res, "move_started", fmt.Sprintf("Moving from %s to %s.", fromName, toName))
	res.Steps = append([]api.Step{{Name: "move", State: api.StepActive}}, res.Steps...)
	rep.Step("move", api.StepActive)
	if err := r.copyForMove(ctx, mv, rep); err != nil {
		r.abortMove(ctx, mv, err)
		return nil, fmt.Errorf("%w; still running on %s", err, fromName)
	}
	if err := r.setMachine(ctx, res, to); err != nil {
		r.abortMove(ctx, mv, err)
		return nil, err
	}
	for _, u := range mv.users {
		_ = r.setMachine(ctx, u, to)
	}
	for _, id := range mv.disks {
		_ = r.Store.SetMachine(ctx, id, to)
	}
	rep.Step("move", api.StepDone)
	return mv, nil
}

// copyForMove moves what a resource keeps on its machine to the new one.
func (r *Reconciler) copyForMove(ctx context.Context, mv *moving, rep engine.Reporter) error {
	res := mv.res
	fromD, fromH, err := r.Machines.Engines(mv.from)
	if err != nil {
		return err // FAIL-03: the data can't be read while its machine is offline
	}
	toD, toH, err := r.Machines.Engines(mv.to)
	if err != nil {
		return err
	}
	switch res.Kind {
	case api.KindDatabase, api.KindCache, api.KindSearch, api.KindEmail:
		mv.address, _ = res.Outputs[engine.OutputPrivateIP].(string)
		if spec, _, err := api.NormalizeSpec(res.Kind, res.Spec); err == nil {
			network, subnet := placementOf(spec)
			if p, err := r.placement(ctx, res.Project, network, subnet); err == nil {
				mv.network = p.NetworkName()
			}
		}
		if err := fromD.StopResource(ctx, res); err != nil {
			return fmt.Errorf("stop it on its machine: %w", err)
		}
		mv.stopped = r.Now()
		if err := r.stream(ctx, mv, rep, func(w io.Writer) error { return fromD.ExportData(ctx, res, w) },
			func(src io.Reader) error { return toD.ImportData(ctx, res, src) }); err != nil {
			return fmt.Errorf("copying the data failed: %w", err)
		}
		if mv.address != "" && mv.network != "" {
			if err := r.Store.MovePoolAddress(ctx, mv.network, mv.address, mv.to); err != nil {
				return err
			}
		}
	case api.KindVolume:
		if volumeType(res) == api.VolumeDisk {
			if fromH == nil || toH == nil {
				return fmt.Errorf("%w: a machine has no hypervisor", engine.ErrNoVMs)
			}
			ref := engine.DiskRef{DiskID: res.ID}
			return r.stream(ctx, mv, rep, func(w io.Writer) error { return fromH.ExportDisk(ctx, ref, w) },
				func(src io.Reader) error { return toH.ImportDisk(ctx, ref, src) })
		}
		users, err := r.volumeUsers(ctx, res)
		if err != nil {
			return err
		}
		for _, u := range users {
			if err := fromD.Delete(ctx, u, engine.ObjectStore{}); err != nil {
				return fmt.Errorf("stop %s, which uses it: %w", u.Name, err)
			}
		}
		mv.users, mv.stopped = users, r.Now()
		if err := toD.EnsureVolume(ctx, res); err != nil {
			return err
		}
		return r.stream(ctx, mv, rep, func(w io.Writer) error { return fromD.SnapshotVolume(ctx, res, w) },
			func(src io.Reader) error { return toD.RestoreVolume(ctx, res, src) })
	case api.KindVM:
		if fromH == nil || toH == nil {
			return fmt.Errorf("%w: a machine has no hypervisor", engine.ErrNoVMs)
		}
		if err := fromH.StopVM(ctx, res.ID); err != nil {
			return fmt.Errorf("shut it down: %w", err)
		}
		mv.stopped = r.Now()
		mv.address, _ = res.Outputs[engine.OutputPrivateIP].(string)
		if spec, _, err := api.NormalizeSpec(res.Kind, res.Spec); err == nil {
			s := spec.(api.VMSpec)
			if p, err := r.placement(ctx, res.Project, s.Network, s.Subnet); err == nil {
				mv.network = p.NetworkName()
			}
			for _, name := range s.Disks {
				if d, err := r.Store.GetResourceByName(ctx, res.ProjectName(), api.KindVolume, name); err == nil {
					mv.disks = append(mv.disks, d.ID)
				}
			}
		}
		refs := []engine.DiskRef{{VMID: res.ID}}
		for _, id := range mv.disks {
			refs = append(refs, engine.DiskRef{DiskID: id})
		}
		for _, ref := range refs {
			if err := r.stream(ctx, mv, rep, func(w io.Writer) error { return fromH.ExportDisk(ctx, ref, w) },
				func(src io.Reader) error { return toH.ImportDisk(ctx, ref, src) }); err != nil {
				return fmt.Errorf("copying its disks failed: %w", err)
			}
		}
		if mv.address != "" && mv.network != "" {
			if err := r.Store.MovePoolAddress(ctx, mv.network, mv.address, mv.to); err != nil {
				return err
			}
		}
	}
	return nil
}

// stream copies from one machine to the other through the control plane, counting the bytes in the move
// step's message every second (MOVE-06).
func (r *Reconciler) stream(ctx context.Context, mv *moving, rep engine.Reporter, read func(io.Writer) error, write func(io.Reader) error) error {
	pr, pw := io.Pipe()
	var n atomic.Int64
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				engine.Note(rep, fmt.Sprintf("Copied %s.", bytesText(mv.copied+n.Load())))
			}
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- write(&countingReader{r: pr, n: &n}) }()
	rerr := read(pw)
	pw.CloseWithError(rerr)
	werr := <-errc
	pr.CloseWithError(werr)
	close(done)
	mv.copied += n.Load()
	if rerr != nil {
		return rerr
	}
	return werr
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// finishMove removes the old copy once the new one is ready, and records the move.
func (r *Reconciler) finishMove(ctx context.Context, mv *moving) {
	old := *mv.res
	old.Machine = mv.from
	fromD, fromH, err := r.Machines.Engines(mv.from)
	if err == nil {
		switch old.Kind {
		case api.KindVM:
			_ = fromH.DeleteVM(ctx, old.ID)
			_ = fromD.DeleteVMLink(ctx, &old)
			for _, id := range mv.disks {
				_ = fromH.DeleteDisk(ctx, id)
			}
		case api.KindVolume:
			if volumeType(&old) == api.VolumeDisk {
				_ = fromH.DeleteDisk(ctx, old.ID)
			} else {
				_ = fromD.Delete(ctx, &old, engine.ObjectStore{})
			}
		default:
			if err := fromD.Delete(ctx, &old, engine.ObjectStore{}); err != nil {
				r.Log.Warn("remove the old copy after a move", "resource_id", old.ID, "error", err)
			}
		}
	}
	msg := fmt.Sprintf("Moved to %s in %s.", r.Machines.Name(ctx, mv.to), r.Now().Sub(mv.started).Round(time.Second))
	if !mv.stopped.IsZero() {
		msg = fmt.Sprintf("Moved to %s in %s; unavailable for %s.", r.Machines.Name(ctx, mv.to), r.Now().Sub(mv.started).Round(time.Second), r.Now().Sub(mv.stopped).Round(time.Second))
	}
	if mv.copied > 0 {
		msg += fmt.Sprintf(" Copied %s.", bytesText(mv.copied))
	}
	r.event(ctx, mv.res, "moved", msg)
	for _, u := range mv.users {
		r.markUpdating(ctx, u, "Moved with the volume "+mv.res.Name+".")
	}
	if mv.res.Kind == api.KindApp {
		r.followApp(ctx, mv.res, mv.to)
	}
	r.Metrics.ReconcileTotal.WithLabelValues(string(mv.res.Kind), "moved").Inc()
	r.MeshChanged()
}

// abortMove leaves the resource where it was, running again (MOVE-06).
func (r *Reconciler) abortMove(ctx context.Context, mv *moving, cause error) {
	ctx = context.WithoutCancel(ctx)
	_ = r.setMachine(ctx, mv.res, mv.from)
	if mv.address != "" && mv.network != "" {
		_ = r.Store.MovePoolAddress(ctx, mv.network, mv.address, mv.from)
	}
	if fromD, fromH, err := r.Machines.Engines(mv.from); err == nil && !mv.stopped.IsZero() {
		switch mv.res.Kind {
		case api.KindVM:
			r.markUpdating(ctx, mv.res, "") // ensureVM starts it again where it is
			_ = fromH
		case api.KindVolume:
			for _, u := range mv.users {
				_ = r.setMachine(ctx, u, mv.from)
				r.markUpdating(ctx, u, "")
			}
		default:
			_ = fromD.Restart(ctx, mv.res)
		}
	}
	r.event(ctx, mv.res, "move_failed", fmt.Sprintf("%v; still running on %s.", cause, r.Machines.Name(ctx, mv.from)))
	r.Metrics.ReconcileTotal.WithLabelValues(string(mv.res.Kind), "move_failed").Inc()
}

// markUpdating sends a resource through its ensure again (users that move with a volume).
func (r *Reconciler) markUpdating(ctx context.Context, res *domain.Resource, why string) {
	fresh, err := r.Store.GetResource(ctx, res.ID)
	if err != nil {
		return
	}
	now := r.Now()
	if fresh.Phase != api.PhaseDeleting && fresh.SetPhase(api.PhaseUpdating, "", now) == nil {
		fresh.Steps, fresh.Attempts, fresh.NextAttemptAt, fresh.UpdatedAt = domain.NewSteps(domain.StepNames[fresh.Kind]...), 0, nil, now
		_, _ = r.Store.SaveStatus(ctx, fresh, fresh.Generation)
		if why != "" {
			r.event(ctx, fresh, "moved", why)
		}
		r.Enqueue(fresh.ID)
	}
}

// volumeUsers are the apps and jobs that mount a filesystem volume.
func (r *Reconciler) volumeUsers(ctx context.Context, vol *domain.Resource) ([]*domain.Resource, error) {
	var out []*domain.Resource
	for _, k := range []api.Kind{api.KindApp, api.KindJob} {
		rows, err := r.Store.ProjectResourcesOfKind(ctx, vol.ProjectName(), k)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Phase != api.PhaseDeleting && slices.Contains(mountedVolumes(row), vol.Name) {
				out = append(out, row)
			}
		}
	}
	return out, nil
}

func mountedVolumes(res *domain.Resource) []string {
	spec, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return nil
	}
	return api.MountedVolumes(spec)
}

func (r *Reconciler) mountsVolumes(res *domain.Resource) bool { return len(mountedVolumes(res)) > 0 }

// volumeUsedByVM is the VM a disk volume is attached to, or "".
func (r *Reconciler) volumeUsedByVM(ctx context.Context, vol *domain.Resource) string {
	vms, err := r.Store.ProjectResourcesOfKind(ctx, vol.ProjectName(), api.KindVM)
	if err != nil {
		return ""
	}
	for _, vm := range vms {
		if spec, _, err := api.NormalizeSpec(api.KindVM, vm.Spec); err == nil && slices.Contains(spec.(api.VMSpec).Disks, vol.Name) && vm.Phase != api.PhaseDeleting {
			return vm.Name
		}
	}
	return ""
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
