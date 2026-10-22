package api

import (
	"crypto/rsa"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// The v0.5 compute contract (L3-002 §8, L2-007): VMs, volumes and snapshots. Rules that need other
// resources or the machine (existence, attachment, CPU and memory limits) are checked by the service.

// VolumeMount mounts a filesystem volume into an app's or job's containers (VOL-04).
type VolumeMount struct {
	Volume   string `json:"volume"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// VMSpec is a virtual machine (ADR-025). Image, keys and the first-boot script are set once (CP-04).
type VMSpec struct {
	// Image is a catalog name, an https:// URL of a cloud image, or "snapshot/<name>" (IMG-01 – IMG-04).
	Image       string `json:"image"`
	ImageSHA256 string `json:"imageSha256,omitempty"`
	// Exactly one of SSHKeys and SSHKeySecret (a secret holding one key per line, VM-03).
	SSHKeys      []string `json:"sshKeys,omitempty"`
	SSHKeySecret string   `json:"sshKeySecret,omitempty"`
	// UserData is the first-boot script: a cloud-init document or a shell script (VM-04).
	UserData  string `json:"userData,omitempty"`
	CPUs      int    `json:"cpus,omitempty"`
	MemoryMiB int    `json:"memoryMiB,omitempty"`
	DiskGiB   int    `json:"diskGiB,omitempty"`
	// Network and Subnet place the VM's link container (ADR-026); empty means the default network.
	Network string `json:"network,omitempty"`
	Subnet  string `json:"subnet,omitempty"`
	// Disks names disk volumes, in attach order (VOL-01).
	Disks []string `json:"disks,omitempty"`
	// Publish lists VM ports published on the same host port (VM-10).
	Publish []int  `json:"publish,omitempty"`
	Power   string `json:"power,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// VolumeSpec is storage that lives on its own (ADR-027).
type VolumeSpec struct {
	Type string `json:"type"`
	// SizeGiB is a disk's size; it only grows (VOL-03). Filesystems have no size.
	SizeGiB int `json:"sizeGiB,omitempty"`
	// Snapshot names a snapshot to start from (VOL-08).
	Snapshot string `json:"snapshot,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// SnapshotSpec copies a volume or a VM's root disk (ADR-028).
type SnapshotSpec struct {
	Source string `json:"source"` // "volume/<name>" or "vm/<name>"
}

// VM power states (VM-06). PowerStarting and PowerStopping appear only in outputs.
const (
	PowerRunning  = "running"
	PowerStopped  = "stopped"
	PowerStarting = "starting"
	PowerStopping = "stopping"
)

// Volume types (ADR-027).
const (
	VolumeDisk       = "disk"
	VolumeFilesystem = "filesystem"
)

// VM actions (VM-06); restart is ActionRestart.
const (
	ActionStart = "start"
	ActionStop  = "stop"
)

// Catalog images (IMG-01). Their addresses and checksums live with the hypervisor (ADR-025).
const (
	ImageUbuntu2404 = "ubuntu-24.04"
	ImageDebian12   = "debian-12"
	// SnapshotImagePrefix starts an image that is a VM snapshot (IMG-04).
	SnapshotImagePrefix = "snapshot/"
)

// Validation limits (L3-002 §8.4), exported for the console.
const (
	VMCPUsMin, VMCPUsMax, VMCPUsDefault                      = 1, 16, 1
	VMMemoryMinMiB, VMMemoryMaxMiB, VMMemoryDefaultMiB       = 512, 65536, 1024
	VMMemoryStepMiB                                          = 128
	VMMemoryHostReserveMiB                                   = 2048
	VMDiskMinGiB, VMDiskMaxGiB, VMDiskDefaultGiB             = 5, 2048, 10
	SSHKeysMax, SSHKeyMaxLen, RSAKeyMinBits                  = 10, 16384, 2048
	UserDataMaxBytes                                         = 65536
	VMDisksMax, VMPublishMax                                 = 8, 10
	DiskSerialLen                                            = 20
	ImageURLMaxLen                                           = 2048
	VolumeSizeMinGiB, VolumeSizeMaxGiB, VolumeSizeDefaultGiB = 1, 2048, 10
	MountsMax, MountPathMaxLen                               = 10, 255
	SSHPort                                                  = 22
)

// Allowed values (L3-002 §8.2).
var (
	ImageCatalog   = []string{ImageUbuntu2404, ImageDebian12}
	PowerStates    = []string{PowerRunning, PowerStopped}
	VolumeTypes    = []string{VolumeDisk, VolumeFilesystem}
	SnapshotKinds  = []Kind{KindVolume, KindVM}
	sha256Pattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	sshKeyTypes    = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoRSA, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256}
	userDataStarts = []string{"#cloud-config", "#!"}
)

// SnapshotImage returns the snapshot a VM image names, or "".
func SnapshotImage(image string) string {
	if name, ok := strings.CutPrefix(image, SnapshotImagePrefix); ok {
		return name
	}
	return ""
}

// IsURLImage reports whether an image is a download address.
func IsURLImage(image string) bool { return strings.HasPrefix(image, "https://") }

// DiskSerial is the serial a disk shows inside its VM: /dev/disk/by-id/virtio-<serial> (VOL-01).
func DiskSerial(volume string) string {
	if len(volume) > DiskSerialLen {
		return volume[:DiskSerialLen]
	}
	return volume
}

func normalizeVM(s VMSpec) VMSpec {
	s.Image = strings.TrimSpace(s.Image)
	s.ImageSHA256 = strings.ToLower(strings.TrimSpace(s.ImageSHA256))
	for i, k := range s.SSHKeys {
		s.SSHKeys[i] = strings.TrimSpace(k)
	}
	if s.CPUs == 0 {
		s.CPUs = VMCPUsDefault
	}
	if s.MemoryMiB == 0 {
		s.MemoryMiB = VMMemoryDefaultMiB
	}
	if s.DiskGiB == 0 {
		s.DiskGiB = VMDiskDefaultGiB
	}
	if s.Power == "" {
		s.Power = PowerRunning
	}
	return s
}

func validateVM(s VMSpec) []FieldError {
	var errs []FieldError
	errs = append(errs, validateVMImage(s)...)
	switch {
	case len(s.SSHKeys) > 0 && s.SSHKeySecret != "":
		errs = append(errs, FieldError{Field: "spec.sshKeys", Code: FieldExactlyOne, Message: "Give the SSH keys here or in a secret — choose one."})
	case s.SSHKeySecret != "":
		if ValidateName(s.SSHKeySecret) != nil {
			errs = append(errs, FieldError{Field: "spec.sshKeySecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
		}
	default:
		errs = append(errs, ValidateSSHKeys(s.SSHKeys, "spec.sshKeys")...)
	}
	if s.UserData != "" {
		switch {
		case len(s.UserData) > UserDataMaxBytes:
			errs = append(errs, FieldError{Field: "spec.userData", Code: FieldTooLong, Message: "The first-boot script can be at most 64 KiB."})
		case !utf8.ValidString(s.UserData):
			errs = append(errs, FieldError{Field: "spec.userData", Code: FieldInvalidFormat, Message: "The first-boot script must be text (UTF-8)."})
		case !slices.ContainsFunc(userDataStarts, func(p string) bool { return strings.HasPrefix(s.UserData, p) }):
			errs = append(errs, FieldError{Field: "spec.userData", Code: FieldInvalidFormat, Message: "Start the first-boot script with #cloud-config or a #! line, like #!/bin/sh."})
		}
	}
	if s.CPUs < VMCPUsMin || s.CPUs > VMCPUsMax {
		errs = append(errs, FieldError{Field: "spec.cpus", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d CPUs.", VMCPUsMin, VMCPUsMax)})
	}
	if s.MemoryMiB < VMMemoryMinMiB || s.MemoryMiB > VMMemoryMaxMiB || s.MemoryMiB%VMMemoryStepMiB != 0 {
		errs = append(errs, FieldError{Field: "spec.memoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB in steps of %d.", VMMemoryMinMiB, VMMemoryMaxMiB, VMMemoryStepMiB)})
	}
	if s.DiskGiB < VMDiskMinGiB || s.DiskGiB > VMDiskMaxGiB {
		errs = append(errs, FieldError{Field: "spec.diskGiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d GiB.", VMDiskMinGiB, VMDiskMaxGiB)})
	}
	errs = append(errs, validatePlacement(s.Network, s.Subnet)...)
	if len(s.Disks) > VMDisksMax {
		errs = append(errs, FieldError{Field: "spec.disks", Code: FieldTooLong, Message: fmt.Sprintf("Attach at most %d disks.", VMDisksMax)})
	}
	serials := map[string]string{}
	for i, d := range s.Disks {
		f := fmt.Sprintf("spec.disks[%d]", i)
		if ValidateName(d) != nil {
			errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Name an existing disk volume."})
			continue
		}
		if prev, dup := serials[DiskSerial(d)]; dup {
			msg := "This disk is already attached."
			if prev != d {
				msg = fmt.Sprintf("%s and %s start with the same 20 characters, so the VM couldn't tell them apart. Rename one.", prev, d)
			}
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: msg})
		}
		serials[DiskSerial(d)] = d
	}
	if len(s.Publish) > VMPublishMax {
		errs = append(errs, FieldError{Field: "spec.publish", Code: FieldTooLong, Message: fmt.Sprintf("Publish at most %d ports.", VMPublishMax)})
	}
	for i, p := range s.Publish {
		f := fmt.Sprintf("spec.publish[%d]", i)
		switch {
		case p < PortMin || p > PortMax:
			errs = append(errs, FieldError{Field: f, Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
		case p == SSHPort:
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "SSH is already reachable through the VM's SSH address; publish other ports."})
		case slices.Index(s.Publish, p) < i:
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "This port is listed twice."})
		}
	}
	if !slices.Contains(PowerStates, s.Power) {
		errs = append(errs, FieldError{Field: "spec.power", Code: FieldNotAllowed, Message: "Choose running or stopped."})
	}
	return errs
}

func validateVMImage(s VMSpec) []FieldError {
	var errs []FieldError
	switch {
	case s.Image == "":
		errs = append(errs, FieldError{Field: "spec.image", Code: FieldRequired, Message: "Choose an image: ubuntu-24.04, debian-12, an https:// URL or snapshot/<name>."})
	case slices.Contains(ImageCatalog, s.Image):
	case SnapshotImage(s.Image) != "" || s.Image == SnapshotImagePrefix:
		if ValidateName(SnapshotImage(s.Image)) != nil {
			errs = append(errs, FieldError{Field: "spec.image", Code: FieldInvalidFormat, Message: "Name an existing snapshot, like snapshot/worker-1."})
		}
	case IsURLImage(s.Image):
		if u, err := url.Parse(s.Image); err != nil || u.Host == "" || len(s.Image) > ImageURLMaxLen {
			errs = append(errs, FieldError{Field: "spec.image", Code: FieldInvalidFormat, Message: "Use an https:// address of a cloud image (qcow2 or raw), up to 2048 characters."})
		}
	default:
		errs = append(errs, FieldError{Field: "spec.image", Code: FieldNotAllowed, Message: "Choose ubuntu-24.04, debian-12, an https:// URL of a cloud image, or snapshot/<name>."})
	}
	if s.ImageSHA256 != "" {
		switch {
		case !IsURLImage(s.Image):
			errs = append(errs, FieldError{Field: "spec.imageSha256", Code: FieldNotAllowed, Message: "Only an image from a URL takes a checksum; catalog images and snapshots are checked already."})
		case !sha256Pattern.MatchString(s.ImageSHA256):
			errs = append(errs, FieldError{Field: "spec.imageSha256", Code: FieldInvalidFormat, Message: "Use the image's SHA-256: 64 hexadecimal characters."})
		}
	}
	return errs
}

// ValidateSSHKeys checks OpenSSH public keys, one per entry (VM-03). The service also checks a key
// secret's value with it.
func ValidateSSHKeys(keys []string, field string) []FieldError {
	if len(keys) == 0 {
		return []FieldError{{Field: field, Code: FieldRequired, Message: "Add at least one SSH public key, like the contents of ~/.ssh/id_ed25519.pub."}}
	}
	if len(keys) > SSHKeysMax {
		return []FieldError{{Field: field, Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d SSH keys.", SSHKeysMax)}}
	}
	var errs []FieldError
	for i, k := range keys {
		f := fmt.Sprintf("%s[%d]", field, i)
		if msg := sshKeyProblem(k); msg != "" {
			errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: msg})
		}
	}
	return errs
}

func sshKeyProblem(k string) string {
	const want = "Paste a public key in OpenSSH format, like ssh-ed25519 AAAA… you@laptop (the .pub file, never the private key)."
	if k == "" || len(k) > SSHKeyMaxLen || strings.ContainsAny(k, "\r\n") {
		return want
	}
	if strings.Contains(k, "PRIVATE KEY") {
		return "This is a private key. Paste the public key instead (the .pub file); never share the private one."
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(k))
	if err != nil || len(rest) > 0 || !slices.Contains(sshKeyTypes, pub.Type()) {
		return want
	}
	if c, ok := pub.(ssh.CryptoPublicKey); ok {
		if r, ok := c.CryptoPublicKey().(*rsa.PublicKey); ok && r.N.BitLen() < RSAKeyMinBits {
			return fmt.Sprintf("This RSA key has %d bits; use at least %d, or an ed25519 key.", r.N.BitLen(), RSAKeyMinBits)
		}
	}
	return ""
}

// SplitKeys turns a key secret's value into keys: one per line, blank lines and # comments skipped.
func SplitKeys(value string) []string {
	var keys []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			keys = append(keys, line)
		}
	}
	return keys
}

func normalizeVolume(s VolumeSpec) VolumeSpec {
	if s.Type == VolumeDisk && s.SizeGiB == 0 {
		s.SizeGiB = VolumeSizeDefaultGiB
	}
	return s
}

func validateVolume(s VolumeSpec) []FieldError {
	var errs []FieldError
	switch s.Type {
	case "":
		errs = append(errs, FieldError{Field: "spec.type", Code: FieldRequired, Message: "Choose disk (for VMs) or filesystem (for apps and jobs)."})
	case VolumeDisk:
		if s.SizeGiB < VolumeSizeMinGiB || s.SizeGiB > VolumeSizeMaxGiB {
			errs = append(errs, FieldError{Field: "spec.sizeGiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d GiB.", VolumeSizeMinGiB, VolumeSizeMaxGiB)})
		}
	case VolumeFilesystem:
		if s.SizeGiB != 0 {
			errs = append(errs, FieldError{Field: "spec.sizeGiB", Code: FieldNotAllowed, Message: "A filesystem volume grows with its files; it has no size to set."})
		}
	default:
		errs = append(errs, FieldError{Field: "spec.type", Code: FieldNotAllowed, Message: "Choose disk or filesystem."})
	}
	if s.Snapshot != "" && ValidateName(s.Snapshot) != nil {
		errs = append(errs, FieldError{Field: "spec.snapshot", Code: FieldInvalidFormat, Message: "Name an existing snapshot."})
	}
	return errs
}

// ParseSnapshotSource splits a snapshot's source into its kind (volume or vm) and name.
func ParseSnapshotSource(s string) (Kind, string, bool) {
	kind, name, ok := strings.Cut(s, "/")
	if !ok || !slices.Contains(SnapshotKinds, Kind(kind)) || ValidateName(name) != nil {
		return "", "", false
	}
	return Kind(kind), name, true
}

func validateSnapshot(s SnapshotSpec) []FieldError {
	if s.Source == "" {
		return []FieldError{{Field: "spec.source", Code: FieldRequired, Message: "Name what to copy: volume/<name> or vm/<name>."}}
	}
	if _, _, ok := ParseSnapshotSource(s.Source); !ok {
		return []FieldError{{Field: "spec.source", Code: FieldInvalidFormat, Message: "Use volume/<name> or vm/<name>."}}
	}
	return nil
}

// validateMounts checks an app's or job's volume mounts (L3-002 §8.4); existence and type are the
// service's job.
func validateMounts(mounts []VolumeMount) []FieldError {
	var errs []FieldError
	if len(mounts) > MountsMax {
		errs = append(errs, FieldError{Field: "spec.volumes", Code: FieldTooLong, Message: fmt.Sprintf("Mount at most %d volumes.", MountsMax)})
	}
	for i, m := range mounts {
		f := fmt.Sprintf("spec.volumes[%d]", i)
		if ValidateName(m.Volume) != nil {
			errs = append(errs, FieldError{Field: f + ".volume", Code: FieldInvalidFormat, Message: "Name an existing filesystem volume."})
		}
		switch {
		case !strings.HasPrefix(m.Path, "/") || path.Clean(m.Path) != m.Path || len(m.Path) > MountPathMaxLen || strings.ContainsAny(m.Path, ":\\\n"):
			errs = append(errs, FieldError{Field: f + ".path", Code: FieldInvalidFormat, Message: "Use an absolute path inside the container, like /data or /var/www/uploads."})
		case m.Path == "/":
			errs = append(errs, FieldError{Field: f + ".path", Code: FieldNotAllowed, Message: "Mount a volume on a folder, not on /."})
		case slices.IndexFunc(mounts, func(o VolumeMount) bool { return o.Path == m.Path }) < i:
			errs = append(errs, FieldError{Field: f + ".path", Code: FieldNotAllowed, Message: "Another volume is already mounted here."})
		}
	}
	return errs
}

// GrowOnlyViolations reports sizes an update lowers (VM-02, VOL-03).
func GrowOnlyViolations(kind Kind, before, after any) []FieldError {
	switch kind {
	case KindVM:
		if after.(VMSpec).DiskGiB < before.(VMSpec).DiskGiB {
			return []FieldError{{Field: "spec.diskGiB", Code: FieldGrowOnly, Message: fmt.Sprintf("The root disk can only grow; it is %d GiB now.", before.(VMSpec).DiskGiB)}}
		}
	case KindVolume:
		if after.(VolumeSpec).SizeGiB < before.(VolumeSpec).SizeGiB {
			return []FieldError{{Field: "spec.sizeGiB", Code: FieldGrowOnly, Message: fmt.Sprintf("A disk can only grow; it is %d GiB now.", before.(VolumeSpec).SizeGiB)}}
		}
	}
	return nil
}

// VMRestarts lists what in an update makes a running VM restart (VM-01, VOL-02); the console and CLI
// say so before sending it (CON-33).
func VMRestarts(before, after VMSpec) []string {
	var why []string
	if before.CPUs != after.CPUs {
		why = append(why, "CPUs")
	}
	if before.MemoryMiB != after.MemoryMiB {
		why = append(why, "memory")
	}
	if !slices.Equal(before.Disks, after.Disks) {
		why = append(why, "disks")
	}
	return why
}
