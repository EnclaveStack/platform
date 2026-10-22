package reconciler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Snapshots (ADR-028): full copies kept in SnapshotDir, written to a .partial file and renamed when
// complete, so an interrupted copy is started again and never mistaken for a finished one.

// Snapshot outputs (L3-002 §8.2).
const (
	OutputSnapshotType = "type"
	OutputSizeBytes    = "sizeBytes"
	OutputTakenAt      = "takenAt"
	OutputInUse        = "inUse"
	partialSuffix      = ".partial"
)

// snapshotFile is where a snapshot of the given type lives.
func (r *Reconciler) snapshotFile(snap *domain.Resource, typ string) string {
	ext := ".tar"
	if typ == api.VolumeDisk {
		ext = ".qcow2"
	}
	return filepath.Join(r.SnapshotDir, snap.ID+ext)
}

func (r *Reconciler) ensureSnapshot(ctx context.Context, res *domain.Resource, rep engine.Reporter) (engine.Outputs, error) {
	if res.Outputs[OutputTakenAt] != nil { // a snapshot is never taken again (SNP-03)
		for _, step := range domain.StepNames[res.Kind] {
			rep.Step(step, api.StepDone)
		}
		return res.Outputs, nil
	}
	raw, _, err := api.NormalizeSpec(api.KindSnapshot, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	kind, name, _ := api.ParseSnapshotSource(raw.(api.SnapshotSpec).Source)
	rep.Step("source", api.StepActive)
	src, err := r.Store.GetResourceByName(ctx, res.Project, kind, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, engine.Permanent(fmt.Errorf("the %s %s no longer exists", kind, name))
	}
	if err != nil {
		return nil, err
	}
	if src.ObservedGeneration == 0 {
		return nil, fmt.Errorf("waiting for the %s %s to be created", kind, name)
	}
	typ := api.VolumeDisk
	if kind == api.KindVolume {
		raw, _, err := api.NormalizeSpec(api.KindVolume, src.Spec)
		if err != nil {
			return nil, engine.Permanent(fmt.Errorf("the volume %s has an unreadable spec", name))
		}
		typ = raw.(api.VolumeSpec).Type
	}
	rep.Step("source", api.StepDone)

	rep.Step("copy", api.StepActive)
	if err := os.MkdirAll(r.SnapshotDir, 0o700); err != nil {
		return nil, err
	}
	dest := r.snapshotFile(res, typ)
	partial := dest + partialSuffix
	_ = os.Remove(partial) // left by an interrupted copy (CP-09)
	started := r.Now()
	out := engine.Outputs{OutputSnapshotType: typ}
	if typ == api.VolumeFilesystem {
		deps, _ := r.Store.Dependants(ctx, src.ID)
		out[OutputInUse] = len(deps) > 0
		if err := r.copyFilesystem(ctx, src, partial); err != nil {
			_ = os.Remove(partial)
			return nil, err
		}
	} else {
		ref, running, err := r.diskRef(ctx, src)
		if err != nil {
			return nil, err
		}
		out[OutputInUse] = running
		size, err := r.Hypervisor.SnapshotDisk(ctx, ref, partial)
		if err != nil {
			_ = os.Remove(partial)
			return nil, err
		}
		out[OutputSizeGiB] = int((size + 1<<30 - 1) >> 30)
	}
	if err := os.Rename(partial, dest); err != nil {
		return nil, err
	}
	st, err := os.Stat(dest)
	if err != nil {
		return nil, err
	}
	out[OutputSizeBytes], out[OutputTakenAt] = st.Size(), started.UTC().Format(time.RFC3339)
	r.Metrics.SnapshotSeconds.WithLabelValues(typ).Observe(r.Now().Sub(started).Seconds())
	r.Metrics.Snapshots.WithLabelValues(typ, "ready").Inc()
	rep.Step("copy", api.StepDone)
	msg := fmt.Sprintf("Copied %s/%s (%d bytes).", kind, name, st.Size())
	if out[OutputInUse] == true && typ == api.VolumeFilesystem {
		msg += " It was in use, so a file written during the copy may be captured part-way."
	}
	r.event(ctx, res, "copied", msg)
	return out, nil
}

func (r *Reconciler) copyFilesystem(ctx context.Context, src *domain.Resource, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := r.Driver.SnapshotVolume(ctx, src, f); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// deleteSnapshot removes a snapshot's file, whichever type it is.
func (r *Reconciler) deleteSnapshot(res *domain.Resource) error {
	for _, typ := range api.VolumeTypes {
		for _, p := range []string{r.snapshotFile(res, typ), r.snapshotFile(res, typ) + partialSuffix} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
