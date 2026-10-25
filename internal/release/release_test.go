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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBuild writes a shell script in place of a compiled program: `<program> version` prints what
// `enclave version` prints, so the install script can run what it installed.
func fakeBuild(_ context.Context, pkg, out string, t Target, tags []string, ldflags string) error {
	version := ldflags[strings.LastIndex(ldflags, "=")+1:]
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	script := fmt.Sprintf("#!/bin/sh\n# %s for %s, tags %v\necho enclave %s\n", pkg, t, tags, version)
	return os.WriteFile(out, []byte(script), 0o755)
}

func root(t *testing.T) string {
	t.Helper()
	r, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func build(t *testing.T, tag string) (string, Result) {
	t.Helper()
	out := t.TempDir()
	res, err := Run(context.Background(), Config{Tag: tag, Root: root(t), Out: out, Build: fakeBuild})
	if err != nil {
		t.Fatal(err)
	}
	return out, res
}

func TestParseVersion(t *testing.T) {
	for tag, want := range map[string]string{"v0.9.0": "0.9.0", "v1.10.2": "1.10.2", "v0.0.0-test.1": "0.0.0-test.1"} {
		if got, err := ParseVersion(tag); err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %q, %v; want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"", "0.9.0", "v0.9", "v0.9.0.1", "release-1", "v0.9.0 "} {
		if _, err := ParseVersion(tag); err == nil {
			t.Errorf("ParseVersion(%q) accepted a tag that isn't vX.Y.Z", tag)
		}
	}
}

func TestRunWritesEveryFile(t *testing.T) {
	out, res := build(t, "v0.9.0")
	want := []string{
		"enclave_0.9.0_darwin_amd64.tar.gz", "enclave_0.9.0_darwin_arm64.tar.gz", "enclave_0.9.0_linux_amd64.tar.gz", "enclave_0.9.0_linux_arm64.tar.gz",
		"install.sh",
		"terraform-provider-enclave_0.9.0_SHA256SUMS",
		"terraform-provider-enclave_0.9.0_darwin_amd64.zip", "terraform-provider-enclave_0.9.0_darwin_arm64.zip",
		"terraform-provider-enclave_0.9.0_linux_amd64.zip", "terraform-provider-enclave_0.9.0_linux_arm64.zip",
		"terraform-provider-enclave_0.9.0_manifest.json",
	}
	if strings.Join(res.Files, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files:\n%s\nwant:\n%s", strings.Join(res.Files, "\n"), strings.Join(want, "\n"))
	}
	for _, f := range append(want, "SHA256SUMS", "enclave.rb", "linux_amd64/enclave", "linux_arm64/enclave") {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "darwin_arm64")); err == nil {
		t.Error("only the Linux programs are left unpacked, for the server image")
	}
}

// Every line of SHA256SUMS is the real SHA-256 of its file, in the format `sha256sum -c` reads.
func TestChecksums(t *testing.T) {
	out, res := build(t, "v0.9.0")
	b, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != len(res.Files) {
		t.Fatalf("%d lines for %d files", len(lines), len(res.Files))
	}
	for _, l := range lines {
		sum, name, ok := strings.Cut(l, "  ")
		if !ok {
			t.Fatalf("line %q isn't `<sum>  <name>`", l)
		}
		f, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(f)
		if hex.EncodeToString(h[:]) != sum {
			t.Errorf("%s: SHA256SUMS says %s", name, sum)
		}
	}
	p, err := os.ReadFile(filepath.Join(out, "terraform-provider-enclave_0.9.0_SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(p), "\n"); n != 5 || strings.Contains(string(p), "  enclave_0.9.0_") {
		t.Errorf("the provider's SHA256SUMS covers its four zips and its manifest only:\n%s", p)
	}
}

func TestArchiveHoldsTheProgramAndItsLicence(t *testing.T) {
	out, _ := build(t, "v0.9.0")
	f, err := os.Open(filepath.Join(out, "enclave_0.9.0_linux_arm64.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]int64{}
	var program string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = h.Mode
		if h.Name == "enclave" {
			b, _ := io.ReadAll(tr)
			program = string(b)
		}
	}
	want := map[string]int64{"enclave": 0o755, "LICENSE": 0o644, "NOTICE": 0o644, "README.md": 0o644}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("archive holds %v, want %v", got, want)
	}
	if !strings.Contains(program, "./cmd/enclave for linux_arm64, tags [embedconsole]") || !strings.Contains(program, "echo enclave 0.9.0") {
		t.Errorf("the archived program was built for the wrong package, platform, tags or version:\n%s", program)
	}
}

func TestProviderInTheRegistryLayout(t *testing.T) {
	out, _ := build(t, "v0.9.0")
	z, err := zip.OpenReader(filepath.Join(out, "terraform-provider-enclave_0.9.0_darwin_arm64.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if len(z.File) != 1 || z.File[0].Name != "terraform-provider-enclave_v0.9.0" || z.File[0].Mode().Perm() != 0o755 {
		t.Fatalf("the zip holds one executable named terraform-provider-enclave_v0.9.0, not %v", z.File)
	}
	r, _ := z.File[0].Open()
	b, _ := io.ReadAll(r)
	r.Close()
	if !strings.Contains(string(b), "./cmd/terraform-provider-enclave for darwin_arm64, tags []") {
		t.Errorf("the provider was built from the wrong package:\n%s", b)
	}
	m, _ := os.ReadFile(filepath.Join(out, "terraform-provider-enclave_0.9.0_manifest.json"))
	if !strings.Contains(string(m), `"protocol_versions": ["6.0"]`) {
		t.Errorf("manifest:\n%s", m)
	}
}

// The same sources give byte-identical archives, so a rebuilt release can be compared with a published one.
func TestArchivesAreReproducible(t *testing.T) {
	_, ra := build(t, "v0.9.0")
	_, rb := build(t, "v0.9.0")
	for _, f := range ra.Files {
		if ra.Checksum[f] != rb.Checksum[f] {
			t.Errorf("%s differs between two builds of the same sources", f)
		}
	}
}

func TestFormula(t *testing.T) {
	out, res := build(t, "v0.9.0")
	b, err := os.ReadFile(filepath.Join(out, "enclave.rb"))
	if err != nil {
		t.Fatal(err)
	}
	f := string(b)
	for _, want := range []string{
		"class Enclave < Formula", `version "0.9.0"`, `license "Apache-2.0"`, "on_macos do", "on_linux do", "on_arm do", "on_intel do",
		`url "https://github.com/EnclaveStack/platform/releases/download/v0.9.0/enclave_0.9.0_darwin_arm64.tar.gz"`,
		`sha256 "` + res.Checksum["enclave_0.9.0_darwin_arm64.tar.gz"] + `"`,
		`sha256 "` + res.Checksum["enclave_0.9.0_linux_amd64.tar.gz"] + `"`,
		`bin.install "enclave"`, `shell_output("#{bin}/enclave version")`,
	} {
		if !strings.Contains(f, want) {
			t.Errorf("formula lacks %s:\n%s", want, f)
		}
	}
	// Each URL is followed by its own archive's checksum.
	for _, tg := range Targets {
		name := ArchiveName("0.9.0", tg)
		i := strings.Index(f, name+`"`)
		if i < 0 || !strings.HasPrefix(strings.TrimSpace(f[i+len(name)+1:]), `sha256 "`+res.Checksum[name]+`"`) {
			t.Errorf("%s isn't followed by its checksum", name)
		}
	}
	if len(FormulaDesc) >= 80 || strings.HasPrefix(strings.ToLower(FormulaDesc), "enclave") {
		t.Errorf("brew audit refuses a description of 80 characters or more, or one starting with the name: %q", FormulaDesc)
	}
	if _, err := Formula("v0.9.0", map[string]string{}); err == nil {
		t.Error("a formula without the archives' checksums was written")
	}
}

func TestRunRefusesABadTag(t *testing.T) {
	_, err := Run(context.Background(), Config{Tag: "0.9", Root: root(t), Out: t.TempDir(), Build: fakeBuild})
	if err == nil || !strings.Contains(err.Error(), "v1.2.3") {
		t.Fatalf("err = %v", err)
	}
}

// serve answers like GitHub's releases: /latest redirects to the newest tag, /download/<tag>/<file> serves dir.
func serve(t *testing.T, tag, dir string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/tag/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>release page</html>") })
	mux.Handle("/download/"+tag+"/", http.StripPrefix("/download/"+tag+"/", http.FileServer(http.Dir(dir))))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

type installRun struct {
	out string
	err error
	dir string
}

func install(t *testing.T, base string, env ...string) installRun {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the install script is for macOS and Linux")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("sh", filepath.Join(root(t), "scripts", "install.sh"))
	// Without cosign on PATH the signature check is skipped, as on a computer without it.
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + t.TempDir(), "ENCLAVE_DOWNLOAD_BASE=" + base, "ENCLAVE_INSTALL_DIR=" + dir}, env...)
	b, err := cmd.CombinedOutput()
	return installRun{string(b), err, dir}
}

func TestInstallScriptInstallsTheLatestRelease(t *testing.T) {
	out, _ := build(t, "v0.9.0")
	s := serve(t, "v0.9.0", out)
	r := install(t, s.URL)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	for _, want := range []string{"Installing enclave 0.9.0 for ", "Checksum OK", "Installed " + r.dir + "/enclave (enclave 0.9.0)", "isn't on your PATH yet"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output lacks %q:\n%s", want, r.out)
		}
	}
	got, err := exec.Command(filepath.Join(r.dir, "enclave"), "version").Output()
	if err != nil || strings.TrimSpace(string(got)) != "enclave 0.9.0" {
		t.Errorf("installed program says %q, %v", got, err)
	}
	// The platform it detected is this computer's.
	if want := "for " + runtime.GOOS + "/" + runtime.GOARCH; !strings.Contains(r.out, want) {
		t.Errorf("output lacks %q:\n%s", want, r.out)
	}
}

func TestInstallScriptChoosesVersionAndPlatform(t *testing.T) {
	out, _ := build(t, "v0.9.1")
	s := serve(t, "v0.9.1", out)
	r := install(t, s.URL, "ENCLAVE_VERSION=0.9.1", "ENCLAVE_OS=Linux", "ENCLAVE_ARCH=aarch64")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "Installing enclave 0.9.1 for linux/arm64") {
		t.Errorf("output:\n%s", r.out)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, "enclave"))
	if !strings.Contains(string(b), "for linux_arm64") {
		t.Errorf("installed the wrong archive:\n%s", b)
	}
}

func TestInstallScriptRefusesAChangedArchive(t *testing.T) {
	out, _ := build(t, "v0.9.0")
	name := ArchiveName("0.9.0", Target{runtime.GOOS, runtime.GOARCH})
	f, err := os.OpenFile(filepath.Join(out, name), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("tampered"))
	f.Close()
	r := install(t, serve(t, "v0.9.0", out).URL)
	if r.err == nil || !strings.Contains(r.out, "checksum mismatch for "+name) || !strings.Contains(r.out, "nothing was installed") {
		t.Fatalf("a changed archive was accepted: %v\n%s", r.err, r.out)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "enclave")); err == nil {
		t.Error("enclave was installed from a changed archive")
	}
}

func TestInstallScriptRefusesWhatItCantInstall(t *testing.T) {
	out, _ := build(t, "v0.9.0")
	s := serve(t, "v0.9.0", out)
	for name, c := range map[string]struct {
		env  []string
		want string
	}{
		"windows":         {[]string{"ENCLAVE_OS=Windows_NT"}, "runs on macOS and Linux, not Windows_NT"},
		"32-bit":          {[]string{"ENCLAVE_ARCH=i686"}, "not i686"},
		"missing version": {[]string{"ENCLAVE_VERSION=v9.9.9"}, "couldn't download enclave_9.9.9_"},
		"not a version":   {[]string{"ENCLAVE_VERSION=latest"}, "a version looks like v1.2.3"},
	} {
		t.Run(name, func(t *testing.T) {
			r := install(t, s.URL, c.env...)
			if r.err == nil || !strings.Contains(r.out, c.want) {
				t.Fatalf("want a failure saying %q: %v\n%s", c.want, r.err, r.out)
			}
			if _, err := os.Stat(filepath.Join(r.dir, "enclave")); err == nil {
				t.Error("something was installed")
			}
		})
	}
}

// The release copies the install script byte for byte: the website serves the same file.
func TestReleaseCarriesTheInstallScript(t *testing.T) {
	out, _ := build(t, "v0.9.0")
	a, _ := os.ReadFile(filepath.Join(out, "install.sh"))
	b, _ := os.ReadFile(filepath.Join(root(t), "scripts", "install.sh"))
	if !bytes.Equal(a, b) {
		t.Error("install.sh in the release differs from scripts/install.sh")
	}
}
