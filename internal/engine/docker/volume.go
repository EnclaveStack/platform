package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
)

// Filesystem volumes (ADR-027) and their snapshots (ADR-028). A volume is one engine volume; copies go
// through the engine's archive API from a container that is created but never started, so no host
// directory is ever mounted (L3-006 §2).

const (
	snapshotMountPath = "/es-volume"
	copyHelperPrefix  = "es-snapshot-"
)

// volumeMounts turns resolved mounts into engine mounts of named volumes.
func volumeMounts(ms []engine.Mount) []mount.Mount {
	var out []mount.Mount
	for _, m := range ms {
		out = append(out, mount.Mount{Type: mount.TypeVolume, Source: m.Volume, Target: m.Path, ReadOnly: m.ReadOnly})
	}
	return out
}

// EnsureVolume implements engine.Driver.
func (d *Driver) EnsureVolume(ctx context.Context, r *domain.Resource) error {
	name := engine.FilesystemVolume(r.ID)
	vctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if _, err := d.cli.VolumeInspect(vctx, name); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return d.classify(fmt.Errorf("inspect volume %s: %w", name, err))
	}
	labels := map[string]string{LabelManaged: "true", LabelResourceID: r.ID, LabelKind: string(r.Kind)}
	if _, err := d.cli.VolumeCreate(vctx, volume.CreateOptions{Name: name, Labels: labels}); err != nil {
		return d.classify(fmt.Errorf("create volume %s: %w", name, err))
	}
	return nil
}

func (d *Driver) deleteVolume(ctx context.Context, r *domain.Resource) error {
	vctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := d.cli.VolumeRemove(vctx, engine.FilesystemVolume(r.ID), true); err != nil && !cerrdefs.IsNotFound(err) {
		return d.classify(fmt.Errorf("remove volume: %w", err))
	}
	return nil
}

func (d *Driver) volumeHealth(ctx context.Context, r *domain.Resource) (engine.Health, error) {
	if _, err := d.cli.VolumeInspect(ctx, engine.FilesystemVolume(r.ID)); cerrdefs.IsNotFound(err) {
		return engine.Missing, nil
	} else if err != nil {
		return engine.Healthy, d.classify(err)
	}
	return engine.Healthy, nil
}

// VolumeUsage implements engine.Driver with the engine's disk-usage report.
func (d *Driver) VolumeUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	du, err := d.cli.DiskUsage(ctx, types.DiskUsageOptions{Types: []types.DiskUsageObject{types.VolumeObject}})
	if err != nil {
		return 0, d.classify(fmt.Errorf("disk usage: %w", err))
	}
	name := engine.FilesystemVolume(r.ID)
	for _, v := range du.Volumes {
		if v != nil && v.Name == name && v.UsageData != nil && v.UsageData.Size >= 0 {
			return v.UsageData.Size, nil
		}
	}
	return 0, nil
}

// SnapshotVolume implements engine.Driver.
func (d *Driver) SnapshotVolume(ctx context.Context, r *domain.Resource, w io.Writer) error {
	return d.exportVolume(ctx, r, engine.FilesystemVolume(r.ID), w)
}

// RestoreVolume implements engine.Driver.
func (d *Driver) RestoreVolume(ctx context.Context, r *domain.Resource, src io.Reader) error {
	return d.importVolume(ctx, r, engine.FilesystemVolume(r.ID), src)
}

// ExportData implements engine.Driver: a data volume, read through the copy helper as a snapshot is.
func (d *Driver) ExportData(ctx context.Context, r *domain.Resource, w io.Writer) error {
	return d.exportVolume(ctx, r, engine.VolumeName(r), w)
}

// ImportData implements engine.Driver.
func (d *Driver) ImportData(ctx context.Context, r *domain.Resource, src io.Reader) error {
	return d.importVolume(ctx, r, engine.VolumeName(r), src)
}

// StopResource implements engine.Driver.
func (d *Driver) StopResource(ctx context.Context, r *domain.Resource) error {
	timeout := 30
	names, err := d.containersFor(ctx, r)
	if err != nil {
		return err
	}
	for _, name := range names {
		sctx, cancel := context.WithTimeout(ctx, stepTimeout)
		err := d.cli.ContainerStop(sctx, name, container.StopOptions{Timeout: &timeout})
		cancel()
		if err != nil && !cerrdefs.IsNotFound(err) {
			return d.classify(fmt.Errorf("stop %s: %w", name, err))
		}
	}
	return nil
}

func (d *Driver) exportVolume(ctx context.Context, r *domain.Resource, vol string, w io.Writer) error {
	return d.withCopyHelper(ctx, r, vol, true, func(id string) error {
		rc, _, err := d.cli.CopyFromContainer(ctx, id, snapshotMountPath+"/.")
		if err != nil {
			return d.classify(fmt.Errorf("read volume: %w", err))
		}
		defer rc.Close()
		if _, err := io.Copy(w, rc); err != nil {
			return fmt.Errorf("copy volume: %w", err)
		}
		return nil
	})
}

func (d *Driver) importVolume(ctx context.Context, r *domain.Resource, vol string, src io.Reader) error {
	return d.withCopyHelper(ctx, r, vol, false, func(id string) error {
		if err := d.cli.CopyToContainer(ctx, id, snapshotMountPath, src, container.CopyToContainerOptions{}); err != nil {
			return d.classify(fmt.Errorf("fill volume: %w", err))
		}
		return nil
	})
}

// withCopyHelper creates (never starts) a container with the volume mounted, runs fn with its ID, and
// removes it.
func (d *Driver) withCopyHelper(ctx context.Context, r *domain.Resource, vol string, readOnly bool, fn func(id string) error) error {
	if err := d.ensureImage(ctx, d.opts.ProbeImage, false); err != nil {
		return err
	}
	name := copyHelperPrefix + strings.TrimPrefix(domain.NewID("x"), "x_")
	cfg := &container.Config{Image: d.opts.ProbeImage, Labels: map[string]string{LabelManaged: "true", LabelResourceID: r.ID}}
	host := &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: vol, Target: snapshotMountPath, ReadOnly: readOnly}}}
	cctx, cancel := context.WithTimeout(ctx, stepTimeout)
	created, err := d.cli.ContainerCreate(cctx, cfg, host, nil, nil, name)
	cancel()
	if err != nil {
		return d.classify(fmt.Errorf("create copy helper: %w", err))
	}
	defer d.remove(context.WithoutCancel(ctx), created.ID) //nolint:errcheck // best effort; drift removes leftovers
	return fn(created.ID)
}
