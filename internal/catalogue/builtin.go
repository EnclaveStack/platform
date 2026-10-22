package catalogue

import (
	"embed"
	"fmt"
	"slices"
	"strings"
)

// The built-in catalogue (CAT-02): templates shipped in the binary, images pinned by version.

//go:embed builtin/*.yaml
var builtinFS embed.FS

var builtins = func() map[string]*Template {
	out := map[string]*Template{}
	entries, _ := builtinFS.ReadDir("builtin")
	for _, e := range entries {
		b, _ := builtinFS.ReadFile("builtin/" + e.Name())
		t, err := Parse(b)
		if err != nil {
			panic(fmt.Sprintf("built-in template %s: %v", e.Name(), err))
		}
		if t.Name+".yaml" != e.Name() {
			panic(fmt.Sprintf("built-in template %s is named %s", e.Name(), t.Name))
		}
		out[t.Name] = t
	}
	return out
}()

// Builtin returns a built-in template by name.
func Builtin(name string) (*Template, bool) {
	t, ok := builtins[name]
	return t, ok
}

// Builtins lists the built-in templates by title.
func Builtins() []*Template {
	out := make([]*Template, 0, len(builtins))
	for _, t := range builtins {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b *Template) int { return strings.Compare(a.Name, b.Name) })
	return out
}
