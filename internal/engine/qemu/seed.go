package qemu

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
	"go.yaml.in/yaml/v3"

	"github.com/enclavestack/platform/internal/engine"
)

// The NoCloud seed (ADR-025): an ISO 9660 image labelled cidata with Rock Ridge names, written in pure
// Go. The instance ID is the VM's ID and never changes, so cloud-init runs first-boot steps once.

// The VM's side of its link to the link container (ADR-026): every link uses the same pair, each in its
// own namespace, from the benchmarking range no network may use.
const (
	LinkGateway = "198.18.0.1"
	LinkAddress = "198.18.0.2"
	LinkPrefix  = 30
	// SSHUser is the user created on every VM (VM-03).
	SSHUser = "enclave"
)

// MAC is the VM's stable link MAC: QEMU's 52:54:00 prefix and three bytes from its ID.
func MAC(vmID string) string {
	s := sha256.Sum256([]byte(vmID))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", s[0], s[1], s[2])
}

// seedFiles are the NoCloud documents.
func seedFiles(in engine.VMInput) (map[string][]byte, error) {
	meta := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", in.ID, in.Name)
	network, err := yaml.Marshal(map[string]any{
		"version": 2,
		"ethernets": map[string]any{"eth0": map[string]any{
			"match":       map[string]any{"macaddress": MAC(in.ID)},
			"set-name":    "eth0",
			"addresses":   []string{fmt.Sprintf("%s/%d", LinkAddress, LinkPrefix)},
			"routes":      []any{map[string]any{"to": "0.0.0.0/0", "via": LinkGateway, "on-link": true}},
			"nameservers": map[string]any{"addresses": []string{LinkGateway}},
		}},
	})
	if err != nil {
		return nil, err
	}
	user, err := userData(in)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"meta-data": []byte(meta), "network-config": network, "user-data": user}, nil
}

// enclaveConfig creates the enclave user and gives the image's default user the same keys (VM-03).
// It is the last part and appends to lists, so a user's own cloud-config keeps its users and keys.
func enclaveConfig(in engine.VMInput) ([]byte, error) {
	b, err := yaml.Marshal(map[string]any{
		"merge_how": []any{
			map[string]any{"name": "list", "settings": []string{"append"}},
			map[string]any{"name": "dict", "settings": []string{"no_replace", "recurse_list"}},
		},
		"users": []any{"default", map[string]any{
			"name": SSHUser, "gecos": "EnclaveStack", "shell": "/bin/bash", "lock_passwd": true,
			"sudo": "ALL=(ALL) NOPASSWD:ALL", "ssh_authorized_keys": in.SSHKeys,
		}},
		"ssh_authorized_keys": in.SSHKeys,
		"ssh_pwauth":          false,
		"disable_root":        true,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte("#cloud-config\n"), b...), nil
}

// userData is EnclaveStack's cloud-config alone, or a MIME multi-part document with the user's script
// first, unchanged (VM-04).
func userData(in engine.VMInput) ([]byte, error) {
	ours, err := enclaveConfig(in)
	if err != nil {
		return nil, err
	}
	if in.Spec.UserData == "" {
		return ours, nil
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	theirs := "text/x-shellscript"
	if strings.HasPrefix(in.Spec.UserData, "#cloud-config") {
		theirs = "text/cloud-config"
	}
	for _, part := range []struct {
		typ, name string
		data      []byte
	}{{theirs, "user-data", []byte(in.Spec.UserData)}, {"text/cloud-config", "enclave.cfg", ours}} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", part.typ+`; charset="utf-8"`)
		h.Set("Content-Disposition", `attachment; filename="`+part.name+`"`)
		h.Set("MIME-Version", "1.0")
		w, err := mw.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(part.data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	head := fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\nMIME-Version: 1.0\n\n", mw.Boundary())
	return append([]byte(head), body.Bytes()...), nil
}

// writeSeed writes the seed ISO at path (mode 0600, L3-006 §7).
func writeSeed(path string, files map[string][]byte) error {
	_ = os.Remove(path)
	size := int64(1 << 20)
	for _, b := range files {
		size += int64(len(b)) + 2048
	}
	d, err := diskfs.Create(path, size, diskfs.SectorSizeDefault)
	if err != nil {
		return fmt.Errorf("create seed: %w", err)
	}
	defer d.Close()
	d.LogicalBlocksize = 2048
	fs, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 0, FSType: filesystem.TypeISO9660, VolumeLabel: "cidata"})
	if err != nil {
		return fmt.Errorf("create seed filesystem: %w", err)
	}
	for _, name := range []string{"meta-data", "network-config", "user-data"} {
		f, err := fs.OpenFile("/"+name, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return err
		}
		if _, err := f.Write(files[name]); err != nil {
			return err
		}
	}
	iso, ok := fs.(*iso9660.FileSystem)
	if !ok {
		return fmt.Errorf("seed filesystem isn't ISO 9660")
	}
	if err := iso.Finalize(iso9660.FinalizeOptions{RockRidge: true, VolumeIdentifier: "cidata"}); err != nil {
		return fmt.Errorf("finalize seed: %w", err)
	}
	return os.Chmod(path, 0o600)
}
