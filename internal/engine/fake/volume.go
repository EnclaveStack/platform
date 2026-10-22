package fake

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
)

// Filesystem volumes (ADR-027): each engine volume is a map of file path → contents, and snapshots
// are real tar archives, so the reconciler's copy path runs end to end.

func (d *Driver) volumeFiles(name string) map[string]string {
	if d.volumes == nil {
		d.volumes = map[string]map[string]string{}
	}
	return d.volumes[name]
}

// EnsureVolume implements engine.Driver.
func (d *Driver) EnsureVolume(ctx context.Context, r *domain.Resource) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.volumeFiles(engine.FilesystemVolume(r.ID)) == nil {
		d.volumes[engine.FilesystemVolume(r.ID)] = map[string]string{}
	}
	return nil
}

// VolumeExists reports whether a filesystem volume's engine volume exists.
func (d *Driver) VolumeExists(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.volumeFiles(engine.FilesystemVolume(id)) != nil
}

// KillVolume simulates a volume removed outside EnclaveStack (drift).
func (d *Driver) KillVolume(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.volumes, engine.FilesystemVolume(id))
}

// WriteFile puts a file into a volume, as an app writing it would.
func (d *Driver) WriteFile(id, path, contents string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if files := d.volumeFiles(engine.FilesystemVolume(id)); files != nil {
		files[path] = contents
	}
}

// Files returns a copy of a volume's files.
func (d *Driver) Files(id string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.volumeFiles(engine.FilesystemVolume(id)))
}

// Mounts returns the mounts an app's newest Ensure or a run's start was given.
func (d *Driver) Mounts(resourceID string) []engine.Mount {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.mounts[resourceID])
}

func (d *Driver) recordMounts(resourceID string, ms []engine.Mount) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mounts == nil {
		d.mounts = map[string][]engine.Mount{}
	}
	d.mounts[resourceID] = slices.Clone(ms)
}

func (d *Driver) deleteVolume(r *domain.Resource) {
	delete(d.volumes, engine.FilesystemVolume(r.ID))
}

// VolumeUsage implements engine.Driver.
func (d *Driver) VolumeUsage(ctx context.Context, r *domain.Resource) (int64, error) {
	if err := d.Ping(ctx); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int64
	for _, c := range d.volumeFiles(engine.FilesystemVolume(r.ID)) {
		n += int64(len(c))
	}
	return n, nil
}

// SnapshotVolume implements engine.Driver.
func (d *Driver) SnapshotVolume(ctx context.Context, r *domain.Resource, w io.Writer) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	files := d.volumeFiles(engine.FilesystemVolume(r.ID))
	if files == nil {
		d.mu.Unlock()
		return fmt.Errorf("the volume %s doesn't exist in the engine", r.Name)
	}
	files = maps.Clone(files)
	d.mu.Unlock()
	tw := tar.NewWriter(w)
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + p, Mode: 0o644, Size: int64(len(files[p])), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, files[p]); err != nil {
			return err
		}
	}
	return tw.Close()
}

// RestoreVolume implements engine.Driver.
func (d *Driver) RestoreVolume(ctx context.Context, r *domain.Resource, src io.Reader) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	tr := tar.NewReader(src)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read snapshot: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		got[cleanTarName(h.Name)] = string(b)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	files := d.volumeFiles(engine.FilesystemVolume(r.ID))
	if files == nil {
		return fmt.Errorf("the volume %s doesn't exist in the engine", r.Name)
	}
	maps.Copy(files, got)
	return nil
}

func cleanTarName(n string) string {
	for len(n) > 2 && n[:2] == "./" {
		n = n[2:]
	}
	return n
}
