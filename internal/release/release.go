// Package release builds the files of an EnclaveStack release (ADR-054, L4-002 §3): an archive of `enclave`
// per platform, the OpenTofu provider in the registry's layout, checksums and the Homebrew formula.
// Signing and publishing are release.yml's; nothing here talks to the network.
package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"
)

// Target is one platform a release is built for.
type Target struct{ OS, Arch string }

func (t Target) String() string { return t.OS + "_" + t.Arch }

// Targets are the platforms of every release: macOS and Linux, Intel and ARM.
var Targets = []Target{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}}

// Repo is where releases are published; the formula's URLs point at it.
const Repo = "EnclaveStack/platform"

// BuildFunc compiles the Go package pkg for t into out. Tests replace it so they don't compile.
type BuildFunc func(ctx context.Context, pkg, out string, t Target, tags []string, ldflags string) error

// Config is one release build.
type Config struct {
	Tag   string    // vX.Y.Z
	Root  string    // the repository: LICENSE, NOTICE, README.md and scripts/install.sh are read from it
	Out   string    // emptied, then filled with the release's files
	Build BuildFunc // default GoBuild
	// ModTime is stamped on every archived file, so the same sources give the same archives.
	ModTime time.Time
}

// Result lists what Run wrote, by file name inside Config.Out.
type Result struct {
	Version  string   // X.Y.Z
	Files    []string // every file SHA256SUMS covers, sorted
	Checksum map[string]string
}

var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// ParseVersion turns a tag (vX.Y.Z, optionally with a -pre-release suffix) into its version without the v.
func ParseVersion(tag string) (string, error) {
	if !semver.MatchString(tag) {
		return "", fmt.Errorf("a release tag looks like v1.2.3, not %q", tag)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// ArchiveName is the archive of `enclave` for a platform.
func ArchiveName(version string, t Target) string {
	return fmt.Sprintf("enclave_%s_%s.tar.gz", version, t)
}

// ProviderZipName is the provider for a platform, named as the OpenTofu and Terraform registries expect.
func ProviderZipName(version string, t Target) string {
	return fmt.Sprintf("terraform-provider-enclave_%s_%s.zip", version, t)
}

// ProviderManifest says which plugin protocol the provider speaks (terraform-plugin-framework: 6).
const ProviderManifest = "{\n  \"version\": 1,\n  \"metadata\": {\n    \"protocol_versions\": [\"6.0\"]\n  }\n}\n"

const ldflagsFormat = "-s -w -X github.com/enclavestack/platform/internal/version.Version=%s"

// Run builds every file of a release into cfg.Out.
func Run(ctx context.Context, cfg Config) (Result, error) {
	version, err := ParseVersion(cfg.Tag)
	if err != nil {
		return Result{}, err
	}
	if cfg.Build == nil {
		cfg.Build = GoBuild
	}
	if cfg.ModTime.IsZero() {
		cfg.ModTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC) // the earliest time a zip can hold
	}
	extras := map[string][]byte{}
	for _, name := range []string{"LICENSE", "NOTICE", "README.md", "scripts/install.sh"} {
		b, err := os.ReadFile(filepath.Join(cfg.Root, name))
		if err != nil {
			return Result{}, fmt.Errorf("a release includes %s: %w", name, err)
		}
		extras[name] = b
	}
	if err := os.RemoveAll(cfg.Out); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return Result{}, err
	}
	work, err := os.MkdirTemp("", "enclave-release-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(work)

	ldflags := fmt.Sprintf(ldflagsFormat, version)
	providerBinary := "terraform-provider-enclave_v" + version
	var providerFiles []string
	for _, t := range Targets {
		bin := filepath.Join(work, t.String(), "enclave")
		if err := cfg.Build(ctx, "./cmd/enclave", bin, t, []string{"embedconsole"}, ldflags); err != nil {
			return Result{}, fmt.Errorf("build enclave for %s: %w", t, err)
		}
		exe, err := os.ReadFile(bin)
		if err != nil {
			return Result{}, err
		}
		archive, err := tarGz(cfg.ModTime, []entry{
			{"enclave", exe, 0o755}, {"LICENSE", extras["LICENSE"], 0o644}, {"NOTICE", extras["NOTICE"], 0o644}, {"README.md", extras["README.md"], 0o644},
		})
		if err != nil {
			return Result{}, err
		}
		if err := write(cfg.Out, ArchiveName(version, t), archive, 0o644); err != nil {
			return Result{}, err
		}
		if t.OS == "linux" { // the server image is built from these (Dockerfile)
			if err := write(cfg.Out, filepath.Join(t.String(), "enclave"), exe, 0o755); err != nil {
				return Result{}, err
			}
		}

		pbin := filepath.Join(work, t.String(), providerBinary)
		if err := cfg.Build(ctx, "./cmd/terraform-provider-enclave", pbin, t, nil, ldflags); err != nil {
			return Result{}, fmt.Errorf("build the provider for %s: %w", t, err)
		}
		pexe, err := os.ReadFile(pbin)
		if err != nil {
			return Result{}, err
		}
		z, err := zipOf(cfg.ModTime, entry{providerBinary, pexe, 0o755})
		if err != nil {
			return Result{}, err
		}
		if err := write(cfg.Out, ProviderZipName(version, t), z, 0o644); err != nil {
			return Result{}, err
		}
		providerFiles = append(providerFiles, ProviderZipName(version, t))
	}
	manifest := fmt.Sprintf("terraform-provider-enclave_%s_manifest.json", version)
	if err := write(cfg.Out, manifest, []byte(ProviderManifest), 0o644); err != nil {
		return Result{}, err
	}
	providerFiles = append(providerFiles, manifest)
	providerSums, err := sumsOf(cfg.Out, providerFiles)
	if err != nil {
		return Result{}, err
	}
	if err := write(cfg.Out, fmt.Sprintf("terraform-provider-enclave_%s_SHA256SUMS", version), []byte(providerSums.text), 0o644); err != nil {
		return Result{}, err
	}
	if err := write(cfg.Out, "install.sh", extras["scripts/install.sh"], 0o755); err != nil {
		return Result{}, err
	}

	var files []string
	entries, err := os.ReadDir(cfg.Out)
	if err != nil {
		return Result{}, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	all, err := sumsOf(cfg.Out, files)
	if err != nil {
		return Result{}, err
	}
	if err := write(cfg.Out, "SHA256SUMS", []byte(all.text), 0o644); err != nil {
		return Result{}, err
	}
	formula, err := Formula(cfg.Tag, all.sums)
	if err != nil {
		return Result{}, err
	}
	if err := write(cfg.Out, "enclave.rb", []byte(formula), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Version: version, Files: files, Checksum: all.sums}, nil
}

// GoBuild compiles with the Go on PATH: static (the SQLite driver is pure Go) and without local paths.
func GoBuild(ctx context.Context, pkg, out string, t Target, tags []string, ldflags string) error {
	args := []string{"build", "-trimpath", "-ldflags", ldflags, "-o", out}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	cmd := exec.CommandContext(ctx, "go", append(args, pkg)...)
	cmd.Env = append(os.Environ(), "GOOS="+t.OS, "GOARCH="+t.Arch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

type entry struct {
	name string
	body []byte
	mode int64
}

func tarGz(mod time.Time, files []entry) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), ModTime: mod, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func zipOf(mod time.Time, f entry) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: mod}
	h.SetMode(os.FileMode(f.mode))
	w, err := zw.CreateHeader(h)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(f.body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func write(dir, name string, b []byte, mode os.FileMode) error {
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, mode)
}

type sums struct {
	sums map[string]string
	text string
}

// sumsOf is a SHA256SUMS file over names in dir, sorted, in the `sha256sum -c` format.
func sumsOf(dir string, names []string) (sums, error) {
	names = slices.Sorted(slices.Values(names))
	s := sums{sums: map[string]string{}}
	var b strings.Builder
	for _, n := range names {
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			return sums{}, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return sums{}, err
		}
		s.sums[n] = hex.EncodeToString(h.Sum(nil))
		fmt.Fprintf(&b, "%s  %s\n", s.sums[n], n)
	}
	s.text = b.String()
	return s, nil
}

// FormulaDesc is the formula's one line; `brew audit` wants it under 80 characters, without the name.
const FormulaDesc = "Self-hosted cloud for apps, databases, caches, buckets and secrets"

var formula = template.Must(template.New("formula").Parse(`# Generated by cmd/release for {{.Tag}} (ADR-054); release.yml commits it to EnclaveStack/homebrew-tap.
class Enclave < Formula
  desc "{{.Desc}}"
  homepage "https://enclavestack.soupler.com"
  version "{{.Version}}"
  license "Apache-2.0"
{{range .Systems}}
  on_{{.OS}} do{{range .Archs}}
    on_{{.CPU}} do
      url "{{.URL}}"
      sha256 "{{.Sum}}"
    end{{end}}
  end
{{end}}
  def install
    bin.install "enclave"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/enclave version")
  end
end
`))

// Formula is the Homebrew formula for a release, from the archives' checksums.
func Formula(tag string, checksums map[string]string) (string, error) {
	version, err := ParseVersion(tag)
	if err != nil {
		return "", err
	}
	type arch struct{ CPU, URL, Sum string }
	type system struct {
		OS    string
		Archs []arch
	}
	var systems []system
	for _, sys := range []struct{ goos, brew string }{{"darwin", "macos"}, {"linux", "linux"}} {
		s := system{OS: sys.brew}
		for _, cpu := range []struct{ goarch, brew string }{{"arm64", "arm"}, {"amd64", "intel"}} {
			name := ArchiveName(version, Target{sys.goos, cpu.goarch})
			sum, ok := checksums[name]
			if !ok {
				return "", fmt.Errorf("the formula needs the checksum of %s", name)
			}
			s.Archs = append(s.Archs, arch{CPU: cpu.brew, URL: fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", Repo, tag, name), Sum: sum})
		}
		systems = append(systems, s)
	}
	var b strings.Builder
	err = formula.Execute(&b, map[string]any{"Tag": tag, "Version": version, "Desc": FormulaDesc, "Systems": systems})
	return b.String(), err
}
