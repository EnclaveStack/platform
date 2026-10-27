package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The standby control plane, agent side (ADR-057): every agent keeps the highest epoch it has seen and
// refuses a control plane with a lower one; a standby's agent also keeps the main machine's copies.

const (
	pathStandbyCopy  = "/agent/v1/standby/copy"
	pathStandbyState = "/agent/v1/standby/state"

	// HeaderEpoch carries the control plane's epoch on every call.
	HeaderEpoch   = "X-Enclave-Epoch"
	headerFenced  = "X-Enclave-Fenced"
	headerSHA256  = "X-Enclave-SHA256"
	headerTakenAt = "X-Enclave-Taken-At"

	classFenced = "fenced"

	// StandbyCopiesKept is how many copies a standby keeps; standbyCopyMax bounds one.
	StandbyCopiesKept = 2
	standbyCopyMax    = 16 << 30

	// The files in a copy (ADR-057).
	CopyManifest = "manifest.json"
	CopyDatabase = "enclave.db"
	CopyKey      = "master.key"
)

// ErrFenced is a call from a control plane a promoted standby replaced.
var ErrFenced = errors.New("this control plane was replaced by a promoted standby")

// Manifest is what a copy says about itself.
type Manifest struct {
	Version   string    `json:"version"`
	MachineID string    `json:"machineId"` // the main machine it was taken on
	Epoch     int64     `json:"epoch"`
	TakenAt   time.Time `json:"takenAt"`
	SHA256    string    `json:"sha256"` // of enclave.db
}

// StandbyCopy is one copy a standby holds.
type StandbyCopy struct {
	N       int       `json:"n"`
	TakenAt time.Time `json:"takenAt"`
	Bytes   int64     `json:"bytes"`
	SHA256  string    `json:"sha256"` // of the archive
}

// StandbyState is GET /agent/v1/standby/state.
type StandbyState struct {
	Copies        []StandbyCopy `json:"copies"`
	LastContactAt *time.Time    `json:"lastContactAt,omitempty"`
}

func epochFile(stateDir string) string  { return filepath.Join(stateDir, "epoch") }
func standbyDir(stateDir string) string { return filepath.Join(stateDir, "standby") }
func copyName(n int, ext string) string { return fmt.Sprintf("copy-%d%s", n, ext) }

// ReadEpoch is the highest epoch this agent has seen (0 with none).
func ReadEpoch(stateDir string) int64 {
	b, err := os.ReadFile(epochFile(stateDir))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// fence refuses a call from a lower epoch and adopts a higher one; calls without the header (tools, tests
// of earlier protocol versions) pass.
func (s *Server) fence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get(HeaderEpoch); v != "" {
			e, err := strconv.ParseInt(v, 10, 64)
			if err != nil || e < 0 {
				http.Error(w, "bad epoch", http.StatusBadRequest)
				return
			}
			if err := s.seeEpoch(e); err != nil {
				w.Header().Set(headerFenced, "1")
				writeError(w, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) seeEpoch(e int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.epochRead {
		s.epoch, s.epochRead = ReadEpoch(s.StateDir), true
	}
	if e < s.epoch {
		return fmt.Errorf("%w: this machine follows epoch %d, the caller is at %d", ErrFenced, s.epoch, e)
	}
	if e > s.epoch && s.StateDir != "" {
		if err := writeFileAtomic(epochFile(s.StateDir), []byte(strconv.FormatInt(e, 10)+"\n")); err != nil {
			return err
		}
	}
	s.epoch = e
	now := time.Now()
	s.lastContact = &now
	return nil
}

// LastContact is when a control plane last called this agent (nil before the first call).
func (s *Server) LastContact() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastContact
}

// putStandbyCopy stores a copy from the main machine, checked against the digest it sent.
func (s *Server) putStandbyCopy(w http.ResponseWriter, r *http.Request) {
	want := r.Header.Get(headerSHA256)
	taken, err := time.Parse(time.RFC3339Nano, r.Header.Get(headerTakenAt))
	if want == "" || err != nil {
		http.Error(w, "the copy needs its digest and time", http.StatusBadRequest)
		return
	}
	dir := standbyDir(s.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeError(w, err)
		return
	}
	tmp, err := os.CreateTemp(dir, "incoming-*")
	if err != nil {
		writeError(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r.Body, standbyCopyMax+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		writeError(w, fmt.Errorf("receive the copy: %w", err))
		return
	case n > standbyCopyMax:
		http.Error(w, "the copy is too large", http.StatusRequestEntityTooLarge)
		return
	case hex.EncodeToString(h.Sum(nil)) != want:
		http.Error(w, "the copy's digest doesn't match", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copies := listCopies(dir)
	next := 1
	if len(copies) > 0 {
		next = copies[0].N + 1
	}
	c := StandbyCopy{N: next, TakenAt: taken.UTC(), Bytes: n, SHA256: want}
	meta, _ := json.Marshal(c)
	if err := os.Rename(tmp.Name(), filepath.Join(dir, copyName(next, ".tar.gz"))); err != nil {
		writeError(w, err)
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, copyName(next, ".json")), meta); err != nil {
		writeError(w, err)
		return
	}
	for _, old := range append([]StandbyCopy{c}, copies...)[min(StandbyCopiesKept, len(copies)+1):] {
		_ = os.Remove(filepath.Join(dir, copyName(old.N, ".tar.gz")))
		_ = os.Remove(filepath.Join(dir, copyName(old.N, ".json")))
	}
	writeResult(w, c)
}

func (s *Server) standbyState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	st := StandbyState{Copies: listCopies(standbyDir(s.StateDir)), LastContactAt: s.lastContact}
	s.mu.Unlock()
	if st.Copies == nil {
		st.Copies = []StandbyCopy{}
	}
	writeResult(w, st)
}

// listCopies returns a standby's copies, newest first.
func listCopies(dir string) []StandbyCopy {
	metas, _ := filepath.Glob(filepath.Join(dir, "copy-*.json"))
	var out []StandbyCopy
	for _, m := range metas {
		b, err := os.ReadFile(m)
		var c StandbyCopy
		if err != nil || json.Unmarshal(b, &c) != nil {
			continue
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b StandbyCopy) int { return b.N - a.N })
	return out
}

// StandbyCopies lists the copies an agent's state directory holds, newest first.
func StandbyCopies(stateDir string) []StandbyCopy { return listCopies(standbyDir(stateDir)) }

// RestoreNewestCopy unpacks the newest copy whose digests check into dataDir, which must not hold a
// control plane yet (ADR-057 step 1). It returns that copy's manifest.
func RestoreNewestCopy(stateDir, dataDir string) (Manifest, error) {
	if _, err := os.Stat(filepath.Join(dataDir, CopyDatabase)); err == nil {
		return Manifest{}, fmt.Errorf("%s already holds a control plane; promote into an empty data directory", dataDir)
	}
	copies := StandbyCopies(stateDir)
	if len(copies) == 0 {
		return Manifest{}, errors.New("this standby holds no copy of the control plane yet")
	}
	var errs []error
	for _, c := range copies {
		m, err := restoreCopy(filepath.Join(standbyDir(stateDir), copyName(c.N, ".tar.gz")), c.SHA256, dataDir)
		if err == nil {
			return m, nil
		}
		errs = append(errs, fmt.Errorf("copy %d: %w", c.N, err))
	}
	return Manifest{}, errors.Join(errs...)
}

func restoreCopy(path, wantSHA, dataDir string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Manifest{}, err
	}
	if hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return Manifest{}, errors.New("the archive's digest doesn't match")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Manifest{}, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Manifest{}, err
	}
	var m Manifest
	got := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		switch hd.Name {
		case CopyManifest, CopyKey:
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return Manifest{}, err
			}
			got[hd.Name] = b
		case CopyDatabase:
			out, err := os.OpenFile(filepath.Join(dataDir, CopyDatabase+".restoring"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return Manifest{}, err
			}
			dh := sha256.New()
			_, err = io.Copy(io.MultiWriter(out, dh), tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return Manifest{}, err
			}
			got[CopyDatabase] = []byte(hex.EncodeToString(dh.Sum(nil)))
		}
	}
	if err := json.Unmarshal(got[CopyManifest], &m); err != nil {
		return Manifest{}, fmt.Errorf("the manifest is unreadable: %w", err)
	}
	if len(got[CopyKey]) == 0 || string(got[CopyDatabase]) != m.SHA256 {
		_ = os.Remove(filepath.Join(dataDir, CopyDatabase+".restoring"))
		return Manifest{}, errors.New("the copy is incomplete or its database doesn't match its manifest")
	}
	if err := os.WriteFile(filepath.Join(dataDir, CopyKey), got[CopyKey], 0o600); err != nil {
		return Manifest{}, err
	}
	return m, os.Rename(filepath.Join(dataDir, CopyDatabase+".restoring"), filepath.Join(dataDir, CopyDatabase))
}

// WriteCopy packs a consistent database file and the master key into w as a copy (ADR-057), and returns
// its manifest.
func WriteCopy(w io.Writer, dbPath, keyPath, version, machineID string, epoch int64, now time.Time) (Manifest, error) {
	db, err := os.Open(dbPath)
	if err != nil {
		return Manifest{}, err
	}
	defer db.Close()
	st, err := db.Stat()
	if err != nil {
		return Manifest{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, db); err != nil {
		return Manifest{}, err
	}
	if _, err := db.Seek(0, io.SeekStart); err != nil {
		return Manifest{}, err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Version: version, MachineID: machineID, Epoch: epoch, TakenAt: now.UTC(), SHA256: hex.EncodeToString(h.Sum(nil))}
	mb, _ := json.Marshal(m)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		size int64
		r    io.Reader
	}{{CopyManifest, int64(len(mb)), strings.NewReader(string(mb))}, {CopyKey, int64(len(key)), strings.NewReader(string(key))}, {CopyDatabase, st.Size(), db}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o600, Size: f.size, ModTime: now}); err != nil {
			return Manifest{}, err
		}
		if _, err := io.Copy(tw, f.r); err != nil {
			return Manifest{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, err
	}
	return m, gz.Close()
}

// SendStandbyCopy PUTs a copy file to a standby's agent.
func (m *Remote) SendStandbyCopy(ctx context.Context, path string, takenAt time.Time) (StandbyCopy, error) {
	f, err := os.Open(path)
	if err != nil {
		return StandbyCopy{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return StandbyCopy{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return StandbyCopy{}, err
	}
	req, err := m.request(ctx, http.MethodPut, pathStandbyCopy, f)
	if err != nil {
		return StandbyCopy{}, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set(headerSHA256, hex.EncodeToString(h.Sum(nil)))
	req.Header.Set(headerTakenAt, takenAt.UTC().Format(time.RFC3339Nano))
	resp, err := m.do(req)
	if err != nil {
		return StandbyCopy{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StandbyCopy{}, readError(resp)
	}
	var c StandbyCopy
	return c, json.NewDecoder(resp.Body).Decode(&c)
}

// StandbyState reads what a standby holds.
func (m *Remote) StandbyState(ctx context.Context) (StandbyState, error) {
	req, err := m.request(ctx, http.MethodGet, pathStandbyState, nil)
	if err != nil {
		return StandbyState{}, err
	}
	resp, err := m.do(req)
	if err != nil {
		return StandbyState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StandbyState{}, readError(resp)
	}
	var st StandbyState
	return st, json.NewDecoder(resp.Body).Decode(&st)
}
