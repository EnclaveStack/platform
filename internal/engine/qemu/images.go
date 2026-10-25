package qemu

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Images (IMG-01 – IMG-05): downloaded once into <data dir>/images/, checked, and shared by every root
// disk based on them.

// CatalogImage is one pinned image for one architecture. Exactly one checksum is set: the one the
// distribution publishes.
type CatalogImage struct {
	URL    string
	SHA256 string
	SHA512 string
	Format string // qcow2 or raw
}

// Catalog maps a catalog name and an architecture to an image.
type Catalog map[string]map[string]CatalogImage

// BuiltinCatalog pins versioned releases, never "latest" (ADR-025). Checksums are the distributions'
// own (Ubuntu SHA256SUMS, Debian SHA512SUMS), checked on 2026-10-21.
var BuiltinCatalog = Catalog{
	api.ImageUbuntu2404: {
		"arm64": {URL: "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-arm64.img",
			SHA256: "50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e", Format: "qcow2"},
		"amd64": {URL: "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img",
			SHA256: "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94", Format: "qcow2"},
	},
	api.ImageDebian12: {
		"arm64": {URL: "https://cloud.debian.org/images/cloud/bookworm/20261018-2635/debian-12-genericcloud-arm64-20261018-2635.qcow2",
			SHA512: "093252dac59ad761350592c43209f098c1495f5567a331be46033a7083a18347abc1894a81ee41cb0ce53f4b4a003b057a239722e1f40726cbd8a36c981a0995", Format: "qcow2"},
		"amd64": {URL: "https://cloud.debian.org/images/cloud/bookworm/20261018-2635/debian-12-genericcloud-amd64-20261018-2635.qcow2",
			SHA512: "dc0d7b526a3a9a9265142fe8e903984c03f8d605ac4ba6f14d15d27988a69581fb05fc06cd2a6d79f6eaf68ba9725f2eb38b5ddd64ad166faadad279af776c9e", Format: "qcow2"},
	},
}

const (
	downloadTimeout = 30 * time.Minute
	imageUnusedFor  = 7 * 24 * time.Hour
)

// imageInfo is the sidecar <key>.json next to a cached image.
type imageInfo struct {
	Name     string    `json:"name"` // catalog name or URL
	URL      string    `json:"url"`
	SHA256   string    `json:"sha256"`
	Format   string    `json:"format"`
	Arch     string    `json:"arch"`
	Size     int64     `json:"size"`
	LastUsed time.Time `json:"lastUsed"`
}

func (h *Hypervisor) imagesDir() string { return filepath.Join(h.opts.DataDir, "images") }

// imageKey names a cached image by its address, so a pinned URL is downloaded once.
func imageKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:16])
}

// cachedImage is a downloaded, checked image.
type cachedImage struct {
	path string
	info imageInfo
}

// ensureImage downloads an image if it isn't cached and returns it (IMG-02, IMG-03).
func (h *Hypervisor) ensureImage(ctx context.Context, t tools, src engine.ImageSource) (cachedImage, error) {
	name, want := src.Catalog, CatalogImage{URL: src.URL, SHA256: src.SHA256}
	if src.Catalog != "" {
		img, ok := h.opts.Catalog[src.Catalog][Arch()]
		if !ok {
			return cachedImage{}, engine.Permanent(fmt.Errorf("the catalog has no %s image for %s", src.Catalog, Arch()))
		}
		want = img
	} else {
		name = src.URL
	}
	key := imageKey(want.URL)
	dir := h.imagesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cachedImage{}, err
	}
	unlock := h.lock("image-" + key)
	defer unlock()
	sidecar := filepath.Join(dir, key+".json")
	if b, err := os.ReadFile(sidecar); err == nil {
		var info imageInfo
		if json.Unmarshal(b, &info) == nil {
			p := filepath.Join(dir, key+"."+info.Format)
			if _, err := os.Stat(p); err == nil {
				info.LastUsed = time.Now().UTC()
				h.writeSidecar(sidecar, info)
				return cachedImage{path: p, info: info}, nil
			}
		}
	}
	partial := filepath.Join(dir, key+".partial")
	got256, got512, size, err := h.download(ctx, want.URL, partial)
	if err != nil {
		_ = os.Remove(partial)
		return cachedImage{}, err
	}
	bad := func(algo, wantSum, gotSum string) error {
		_ = os.Remove(partial)
		return engine.Permanent(fmt.Errorf("the image from %s doesn't match its checksum: expected %s %s, got %s. Check the address and the checksum", want.URL, algo, wantSum, gotSum))
	}
	if want.SHA256 != "" && want.SHA256 != got256 {
		return cachedImage{}, bad("SHA-256", want.SHA256, got256)
	}
	if want.SHA512 != "" && want.SHA512 != got512 {
		return cachedImage{}, bad("SHA-512", want.SHA512, got512)
	}
	format, err := imageFormat(ctx, t, partial)
	if err != nil {
		_ = os.Remove(partial)
		return cachedImage{}, err
	}
	final := filepath.Join(dir, key+"."+format)
	if err := os.Rename(partial, final); err != nil {
		return cachedImage{}, err
	}
	info := imageInfo{Name: name, URL: want.URL, SHA256: got256, Format: format, Arch: Arch(), Size: size, LastUsed: time.Now().UTC()}
	h.writeSidecar(sidecar, info)
	return cachedImage{path: final, info: info}, nil
}

func (h *Hypervisor) writeSidecar(path string, info imageInfo) {
	b, _ := json.Marshal(info)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		h.log.Warn("write image sidecar failed", "path", path, "error", err)
	}
}

// download streams url into path, hashing as it goes. Network errors are transient; 4xx answers are
// permanent (IMG-02).
func (h *Hypervisor) download(ctx context.Context, url, path string) (sum256, sum512 string, size int64, err error) {
	dctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", 0, engine.Permanent(fmt.Errorf("the image address %s is invalid: %w", url, err))
	}
	req.Header.Set("User-Agent", "enclave")
	resp, err := h.opts.HTTP.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return "", "", 0, engine.Permanent(fmt.Errorf("download %s: the server answered %s", url, resp.Status))
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("download %s: the server answered %s", url, resp.Status)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", 0, err
	}
	h256, h512 := sha256.New(), sha512.New()
	size, err = io.Copy(io.MultiWriter(f, h256, h512), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", "", 0, fmt.Errorf("download %s: %w", url, err)
	}
	return hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h512.Sum(nil)), size, nil
}

// imageFormat asks qemu-img what a file is; only qcow2 and raw images are accepted (IMG-03).
func imageFormat(ctx context.Context, t tools, path string) (string, error) {
	info, err := diskInfo(ctx, t, path)
	if err != nil {
		return "", err
	}
	if info.Format != "qcow2" && info.Format != "raw" {
		return "", engine.Permanent(fmt.Errorf("the image is %s; use a qcow2 or raw cloud image", info.Format))
	}
	return info.Format, nil
}

// imageUsers counts the VMs whose root disk is based on each cached image.
func (h *Hypervisor) imageUsers() map[string]int {
	users := map[string]int{}
	dirs, _ := os.ReadDir(filepath.Join(h.opts.DataDir, "vm"))
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(h.opts.DataDir, "vm", d.Name(), "image")); err == nil {
			users[strings.TrimSpace(string(b))]++
		}
	}
	return users
}

func (h *Hypervisor) cachedImages() []api.CachedImage {
	users := h.imageUsers()
	entries, _ := os.ReadDir(h.imagesDir())
	var out []api.CachedImage
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		var info imageInfo
		if b, err := os.ReadFile(filepath.Join(h.imagesDir(), e.Name())); err != nil || json.Unmarshal(b, &info) != nil {
			continue
		}
		out = append(out, api.CachedImage{Name: info.Name, Arch: info.Arch, SizeBytes: info.Size, UsedBy: users[key]})
	}
	slices.SortFunc(out, func(a, b api.CachedImage) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// pruneImages removes images no VM uses that weren't used for imageUnusedFor (IMG-05).
func (h *Hypervisor) pruneImages() {
	users := h.imageUsers()
	entries, _ := os.ReadDir(h.imagesDir())
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || users[key] > 0 {
			continue
		}
		var info imageInfo
		sidecar := filepath.Join(h.imagesDir(), e.Name())
		if b, err := os.ReadFile(sidecar); err != nil || json.Unmarshal(b, &info) != nil || time.Since(info.LastUsed) < imageUnusedFor {
			continue
		}
		unlock := h.lock("image-" + key)
		if err := os.Remove(filepath.Join(h.imagesDir(), key+"."+info.Format)); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(sidecar)
			h.log.Info("removed unused image", "image", info.Name)
		}
		unlock()
	}
}
