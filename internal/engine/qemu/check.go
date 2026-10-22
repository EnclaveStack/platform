// Package qemu is the hypervisor (ADR-025): one QEMU process per VM on the host, with hardware
// acceleration (HVF on macOS, KVM on Linux), driven over QMP. It owns VM directories, the image cache,
// disk volumes' files and disk snapshots.
package qemu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Requirements (VM-11).
const (
	MinMajor        = 8
	checkCacheFor   = 10 * time.Second
	commandTimeout  = 10 * time.Second
	defaultStopWait = 60 * time.Second
)

// Options configure the hypervisor.
type Options struct {
	// DataDir holds vm/, images/ and disks/ (L3-005 §7).
	DataDir string
	// QEMU is the qemu-system binary; empty finds qemu-system-aarch64 or qemu-system-x86_64 on PATH.
	QEMU string
	// StopWait is how long a guest gets to shut down before it is forced off (VM-06).
	StopWait time.Duration
	// HTTP downloads images; nil uses a client without a timeout (downloads are bounded by context).
	HTTP *http.Client
	// Catalog replaces the built-in catalog (tests).
	Catalog Catalog
	Log     *slog.Logger
}

// Hypervisor implements engine.Hypervisor with QEMU.
type Hypervisor struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	locks   map[string]*sync.Mutex // one operation at a time per VM or disk
	checked time.Time
	support engine.VMSupport
	tools   tools
}

// tools are the binaries and firmware Check found.
type tools struct {
	system   string // qemu-system-*
	img      string // qemu-img
	firmware string // UEFI code for arm64; "" on amd64
	major    int
	minor    int
	accel    string
}

// New returns a hypervisor; nothing is checked until Check or the first VM.
func New(opts Options) *Hypervisor {
	if opts.StopWait == 0 {
		opts.StopWait = defaultStopWait
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{}
	}
	if opts.Catalog == nil {
		opts.Catalog = BuiltinCatalog
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Hypervisor{opts: opts, log: opts.Log.With("component", "qemu"), locks: map[string]*sync.Mutex{}}
}

// lock serialises operations on one VM or disk.
func (h *Hypervisor) lock(id string) func() {
	h.mu.Lock()
	l := h.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		h.locks[id] = l
	}
	h.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Arch is the guest architecture: the host's own (IMG-03).
func Arch() string { return runtime.GOARCH }

func systemBinary() string {
	if runtime.GOARCH == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

// installHint is the command that installs QEMU here.
func installHint() string {
	if runtime.GOOS == "darwin" {
		return "brew install qemu"
	}
	return "sudo apt install qemu-system qemu-utils (or your distribution's QEMU packages)"
}

var versionPattern = regexp.MustCompile(`version (\d+)\.(\d+)`)

// Check implements engine.Hypervisor. The result is cached briefly: it runs on every GET /machine.
func (h *Hypervisor) Check(ctx context.Context) engine.VMSupport {
	h.mu.Lock()
	if !h.checked.IsZero() && time.Since(h.checked) < checkCacheFor {
		s := h.support
		h.mu.Unlock()
		return s
	}
	h.mu.Unlock()
	t, s := h.check(ctx)
	h.mu.Lock()
	h.support, h.tools, h.checked = s, t, time.Now()
	h.mu.Unlock()
	return s
}

func (h *Hypervisor) check(ctx context.Context) (tools, engine.VMSupport) {
	s := engine.VMSupport{Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), MemoryTotalBytes: hostMemory()}
	no := func(format string, a ...any) (tools, engine.VMSupport) {
		s.Available, s.Message = false, fmt.Sprintf(format, a...)
		return tools{}, s
	}
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		return no("VMs need an arm64 or amd64 machine; this one is %s.", runtime.GOARCH)
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return no("VMs run on macOS and Linux; this machine runs %s.", runtime.GOOS)
	}
	var t tools
	t.system = h.opts.QEMU
	if t.system == "" {
		p, err := exec.LookPath(systemBinary())
		if err != nil {
			return no("QEMU isn't installed. Install QEMU: %s", installHint())
		}
		t.system = p
	}
	t.img = filepath.Join(filepath.Dir(t.system), "qemu-img")
	if _, err := os.Stat(t.img); err != nil {
		p, err := exec.LookPath("qemu-img")
		if err != nil {
			return no("qemu-img isn't installed. Install QEMU: %s", installHint())
		}
		t.img = p
	}
	vctx, cancel := context.WithTimeout(ctx, commandTimeout)
	out, err := exec.CommandContext(vctx, t.system, "--version").Output()
	cancel()
	m := versionPattern.FindStringSubmatch(string(out))
	if err != nil || m == nil {
		return no("%s doesn't run (%v). Reinstall QEMU: %s", t.system, err, installHint())
	}
	t.major, _ = strconv.Atoi(m[1])
	t.minor, _ = strconv.Atoi(m[2])
	s.QEMUVersion = m[1] + "." + m[2]
	if t.major < MinMajor {
		return no("QEMU %s is too old; VMs need %d.0 or later. Upgrade QEMU: %s", s.QEMUVersion, MinMajor, installHint())
	}
	if runtime.GOARCH == "arm64" {
		if t.firmware = findFirmware(t.system); t.firmware == "" {
			return no("The UEFI firmware for arm64 VMs is missing. Install it: sudo apt install qemu-efi-aarch64")
		}
	}
	accel, problem := accelerator()
	if problem != "" {
		return no("%s", problem)
	}
	t.accel = accel
	s.Available, s.Accelerator = true, accel
	s.Message = fmt.Sprintf("VMs can run: QEMU %s with %s.", s.QEMUVersion, strings.ToUpper(accel))
	return t, s
}

// findFirmware looks next to QEMU (Homebrew) and in the distributions' paths.
func findFirmware(system string) string {
	for _, p := range []string{
		filepath.Join(filepath.Dir(system), "..", "share", "qemu", "edk2-aarch64-code.fd"),
		"/usr/share/qemu/edk2-aarch64-code.fd",
		"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
		"/usr/share/AAVMF/AAVMF_CODE.fd",
		"/usr/share/edk2/aarch64/QEMU_EFI.fd",
		"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return filepath.Clean(p)
		}
	}
	return ""
}

// ready returns the tools when VMs can run, or ErrNoVMs with the reason.
func (h *Hypervisor) ready(ctx context.Context) (tools, error) {
	s := h.Check(ctx)
	if !s.Available {
		return tools{}, fmt.Errorf("%w: %s", engine.ErrNoVMs, s.Message)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tools, nil
}

// readyTools is ready for disk work, which needs only qemu-img: still, no QEMU means no disks.
func (h *Hypervisor) readyTools(ctx context.Context) (tools, error) { return h.ready(ctx) }

// run runs a tool and returns its trimmed output, with stderr in the error.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			msg = "timed out after " + timeout.String()
		}
		return "", fmt.Errorf("%s %s: %s", filepath.Base(name), args[0], msg)
	}
	return strings.TrimSpace(string(out)), nil
}

var _ engine.Hypervisor = (*Hypervisor)(nil)

// CachedImages implements engine.Hypervisor.
func (h *Hypervisor) CachedImages(context.Context) []api.CachedImage { return h.cachedImages() }
