package domain

import (
	"net/netip"
	"testing"
)

func given(ch PoolChanges, m string) []string {
	var out []string
	for a, who := range ch.Give {
		if who == m {
			out = append(out, a)
		}
	}
	return out
}

func TestTopUpGivesEachMachineItsOwnAddresses(t *testing.T) {
	ch := TopUp(PoolNetwork{CIDR: "10.20.0.0/24", Machines: []string{"main", "mch_a"}, Pooled: map[string]string{}, Used: map[string]string{}})
	a, b := given(ch, "main"), given(ch, "mch_a")
	if len(a) != PoolWant || len(b) != PoolWant {
		t.Fatalf("main %d, mch_a %d", len(a), len(b))
	}
	seen := map[string]bool{}
	for _, x := range append(a, b...) {
		if seen[x] {
			t.Errorf("%s given twice", x)
		}
		seen[x] = true
		if x == "10.20.0.0" || x == "10.20.0.1" || x == "10.20.0.255" {
			t.Errorf("gave the network, gateway or broadcast address %s", x)
		}
	}
}

func TestTopUpKeepsWhatRunsAndFindsConflicts(t *testing.T) {
	n := PoolNetwork{CIDR: "10.20.0.0/24", Machines: []string{"main", "mch_a"},
		Pooled: map[string]string{"10.20.0.9": "mch_a"},
		Used:   map[string]string{"10.20.0.2": "main", "10.20.0.3": "main", "10.20.0.9": "main"}}
	ch := TopUp(n)
	if ch.Give["10.20.0.2"] != "main" || ch.Give["10.20.0.3"] != "main" {
		t.Errorf("addresses in use didn't go to their machine: %v", ch.Give)
	}
	if len(ch.Conflicts) != 1 || ch.Conflicts[0] != "10.20.0.9" {
		t.Errorf("conflicts %v", ch.Conflicts)
	}
	if _, ok := ch.Give["10.20.0.9"]; ok {
		t.Error("a conflicting address was given again")
	}
}

func TestTopUpOnlyTopsUp(t *testing.T) {
	pooled := map[string]string{}
	for _, a := range []string{"10.20.0.2", "10.20.0.3", "10.20.0.4", "10.20.0.5", "10.20.0.6", "10.20.0.7", "10.20.0.8", "10.20.0.9"} {
		pooled[a] = "main"
	}
	ch := TopUp(PoolNetwork{CIDR: "10.20.0.0/24", Machines: []string{"main"}, Pooled: pooled, Used: map[string]string{"10.20.0.2": "main"}})
	if got := given(ch, "main"); len(got) != 1 {
		t.Errorf("with 7 free, main got %v", got)
	}
}

func TestTopUpSharesSmallSubnetsAndEveryRange(t *testing.T) {
	n := PoolNetwork{CIDR: "10.30.0.0/24", Ranges: []string{"10.30.0.0/25", "10.30.0.128/29"}, Machines: []string{"main", "mch_a"},
		Pooled: map[string]string{}, Used: map[string]string{}}
	ch := TopUp(n)
	small := netip.MustParsePrefix("10.30.0.128/29")
	per := map[string]int{}
	for a, m := range ch.Give {
		if small.Contains(netip.MustParseAddr(a)) {
			per[m]++
		}
	}
	// A /29 has 6 usable addresses: each of two machines keeps one, leaving room for later machines.
	if per["main"] != 1 || per["mch_a"] != 1 {
		t.Errorf("in the /29: %v", per)
	}
	if len(given(ch, "main")) != PoolWant+1 {
		t.Errorf("main's pool across both ranges: %d", len(given(ch, "main")))
	}
}
