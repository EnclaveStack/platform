package docker

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"sync"
	"testing"
)

// Test networks get a /24 in 10.100.0.0 – 10.249.0.0 that overlaps no network the engine already has, nor
// one an earlier test in this process took: a random pick alone collided with networks left by other
// packages' tests on the same runner.

var (
	takenMu   sync.Mutex
	takenCIDR = map[netip.Prefix]bool{}
)

// pickFreeCIDR returns a /24 of 10.100–249.0.0 that overlaps none of used, trying random ones first.
func pickFreeCIDR(used []netip.Prefix, rnd func(int) int) (netip.Prefix, bool) {
	free := func(p netip.Prefix) bool {
		for _, u := range used {
			if u.Overlaps(p) {
				return false
			}
		}
		return true
	}
	at := func(b int) netip.Prefix { return netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/24", b)) }
	for range 50 {
		if p := at(100 + rnd(150)); free(p) {
			return p, true
		}
	}
	for b := 100; b < 250; b++ {
		if p := at(b); free(p) {
			return p, true
		}
	}
	return netip.Prefix{}, false
}

// claimCIDR picks a free /24 given what the engine uses and marks it taken for this process.
func claimCIDR(engineUsed []netip.Prefix) (string, string, string) {
	takenMu.Lock()
	defer takenMu.Unlock()
	used := append([]netip.Prefix(nil), engineUsed...)
	for p := range takenCIDR {
		used = append(used, p)
	}
	p, ok := pickFreeCIDR(used, rand.IntN)
	if !ok {
		panic("no free 10.x.0.0/24 left for test networks")
	}
	takenCIDR[p] = true
	b := p.Addr().As4()[1]
	return p.String(), fmt.Sprintf("10.%d.0.0/25", b), fmt.Sprintf("10.%d.0.128/25", b)
}

func TestRandomCIDRAvoidsUsedRanges(t *testing.T) {
	var used []netip.Prefix
	for b := 100; b < 250; b++ {
		if b != 173 {
			used = append(used, netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/24", b)))
		}
	}
	if p, ok := pickFreeCIDR(used, rand.IntN); !ok || p.String() != "10.173.0.0/24" {
		t.Fatalf("got %v %v, want the one free range", p, ok)
	}
	// A wide network covering everything leaves nothing.
	if _, ok := pickFreeCIDR([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, rand.IntN); ok {
		t.Fatal("a /8 in use should leave no range")
	}
	// Ranges claimed in this process are never handed out twice.
	seen := map[string]bool{}
	for range 20 {
		cidr, first, second := claimCIDR(nil)
		if seen[cidr] {
			t.Fatalf("%s handed out twice", cidr)
		}
		seen[cidr] = true
		if !netip.MustParsePrefix(cidr).Contains(netip.MustParsePrefix(first).Addr()) || !netip.MustParsePrefix(cidr).Contains(netip.MustParsePrefix(second).Addr()) {
			t.Fatalf("%s doesn't hold %s and %s", cidr, first, second)
		}
	}
}
