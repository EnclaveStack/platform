//go:build embedconsole

package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

var embedded = mustSub(dist, "dist")

func mustSub(f embed.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}
