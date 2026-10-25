package qemu

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// VM processes (ADR-025): one daemonized QEMU per VM, in its own session so it outlives the control
// plane, with -no-shutdown so a guest that powers itself off is told apart from a crash.

const (
	startWait      = 60 * time.Second
	quitWait       = 10 * time.Second
	consoleMax     = 1 << 20
	maxSocketPath  = 100 // macOS limits UNIX socket paths to 104 bytes
	statusShutdown = "shutdown"
)

type vmPaths struct {
	dir, root, seed, console, pid, sock, config, image, sha string
}

func (h *Hypervisor) paths(id string) vmPaths {
	dir := filepath.Join(h.opts.DataDir, "vm", id)
	p := vmPaths{dir: dir, root: filepath.Join(dir, "root.qcow2"), seed: filepath.Join(dir, "seed.iso"), console: filepath.Join(dir, "console.log"),
		pid: filepath.Join(dir, "qemu.pid"), sock: filepath.Join(dir, "qmp.sock"), config: filepath.Join(dir, "config"),
		image: filepath.Join(dir, "image"), sha: filepath.Join(dir, "image.sha256")}
	if len(p.sock) > maxSocketPath {
		p.sock = filepath.Join(shortSocketDir(), id+".qmp")
	}
	return p
}

// shortSocketDir is the fallback for long data directories: /tmp/enclave-<uid> (ADR-025).
func shortSocketDir() string { return filepath.Join("/tmp", "enclave-"+strconv.Itoa(os.Getuid())) }

// ensureSocketDir makes sure the socket's directory is ours alone: owned by this user, mode 0700. A
// directory planted by someone else is refused (L3-006 §7).
func ensureSocketDir(sock string) error {
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode().Perm() != 0o700 || (ok && int(sys.Uid) != os.Getuid()) {
		return engine.Permanent(fmt.Errorf("%s must be a directory owned by this user with mode 0700; remove it and try again", dir))
	}
	return nil
}

// pid reads the pidfile; 0 when there is none.
func (h *Hypervisor) pid(p vmPaths) int {
	b, err := os.ReadFile(p.pid)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// alive reports whether the VM's QEMU runs: the pidfile names a live process whose command line is
// QEMU with this VM's directory (a recycled PID is not mistaken for it).
func (h *Hypervisor) alive(p vmPaths) bool {
	pid := h.pid(p)
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	return strings.Contains(commandLine(pid), p.dir)
}

func commandLine(pid int) string {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err == nil {
			return strings.ReplaceAll(string(b), "\x00", " ")
		}
	}
	out, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// escape doubles commas: QEMU's option syntax separates with them.
func escape(s string) string { return strings.ReplaceAll(s, ",", ",,") }

// configKey is what a running VM must match: changing it restarts the VM (VM-01, VOL-02).
func configKey(in engine.VMInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cpus=%d mem=%d link=%s:%d", in.Spec.CPUs, in.Spec.MemoryMiB, in.Link.Host, in.Link.Port)
	for _, d := range in.Disks {
		fmt.Fprintf(&b, " disk=%s:%s", d.DiskID, d.Serial)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// args is the QEMU command line for a VM.
func (h *Hypervisor) args(t tools, in engine.VMInput, p vmPaths) []string {
	machine := "q35"
	if runtime.GOARCH == "arm64" {
		machine = "virt"
	}
	a := []string{"-name", "es-vm-" + in.Name, "-machine", machine + ",accel=" + t.accel, "-cpu", "host",
		"-smp", strconv.Itoa(in.Spec.CPUs), "-m", strconv.Itoa(in.Spec.MemoryMiB) + "M"}
	if t.firmware != "" {
		a = append(a, "-bios", t.firmware)
	}
	a = append(a,
		"-drive", "if=none,id=root,format=qcow2,discard=unmap,file="+escape(p.root), "-device", "virtio-blk-pci,drive=root,bootindex=0",
		"-drive", "if=none,id=seed,format=raw,readonly=on,file="+escape(p.seed), "-device", "virtio-blk-pci,drive=seed")
	for _, d := range in.Disks {
		id := "disk-" + d.DiskID
		a = append(a, "-drive", "if=none,id="+id+",format=qcow2,discard=unmap,file="+escape(h.diskPath(d.DiskID)),
			"-device", "virtio-blk-pci,drive="+id+",serial="+escape(d.Serial))
	}
	if in.Link.Port > 0 {
		reconnect := "reconnect-ms=1000"
		if t.major < 9 || (t.major == 9 && t.minor < 2) {
			reconnect = "reconnect=1"
		}
		a = append(a, "-netdev", fmt.Sprintf("stream,id=net0,server=off,addr.type=inet,addr.host=%s,addr.port=%d,%s", in.Link.Host, in.Link.Port, reconnect),
			"-device", "virtio-net-pci,netdev=net0,mac="+MAC(in.ID))
	} else {
		a = append(a, "-nic", "none")
	}
	return append(a, "-device", "virtio-rng-pci", "-display", "none", "-serial", "file:"+escape(p.console),
		"-qmp", "unix:"+escape(p.sock)+",server=on,wait=off", "-no-shutdown", "-daemonize", "-pidfile", p.pid)
}

// EnsureVM implements engine.Hypervisor.
func (h *Hypervisor) EnsureVM(ctx context.Context, in engine.VMInput, report engine.Reporter) (engine.VMState, error) {
	t, err := h.ready(ctx)
	if err != nil {
		return engine.VMState{}, err
	}
	defer h.lock(in.ID)()
	p := h.paths(in.ID)
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return engine.VMState{}, err
	}

	report.Step("image", api.StepActive)
	if err := h.ensureRoot(ctx, t, in, p); err != nil {
		return engine.VMState{}, err
	}
	sha, _ := os.ReadFile(p.sha)
	state := engine.VMState{ImageSHA256: strings.TrimSpace(string(sha))}
	report.Step("image", api.StepDone)

	report.Step("disk", api.StepActive)
	running := h.alive(p)
	vmForResize := ""
	if running {
		vmForResize = in.ID
	}
	if err := h.grow(ctx, t, p.root, "root", vmForResize, in.Spec.DiskGiB); err != nil {
		return state, err
	}
	for _, d := range in.Disks {
		if _, err := os.Stat(h.diskPath(d.DiskID)); err != nil {
			return state, fmt.Errorf("the disk volume %s isn't ready yet", d.Serial)
		}
	}
	if _, err := os.Stat(p.seed); errors.Is(err, os.ErrNotExist) {
		files, err := seedFiles(in)
		if err != nil {
			return state, err
		}
		if err := writeSeed(p.seed, files); err != nil {
			return state, err
		}
	}
	report.Step("disk", api.StepDone)
	report.Step("network", api.StepDone) // the link container is the driver's (ADR-026)

	report.Step("boot", api.StepActive)
	if in.Spec.Power == api.PowerStopped {
		if running {
			if err := h.stopLocked(ctx, p); err != nil {
				return state, err
			}
		}
		report.Step("boot", api.StepDone)
		return state, nil
	}
	key := configKey(in)
	current, _ := os.ReadFile(p.config)
	if running && !in.Restart && string(current) == key {
		if st, err := qmpStatus(ctx, p.sock); err != nil || st != statusShutdown {
			state.Running = true
			report.Step("boot", api.StepDone)
			return state, nil
		}
	}
	state.Reason = "start"
	switch {
	case !fileExists(p.config):
		state.Reason = "create"
	case running && in.Restart:
		state.Reason = "restart"
	case running:
		state.Reason = "config"
	}
	if running {
		if err := h.stopLocked(ctx, p); err != nil {
			return state, err
		}
	}
	if err := h.start(ctx, t, in, p); err != nil {
		return state, err
	}
	if err := os.WriteFile(p.config, []byte(key), 0o600); err != nil {
		return state, err
	}
	state.Running, state.Started = true, true
	report.Step("boot", api.StepDone)
	return state, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ensureRoot creates the root disk once: an overlay on the cached image, or a full copy of a snapshot
// (VM-02, IMG-04).
func (h *Hypervisor) ensureRoot(ctx context.Context, t tools, in engine.VMInput, p vmPaths) error {
	if fileExists(p.root) {
		return nil
	}
	if in.Image.SnapshotFile != "" {
		if err := copyImage(ctx, t, in.Image.SnapshotFile, p.root, in.Spec.DiskGiB); err != nil {
			return err
		}
		return os.WriteFile(p.sha, nil, 0o600)
	}
	img, err := h.ensureImage(ctx, t, in.Image)
	if err != nil {
		return err
	}
	info, err := diskInfo(ctx, t, img.path)
	if err != nil {
		return err
	}
	if need := (info.VirtualSize + gib - 1) / gib; int64(in.Spec.DiskGiB) < need {
		return engine.Permanent(fmt.Errorf("the image needs a root disk of at least %d GiB; set diskGiB to %d or more", need, need))
	}
	partial := p.root + ".partial"
	_ = os.Remove(partial)
	if _, err := run(ctx, commandTimeout, t.img, "create", "-q", "-f", "qcow2", "-F", img.info.Format, "-b", img.path, partial, strconv.Itoa(in.Spec.DiskGiB)+"G"); err != nil {
		return err
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(p.image, []byte(imageKey(img.info.URL)), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(p.sha, []byte(img.info.SHA256), 0o600); err != nil {
		return err
	}
	return os.Rename(partial, p.root)
}

// start launches QEMU and waits until its monitor answers.
func (h *Hypervisor) start(ctx context.Context, t tools, in engine.VMInput, p vmPaths) error {
	if err := ensureSocketDir(p.sock); err != nil {
		return err
	}
	_ = os.Remove(p.sock)
	_ = os.Remove(p.pid)
	if st, err := os.Stat(p.console); err == nil && st.Size() > 0 {
		_ = os.Rename(p.console, p.console+".1")
	}
	cmd := exec.Command(t.system, h.args(t, in, p)...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	cmd.Dir = p.dir
	if err := cmd.Run(); err != nil { // -daemonize: returns once the VM is set up
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("QEMU didn't start: %s", msg)
	}
	deadline := time.Now().Add(startWait)
	for {
		if _, err := qmpStatus(ctx, p.sock); err == nil {
			h.log.Info("VM started", "vm", in.Name, "pid", h.pid(p))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("QEMU started but its monitor didn't answer within %s", startWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// StopVM implements engine.Hypervisor.
func (h *Hypervisor) StopVM(ctx context.Context, id string) error {
	defer h.lock(id)()
	return h.stopLocked(ctx, h.paths(id))
}

// stopLocked asks the guest to power off (ACPI), waits StopWait, then quits QEMU, and kills it if it
// is still there quitWait later (VM-06).
func (h *Hypervisor) stopLocked(ctx context.Context, p vmPaths) error {
	if !h.alive(p) {
		return nil
	}
	if st, err := qmpStatus(ctx, p.sock); err == nil && st != statusShutdown {
		_ = qmpRun(ctx, p.sock, "system_powerdown", nil)
		deadline := time.Now().Add(h.opts.StopWait)
		for time.Now().Before(deadline) && h.alive(p) {
			if st, err := qmpStatus(ctx, p.sock); err == nil && st == statusShutdown {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	_ = qmpRun(ctx, p.sock, "quit", nil)
	deadline := time.Now().Add(quitWait)
	for time.Now().Before(deadline) && h.alive(p) {
		time.Sleep(100 * time.Millisecond)
	}
	if h.alive(p) {
		_ = syscall.Kill(h.pid(p), syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
	}
	if h.alive(p) {
		return fmt.Errorf("QEMU (pid %d) didn't stop", h.pid(p))
	}
	_ = os.Remove(p.pid)
	_ = os.Remove(p.sock)
	return nil
}

// DeleteVM implements engine.Hypervisor.
func (h *Hypervisor) DeleteVM(ctx context.Context, id string) error {
	unlock := h.lock(id)
	p := h.paths(id)
	if err := h.stopLocked(ctx, p); err != nil {
		unlock()
		return err
	}
	err := os.RemoveAll(p.dir)
	_ = os.Remove(p.sock)
	unlock()
	if err == nil {
		h.pruneImages()
	}
	return err
}

// VMStatus implements engine.Hypervisor.
func (h *Hypervisor) VMStatus(ctx context.Context, id string) (engine.VMStatus, error) {
	p := h.paths(id)
	if !h.alive(p) {
		return engine.VMMissing, nil
	}
	st, err := qmpStatus(ctx, p.sock)
	if err == nil && st == statusShutdown {
		return engine.VMGuestShutdown, nil
	}
	return engine.VMRunning, nil // busy monitors (another operation holds it) mean it runs
}

// ConsoleLog implements engine.Hypervisor: the last lines of the serial console (VM-09).
func (h *Hypervisor) ConsoleLog(_ context.Context, id string, tail int) ([]api.LogLine, error) {
	p := h.paths(id)
	f, err := os.Open(p.console)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > consoleMax {
		if _, err := f.Seek(-consoleMax, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	var lines []api.LogLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		lines = append(lines, api.LogLine{Time: st.ModTime().UTC(), Stream: "stdout", Message: stripANSI(line)})
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

// stripANSI drops terminal escape sequences (colours, cursor moves) that boot consoles print.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
