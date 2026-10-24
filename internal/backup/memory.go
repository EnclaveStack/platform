package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryOpener keeps objects in memory, per endpoint and bucket. It backs the fake engine (tests, demos),
// where there is no object store to talk to.
type MemoryOpener struct {
	mu       sync.Mutex
	buckets  map[string]map[string][]byte
	meta     map[string]map[string]map[string]string // target → key → user metadata
	modified map[string]map[string]time.Time
	refuse   map[string]string // target → error message
}

// NewMemoryOpener returns an empty in-memory store.
func NewMemoryOpener() *MemoryOpener {
	return &MemoryOpener{buckets: map[string]map[string][]byte{}, meta: map[string]map[string]map[string]string{}, modified: map[string]map[string]time.Time{}, refuse: map[string]string{}}
}

// Refuse makes every call to a target fail with msg ("" clears it): e.g. rejected credentials.
func (m *MemoryOpener) Refuse(t Target, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg == "" {
		delete(m.refuse, t.Describe())
		return
	}
	m.refuse[t.Describe()] = msg
}

// Objects returns the keys stored in a target, sorted.
func (m *MemoryOpener) Objects(t Target) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.buckets[t.Describe()] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Corrupt changes an object's bytes (tests checksum verification).
func (m *MemoryOpener) Corrupt(t Target, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.buckets[t.Describe()][key]; ok {
		m.buckets[t.Describe()][key] = append(append([]byte(nil), b...), 'x')
	}
}

// Put stores bytes directly (tests: objects written by other tools).
func (m *MemoryOpener) Put(t Target, key string, b []byte) { m.PutWithMeta(t, key, b, nil) }

// PutWithMeta stores bytes with user metadata, as an uploader setting x-amz-meta-* would.
func (m *MemoryOpener) PutWithMeta(t Target, key string, b []byte, meta map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store(t.Describe(), key, b, meta)
}

// Meta returns an object's user metadata.
func (m *MemoryOpener) Meta(t Target, key string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.meta[t.Describe()][key])
}

func (m *MemoryOpener) store(id, key string, b []byte, meta map[string]string) {
	if m.buckets[id] == nil {
		m.buckets[id], m.meta[id], m.modified[id] = map[string][]byte{}, map[string]map[string]string{}, map[string]time.Time{}
	}
	m.buckets[id][key] = b
	m.meta[id][key] = maps.Clone(meta)
	m.modified[id][key] = time.Now().UTC()
}

// Open implements Opener.
func (m *MemoryOpener) Open(_ context.Context, t Target) (Store, error) {
	return &memStore{m: m, id: t.Describe()}, nil
}

type memStore struct {
	m  *MemoryOpener
	id string
}

func (s *memStore) check() error {
	if msg := s.m.refuse[s.id]; msg != "" {
		return errors.New(msg)
	}
	return nil
}

func (s *memStore) Put(_ context.Context, key string, r io.Reader, meta map[string]string) (Object, error) {
	h := newHashingReader(r)
	b, err := io.ReadAll(h)
	if err != nil {
		return Object{}, err // nothing stored: like an aborted multipart upload
	}
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if err := s.check(); err != nil {
		return Object{}, err
	}
	s.m.store(s.id, key, b, meta)
	return Object{Key: key, Size: int64(len(b)), SHA256: h.sum(), Modified: s.m.modified[s.id][key]}, nil
}

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	b, ok := s.m.buckets[s.id][key]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *memStore) Head(_ context.Context, key string) (ObjectHead, error) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if err := s.check(); err != nil {
		return ObjectHead{}, err
	}
	b, ok := s.m.buckets[s.id][key]
	if !ok {
		return ObjectHead{}, ErrNotFound
	}
	return ObjectHead{Object: Object{Key: key, Size: int64(len(b)), Modified: s.m.modified[s.id][key]}, ContentType: ContentType(key), Meta: maps.Clone(s.m.meta[s.id][key])}, nil
}

func (s *memStore) Browse(_ context.Context, prefix, after string, max int) ([]string, []Object, bool, error) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, nil, false, err
	}
	entries := map[string]Object{} // a folder ends in "/"
	for k, b := range s.m.buckets[s.id] {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if i := strings.Index(k[len(prefix):], "/"); i >= 0 {
			entries[k[:len(prefix)+i+1]] = Object{}
		} else {
			entries[k] = Object{Key: k, Size: int64(len(b)), Modified: s.m.modified[s.id][k]}
		}
	}
	keys := slices.Sorted(maps.Keys(entries))
	var folders []string
	var objects []Object
	for _, k := range keys {
		if k <= after {
			continue
		}
		if len(folders)+len(objects) == max {
			return folders, objects, true, nil
		}
		if strings.HasSuffix(k, "/") {
			folders = append(folders, k)
		} else {
			objects = append(objects, entries[k])
		}
	}
	return folders, objects, false, nil
}

func (s *memStore) List(_ context.Context, prefix string) ([]Object, error) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	var out []Object
	for k, b := range s.m.buckets[s.id] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k, Size: int64(len(b))})
		}
	}
	return out, nil
}

func (s *memStore) Delete(_ context.Context, key string) error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	delete(s.m.buckets[s.id], key)
	delete(s.m.meta[s.id], key)
	delete(s.m.modified[s.id], key)
	return nil
}
