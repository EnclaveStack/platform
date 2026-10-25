package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const (
	testKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave"
	testWeakRSA = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC8NQlFP2/MqOY7Ds2YyQIRtR6AjXVlw/94wZgQDFjXLjUkpOaaDOGNvcIhzhlyUtX925s0qxdGRyFyuphfZfZ+jjH0ON2HaL1528IjvvDE39cM63W+F/6C+3eERlpsY2dUbXf6z5peAf/VKY/hLfLeZ+n8UCzS6N4eqJpkgXkyZQ== weak"
)

func vmJSON(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	m := map[string]any{"image": ImageDebian12, "sshKeys": []string{testKey}}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestVMValidation(t *testing.T) {
	s, errs := normalize(t, KindVM, vmJSON(t, nil))
	if len(errs) != 0 {
		t.Fatalf("valid VM refused: %v", errs)
	}
	vm := s.(VMSpec)
	if vm.CPUs != VMCPUsDefault || vm.MemoryMiB != VMMemoryDefaultMiB || vm.DiskGiB != VMDiskDefaultGiB || vm.Power != PowerRunning {
		t.Errorf("defaults not applied: %+v", vm)
	}
	for _, image := range []string{ImageUbuntu2404, "https://cloud.example.com/img/noble-arm64.qcow2", "snapshot/worker-1"} {
		if _, errs := normalize(t, KindVM, vmJSON(t, func(m map[string]any) { m["image"] = image })); len(errs) != 0 {
			t.Errorf("image %s refused: %v", image, errs)
		}
	}
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		want   string
	}{
		{"no image", func(m map[string]any) { delete(m, "image") }, "spec.image:REQUIRED"},
		{"unknown image", func(m map[string]any) { m["image"] = "windows-11" }, "spec.image:NOT_ALLOWED"},
		{"plain http", func(m map[string]any) { m["image"] = "http://example.com/a.qcow2" }, "spec.image:NOT_ALLOWED"},
		{"bad snapshot name", func(m map[string]any) { m["image"] = "snapshot/Bad_Name" }, "spec.image:INVALID_FORMAT"},
		{"empty snapshot name", func(m map[string]any) { m["image"] = "snapshot/" }, "spec.image:INVALID_FORMAT"},
		{"checksum on catalog", func(m map[string]any) { m["imageSha256"] = strings.Repeat("a", 64) }, "spec.imageSha256:NOT_ALLOWED"},
		{"bad checksum", func(m map[string]any) {
			m["image"], m["imageSha256"] = "https://example.com/a.qcow2", "xyz"
		}, "spec.imageSha256:INVALID_FORMAT"},
		{"no keys", func(m map[string]any) { delete(m, "sshKeys") }, "spec.sshKeys:REQUIRED"},
		{"keys and secret", func(m map[string]any) { m["sshKeySecret"] = "my-keys" }, "spec.sshKeys:EXACTLY_ONE"},
		{"not a key", func(m map[string]any) { m["sshKeys"] = []string{"hello"} }, "spec.sshKeys[0]:INVALID_FORMAT"},
		{"private key", func(m map[string]any) {
			m["sshKeys"] = []string{"-----BEGIN OPENSSH PRIVATE KEY-----"}
		}, "spec.sshKeys[0]:INVALID_FORMAT"},
		{"two keys on one line", func(m map[string]any) { m["sshKeys"] = []string{testKey + "\n" + testKey} }, "spec.sshKeys[0]:INVALID_FORMAT"},
		{"weak rsa", func(m map[string]any) { m["sshKeys"] = []string{testWeakRSA} }, "spec.sshKeys[0]:INVALID_FORMAT"},
		{"too many keys", func(m map[string]any) { m["sshKeys"] = slices.Repeat([]string{testKey}, SSHKeysMax+1) }, "spec.sshKeys:TOO_LONG"},
		{"user data header", func(m map[string]any) { m["userData"] = "apt install x" }, "spec.userData:INVALID_FORMAT"},
		{"user data size", func(m map[string]any) { m["userData"] = "#!/bin/sh\n" + strings.Repeat("x", UserDataMaxBytes) }, "spec.userData:TOO_LONG"},
		{"cpus", func(m map[string]any) { m["cpus"] = 17 }, "spec.cpus:OUT_OF_RANGE"},
		{"memory small", func(m map[string]any) { m["memoryMiB"] = 256 }, "spec.memoryMiB:OUT_OF_RANGE"},
		{"memory step", func(m map[string]any) { m["memoryMiB"] = 1000 }, "spec.memoryMiB:OUT_OF_RANGE"},
		{"disk", func(m map[string]any) { m["diskGiB"] = 4 }, "spec.diskGiB:OUT_OF_RANGE"},
		{"subnet without network", func(m map[string]any) { m["subnet"] = "apps" }, "spec.subnet:REQUIRED"},
		{"disk twice", func(m map[string]any) { m["disks"] = []string{"data", "data"} }, "spec.disks[1]:NOT_ALLOWED"},
		{"disk serial clash", func(m map[string]any) {
			m["disks"] = []string{"customer-uploads-2026-a", "customer-uploads-2026-b"}
		}, "spec.disks[1]:NOT_ALLOWED"},
		{"too many disks", func(m map[string]any) {
			m["disks"] = []string{"d-1", "d-2", "d-3", "d-4", "d-5", "d-6", "d-7", "d-8", "d-9"}
		}, "spec.disks:TOO_LONG"},
		{"publish ssh", func(m map[string]any) { m["publish"] = []int{22} }, "spec.publish[0]:NOT_ALLOWED"},
		{"publish twice", func(m map[string]any) { m["publish"] = []int{80, 80} }, "spec.publish[1]:NOT_ALLOWED"},
		{"publish range", func(m map[string]any) { m["publish"] = []int{70000} }, "spec.publish[0]:OUT_OF_RANGE"},
		{"power", func(m map[string]any) { m["power"] = "paused" }, "spec.power:NOT_ALLOWED"},
	}
	for _, c := range cases {
		if _, errs := normalize(t, KindVM, vmJSON(t, c.mutate)); !slices.Contains(errs, c.want) {
			t.Errorf("%s: want %s, got %v", c.name, c.want, errs)
		}
	}
}

func TestSSHKeysAcceptCommonFormats(t *testing.T) {
	if errs := ValidateSSHKeys([]string{testKey, strings.TrimSuffix(testKey, " test@enclave")}, "spec.sshKeys"); len(errs) != 0 {
		t.Errorf("keys with and without a comment refused: %v", errs)
	}
	if got := SplitKeys("# laptop\n" + testKey + "\n\n  " + testKey + "  \n"); len(got) != 2 || got[1] != testKey {
		t.Errorf("SplitKeys = %q", got)
	}
}

func TestDiskSerialIsTheNameCutTo20(t *testing.T) {
	if DiskSerial("data") != "data" || DiskSerial("customer-uploads-2026-a") != "customer-uploads-202" {
		t.Errorf("serials: %q %q", DiskSerial("data"), DiskSerial("customer-uploads-2026-a"))
	}
}

func TestVolumeValidation(t *testing.T) {
	s, errs := normalize(t, KindVolume, `{"type":"disk"}`)
	if len(errs) != 0 || s.(VolumeSpec).SizeGiB != VolumeSizeDefaultGiB {
		t.Errorf("disk defaults: %+v %v", s, errs)
	}
	if s, errs := normalize(t, KindVolume, `{"type":"filesystem","snapshot":"before"}`); len(errs) != 0 || s.(VolumeSpec).SizeGiB != 0 {
		t.Errorf("filesystem from a snapshot: %+v %v", s, errs)
	}
	cases := map[string]string{
		`{}`:                                 "spec.type:REQUIRED",
		`{"type":"nfs"}`:                     "spec.type:NOT_ALLOWED",
		`{"type":"disk","sizeGiB":4096}`:     "spec.sizeGiB:OUT_OF_RANGE",
		`{"type":"filesystem","sizeGiB":10}`: "spec.sizeGiB:NOT_ALLOWED",
		`{"type":"disk","snapshot":"A b"}`:   "spec.snapshot:INVALID_FORMAT",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindVolume, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
}

func TestSnapshotValidation(t *testing.T) {
	for _, src := range []string{"volume/data", "vm/worker"} {
		if _, errs := normalize(t, KindSnapshot, `{"source":"`+src+`"}`); len(errs) != 0 {
			t.Errorf("%s refused: %v", src, errs)
		}
	}
	cases := map[string]string{
		`{}`:                        "spec.source:REQUIRED",
		`{"source":"app/web"}`:      "spec.source:INVALID_FORMAT",
		`{"source":"volume"}`:       "spec.source:INVALID_FORMAT",
		`{"source":"volume/Bad X"}`: "spec.source:INVALID_FORMAT",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindSnapshot, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
}

func TestMountsOnAppsAndJobs(t *testing.T) {
	ok := `{"image":"nginx:1.27","volumes":[{"volume":"uploads","path":"/usr/share/nginx/html"},{"volume":"cache","path":"/cache","readOnly":true}]}`
	if _, errs := normalize(t, KindApp, ok); len(errs) != 0 {
		t.Fatalf("valid mounts refused: %v", errs)
	}
	cases := map[string]string{
		`[{"volume":"uploads","path":"data"}]`:                                    "spec.volumes[0].path:INVALID_FORMAT",
		`[{"volume":"uploads","path":"/data/../etc"}]`:                            "spec.volumes[0].path:INVALID_FORMAT",
		`[{"volume":"uploads","path":"/data/"}]`:                                  "spec.volumes[0].path:INVALID_FORMAT",
		`[{"volume":"uploads","path":"/"}]`:                                       "spec.volumes[0].path:NOT_ALLOWED",
		`[{"volume":"uploads","path":"/a:b"}]`:                                    "spec.volumes[0].path:INVALID_FORMAT",
		`[{"volume":"Up","path":"/data"}]`:                                        "spec.volumes[0].volume:INVALID_FORMAT",
		`[{"volume":"uploads","path":"/data"},{"volume":"other","path":"/data"}]`: "spec.volumes[1].path:NOT_ALLOWED",
	}
	for mounts, want := range cases {
		if _, errs := normalize(t, KindApp, `{"image":"nginx:1.27","volumes":`+mounts+`}`); !slices.Contains(errs, want) {
			t.Errorf("app %s: want %s, got %v", mounts, want, errs)
		}
		if _, errs := normalize(t, KindJob, `{"schedule":"@daily","image":"alpine:3.20","volumes":`+mounts+`}`); !slices.Contains(errs, want) {
			t.Errorf("job %s: want %s, got %v", mounts, want, errs)
		}
	}
}

func TestComputeImmutableAndGrowOnly(t *testing.T) {
	spec := func(kind Kind, raw string) any {
		s, errs, err := NormalizeSpec(kind, json.RawMessage(raw))
		if err != nil || len(errs) != 0 {
			t.Fatalf("%s: %v %v", raw, err, errs)
		}
		return s
	}
	vm := spec(KindVM, vmJSON(t, nil))
	if v := ImmutableViolations(KindVM, vm, spec(KindVM, vmJSON(t, func(m map[string]any) { m["cpus"], m["diskGiB"], m["power"] = 2, 20, "stopped" }))); len(v) != 0 {
		t.Errorf("mutable VM fields refused: %v", v)
	}
	for field, mutate := range map[string]func(m map[string]any){
		"spec.image":    func(m map[string]any) { m["image"] = ImageUbuntu2404 },
		"spec.sshKeys":  func(m map[string]any) { m["sshKeys"] = []string{testKey, testKey} },
		"spec.userData": func(m map[string]any) { m["userData"] = "#!/bin/sh\ntrue" },
	} {
		if v := ImmutableViolations(KindVM, vm, spec(KindVM, vmJSON(t, mutate))); len(v) != 1 || v[0].Field != field || v[0].Code != FieldImmutable {
			t.Errorf("%s: %v", field, v)
		}
	}
	big := spec(KindVM, vmJSON(t, func(m map[string]any) { m["diskGiB"] = 20 }))
	if v := GrowOnlyViolations(KindVM, big, vm); len(v) != 1 || v[0].Code != FieldGrowOnly {
		t.Errorf("shrinking the root disk: %v", v)
	}
	disk := spec(KindVolume, `{"type":"disk","sizeGiB":50}`)
	if v := GrowOnlyViolations(KindVolume, disk, spec(KindVolume, `{"type":"disk","sizeGiB":40}`)); len(v) != 1 || v[0].Field != "spec.sizeGiB" {
		t.Errorf("shrinking a disk: %v", v)
	}
	if v := ImmutableViolations(KindVolume, disk, spec(KindVolume, `{"type":"filesystem"}`)); len(v) != 1 {
		t.Errorf("changing a volume's type: %v", v)
	}
	if v := ImmutableViolations(KindSnapshot, spec(KindSnapshot, `{"source":"vm/a-b"}`), spec(KindSnapshot, `{"source":"vm/c-d"}`)); len(v) != 1 {
		t.Errorf("changing a snapshot's source: %v", v)
	}
}

func TestVMRestartsNamesWhatRestarts(t *testing.T) {
	a := VMSpec{CPUs: 1, MemoryMiB: 1024, Disks: []string{"data"}, Network: "a", Publish: []int{80}}
	b := a
	b.Network, b.Publish, b.Power, b.DiskGiB = "b", []int{80, 443}, PowerStopped, 20
	if got := VMRestarts(a, b); len(got) != 0 {
		t.Errorf("network, publish, power and root disk restart nothing, got %v", got)
	}
	b.CPUs, b.MemoryMiB, b.Disks = 2, 2048, nil
	if got := VMRestarts(a, b); !slices.Equal(got, []string{"CPUs", "memory", "disks"}) {
		t.Errorf("VMRestarts = %v", got)
	}
}
