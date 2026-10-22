package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.5 rules that need other resources (L3-002 §8.4, §8.5): mounts, volumes from snapshots and
// snapshot sources.

// volumeSpecOf reads a live volume's spec.
func volumeSpecOf(res *domain.Resource) (api.VolumeSpec, error) {
	var vs api.VolumeSpec
	err := json.Unmarshal(res.Spec, &vs)
	return vs, err
}

// mountRefs checks an app's or job's mounts name filesystem volumes and references them (VOL-04, VOL-07).
func (s *Resources) mountRefs(ctx context.Context, q *store.Queries, project string, mounts []api.VolumeMount) ([]store.Ref, error) {
	var refs []store.Ref
	for i, m := range mounts {
		f := fmt.Sprintf("spec.volumes[%d].volume", i)
		v, err := live(ctx, q, project, api.KindVolume, m.Volume, f)
		if err != nil {
			return nil, err
		}
		vs, err := volumeSpecOf(v)
		if err != nil {
			return nil, err
		}
		if vs.Type != api.VolumeFilesystem {
			return nil, api.Validation([]api.FieldError{{Field: f, Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("%s is a disk volume, which only a VM can use. Mount a filesystem volume.", m.Volume)}})
		}
		refs = append(refs, store.Ref{ToID: v.ID, Field: f})
	}
	return dedupeRefs(refs), nil
}

// snapshotType is the type of volume a snapshot can fill: a VM's root disk counts as a disk (VOL-08).
func snapshotType(snap *domain.Resource) string {
	if t, ok := snap.Outputs["type"].(string); ok {
		return t
	}
	var ss api.SnapshotSpec
	if json.Unmarshal(snap.Spec, &ss) == nil {
		if kind, _, ok := api.ParseSnapshotSource(ss.Source); ok && kind == api.KindVM {
			return api.VolumeDisk
		}
	}
	return ""
}

// readySnapshot returns a snapshot that has finished copying, or a field error.
func readySnapshot(ctx context.Context, q *store.Queries, project, name, field string) (*domain.Resource, error) {
	snap, err := live(ctx, q, project, api.KindSnapshot, name, field)
	if err != nil {
		return nil, err
	}
	if snap.Phase != api.PhaseReady {
		return nil, api.Validation([]api.FieldError{{Field: field, Code: api.FieldNotAllowed,
			Message: fmt.Sprintf("The snapshot %s isn't ready yet. Wait until its copy is done.", name)}})
	}
	return snap, nil
}

// volumeRefs checks a volume's starting snapshot until the volume holds its copy (VOL-08, SNP-06).
func (s *Resources) volumeRefs(ctx context.Context, q *store.Queries, r *domain.Resource, vs api.VolumeSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	if vs.Snapshot == "" || r.Outputs[engine.OutputFilledFrom] != nil {
		return nil, nil
	}
	snap, err := readySnapshot(ctx, q, project, vs.Snapshot, "spec.snapshot")
	if err != nil {
		return nil, err
	}
	if t := snapshotType(snap); t != vs.Type {
		return nil, api.Validation([]api.FieldError{{Field: "spec.snapshot", Code: api.FieldNotAllowed,
			Message: fmt.Sprintf("The snapshot %s is a %s snapshot; it can only start a %s volume.", vs.Snapshot, t, t)}})
	}
	if size, ok := snap.Outputs["sizeGiB"].(float64); ok && vs.Type == api.VolumeDisk && float64(vs.SizeGiB) < size {
		return nil, api.Validation([]api.FieldError{{Field: "spec.sizeGiB", Code: api.FieldOutOfRange,
			Message: fmt.Sprintf("The snapshot %s holds a %d GiB disk; make the volume at least that big.", vs.Snapshot, int(size))}})
	}
	return []store.Ref{{ToID: snap.ID, Field: "spec.snapshot"}}, nil
}

// snapshotSourceCheck checks a snapshot's source exists. A snapshot doesn't reference its source:
// either can be deleted at any time once the copy is done (SNP-02).
func (s *Resources) snapshotSourceCheck(ctx context.Context, q *store.Queries, project string, ss api.SnapshotSpec) error {
	kind, name, _ := api.ParseSnapshotSource(ss.Source)
	_, err := live(ctx, q, project, kind, name, "spec.source")
	return err
}

// vmRefs checks a VM's network, disks, key secret and starting snapshot, and the machine's limits
// (L3-002 §8.4, §8.5).
func (s *Resources) vmRefs(ctx context.Context, q *store.Queries, r *domain.Resource, vs api.VMSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	refs, err := s.placementRefs(ctx, q, project, vs.Network, vs.Subnet, len(vs.Publish) > 0)
	if err != nil {
		return nil, err
	}
	for i, name := range vs.Disks {
		f := fmt.Sprintf("spec.disks[%d]", i)
		v, err := live(ctx, q, project, api.KindVolume, name, f)
		if err != nil {
			return nil, err
		}
		vol, err := volumeSpecOf(v)
		if err != nil {
			return nil, err
		}
		if vol.Type != api.VolumeDisk {
			return nil, api.Validation([]api.FieldError{{Field: f, Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("%s is a filesystem volume, which apps and jobs mount. Attach a disk volume.", name)}})
		}
		deps, err := q.Dependants(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			if d.Kind == api.KindVM && d.ID != r.ID {
				msg := fmt.Sprintf("The disk %s is attached to the VM %s. Detach it there first.", name, d.Name)
				return nil, &api.Error{Code: api.CodeVolumeAttached, Message: msg, Details: []api.FieldError{{Field: f, Code: api.FieldNotAllowed, Message: msg}}}
			}
		}
		refs = append(refs, store.Ref{ToID: v.ID, Field: f})
	}
	if vs.SSHKeySecret != "" {
		sec, err := live(ctx, q, project, api.KindSecret, vs.SSHKeySecret, "spec.sshKeySecret")
		if err != nil {
			return nil, err
		}
		if r.Outputs[engine.OutputPower] == nil { // keys are read once, at the first boot (VM-03)
			v, err := s.Box.Open(sec.SecretCiphertext, []byte(sec.ID))
			if err != nil {
				return nil, err
			}
			if errs := api.ValidateSSHKeys(api.SplitKeys(string(v)), "spec.sshKeySecret"); len(errs) > 0 {
				return nil, api.Validation([]api.FieldError{{Field: "spec.sshKeySecret", Code: api.FieldInvalidFormat,
					Message: fmt.Sprintf("The secret %s must hold SSH public keys, one per line: %s", vs.SSHKeySecret, errs[0].Message)}})
			}
		}
		refs = append(refs, store.Ref{ToID: sec.ID, Field: "spec.sshKeySecret"})
	}
	if name := api.SnapshotImage(vs.Image); name != "" && r.Outputs[engine.OutputPower] == nil {
		snap, err := readySnapshot(ctx, q, project, name, "spec.image")
		if err != nil {
			return nil, err
		}
		if snapshotType(snap) != api.VolumeDisk {
			return nil, api.Validation([]api.FieldError{{Field: "spec.image", Code: api.FieldNotAllowed,
				Message: fmt.Sprintf("The snapshot %s holds files, not a disk; a VM starts from a disk or VM snapshot.", name)}})
		}
		if size, ok := snap.Outputs["sizeGiB"].(float64); ok && float64(vs.DiskGiB) < size {
			return nil, api.Validation([]api.FieldError{{Field: "spec.diskGiB", Code: api.FieldOutOfRange,
				Message: fmt.Sprintf("The snapshot %s holds a %d GiB disk; make the root disk at least that big.", name, int(size))}})
		}
		refs = append(refs, store.Ref{ToID: snap.ID, Field: "spec.image"})
	}
	return dedupeRefs(refs), s.vmLimits(ctx, vs)
}

// vmLimits keeps a VM within the machine (VM-01): its CPUs and its memory minus 2 GiB. Without QEMU the
// limits are unknown, and the VM waits for it anyway (VM-11).
func (s *Resources) vmLimits(ctx context.Context, vs api.VMSpec) error {
	if s.Hypervisor == nil {
		return nil
	}
	sup := s.Hypervisor.Check(ctx)
	if !sup.Available || sup.CPUs == 0 || sup.MemoryTotalBytes == 0 {
		return nil
	}
	var errs []api.FieldError
	if vs.CPUs > sup.CPUs {
		errs = append(errs, api.FieldError{Field: "spec.cpus", Code: api.FieldOutOfRange, Message: fmt.Sprintf("This machine has %d CPUs.", sup.CPUs)})
	}
	if limit := int(sup.MemoryTotalBytes>>20) - api.VMMemoryHostReserveMiB; vs.MemoryMiB > limit {
		errs = append(errs, api.FieldError{Field: "spec.memoryMiB", Code: api.FieldOutOfRange,
			Message: fmt.Sprintf("This machine has %d MiB of memory, and keeps %d MiB for itself: use at most %d MiB.", sup.MemoryTotalBytes>>20, api.VMMemoryHostReserveMiB, limit)})
	}
	if len(errs) > 0 {
		return api.Validation(errs)
	}
	return nil
}

// power sets a VM's power through the normal update path, so the change is a new generation that every
// client sees (VM-06).
func (s *Resources) power(ctx context.Context, p Principal, id, action string) (api.Resource, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.Resource{}, notFound()
	}
	if err != nil {
		return api.Resource{}, err
	}
	if r.Kind != api.KindVM {
		return api.Resource{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("Only a VM can be %sed; a %s can't.", action, r.Kind))
	}
	power := api.PowerRunning
	if action == api.ActionStop {
		power = api.PowerStopped
	}
	patch, _ := json.Marshal(map[string]string{"power": power})
	out, _, err := s.Update(ctx, p, id, api.UpdateResourceRequest{Spec: patch}, nil)
	return out, err
}

// vmFacts adds whether VMs can run, the cached images and the space snapshots hold (L3-002 §8.2).
func (s *Resources) vmFacts(ctx context.Context, m *api.Machine) {
	m.Images = []api.CachedImage{}
	m.VMs = api.VMSupport{Message: "This server runs without a hypervisor."}
	if s.Hypervisor != nil {
		m.VMs = s.Hypervisor.Check(ctx).VMSupport
		if imgs := s.Hypervisor.CachedImages(ctx); imgs != nil {
			m.Images = imgs
		}
	}
	if s.SnapshotDir == "" {
		return
	}
	entries, _ := os.ReadDir(s.SnapshotDir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			m.SnapshotBytes += info.Size()
		}
	}
}
