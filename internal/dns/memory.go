package dns

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Memory is an in-memory provider for every zone, used with the fake engine (tests, demos). Tests can
// add zones, write records "outside EnclaveStack" and inject failures.
type Memory struct {
	mu      sync.Mutex
	zones   map[string]bool    // zone name → exists
	records map[string]*Record // "fqdn|type"
	// Fail makes every call fail with this error (nil: work normally).
	Fail     error
	calls    int
	restrict bool
}

// NewMemory returns an empty provider. With zones, only those exist; without, every zone does (so the
// console and CLI tests can create any zone).
func NewMemory(zones ...string) *Memory {
	m := &Memory{zones: map[string]bool{}, records: map[string]*Record{}, restrict: len(zones) > 0}
	for _, z := range zones {
		m.zones[z] = true
	}
	return m
}

// AddZone makes a zone exist (and only listed zones from then on).
func (m *Memory) AddZone(zone string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.zones[zone], m.restrict = true, true
}

// Put writes a record as if someone changed it at the provider.
func (m *Memory) Put(rec Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := rec
	r.Values = slices.Clone(rec.Values)
	m.records[rec.FQDN+"|"+rec.Type] = &r
}

// Lookup returns a record, or nil.
func (m *Memory) Lookup(fqdn, typ string) *Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.records[fqdn+"|"+typ]; r != nil {
		c := *r
		c.Values = slices.Clone(r.Values)
		return &c
	}
	return nil
}

// Calls returns how many provider calls were made.
func (m *Memory) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// SetFail makes calls fail with err until it is set back to nil.
func (m *Memory) SetFail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Fail = err
}

// Open is the Opener: every provider name is served from memory. Credentials must be valid JSON with
// at least one field, so a test can exercise rejected credentials with "{}".
func (m *Memory) Open(provider, zone string, credentials []byte) (Provider, error) {
	if !strings.Contains(string(credentials), ":") {
		return &memoryZone{m: m, zone: zone, bad: true}, nil
	}
	return &memoryZone{m: m, zone: zone}, nil
}

type memoryZone struct {
	m    *Memory
	zone string
	bad  bool
}

func (z *memoryZone) enter() error {
	z.m.calls++
	if z.m.Fail != nil {
		return z.m.Fail
	}
	if z.bad {
		return fmt.Errorf("%w: missing the zone's DNS edit permission", ErrPermission)
	}
	if z.m.restrict && !z.m.zones[z.zone] {
		return fmt.Errorf("%w: %s", ErrNoZone, z.zone)
	}
	return nil
}

func (z *memoryZone) Zone(ctx context.Context) (Zone, error) {
	z.m.mu.Lock()
	defer z.m.mu.Unlock()
	if err := z.enter(); err != nil {
		return Zone{}, err
	}
	return Zone{ID: "mem-" + z.zone, NameServers: []string{"ns1.memory.test", "ns2.memory.test"}}, nil
}

func (z *memoryZone) Get(ctx context.Context, fqdn, typ string) (*Record, error) {
	z.m.mu.Lock()
	defer z.m.mu.Unlock()
	if err := z.enter(); err != nil {
		return nil, err
	}
	if r := z.m.records[fqdn+"|"+typ]; r != nil {
		c := *r
		c.Values = slices.Clone(r.Values)
		return &c, nil
	}
	return nil, nil
}

func (z *memoryZone) Set(ctx context.Context, rec Record, owner string) error {
	z.m.mu.Lock()
	defer z.m.mu.Unlock()
	if err := z.enter(); err != nil {
		return err
	}
	r := rec
	r.Values, r.Owner = slices.Clone(rec.Values), owner
	z.m.records[rec.FQDN+"|"+rec.Type] = &r
	return nil
}

func (z *memoryZone) Delete(ctx context.Context, fqdn, typ string) error {
	z.m.mu.Lock()
	defer z.m.mu.Unlock()
	if err := z.enter(); err != nil {
		return err
	}
	delete(z.m.records, fqdn+"|"+typ)
	return nil
}
