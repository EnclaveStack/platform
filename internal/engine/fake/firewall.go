package fake

import (
	"context"
	"net/netip"
	"reflect"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/firewall"
)

// Firewalls (ADR-021): the fake records each resource's last policy instead of enforcing it, so
// integration tests assert the resolved rules. A simulated restart forgets them, as a fresh network
// namespace would.

type fakeRules struct {
	policy  *firewall.Policy
	applied int // how many times rules were loaded
}

// ApplyFirewall implements engine.Driver.
func (d *Driver) ApplyFirewall(ctx context.Context, r *domain.Resource, p *firewall.Policy) (int, error) {
	if err := d.Ping(ctx); err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rt := d.runtimes[r.ID]
	if rt == nil || !rt.running {
		return 0, nil
	}
	return d.load(r.ID, p), nil
}

// load records p for a resource unless it already holds it; it returns 1 when it changed. Callers
// hold d.mu.
func (d *Driver) load(id string, p *firewall.Policy) int {
	cur, ok := d.rules[id]
	if ok && reflect.DeepEqual(cur.policy, p) {
		return 0
	}
	if !ok && p == nil {
		return 0
	}
	d.rules[id] = &fakeRules{policy: p, applied: cur.count() + 1}
	return 1
}

func (r *fakeRules) count() int {
	if r == nil {
		return 0
	}
	return r.applied
}

// Endpoints implements engine.Driver.
func (d *Driver) Endpoints(ctx context.Context, r *domain.Resource) ([]netip.Addr, error) {
	if err := d.Ping(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rt := d.runtimes[r.ID]
	if rt == nil || !rt.running {
		return nil, nil
	}
	a, err := netip.ParseAddr(rt.ip)
	if err != nil {
		return nil, nil
	}
	return []netip.Addr{a}, nil
}

// FirewallOf returns the policy last loaded into a resource and how many times rules were loaded.
func (d *Driver) FirewallOf(resourceID string) (*firewall.Policy, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.rules[resourceID]
	if r == nil {
		return nil, 0
	}
	return r.policy, r.applied
}

// RestartContainer simulates a container restarted outside EnclaveStack: its rules are gone.
func (d *Driver) RestartContainer(resourceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.rules[resourceID]; r != nil {
		r.policy = nil
	}
}
