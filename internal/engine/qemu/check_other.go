//go:build !darwin && !linux

package qemu

func hostMemory() int64 { return 0 }

func accelerator() (string, string) { return "", "VMs run on macOS and Linux only." }
