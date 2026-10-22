package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryOpener keeps objects in memory, per endpoint and bucket. It backs the fake engine (tests, demos),
// where there is no object store to talk to.
type MemoryOpener struct {
	mu      sync.Mutex
	buckets map[string]map[string][]byte
	refuse  map[string]string // target → error message
}

// NewMemoryOpener returns an empty in-memory store.
func NewMemoryOpener() *MemoryOpener {
	return &MemoryOpener{buckets: map[string]map[string][]byte{}, refuse: map[string]string{}}
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
func (m *MemoryOpener) Put(t Target, key string, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buckets[t.Describe()] == nil {
		m.buckets[t.Describe()] = map[string][]byte{}
	}
	m.buckets[t.Describe()][key] = b
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

func (s *memStore) Put(_ context.Context, key string, r io.Reader, _ map[string]string) (Object, error) {
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
	if s.m.buckets[s.id] == nil {
		s.m.buckets[s.id] = map[string][]byte{}
	}
	s.m.buckets[s.id][key] = b
	return Object{Key: key, Size: int64(len(b)), SHA256: h.sum(), Modified: time.Now()}, nil
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
	return nil
}
