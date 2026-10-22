package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Hypervisor is an in-memory engine.Hypervisor (ADR-025). Disks hold a string of "contents", and
// snapshot files are real files holding JSON, so the reconciler's copy paths run end to end. Tests drive
// guest shutdowns, crashes and a machine without QEMU.
type Hypervisor struct {
	mu      sync.Mutex
	support engine.VMSupport
	vms     map[string]*VM
	disks   map[string]*disk
	images  map[string]int // image key → VMs using it
}

// VM is the fake's view of one VM.
type VM struct {
	Spec          api.VMSpec
	SSHKeys       []string
	Image         engine.ImageSource
	Disks         []engine.DiskAttachment
	Link          engine.VMLink
	Running       bool
	GuestShutdown bool
	Starts        int
	Reasons       []string
	RootGiB       int
	Root          string // the root disk's contents
	config        string
}

type disk struct {
	sizeGiB  int
	contents string
}

type snapshotFile struct {
	SizeGiB  int    `json:"sizeGiB"`
	Contents string `json:"contents"`
}

// NewHypervisor returns a fake that can run VMs on an 8-CPU, 16 GiB arm64 machine.
func NewHypervisor() *Hypervisor {
	return &Hypervisor{vms: map[string]*VM{}, disks: map[string]*disk{}, images: map[string]int{},
		support: engine.VMSupport{VMSupport: api.VMSupport{Available: true, Message: "VMs can run (fake hypervisor).", QEMUVersion: "fake", Accelerator: "hvf"},
			Arch: "arm64", CPUs: 8, MemoryTotalBytes: 16 << 30}}
}

// SetSupport simulates QEMU being missing (available false, with the reason) or installed.
func (h *Hypervisor) SetSupport(available bool, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.support.Available, h.support.Message = available, message
}

// VM returns a copy of a VM's state, or nil.
func (h *Hypervisor) VM(id string) *VM {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.vms[id]; v != nil {
		c := *v
		c.Reasons = slices.Clone(v.Reasons)
		return &c
	}
	return nil
}

// GuestPowerOff simulates `poweroff` inside the guest (VM-07).
func (h *Hypervisor) GuestPowerOff(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.vms[id]; v != nil && v.Running {
		v.Running, v.GuestShutdown = false, true
	}
}

// Crash simulates the QEMU process dying (CP-10).
func (h *Hypervisor) Crash(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.vms[id]; v != nil {
		v.Running, v.GuestShutdown = false, false
	}
}

// WriteRoot and WriteDisk simulate the guest writing to its disks.
func (h *Hypervisor) WriteRoot(id, contents string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.vms[id]; v != nil {
		v.Root = contents
	}
}

func (h *Hypervisor) WriteDisk(id, contents string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d := h.disks[id]; d != nil {
		d.contents = contents
	}
}

// Disk returns a disk volume's size and contents; ok is false when it doesn't exist.
func (h *Hypervisor) Disk(id string) (sizeGiB int, contents string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d := h.disks[id]; d != nil {
		return d.sizeGiB, d.contents, true
	}
	return 0, "", false
}

// Check implements engine.Hypervisor.
func (h *Hypervisor) Check(context.Context) engine.VMSupport {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.support
}

func imageKey(s engine.ImageSource) string {
	switch {
	case s.Catalog != "":
		return s.Catalog
	case s.URL != "":
		return s.URL
	}
	return ""
}

// EnsureVM implements engine.Hypervisor.
func (h *Hypervisor) EnsureVM(ctx context.Context, in engine.VMInput, report engine.Reporter) (engine.VMState, error) {
	if s := h.Check(ctx); !s.Available {
		return engine.VMState{}, fmt.Errorf("%w: %s", engine.ErrNoVMs, s.Message)
	}
	var root string
	if in.Image.SnapshotFile != "" {
		snap, err := readSnapshotFile(in.Image.SnapshotFile)
		if err != nil {
			return engine.VMState{}, err
		}
		root = snap.Contents
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.vms[in.ID]
	report.Step("image", api.StepDone)
	if v == nil {
		v = &VM{Image: in.Image, SSHKeys: slices.Clone(in.SSHKeys), RootGiB: in.Spec.DiskGiB, Root: root}
		h.vms[in.ID] = v
		if k := imageKey(in.Image); k != "" {
			h.images[k]++
		}
	}
	v.RootGiB = max(v.RootGiB, in.Spec.DiskGiB)
	report.Step("disk", api.StepDone)
	for _, d := range in.Disks {
		if h.disks[d.DiskID] == nil {
			return engine.VMState{}, fmt.Errorf("the disk %s doesn't exist", d.DiskID)
		}
	}
	v.Spec, v.Disks, v.Link = in.Spec, slices.Clone(in.Disks), in.Link
	report.Step("network", api.StepDone)
	config := fmt.Sprint(in.Spec.CPUs, in.Spec.MemoryMiB, in.Disks, in.Link)
	state := engine.VMState{ImageSHA256: strings.Repeat("f", 64)}
	switch {
	case in.Spec.Power == api.PowerStopped:
		v.Running, v.GuestShutdown = false, false
	case !v.Running || in.Restart || v.config != config:
		reason := "start"
		switch {
		case v.Starts == 0:
			reason = "create"
		case v.Running && in.Restart:
			reason = "restart"
		case v.Running:
			reason = "config"
		}
		v.Running, v.GuestShutdown, v.config = true, false, config
		v.Starts++
		v.Reasons = append(v.Reasons, reason)
		state.Started, state.Reason = true, reason
	}
	state.Running = v.Running
	report.Step("boot", api.StepDone)
	return state, nil
}

// StopVM implements engine.Hypervisor.
func (h *Hypervisor) StopVM(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.vms[id]; v != nil {
		v.Running, v.GuestShutdown = false, false
	}
	return nil
}

// DeleteVM implements engine.Hypervisor.
func (h *Hypervisor) DeleteVM(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.vms[id]; v != nil {
		if k := imageKey(v.Image); k != "" {
			h.images[k]--
		}
	}
	delete(h.vms, id)
	return nil
}

// VMStatus implements engine.Hypervisor.
func (h *Hypervisor) VMStatus(_ context.Context, id string) (engine.VMStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.vms[id]
	switch {
	case v == nil:
		return engine.VMMissing, nil
	case v.GuestShutdown:
		return engine.VMGuestShutdown, nil
	case v.Running:
		return engine.VMRunning, nil
	}
	return engine.VMMissing, nil
}

// ConsoleLog implements engine.Hypervisor.
func (h *Hypervisor) ConsoleLog(_ context.Context, id string, tail int) ([]api.LogLine, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.vms[id]
	if v == nil {
		return nil, nil
	}
	var lines []api.LogLine
	for i := range v.Starts {
		lines = append(lines, api.LogLine{Time: time.Now(), Stream: "stdout", Message: fmt.Sprintf("boot %d: login:", i+1)})
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

// EnsureDisk implements engine.Hypervisor.
func (h *Hypervisor) EnsureDisk(_ context.Context, d engine.DiskRef, sizeGiB int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur := h.disks[d.DiskID]; cur != nil {
		cur.sizeGiB = max(cur.sizeGiB, sizeGiB)
		return nil
	}
	h.disks[d.DiskID] = &disk{sizeGiB: sizeGiB}
	return nil
}

// DeleteDisk implements engine.Hypervisor.
func (h *Hypervisor) DeleteDisk(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.disks, id)
	return nil
}

// SnapshotDisk implements engine.Hypervisor.
func (h *Hypervisor) SnapshotDisk(_ context.Context, d engine.DiskRef, dest string) (int64, error) {
	h.mu.Lock()
	var snap snapshotFile
	switch {
	case d.DiskID != "":
		disk := h.disks[d.DiskID]
		if disk == nil {
			h.mu.Unlock()
			return 0, fmt.Errorf("the disk %s doesn't exist", d.DiskID)
		}
		snap = snapshotFile{SizeGiB: disk.sizeGiB, Contents: disk.contents}
	default:
		v := h.vms[d.VMID]
		if v == nil {
			h.mu.Unlock()
			return 0, fmt.Errorf("the VM %s doesn't exist", d.VMID)
		}
		snap = snapshotFile{SizeGiB: v.RootGiB, Contents: v.Root}
	}
	h.mu.Unlock()
	b, _ := json.Marshal(snap)
	if err := os.WriteFile(dest, b, 0o600); err != nil {
		return 0, err
	}
	return int64(snap.SizeGiB) << 30, nil
}

// CopyDisk implements engine.Hypervisor.
func (h *Hypervisor) CopyDisk(_ context.Context, src, id string, sizeGiB int) error {
	snap, err := readSnapshotFile(src)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disks[id] = &disk{sizeGiB: max(sizeGiB, snap.SizeGiB), contents: snap.Contents}
	return nil
}

// CachedImages implements engine.Hypervisor.
func (h *Hypervisor) CachedImages(context.Context) []api.CachedImage {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []api.CachedImage
	for k, n := range h.images {
		out = append(out, api.CachedImage{Name: k, Arch: h.support.Arch, SizeBytes: 300 << 20, UsedBy: n})
	}
	slices.SortFunc(out, func(a, b api.CachedImage) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func readSnapshotFile(path string) (snapshotFile, error) {
	var snap snapshotFile
	b, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	return snap, json.Unmarshal(b, &snap)
}
