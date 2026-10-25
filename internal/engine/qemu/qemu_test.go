package qemu

import (
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"go.yaml.in/yaml/v3"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Unit tests that need no QEMU (L5-002 §8): seeds, command lines, socket paths, the catalog.

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave"

func input(userData string) engine.VMInput {
	return engine.VMInput{ID: "vm_abc", Name: "worker", SSHKeys: []string{testKey},
		Spec: api.VMSpec{CPUs: 2, MemoryMiB: 2048, DiskGiB: 10, UserData: userData}}
}

func TestSeedReadsBackWithCloudInitNames(t *testing.T) {
	files, err := seedFiles(input(""))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "seed.iso")
	if err := writeSeed(path, files); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("seed mode %v", st.Mode().Perm())
	}
	d, err := diskfs.Open(path, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fs, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatal(err)
	}
	if label := strings.TrimRight(strings.TrimSpace(fs.Label()), "\x00"); label != "cidata" {
		t.Errorf("volume label %q, want cidata", label)
	}
	for _, name := range []string{"meta-data", "network-config", "user-data"} {
		f, err := fs.OpenFile("/"+name, os.O_RDONLY)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := io.ReadAll(f)
		if string(got) != string(files[name]) {
			t.Errorf("%s reads back as %q", name, got)
		}
	}
	if !strings.Contains(string(files["meta-data"]), "instance-id: vm_abc\nlocal-hostname: worker") {
		t.Errorf("meta-data %s", files["meta-data"])
	}
	var net map[string]any
	if err := yaml.Unmarshal(files["network-config"], &net); err != nil {
		t.Fatal(err)
	}
	eth := net["ethernets"].(map[string]any)["eth0"].(map[string]any)
	if eth["match"].(map[string]any)["macaddress"] != MAC("vm_abc") || eth["addresses"].([]any)[0] != "198.18.0.2/30" {
		t.Errorf("network-config %v", net)
	}
}

func TestUserDataAddsTheEnclaveStackUserAfterTheUsersScript(t *testing.T) {
	plain, err := userData(input(""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(plain), "#cloud-config\n") || !strings.Contains(string(plain), "name: enclave") ||
		!strings.Contains(string(plain), testKey) || !strings.Contains(string(plain), "ssh_pwauth: false") {
		t.Errorf("plain user-data:\n%s", plain)
	}
	for script, wantType := range map[string]string{"#!/bin/sh\necho hi\n": "text/x-shellscript", "#cloud-config\npackages: [htop]\n": "text/cloud-config"} {
		doc, err := userData(input(script))
		if err != nil {
			t.Fatal(err)
		}
		msg, err := mail.ReadMessage(strings.NewReader(string(doc)))
		if err != nil {
			t.Fatalf("not a MIME document: %v\n%s", err, doc)
		}
		_, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		mr := multipart.NewReader(msg.Body, params["boundary"])
		var types, bodies []string
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			mt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			types, bodies = append(types, mt), append(bodies, string(b))
		}
		if !slices.Equal(types, []string{wantType, "text/cloud-config"}) || bodies[0] != script || !strings.Contains(bodies[1], "merge_how") {
			t.Errorf("parts %v %q", types, bodies)
		}
	}
}

func TestCommandLine(t *testing.T) {
	h := New(Options{DataDir: "/data,dir"})
	in := input("")
	in.Disks = []engine.DiskAttachment{{DiskID: "vol_1", Serial: "data"}}
	in.Link = engine.VMLink{Host: "127.0.0.1", Port: 47601}
	a := strings.Join(h.args(tools{accel: "hvf", firmware: "/fw.fd", major: 11, minor: 1}, in, h.paths(in.ID)), " ")
	for _, want := range []string{"-cpu host -smp 2 -m 2048M", "file=/data,,dir/vm/vm_abc/root.qcow2", "serial=data",
		"addr.host=127.0.0.1,addr.port=47601,reconnect-ms=1000", "mac=" + MAC("vm_abc"), "-no-shutdown -daemonize", "-bios /fw.fd"} {
		if !strings.Contains(a, want) {
			t.Errorf("command line lacks %q:\n%s", want, a)
		}
	}
	old := strings.Join(h.args(tools{accel: "kvm", major: 8, minor: 2}, in, h.paths(in.ID)), " ")
	if !strings.Contains(old, "reconnect=1") || strings.Contains(old, "-bios") {
		t.Errorf("QEMU 8.2 command line:\n%s", old)
	}
	in.Link = engine.VMLink{}
	if a := strings.Join(h.args(tools{accel: "hvf"}, in, h.paths(in.ID)), " "); !strings.Contains(a, "-nic none") {
		t.Errorf("no link should mean no NIC: %s", a)
	}
}

func TestConfigKeyChangesWithWhatRestarts(t *testing.T) {
	a := input("")
	b := a
	b.Spec.Power, b.Spec.DiskGiB, b.Spec.Publish = api.PowerStopped, 20, []int{80}
	if configKey(a) != configKey(b) {
		t.Error("power, root disk size and publish must not restart the VM")
	}
	b.Spec.CPUs = 4
	if configKey(a) == configKey(b) {
		t.Error("CPUs must restart the VM")
	}
	c := a
	c.Disks = []engine.DiskAttachment{{DiskID: "vol_1", Serial: "data"}}
	if configKey(a) == configKey(c) {
		t.Error("disks must restart the VM")
	}
}

func TestLongDataDirsUseAShortSocketDirOwnedByUs(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	p := New(Options{DataDir: long}).paths("vm_abc")
	if len(p.sock) > maxSocketPath || filepath.Dir(p.sock) != shortSocketDir() {
		t.Errorf("socket %s", p.sock)
	}
	short := New(Options{DataDir: "/d"}).paths("vm_abc")
	if short.sock != "/d/vm/vm_abc/qmp.sock" {
		t.Errorf("socket %s", short.sock)
	}
	open := filepath.Join(t.TempDir(), "planted")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ensureSocketDir(filepath.Join(open, "x.qmp")); err == nil {
		t.Error("a world-writable socket directory was accepted")
	}
	if err := ensureSocketDir(filepath.Join(t.TempDir(), "mine", "x.qmp")); err != nil {
		t.Errorf("a fresh directory: %v", err)
	}
}

func TestCatalogIsPinnedForBothArchitectures(t *testing.T) {
	for name, byArch := range BuiltinCatalog {
		if !slices.Contains(api.ImageCatalog, name) {
			t.Errorf("%s isn't in the API's catalog", name)
		}
		for _, arch := range []string{"arm64", "amd64"} {
			img, ok := byArch[arch]
			if !ok || !strings.HasPrefix(img.URL, "https://") || strings.Contains(img.URL, "latest") || strings.Contains(img.URL, "current") ||
				(len(img.SHA256) != 64) == (len(img.SHA512) != 128) || img.Format != "qcow2" {
				t.Errorf("%s/%s: %+v", name, arch, img)
			}
		}
	}
	for _, name := range api.ImageCatalog {
		if _, ok := BuiltinCatalog[name]; !ok {
			t.Errorf("the API offers %s but the hypervisor has no image for it", name)
		}
	}
}

func TestStripANSIAndMAC(t *testing.T) {
	if got := stripANSI("\x1b[0;32m  OK  \x1b[0m Reached target"); got != "  OK   Reached target" {
		t.Errorf("stripANSI = %q", got)
	}
	if a, again := MAC("vm_abc"), MAC("vm_abc"); a != again || a == MAC("vm_abd") || !strings.HasPrefix(MAC("vm_abc"), "52:54:00:") {
		t.Errorf("MAC %s", MAC("vm_abc"))
	}
}
