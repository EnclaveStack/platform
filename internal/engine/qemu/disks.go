package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
)

// Disk files (ADR-027, ADR-028): disk volumes are thin qcow2 files in <data dir>/disks/. Copies are
// flat qcow2 files written through a .partial name.

const (
	copyTimeout = 2 * time.Hour
	gib         = int64(1) << 30
)

type imgInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
}

// diskInfo reads an image's format and size; -U lets it read an image a running VM has open.
func diskInfo(ctx context.Context, t tools, path string) (imgInfo, error) {
	out, err := run(ctx, commandTimeout, t.img, "info", "-U", "--output=json", path)
	if err != nil {
		return imgInfo{}, err
	}
	var info imgInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return imgInfo{}, fmt.Errorf("qemu-img info %s: %w", path, err)
	}
	return info, nil
}

func (h *Hypervisor) diskPath(id string) string {
	return filepath.Join(h.opts.DataDir, "disks", id+".qcow2")
}

// EnsureDisk implements engine.Hypervisor.
func (h *Hypervisor) EnsureDisk(ctx context.Context, d engine.DiskRef, sizeGiB int) error {
	t, err := h.readyTools(ctx)
	if err != nil {
		return err
	}
	if d.Attached != "" {
		defer h.lock(d.Attached)()
	}
	defer h.lock("disk-" + d.DiskID)()
	path := h.diskPath(d.DiskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		partial := path + ".partial"
		if _, err := run(ctx, commandTimeout, t.img, "create", "-q", "-f", "qcow2", partial, strconv.Itoa(sizeGiB)+"G"); err != nil {
			return err
		}
		if err := os.Chmod(partial, 0o600); err != nil {
			return err
		}
		return os.Rename(partial, path)
	}
	return h.grow(ctx, t, path, "disk-"+d.DiskID, d.Attached, sizeGiB)
}

// grow resizes an image to sizeGiB if it is smaller: live through the VM's monitor when vmID runs it
// (VOL-03, VM-02), otherwise with qemu-img.
func (h *Hypervisor) grow(ctx context.Context, t tools, path, device, vmID string, sizeGiB int) error {
	info, err := diskInfo(ctx, t, path)
	if err != nil {
		return err
	}
	want := int64(sizeGiB) * gib
	if info.VirtualSize >= want {
		return nil
	}
	if vmID != "" {
		if p := h.paths(vmID); h.alive(p) {
			return qmpRun(ctx, p.sock, "block_resize", map[string]any{"device": device, "size": want})
		}
	}
	_, err = run(ctx, commandTimeout, t.img, "resize", "-q", path, strconv.Itoa(sizeGiB)+"G")
	return err
}

// DeleteDisk implements engine.Hypervisor.
func (h *Hypervisor) DeleteDisk(_ context.Context, id string) error {
	defer h.lock("disk-" + id)()
	for _, p := range []string{h.diskPath(id), h.diskPath(id) + ".partial"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// SnapshotDisk implements engine.Hypervisor.
func (h *Hypervisor) SnapshotDisk(ctx context.Context, d engine.DiskRef, dest string) (int64, error) {
	t, err := h.readyTools(ctx)
	if err != nil {
		return 0, err
	}
	vmID, device, path := d.VMID, "root", ""
	if d.VMID != "" {
		path = h.paths(d.VMID).root
	} else {
		vmID, device, path = d.Attached, "disk-"+d.DiskID, h.diskPath(d.DiskID)
	}
	if vmID != "" {
		defer h.lock(vmID)()
	}
	if d.DiskID != "" {
		defer h.lock("disk-" + d.DiskID)()
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("the disk to copy is missing: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	if p := h.paths(vmID); vmID != "" && h.alive(p) {
		err = h.liveBackup(cctx, p, device, dest)
	} else {
		_, err = run(cctx, copyTimeout, t.img, "convert", "-U", "-O", "qcow2", path, dest)
	}
	if err != nil {
		_ = os.Remove(dest)
		return 0, err
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return 0, err
	}
	info, err := diskInfo(ctx, t, dest)
	return info.VirtualSize, err
}

// liveBackup copies a running VM's disk with drive-backup: a point-in-time copy, flattened, without
// pausing the guest (SNP-01, SNP-05; 0.8 s for 1.13 GiB in the ADR-028 spike).
func (h *Hypervisor) liveBackup(ctx context.Context, p vmPaths, device, dest string) error {
	q, err := dialQMP(ctx, p.sock)
	if err != nil {
		return err
	}
	defer q.Close()
	job := "es-backup-" + domain.NewID("j")
	if _, err := q.execute(ctx, "drive-backup", map[string]any{"device": device, "target": dest, "format": "qcow2", "sync": "full", "job-id": job}); err != nil {
		return err
	}
	for {
		m, err := q.waitEvent(ctx, "BLOCK_JOB_COMPLETED", "BLOCK_JOB_ERROR", "BLOCK_JOB_CANCELLED")
		if err != nil {
			return fmt.Errorf("waiting for the copy: %w", err)
		}
		var data struct {
			Device string `json:"device"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal(m.Data, &data)
		if data.Device != job {
			continue
		}
		switch {
		case m.Event == "BLOCK_JOB_COMPLETED" && data.Error == "":
			return nil
		case data.Error != "":
			return fmt.Errorf("QEMU couldn't copy the disk: %s", data.Error)
		default:
			return fmt.Errorf("QEMU stopped copying the disk (%s)", m.Event)
		}
	}
}

// CopyDisk implements engine.Hypervisor.
func (h *Hypervisor) CopyDisk(ctx context.Context, src, id string, sizeGiB int) error {
	t, err := h.readyTools(ctx)
	if err != nil {
		return err
	}
	defer h.lock("disk-" + id)()
	path := h.diskPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return copyImage(ctx, t, src, path, sizeGiB)
}

// copyImage writes a flat qcow2 copy of src at dest through a .partial name, grown to sizeGiB.
func copyImage(ctx context.Context, t tools, src, dest string, sizeGiB int) error {
	partial := dest + ".partial"
	_ = os.Remove(partial)
	if _, err := run(ctx, copyTimeout, t.img, "convert", "-U", "-O", "qcow2", src, partial); err != nil {
		return err
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		return err
	}
	info, err := diskInfo(ctx, t, partial)
	if err != nil {
		return err
	}
	if want := int64(sizeGiB) * gib; info.VirtualSize < want {
		if _, err := run(ctx, commandTimeout, t.img, "resize", "-q", partial, strconv.Itoa(sizeGiB)+"G"); err != nil {
			return err
		}
	}
	return os.Rename(partial, dest)
}
