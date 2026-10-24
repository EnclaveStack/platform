package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enclavestack/platform/internal/cli"
	"github.com/enclavestack/platform/pkg/api"
)

// Bucket objects from the CLI (L2-002 CLI-55, L2-013).
func TestBucketObjectsFromTheCLI(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.ok("bucket", "create", "media", "--wait")
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mov")
	if err := os.WriteFile(clip, []byte("video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := h.ok("bucket", "objects", "media"); !strings.Contains(r.out, "No files in media/") {
		t.Errorf("empty: %s", r.out)
	}
	if r := h.ok("bucket", "upload", "media", clip, "incoming/", "--tag", "video-id=v42"); !strings.Contains(r.out, "Uploaded 11 B to media/incoming/clip.mov") {
		t.Errorf("upload: %s", r.out)
	}
	h.ok("bucket", "upload", "media", clip) // the file's name at the top
	r := h.ok("bucket", "objects", "media")
	if !strings.Contains(r.out, "incoming/") || !strings.Contains(r.out, "folder") || !strings.Contains(r.out, "clip.mov") {
		t.Errorf("objects: %s", r.out)
	}
	r = h.ok("bucket", "objects", "media", "--recursive", "-o", "json")
	var l api.ObjectList
	if err := json.Unmarshal([]byte(r.out), &l); err != nil || len(l.Items) != 2 || len(l.Folders) != 0 {
		t.Errorf("recursive: %s %v", r.out, err)
	}
	out := filepath.Join(dir, "back.mov")
	h.ok("bucket", "download", "media", "incoming/clip.mov", out)
	if b, _ := os.ReadFile(out); string(b) != "video bytes" {
		t.Errorf("downloaded %q", b)
	}
	if r := h.ok("bucket", "download", "media", "incoming/clip.mov", "-"); r.out != "video bytes" {
		t.Errorf("to stdout: %q", r.out)
	}
	h.ok("bucket", "delete-object", "media", "clip.mov", "--yes")
	if r := h.ok("bucket", "objects", "media"); strings.Contains(r.out, " clip.mov") || !strings.Contains(r.out, "incoming/") {
		t.Errorf("after delete: %s", r.out)
	}
	// Mistakes.
	if r := h.run("bucket", "upload", "media", clip, "--tag", "novalue"); r.code != cli.ExitUsage {
		t.Errorf("a bad tag flag: %d %s", r.code, r.err)
	}
	if r := h.run("bucket", "upload", "media", clip, "a/../b"); r.code != cli.ExitError || !strings.Contains(r.err, "..") {
		t.Errorf("a bad key: %d %s", r.code, r.err)
	}
	if r := h.run("bucket", "download", "media", "nothing.mov", filepath.Join(dir, "x")); r.code != cli.ExitNotFound {
		t.Errorf("a missing file: %d %s", r.code, r.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("a failed download left a file")
	}
}
