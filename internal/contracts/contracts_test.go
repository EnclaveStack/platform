package contracts

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGeneratedFileIsCurrent fails when pkg/api changed without `make contracts` (ADR-002).
func TestGeneratedFileIsCurrent(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", GeneratedPath)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run make contracts)", GeneratedPath, err)
	}
	if string(got) != TypeScript() {
		t.Fatalf("%s is stale: run `make contracts` and commit the result", GeneratedPath)
	}
}
