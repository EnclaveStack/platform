//go:build qemu

package qemu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Real QEMU (L5-002 §8, make engine-qemu): boot from the catalog's Debian image with HVF or KVM, run the
// first-boot script, adopt after a control-plane restart, resize and snapshot live, tell a guest
// shutdown from a crash, stop. Skipped with the reason when this machine can't run VMs.

type steps struct{}

func (steps) Step(string, api.StepState) {}

// testImage serves the catalog's Debian image from ~/.cache/enclave-test, downloading it once.
func testImage(t *testing.T) Catalog {
	t.Helper()
	img := BuiltinCatalog[api.ImageDebian12][runtime.GOARCH]
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".cache", "enclave-test", "debian-12-"+runtime.GOARCH+".qcow2")
	if _, err := os.Stat(cache); err != nil {
		if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Logf("downloading %s once into %s", img.URL, cache)
		resp, err := http.Get(img.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		f, err := os.Create(cache + ".partial")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err := os.Rename(cache+".partial", cache); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, cache) }))
	t.Cleanup(srv.Close)
	img.URL = srv.URL + "/debian-12.qcow2"
	return Catalog{api.ImageDebian12: {runtime.GOARCH: img}}
}

func serialDevice() string {
	if runtime.GOARCH == "arm64" {
		return "/dev/ttyAMA0"
	}
	return "/dev/ttyS0"
}

func waitConsole(t *testing.T, h *Hypervisor, id, marker string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		lines, _ := h.ConsoleLog(context.Background(), id, 0)
		for _, l := range lines {
			if strings.Contains(l.Message, marker) {
				return l.Message
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	lines, _ := h.ConsoleLog(context.Background(), id, 30)
	var tail []string
	for _, l := range lines {
		tail = append(tail, l.Message)
	}
	t.Fatalf("%q never appeared on the console; last lines:\n%s", marker, strings.Join(tail, "\n"))
	return ""
}

func TestRealVMLifecycle(t *testing.T) {
	dir := t.TempDir()
	h := New(Options{DataDir: dir, Catalog: testImage(t), StopWait: 45 * time.Second})
	ctx := context.Background()
	if s := h.Check(ctx); !s.Available {
		t.Skipf("VMs can't run here: %s", s.Message)
	}
	in := engine.VMInput{ID: "vm_realtest", Name: "real-test", SSHKeys: []string{testKey}, Image: engine.ImageSource{Catalog: api.ImageDebian12},
		Spec: api.VMSpec{CPUs: 1, MemoryMiB: 1024, DiskGiB: 1, Power: api.PowerRunning,
			UserData: "#!/bin/sh\necho \"HC-FIRST-BOOT host=$(hostname) user=$(id -un enclave) sudo=$(sudo -n -u enclave sudo -n true && echo yes)\" > " + serialDevice() + "\n"}}
	t.Cleanup(func() { _ = h.DeleteVM(context.Background(), in.ID) })

	// A root disk smaller than the image is refused permanently.
	if _, err := h.EnsureVM(ctx, in, steps{}); err == nil || !engine.IsPermanent(err) {
		t.Fatalf("a 1 GiB root disk for a larger image: %v", err)
	}
	in.Spec.DiskGiB = 10
	started := time.Now()
	st, err := h.EnsureVM(ctx, in, steps{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || !st.Started || st.Reason != "create" || len(st.ImageSHA256) != 64 {
		t.Fatalf("first ensure %+v", st)
	}
	line := waitConsole(t, h, in.ID, "HC-FIRST-BOOT", 3*time.Minute)
	t.Logf("first boot after %s: %s", time.Since(started).Round(time.Second), line)
	if !strings.Contains(line, "host=real-test") || !strings.Contains(line, "user=enclave") || !strings.Contains(line, "sudo=yes") {
		t.Errorf("first boot: %s", line)
	}
	if imgs := h.CachedImages(ctx); len(imgs) != 1 || imgs[0].UsedBy != 1 {
		t.Errorf("cached images %+v", imgs)
	}

	// Unchanged: nothing restarts. A new hypervisor (a control-plane restart) adopts the same process.
	pid := h.pid(h.paths(in.ID))
	if st, err := h.EnsureVM(ctx, in, steps{}); err != nil || st.Started {
		t.Fatalf("second ensure %+v %v", st, err)
	}
	h2 := New(Options{DataDir: dir, Catalog: h.opts.Catalog, StopWait: 45 * time.Second})
	if st, err := h2.EnsureVM(ctx, in, steps{}); err != nil || st.Started || h2.pid(h2.paths(in.ID)) != pid {
		t.Fatalf("adoption %+v %v (pid %d → %d)", st, err, pid, h2.pid(h2.paths(in.ID)))
	}

	// The root disk grows live.
	in.Spec.DiskGiB = 12
	if st, err := h.EnsureVM(ctx, in, steps{}); err != nil || st.Started {
		t.Fatalf("grow %+v %v", st, err)
	}
	if info, _ := diskInfo(ctx, h.tools, h.paths(in.ID).root); info.VirtualSize != 12*gib {
		t.Errorf("root disk %d bytes after growing", info.VirtualSize)
	}

	// A live snapshot, without stopping the guest.
	dest := filepath.Join(dir, "snap.qcow2")
	t0 := time.Now()
	size, err := h.SnapshotDisk(ctx, engine.DiskRef{VMID: in.ID}, dest)
	if err != nil || size != 12*gib {
		t.Fatalf("snapshot %d %v", size, err)
	}
	t.Logf("live snapshot of the root disk in %s", time.Since(t0).Round(time.Millisecond))
	if s, _ := h.VMStatus(ctx, in.ID); s != engine.VMRunning {
		t.Errorf("status after the snapshot %v", s)
	}

	// A guest that powers itself off is told apart from a crash (VM-07).
	if err := qmpRun(ctx, h.paths(in.ID).sock, "system_powerdown", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		if s, _ := h.VMStatus(ctx, in.ID); s == engine.VMGuestShutdown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the guest never reported its shutdown")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := h.StopVM(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.VMStatus(ctx, in.ID); s != engine.VMMissing {
		t.Errorf("status after stop %v", s)
	}

	// Start again (the first-boot script doesn't run twice), then crash it.
	st, err = h.EnsureVM(ctx, in, steps{})
	if err != nil || st.Reason != "start" {
		t.Fatalf("restart after stop %+v %v", st, err)
	}
	waitConsole(t, h, in.ID, "login:", 3*time.Minute)
	lines, _ := h.ConsoleLog(ctx, in.ID, 0)
	for _, l := range lines {
		if strings.Contains(l.Message, "HC-FIRST-BOOT") {
			t.Error("the first-boot script ran again")
		}
	}
	if err := syscall.Kill(h.pid(h.paths(in.ID)), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if s, _ := h.VMStatus(ctx, in.ID); s != engine.VMMissing {
		t.Errorf("status after a crash %v", s)
	}

	// Power stopped keeps it off; delete removes everything.
	in.Spec.Power = api.PowerStopped
	if st, err := h.EnsureVM(ctx, in, steps{}); err != nil || st.Running {
		t.Fatalf("stopped %+v %v", st, err)
	}
	if err := h.DeleteVM(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.paths(in.ID).dir); !os.IsNotExist(err) {
		t.Errorf("VM directory left: %v", err)
	}
}

func TestRealDiskVolumes(t *testing.T) {
	dir := t.TempDir()
	h := New(Options{DataDir: dir})
	ctx := context.Background()
	if s := h.Check(ctx); !s.Available {
		t.Skipf("VMs can't run here: %s", s.Message)
	}
	if err := h.EnsureDisk(ctx, engine.DiskRef{DiskID: "vol_a"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := h.EnsureDisk(ctx, engine.DiskRef{DiskID: "vol_a"}, 3); err != nil {
		t.Fatal(err)
	}
	if info, _ := diskInfo(ctx, h.tools, h.diskPath("vol_a")); info.VirtualSize != 3*gib || info.Format != "qcow2" {
		t.Errorf("disk %+v", info)
	}
	snap := filepath.Join(dir, "a.qcow2")
	if size, err := h.SnapshotDisk(ctx, engine.DiskRef{DiskID: "vol_a"}, snap); err != nil || size != 3*gib {
		t.Fatalf("snapshot %d %v", size, err)
	}
	if err := h.CopyDisk(ctx, snap, "vol_b", 5); err != nil {
		t.Fatal(err)
	}
	if info, _ := diskInfo(ctx, h.tools, h.diskPath("vol_b")); info.VirtualSize != 5*gib {
		t.Errorf("copy %+v", info)
	}
	if err := h.DeleteDisk(ctx, "vol_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.diskPath("vol_a")); !os.IsNotExist(err) {
		t.Error("disk file left")
	}
}
