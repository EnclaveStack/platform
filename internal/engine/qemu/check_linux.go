package qemu

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

func hostMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb << 10
		}
	}
	return 0
}

// accelerator is KVM, which needs read-write access to /dev/kvm (VM-11).
func accelerator() (string, string) {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "/dev/kvm is missing: turn on virtualization in the firmware settings and load the kvm module (sudo modprobe kvm)."
	case errors.Is(err, fs.ErrPermission):
		return "", "This user can't use /dev/kvm. Run: sudo usermod -aG kvm $USER, then log in again."
	case err != nil:
		return "", "Can't open /dev/kvm: " + err.Error()
	}
	f.Close()
	return "kvm", ""
}
