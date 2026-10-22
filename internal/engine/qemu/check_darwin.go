package qemu

import "golang.org/x/sys/unix"

func hostMemory() int64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(n)
}

// accelerator is the Hypervisor framework (VM-11).
func accelerator() (string, string) {
	if v, err := unix.SysctlUint32("kern.hv_support"); err != nil || v != 1 {
		return "", "This Mac has no hardware virtualization (the Hypervisor framework is unavailable), so it can't run VMs."
	}
	return "hvf", ""
}
