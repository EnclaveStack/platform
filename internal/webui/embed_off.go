//go:build !embedconsole

package webui

import "io/fs"

var embedded fs.FS
