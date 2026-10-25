package reconciler_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// VMs (ADR-025, ADR-026, L2-007 VM-*, VMN-*, IMG-04) through the real API on the fake engine and the fake
// hypervisor.

const vmKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave"

func newVM(t *testing.T, c *client.Client, name string, spec api.VMSpec) api.Resource {
	t.Helper()
	if spec.Image == "" {
		spec.Image = api.ImageDebian12
	}
	if len(spec.SSHKeys) == 0 && spec.SSHKeySecret == "" {
		spec.SSHKeys = []string{vmKey}
	}
	res, err := c.CreateResource(context.Background(), api.KindVM, name, spec)
	if err != nil {
		t.Fatalf("create VM %s: %v", name, err)
	}
	return waitReadyWithin(t, c, res.ID, 10*time.Second)
}

func TestVMsRunPowerAndRestart(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DriftEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	m, err := c.Machine(ctx)
	if err != nil || !m.VMs.Available || m.Images == nil {
		t.Fatalf("machine %+v %v", m.VMs, err)
	}
	vm := newVM(t, c, "worker", api.VMSpec{CPUs: 2, MemoryMiB: 2048, UserData: "#!/bin/sh\necho hi\n"})
	o := vm.Outputs
	if o["power"] != api.PowerRunning || o["sshUser"] != "enclave" || o["privateDns"] != "worker.vm.internal" ||
		!strings.HasPrefix(o["sshAddress"].(string), "127.0.0.1:") || o["privateIp"] == nil || len(o["imageSha256"].(string)) != 64 {
		t.Fatalf("outputs %v", o)
	}
	hv := env.FakeHypervisor.VM(vm.ID)
	if hv == nil || !hv.Running || hv.SSHKeys[0] != vmKey || hv.Image.Catalog != api.ImageDebian12 || hv.Link.Port != int(o["linkPort"].(float64)) {
		t.Fatalf("hypervisor %+v", hv)
	}
	if m, _ := c.Machine(ctx); m.ReservedCPUs != 2 || m.MemoryReservedBytes < 2048<<20 {
		t.Errorf("reserved %d CPUs, %d bytes", m.ReservedCPUs, m.MemoryReservedBytes)
	}
	if lines, err := c.Logs(ctx, vm.ID, 10); err != nil || len(lines) == 0 || !strings.Contains(lines[0].Message, "login:") {
		t.Errorf("boot console %v %v", lines, err)
	}

	// Limits, immutable fields.
	for name, spec := range map[string]api.VMSpec{"cpus": {CPUs: 9}, "memory": {MemoryMiB: 15360}} {
		spec.Image, spec.SSHKeys = api.ImageDebian12, []string{vmKey}
		if _, err := c.CreateResource(ctx, api.KindVM, "too-"+name, spec); apiCode(err) != api.CodeValidationFailed {
			t.Errorf("%s over the machine: %v", name, err)
		}
	}
	for field, value := range map[string]any{"sshKeys": []string{vmKey, vmKey}, "image": api.ImageUbuntu2404, "userData": "#!/bin/sh\n"} {
		if _, err := c.UpdateResource(ctx, vm.ID, map[string]any{field: value}, nil); apiCode(err) != api.CodeImmutableField {
			t.Errorf("changing %s: %v", field, err)
		}
	}

	// Stop, start, restart, and a change that restarts it.
	if _, err := c.Action(ctx, vm.ID, api.ActionStop); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, _ := c.GetResource(ctx, vm.ID)
		return got.Outputs["power"] == api.PowerStopped && got.Status.Phase == api.PhaseReady
	})
	if env.FakeHypervisor.VM(vm.ID).Running {
		t.Error("stopped, yet running")
	}
	if m, _ := c.Machine(ctx); m.ReservedCPUs != 0 {
		t.Errorf("a stopped VM reserves %d CPUs", m.ReservedCPUs)
	}
	if _, err := c.Action(ctx, vm.ID, api.ActionStart); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, _ := c.GetResource(ctx, vm.ID)
		return got.Outputs["power"] == api.PowerRunning && got.Status.Phase == api.PhaseReady
	})
	if _, err := c.Action(ctx, vm.ID, api.ActionRestart); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return slices.Contains(env.FakeHypervisor.VM(vm.ID).Reasons, "restart") })
	// The restart must finish first: a change made while it runs is applied by that same restart.
	waitReadyWithin(t, c, vm.ID, 10*time.Second)
	if _, err := c.UpdateResource(ctx, vm.ID, map[string]any{"cpus": 4}, nil); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, vm.ID, 10*time.Second)
	if r := env.FakeHypervisor.VM(vm.ID).Reasons; r[len(r)-1] != "config" {
		t.Errorf("start reasons %v", r)
	}
	if _, err := c.Action(ctx, vm.ID, api.ActionRedeploy); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("redeploying a VM: %v", err)
	}

	// A guest that powers itself off stays off and says so (VM-07); a crash is started again (CP-10).
	before, _ := c.GetResource(ctx, vm.ID)
	env.FakeHypervisor.GuestPowerOff(vm.ID)
	waitFor(t, 10*time.Second, func() bool {
		got, _ := c.GetResource(ctx, vm.ID)
		return got.Outputs["power"] == api.PowerStopped && got.Generation == before.Generation+1 && got.Status.Phase == api.PhaseReady
	})
	if !hasEvent(t, c, vm.ID, "guest_shutdown") || env.FakeHypervisor.VM(vm.ID).Running {
		t.Error("a guest shutdown wasn't recorded, or the VM was started again")
	}
	if _, err := c.Action(ctx, vm.ID, api.ActionStart); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return env.FakeHypervisor.VM(vm.ID).Running })
	starts := env.FakeHypervisor.VM(vm.ID).Starts
	env.FakeHypervisor.Crash(vm.ID)
	waitFor(t, 10*time.Second, func() bool {
		v := env.FakeHypervisor.VM(vm.ID)
		return v.Running && v.Starts == starts+1
	})

	// A link removed outside EnclaveStack comes back on the same ports, without restarting the VM.
	waitReadyWithin(t, c, vm.ID, 10*time.Second)
	ports, _ := c.GetResource(ctx, vm.ID)
	starts = env.FakeHypervisor.VM(vm.ID).Starts
	env.Fake.Kill(vm.ID)
	waitFor(t, 10*time.Second, func() bool { return env.Fake.HasLink(vm.ID) })
	after := waitReadyWithin(t, c, vm.ID, 10*time.Second)
	if after.Outputs["linkPort"] != ports.Outputs["linkPort"] || after.Outputs["sshAddress"] != ports.Outputs["sshAddress"] || env.FakeHypervisor.VM(vm.ID).Starts != starts {
		t.Errorf("link restored on %v / %v (was %v / %v), starts %d → %d", after.Outputs["linkPort"], after.Outputs["sshAddress"],
			ports.Outputs["linkPort"], ports.Outputs["sshAddress"], starts, env.FakeHypervisor.VM(vm.ID).Starts)
	}

	// Delete: the VM and its link go.
	if _, err := c.DeleteResource(ctx, vm.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, vm.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if env.FakeHypervisor.VM(vm.ID) != nil || env.Fake.HasLink(vm.ID) {
		t.Error("the VM or its link outlived the resource")
	}
}

func TestVMDisksKeysSnapshotsAndFirewalls(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()

	disk, _ := c.CreateResource(ctx, api.KindVolume, "data", api.VolumeSpec{Type: api.VolumeDisk, SizeGiB: 20})
	waitReadyWithin(t, c, disk.ID, 10*time.Second)
	fs, _ := c.CreateResource(ctx, api.KindVolume, "files", api.VolumeSpec{Type: api.VolumeFilesystem})
	waitReadyWithin(t, c, fs.ID, 10*time.Second)

	vm := newVM(t, c, "worker", api.VMSpec{Disks: []string{"data"}})
	if d := env.FakeHypervisor.VM(vm.ID).Disks; len(d) != 1 || d[0] != (engine.DiskAttachment{DiskID: disk.ID, Serial: "data"}) {
		t.Fatalf("disks %+v", d)
	}
	// One VM per disk; only disks; attached disks stay.
	if _, err := c.CreateResource(ctx, api.KindVM, "other", api.VMSpec{Image: api.ImageDebian12, SSHKeys: []string{vmKey}, Disks: []string{"data"}}); apiCode(err) != api.CodeVolumeAttached {
		t.Errorf("a second VM on the disk: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindVM, "fsvm", api.VMSpec{Image: api.ImageDebian12, SSHKeys: []string{vmKey}, Disks: []string{"files"}}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a filesystem as a VM disk: %v", err)
	}
	if _, err := c.DeleteResource(ctx, disk.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting an attached disk: %v", err)
	}

	// Keys from a secret: checked when the VM is created.
	bad, _ := c.CreateResource(ctx, api.KindSecret, "not-keys", api.SecretSpec{Value: "hunter2"})
	waitReadyWithin(t, c, bad.ID, 10*time.Second)
	if _, err := c.CreateResource(ctx, api.KindVM, "badkeys", api.VMSpec{Image: api.ImageDebian12, SSHKeySecret: "not-keys"}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a secret without keys: %v", err)
	}
	good, _ := c.CreateResource(ctx, api.KindSecret, "team-keys", api.SecretSpec{Value: "# ops\n" + vmKey + "\n"})
	waitReadyWithin(t, c, good.ID, 10*time.Second)
	keyed := newVM(t, c, "keyed", api.VMSpec{SSHKeySecret: "team-keys"})
	if k := env.FakeHypervisor.VM(keyed.ID).SSHKeys; len(k) != 1 || k[0] != vmKey {
		t.Errorf("keys from the secret %v", k)
	}

	// A VM from a snapshot of another (IMG-04); the snapshot may go afterwards (SNP-06).
	env.FakeHypervisor.WriteRoot(vm.ID, "worker's files")
	snap, _ := c.CreateResource(ctx, api.KindSnapshot, "worker-1", api.SnapshotSpec{Source: "vm/worker"})
	s := waitReadyWithin(t, c, snap.ID, 10*time.Second)
	if s.Outputs["type"] != api.VolumeDisk || s.Outputs["inUse"] != true {
		t.Errorf("VM snapshot outputs %v", s.Outputs)
	}
	clone := newVM(t, c, "clone", api.VMSpec{Image: "snapshot/worker-1"})
	if v := env.FakeHypervisor.VM(clone.ID); v.Root != "worker's files" || v.Image.SnapshotFile == "" {
		t.Errorf("clone %+v", v)
	}
	if _, err := c.DeleteResource(ctx, snap.ID, nil); err != nil {
		t.Errorf("deleting the snapshot a VM came from: %v", err)
	}
	if _, err := c.UpdateResource(ctx, clone.ID, map[string]any{"cpus": 2}, nil); err != nil {
		t.Errorf("updating a VM whose snapshot went: %v", err)
	}
	if _, err := c.CreateResource(ctx, api.KindVM, "from-files", api.VMSpec{Image: "snapshot/nothing", SSHKeys: []string{vmKey}}); apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a VM from a missing snapshot: %v", err)
	}

	// Firewalls target VMs and name them as sources (VMN-03).
	fw, err := c.CreateResource(ctx, api.KindFirewall, "vm-access", api.FirewallSpec{Targets: []string{"vm/worker"},
		Ingress: []api.FirewallRule{{From: []string{"vm/keyed"}, Protocol: api.ProtocolTCP, Ports: []string{"9000"}}}})
	if err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, fw.ID, 10*time.Second)
	if p, _ := env.Fake.FirewallOf(vm.ID); p == nil || len(p.Ingress) != 1 || len(p.Ingress[0].Peers) != 1 {
		t.Errorf("VM firewall policy %+v", p)
	}
	if _, err := c.DeleteResource(ctx, fw.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, fw.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Deleting the VM keeps its disk (VM-12), which can then go.
	if _, err := c.DeleteResource(ctx, vm.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitGone(ctx, vm.ID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := env.FakeHypervisor.Disk(disk.ID); !ok {
		t.Fatal("the VM took its data disk with it")
	}
	if _, err := c.DeleteResource(ctx, disk.ID, nil); err != nil {
		t.Errorf("deleting a detached disk: %v", err)
	}
}

func TestVMsWaitForQEMU(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	env.FakeHypervisor.SetSupport(false, "QEMU isn't installed. Install QEMU: brew install qemu")
	if m, _ := c.Machine(ctx); m.VMs.Available || !strings.Contains(m.VMs.Message, "brew install qemu") {
		t.Errorf("machine VMs %+v", m.VMs)
	}
	vm, err := c.CreateResource(ctx, api.KindVM, "waiting", api.VMSpec{Image: api.ImageDebian12, SSHKeys: []string{vmKey}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		got, _ := c.GetResource(ctx, vm.ID)
		return strings.Contains(got.Status.Message, "brew install qemu")
	})
	if got, _ := c.GetResource(ctx, vm.ID); got.Status.Phase == api.PhaseFailed || got.Status.Attempts != 0 {
		t.Errorf("a missing QEMU must neither fail the VM nor spend its attempts: %+v", got.Status)
	}
	env.FakeHypervisor.SetSupport(true, "")
	if _, err := c.Action(ctx, vm.ID, api.ActionReconcile); err != nil {
		t.Fatal(err)
	}
	waitReadyWithin(t, c, vm.ID, 10*time.Second)
}
