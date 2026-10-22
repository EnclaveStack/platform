package engine

import (
	"context"
	"errors"

	"github.com/enclavestack/platform/pkg/api"
)

// Hypervisor runs VMs and owns their disk files (ADR-025, ADR-027, ADR-028). Its one implementation is
// internal/engine/qemu; tests use fake.Hypervisor. Like Driver, every method is idempotent, and only the
// reconciler calls the ones that change state (internal/archtest).
type Hypervisor interface {
	// Check reports whether VMs can run here and the host's CPUs and memory. It never fails: a missing
	// piece is the reason in Support.Message (VM-11).
	Check(ctx context.Context) VMSupport

	// EnsureVM converges one VM: its root disk and seed exist, its process runs or not as Spec.Power
	// says, and a running process has the configuration asked for (it is restarted when CPUs, memory,
	// disks or the link changed, or when Restart is set).
	EnsureVM(ctx context.Context, in VMInput, report Reporter) (VMState, error)
	// StopVM asks the guest to shut down and forces it off after the grace period (VM-06).
	StopVM(ctx context.Context, vmID string) error
	// DeleteVM stops a VM and removes its directory: root disk, seed, console log (VM-12).
	DeleteVM(ctx context.Context, vmID string) error
	// VMStatus reports what the VM's process is doing.
	VMStatus(ctx context.Context, vmID string) (VMStatus, error)
	// ConsoleLog returns the last lines of the VM's serial console (VM-09).
	ConsoleLog(ctx context.Context, vmID string, tail int) ([]api.LogLine, error)

	// EnsureDisk creates a disk volume's file of at least sizeGiB, growing it (live, when a running VM
	// has it attached) when it is smaller (VOL-01, VOL-03). DeleteDisk removes it.
	EnsureDisk(ctx context.Context, d DiskRef, sizeGiB int) error
	DeleteDisk(ctx context.Context, diskID string) error
	// SnapshotDisk copies a disk volume or a VM's root disk into dest as a flat qcow2 file, live when its
	// VM runs (SNP-01, SNP-05). It returns the copy's virtual size in bytes.
	SnapshotDisk(ctx context.Context, d DiskRef, dest string) (int64, error)
	// CopyDisk fills a new disk volume from a snapshot file, grown to sizeGiB (VOL-08).
	CopyDisk(ctx context.Context, src string, diskID string, sizeGiB int) error

	// CachedImages lists downloaded images and how many VMs use each (IMG-05).
	CachedImages(ctx context.Context) []api.CachedImage
	// VMStats reads a running VM process's cumulative CPU time and resident memory (ADR-037);
	// ok is false when it doesn't run.
	VMStats(ctx context.Context, vmID string) (stats ProcessStats, ok bool, err error)
}

// ProcessStats are a VM process's counters.
type ProcessStats struct {
	CPUSeconds  float64
	MemoryBytes int64
	PID         int
}

// ErrNoVMs means VMs can't run on this machine; the VM stays pending with Support.Message (VM-11).
var ErrNoVMs = errors.New("VMs can't run on this machine")

// VMSupport is what Check found. Host figures are the machine's own, not the container engine's.
type VMSupport struct {
	api.VMSupport
	Arch             string // "arm64" or "amd64"
	CPUs             int
	MemoryTotalBytes int64
}

// VMInput is everything the hypervisor needs to converge one VM. Secret material (the keys from a key
// secret) is resolved by the reconciler.
type VMInput struct {
	ID      string
	Name    string
	Spec    api.VMSpec
	SSHKeys []string
	// Image is where the root disk comes from.
	Image ImageSource
	// Disks are the data disks in attach order.
	Disks []DiskAttachment
	// Link is where the VM's NIC streams its frames (ADR-026): the link container's loopback port.
	Link VMLink
	// Restart forces a running VM to restart (the restart action).
	Restart bool
}

// ImageSource is exactly one of a catalog name, a URL (with an optional checksum) or a snapshot file.
type ImageSource struct {
	Catalog      string
	URL          string
	SHA256       string
	SnapshotFile string
}

// DiskAttachment is one data disk of a VM.
type DiskAttachment struct {
	DiskID string
	Serial string // the name the guest sees: /dev/disk/by-id/virtio-<Serial>
}

// VMLink is the loopback address of a VM's link container.
type VMLink struct {
	Host string
	Port int
}

// DiskRef names a disk: a VM's root disk (VMID only), or a disk volume (DiskID), with the running VM
// that has it attached, if any, so copies and resizes go through that VM's QEMU.
type DiskRef struct {
	VMID     string
	DiskID   string
	Attached string
}

// VMState is what EnsureVM left behind.
type VMState struct {
	Running     bool
	ImageSHA256 string
	// Started is set when EnsureVM started or restarted the process; Reason says why ("create",
	// "start", "restart", "config", "crash").
	Started bool
	Reason  string
}

// VMStatus is a VM process's observed state.
type VMStatus int

const (
	// VMMissing means no process runs for the VM (never started, stopped, or crashed).
	VMMissing VMStatus = iota
	// VMRunning means the guest runs.
	VMRunning
	// VMGuestShutdown means the guest powered itself off; QEMU waits (-no-shutdown) for the control
	// plane to record it and quit (VM-07).
	VMGuestShutdown
)
