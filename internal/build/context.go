package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/patternmatcher/ignorefile"
)

// Context is a checked build context: a directory inside the checkout, the Dockerfile relative to it,
// and the .dockerignore patterns the builder applies (BuildKit reads the files through the build
// session; ADR-019 "as implemented").
type Context struct {
	Dir        string
	Dockerfile string
	Excludes   []string
}

// PrepareContext checks that the context and the Dockerfile stay inside the checkout, even through
// symlinks, and reads .dockerignore.
func PrepareContext(root, contextDir, dockerfile string) (Context, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Context{}, err
	}
	ctxPath, err := filepath.EvalSymlinks(filepath.Join(realRoot, filepath.FromSlash(contextDir)))
	if err != nil {
		return Context{}, Permanent(fmt.Errorf("the build context %q doesn't exist in the repository", contextDir))
	}
	if !inside(realRoot, ctxPath) {
		return Context{}, Permanent(fmt.Errorf("the build context %q leaves the repository", contextDir))
	}
	if info, err := os.Stat(ctxPath); err != nil || !info.IsDir() {
		return Context{}, Permanent(fmt.Errorf("the build context %q isn't a directory", contextDir))
	}
	real, err := filepath.EvalSymlinks(filepath.Join(ctxPath, filepath.FromSlash(dockerfile)))
	if err != nil {
		return Context{}, Permanent(fmt.Errorf("there's no %s in the build context %q", dockerfile, contextDir))
	}
	if !inside(ctxPath, real) {
		return Context{}, Permanent(fmt.Errorf("the Dockerfile %q leaves the build context", dockerfile))
	}
	var excludes []string
	if f, err := os.Open(filepath.Join(ctxPath, ".dockerignore")); err == nil {
		excludes, err = ignorefile.ReadAll(f)
		f.Close()
		if err != nil {
			return Context{}, Permanent(fmt.Errorf(".dockerignore can't be read: %w", err))
		}
	}
	return Context{Dir: ctxPath, Dockerfile: filepath.ToSlash(filepath.Clean(dockerfile)), Excludes: excludes}, nil
}

func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
