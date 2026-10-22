package vmlink

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"slices"
	"strings"
)

// The link image (ADR-026) is built on the machine from this package's own source: a pinned golang stage
// compiles it (standard library only) and a pinned Alpine stage adds nftables and iproute2.

//go:embed dns.go frame.go link.go rules.go
var sources embed.FS

// Base images, pinned by digest.
const (
	GoImage     = "golang@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca" // golang:1.26-alpine
	AlpineImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
)

// Dockerfile builds the helper.
const Dockerfile = "FROM " + GoImage + " AS build\n" +
	"WORKDIR /src\nCOPY . .\nRUN CGO_ENABLED=0 go build -trimpath -o /es-vmlink .\n" +
	"FROM " + AlpineImage + "\nRUN apk add --no-cache nftables iproute2\n" +
	"COPY --from=build /es-vmlink /usr/local/bin/es-vmlink\nENTRYPOINT [\"/usr/local/bin/es-vmlink\"]\n"

// BuildContext is the image's build context: this package's files as package main, a main.go that calls
// Main, a go.mod and the Dockerfile.
func BuildContext() map[string][]byte {
	files := map[string][]byte{
		"go.mod":     []byte("module hcvmlink\n\ngo 1.26\n"),
		"main.go":    []byte("package main\n\nfunc main() { Main() }\n"),
		"Dockerfile": []byte(Dockerfile),
	}
	entries, _ := sources.ReadDir(".")
	for _, e := range entries {
		b, _ := sources.ReadFile(e.Name())
		files[e.Name()] = []byte(strings.Replace(string(b), "package vmlink\n", "package main\n", 1))
	}
	return files
}

// ImageTag is the helper image's local tag, derived from its build context so any change builds anew.
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
	return "enclave.local/vmlink:" + hex.EncodeToString(h.Sum(nil)[:6])
}
