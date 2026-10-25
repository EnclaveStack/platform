// Package mesh is the helper of networks across machines (ADR-045): the program in each machine's mesh
// container and in every stand-in. Its image is built on the machine from this package's own source, like
// the VM link's (ADR-026), on the same pinned base images, with WireGuard's tools added.
package mesh

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/enclavestack/platform/internal/vmlink"
)

//go:embed mesh.go
var sources embed.FS

// Dockerfile builds the helper.
const Dockerfile = "FROM " + vmlink.GoImage + " AS build\n" +
	"WORKDIR /src\nCOPY . .\nRUN CGO_ENABLED=0 go build -trimpath -o /es-mesh .\n" +
	"FROM " + vmlink.AlpineImage + "\nRUN apk add --no-cache wireguard-tools-wg nftables iproute2\n" +
	"COPY --from=build /es-mesh /usr/local/bin/es-mesh\nENTRYPOINT [\"/usr/local/bin/es-mesh\"]\nCMD [\"run\"]\n"

// BuildContext is the image's build context: the helper as package main, a main.go, a go.mod and the
// Dockerfile.
func BuildContext() map[string][]byte {
	files := map[string][]byte{
		"go.mod":     []byte("module esmesh\n\ngo 1.26\n"),
		"main.go":    []byte("package main\n\nfunc main() { Main() }\n"),
		"Dockerfile": []byte(Dockerfile),
	}
	entries, _ := sources.ReadDir(".")
	for _, e := range entries {
		b, _ := sources.ReadFile(e.Name())
		files[e.Name()] = []byte(strings.Replace(string(b), "package mesh\n", "package main\n", 1))
	}
	return files
}

// ImageTag is the helper image's local tag, derived from its build context.
func ImageTag() string {
	files := BuildContext()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write(files[n])
	}
	return "enclave.local/mesh:" + hex.EncodeToString(h.Sum(nil)[:6])
}

// MAC is the mesh's address on a network, the same on every start, so stand-ins can point at it
// (02:65:73 and three bytes of the network's hash, as the VM link fixes its own, ADR-026).
func MAC(network string) string {
	h := sha256.Sum256([]byte(network))
	return "02:65:73:" + hex.EncodeToString(h[0:1]) + ":" + hex.EncodeToString(h[1:2]) + ":" + hex.EncodeToString(h[2:3])
}
