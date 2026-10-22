// Package backup stores backup objects in S3-compatible storage — a EnclaveStack bucket or external S3 —
// and applies retention (ADR-018, L2-005 BAK-02 – BAK-04). Uploads stream: a dump is never held in full
// by the control plane.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Target is a bucket and how to reach it.
type Target struct {
	Endpoint  string // scheme://host[:port]
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// Describe names the target for messages and artifacts, without credentials.
func (t Target) Describe() string { return strings.TrimSuffix(t.Endpoint, "/") + "/" + t.Bucket }

// Object is one stored backup.
type Object struct {
	Key      string
	Size     int64
	SHA256   string
	Modified time.Time
}

// Store reads and writes backup objects in one bucket.
type Store interface {
	// Put streams r to key and returns its size and SHA-256. A failed upload leaves nothing behind.
	Put(ctx context.Context, key string, r io.Reader, meta map[string]string) (Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]Object, error)
	Delete(ctx context.Context, key string) error
}

// Opener connects to a target.
type Opener interface {
	Open(ctx context.Context, t Target) (Store, error)
}

// ErrNotFound means the object doesn't exist.
var ErrNotFound = errors.New("the backup object doesn't exist")

// Key builds an object key: <prefix>/<source>/<UTC timestamp, ms>-<run id>.<ext> (BAK-02). Keys sort by
// time, which retention relies on; milliseconds keep two runs in the same second in order.
func Key(prefix, source string, at time.Time, runID, ext string) string {
	return fmt.Sprintf("%s/%s/%s-%s.%s", strings.Trim(prefix, "/"), source, at.UTC().Format(keyTime), runID, ext)
}

// keyTime is the timestamp layout in object keys.
const keyTime = "20060102T150405.000Z"

// ours matches the base names Key produces, so retention never touches anyone else's objects.
var ours = regexp.MustCompile(`^(\d{8}T\d{6}\.\d{3}Z)-(run_[a-z2-7]{16})\.(pgdump|rdb|sql|archive\.gz|zip)$`)

// RunIDOf returns the run ID in a key Key produced, or "".
func RunIDOf(key string) string {
	if m := ours.FindStringSubmatch(path.Base(key)); m != nil {
		return m[2]
	}
	return ""
}

// Retain deletes a source's backups beyond the newest keep, and (keepDays > 0) those older than
// keepDays. Only objects whose run owns reports true are counted or deleted (nil: every object Key
// produced), so two backups of one source into the same place never delete each other's. The newest is
// never deleted. It returns the deleted keys.
func Retain(ctx context.Context, s Store, prefix, source string, keep, keepDays int, now time.Time, owns func(runID string) bool) ([]string, error) {
	objs, err := s.List(ctx, strings.Trim(prefix, "/")+"/"+source+"/")
	if err != nil {
		return nil, err
	}
	type dated struct {
		key string
		at  time.Time
	}
	var mine []dated
	for _, o := range objs {
		m := ours.FindStringSubmatch(path.Base(o.Key))
		if m == nil || (owns != nil && !owns(m[2])) {
			continue
		}
		at, err := time.Parse(keyTime, m[1])
		if err != nil {
			continue
		}
		mine = append(mine, dated{o.Key, at})
	}
	slices.SortFunc(mine, func(a, b dated) int { return strings.Compare(b.key, a.key) }) // newest first: keys sort by time
	var deleted []string
	for i, o := range mine {
		old := keepDays > 0 && now.Sub(o.at) > time.Duration(keepDays)*24*time.Hour
		if i == 0 || (i < keep && !old) {
			continue
		}
		if err := s.Delete(ctx, o.key); err != nil {
			return deleted, err
		}
		deleted = append(deleted, o.key)
	}
	return deleted, nil
}

// hashingReader counts and hashes what passes through it.
type hashingReader struct {
	r    io.Reader
	h    hash.Hash
	size int64
}

func newHashingReader(r io.Reader) *hashingReader { return &hashingReader{r: r, h: sha256.New()} }

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	h.size += int64(n)
	return n, err
}

func (h *hashingReader) sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

// VerifyingCopy copies src to dst and reports whether the SHA-256 of what was copied equals want.
func VerifyingCopy(dst io.Writer, src io.Reader, want string) (int64, error) {
	h := newHashingReader(src)
	n, err := io.Copy(dst, h)
	if err != nil {
		return n, err
	}
	if want != "" && h.sum() != want {
		return n, fmt.Errorf("the backup's checksum doesn't match (stored %s…, read %s…): the object was changed or damaged", want[:12], h.sum()[:12])
	}
	return n, nil
}
