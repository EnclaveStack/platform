package reconciler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Volumes (ADR-027): filesystem volumes are engine volumes mounted into apps and jobs; disk volumes
// are the hypervisor's files. A volume may start from a snapshot (ADR-028).

// Volume outputs (L3-002 §8.2).
const (
	OutputAttachedTo = "attachedTo"
	OutputSizeGiB    = "sizeGiB"
	OutputUsedBytes  = "usedBytes"
	// usageEvery bounds how often a filesystem's used space is measured (ADR-027).
	usageEvery = 5 * time.Minute
)

// resolveMounts turns an app's or job's mounts into engine mounts. A volume still being created makes
// the caller wait and retry.
func (r *Reconciler) resolveMounts(ctx context.Context, project string, mounts []api.VolumeMount) ([]engine.Mount, error) {
	var out []engine.Mount
	for _, m := range mounts {
		vol, err := r.Store.GetResourceByName(ctx, project, api.KindVolume, m.Volume)
		if errors.Is(err, store.ErrNotFound) {
			return nil, engine.Permanent(fmt.Errorf("the volume %s no longer exists", m.Volume))
		}
		if err != nil {
			return nil, err
		}
		if vol.ObservedGeneration == 0 {
			return nil, fmt.Errorf("waiting for the volume %s to be created", m.Volume)
		}
		out = append(out, engine.Mount{Volume: engine.FilesystemVolume(vol.ID), Path: m.Path, ReadOnly: m.ReadOnly})
	}
	return out, nil
}

func (r *Reconciler) ensureVolume(ctx context.Context, res *domain.Resource, rep engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindVolume, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	s := raw.(api.VolumeSpec)
	out := engine.Outputs{}
	if from, ok := res.Outputs[engine.OutputFilledFrom]; ok {
		out[engine.OutputFilledFrom] = from
	}
	rep.Step("create", api.StepActive)
	if s.Type == api.VolumeDisk {
		if err := r.ensureDisk(ctx, res, s); err != nil {
			return nil, err
		}
		out[OutputSizeGiB] = s.SizeGiB
	} else if err := r.Driver.EnsureVolume(ctx, res); err != nil {
		return nil, err
	}
	rep.Step("create", api.StepDone)

	rep.Step("fill", api.StepActive)
	if s.Snapshot != "" && out[engine.OutputFilledFrom] == nil {
		if err := r.fillVolume(ctx, res, s); err != nil {
			return nil, err
		}
		out[engine.OutputFilledFrom] = s.Snapshot
		// The volume holds its own copy now, so the snapshot may go (SNP-06).
		if err := r.Store.WithTx(ctx, func(q *store.Queries) error { return q.SetRefs(ctx, res.ID, nil) }); err != nil {
			return nil, err
		}
		r.event(ctx, res, "filled", fmt.Sprintf("Filled from the snapshot %s.", s.Snapshot))
	}
	rep.Step("fill", api.StepDone)
	r.volumeFacts(ctx, res, s, out, true)
	return out, nil
}

// fillVolume copies a snapshot into a new volume.
func (r *Reconciler) fillVolume(ctx context.Context, res *domain.Resource, s api.VolumeSpec) error {
	snap, err := r.Store.GetResourceByName(ctx, res.Project, api.KindSnapshot, s.Snapshot)
	if errors.Is(err, store.ErrNotFound) {
		return engine.Permanent(fmt.Errorf("the snapshot %s was deleted before %s could be filled from it", s.Snapshot, res.Name))
	}
	if err != nil {
		return err
	}
	if s.Type == api.VolumeDisk {
		return r.fillDisk(ctx, res, s, snap)
	}
	f, err := os.Open(r.snapshotFile(snap, api.VolumeFilesystem))
	if errors.Is(err, os.ErrNotExist) {
		return engine.Permanent(fmt.Errorf("the snapshot %s's file is missing from %s", s.Snapshot, r.SnapshotDir))
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return r.Driver.RestoreVolume(ctx, res, f)
}

// volumeFacts sets what a volume is attached to and, for filesystems, the space its files use
// (measured at most every usageEvery unless now is set).
func (r *Reconciler) volumeFacts(ctx context.Context, res *domain.Resource, s api.VolumeSpec, out engine.Outputs, now bool) {
	deps, err := r.Store.Dependants(ctx, res.ID)
	if err == nil {
		var names []string
		for _, d := range deps {
			if d.Phase != api.PhaseDeleting {
				names = append(names, string(d.Kind)+"/"+d.Name)
			}
		}
		slices.Sort(names)
		out[OutputAttachedTo] = strings.Join(names, ",")
	}
	if s.Type != api.VolumeFilesystem {
		return
	}
	if used, ok := res.Outputs[OutputUsedBytes]; ok {
		out[OutputUsedBytes] = used
	}
	if !now && r.Now().Sub(res.UpdatedAt) < usageEvery {
		return
	}
	if n, err := r.Driver.VolumeUsage(ctx, res); err == nil {
		out[OutputUsedBytes] = n
	}
}

// refreshVolume is the drift pass's look at a healthy volume: attachments and used space.
func (r *Reconciler) refreshVolume(ctx context.Context, res *domain.Resource) {
	raw, _, err := api.NormalizeSpec(api.KindVolume, res.Spec)
	if err != nil {
		return
	}
	out := engine.Outputs{}
	for k, v := range res.Outputs {
		out[k] = v
	}
	r.volumeFacts(ctx, res, raw.(api.VolumeSpec), out, false)
	if out[OutputAttachedTo] == res.Outputs[OutputAttachedTo] && fmt.Sprint(out[OutputUsedBytes]) == fmt.Sprint(res.Outputs[OutputUsedBytes]) {
		return
	}
	res.Outputs, res.UpdatedAt = out, r.Now()
	_, _ = r.Store.SaveStatus(ctx, res, res.Generation)
}

// volumeType reads a volume's type; "" when its spec is unreadable.
func volumeType(res *domain.Resource) string {
	raw, _, err := api.NormalizeSpec(api.KindVolume, res.Spec)
	if err != nil {
		return ""
	}
	return raw.(api.VolumeSpec).Type
}
