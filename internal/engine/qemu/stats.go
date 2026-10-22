package qemu

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/enclavestack/platform/internal/engine"
)

// VMStats implements engine.Hypervisor (ADR-037): the QEMU process's CPU time and resident memory, read
// with ps, which reports both the same way on macOS and Linux.
func (h *Hypervisor) VMStats(ctx context.Context, id string) (engine.ProcessStats, bool, error) {
	p := h.paths(id)
	if !h.alive(p) {
		return engine.ProcessStats{}, false, nil
	}
	pid := h.pid(p)
	out, err := exec.CommandContext(ctx, "ps", "-o", "time=,rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return engine.ProcessStats{}, false, fmt.Errorf("read the VM's process stats: %w", err)
	}
	cpu, rss, err := parsePS(string(out))
	if err != nil {
		return engine.ProcessStats{}, false, err
	}
	return engine.ProcessStats{CPUSeconds: cpu, MemoryBytes: rss << 10, PID: pid}, true, nil
}

// parsePS reads "[[dd-]hh:]mm:ss[.ss] rss-KiB" as printed by ps -o time=,rss=.
func parsePS(s string) (float64, int64, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected ps output %q", s)
	}
	rss, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected ps memory %q", f[1])
	}
	t := f[0]
	days := 0.0
	if d, rest, ok := strings.Cut(t, "-"); ok {
		n, err := strconv.ParseFloat(d, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("unexpected ps time %q", t)
		}
		days, t = n, rest
	}
	secs := 0.0
	for _, part := range strings.Split(t, ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("unexpected ps time %q", f[0])
		}
		secs = secs*60 + n
	}
	return days*86400 + secs, rss, nil
}
