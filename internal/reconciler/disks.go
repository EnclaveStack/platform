package reconciler

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Disk volumes (ADR-027) are the hypervisor's qcow2 files.

// hypervisorReady returns ErrNoVMs with the reason when VMs and disks can't be handled here (VM-11).
func (r *Reconciler) hypervisorReady(ctx context.Context) error {
	if r.Hypervisor == nil {
		return fmt.Errorf("%w: this server runs without a hypervisor", engine.ErrNoVMs)
	}
	if s := r.Hypervisor.Check(ctx); !s.Available {
		return fmt.Errorf("%w: %s", engine.ErrNoVMs, s.Message)
	}
	return nil
}

// ensureDisk creates or grows a disk volume's file (VOL-01, VOL-03). A disk that starts from a snapshot
// is created by fillDisk instead.
func (r *Reconciler) ensureDisk(ctx context.Context, res *domain.Resource, s api.VolumeSpec) error {
	if err := r.hypervisorReady(ctx); err != nil {
		return err
	}
	if s.Snapshot != "" && res.Outputs[engine.OutputFilledFrom] == nil {
		return nil
	}
	return r.Hypervisor.EnsureDisk(ctx, engine.DiskRef{DiskID: res.ID, Attached: r.runningVMWith(ctx, res)}, s.SizeGiB)
}

func (r *Reconciler) fillDisk(ctx context.Context, res *domain.Resource, s api.VolumeSpec, snap *domain.Resource) error {
	src := r.snapshotFile(snap, api.VolumeDisk)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return engine.Permanent(fmt.Errorf("the snapshot %s's file is missing from %s", snap.Name, r.SnapshotDir))
	}
	return r.Hypervisor.CopyDisk(ctx, src, res.ID, s.SizeGiB)
}

// runningVMWith returns the ID of the running VM a disk volume is attached to, or "".
func (r *Reconciler) runningVMWith(ctx context.Context, vol *domain.Resource) string {
	deps, err := r.Store.Dependants(ctx, vol.ID)
	if err != nil {
		return ""
	}
	for _, d := range deps {
		if d.Kind != api.KindVM {
			continue
		}
		if st, err := r.Hypervisor.VMStatus(ctx, d.ID); err == nil && st == engine.VMRunning {
			return d.ID
		}
	}
	return ""
}

// diskRef names the disk a snapshot copies and says whether a running VM is using it.
func (r *Reconciler) diskRef(ctx context.Context, src *domain.Resource) (engine.DiskRef, bool, error) {
	if err := r.hypervisorReady(ctx); err != nil {
		return engine.DiskRef{}, false, err
	}
	if src.Kind == api.KindVM {
		st, err := r.Hypervisor.VMStatus(ctx, src.ID)
		return engine.DiskRef{VMID: src.ID}, err == nil && st == engine.VMRunning, err
	}
	attached := r.runningVMWith(ctx, src)
	return engine.DiskRef{DiskID: src.ID, Attached: attached}, attached != "", nil
}
