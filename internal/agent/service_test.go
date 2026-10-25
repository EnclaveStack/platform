package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service installed from Homebrew names the opt link, which survives `brew upgrade` (ADR-054).
func TestStableExecutable(t *testing.T) {
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", "enclave", "0.9.0", "bin", "enclave")
	opt := filepath.Join(prefix, "opt", "enclave", "bin", "enclave")
	for _, p := range []string{cellar, opt} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := StableExecutable(cellar); got != opt {
		t.Errorf("StableExecutable(%s) = %s, want %s", cellar, got, opt)
	}
	u, err := Unit("darwin", t.TempDir(), StableExecutable(cellar), "/state")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.Content, "Cellar") || !strings.Contains(u.Content, opt) {
		t.Errorf("the launchd agent names a versioned folder:\n%s", u.Content)
	}

	// Anything that isn't a Homebrew Cellar path, or has no opt link, is kept as it is.
	for _, exe := range []string{
		"/usr/local/bin/enclave",
		filepath.Join(prefix, "build", "enclave"),
		filepath.Join(t.TempDir(), "Cellar", "enclave", "0.9.0", "bin", "enclave"), // no opt link beside it
		filepath.Join(prefix, "Cellar", "enclave", "0.9.0", "libexec", "enclave"),
	} {
		if got := StableExecutable(exe); got != exe {
			t.Errorf("StableExecutable(%s) = %s, want it unchanged", exe, got)
		}
	}
}
