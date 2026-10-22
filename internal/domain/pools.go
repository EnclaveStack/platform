package domain

import (
	"net/netip"
	"slices"
)

// Address pools (ADR-045, DAR-036): with two or more machines, every container takes its address from its
// machine's pool, and an address is in at most one machine's pool, so no two machines ever hand out the
// same one (XNET-02).

// PoolWant is how many free addresses each machine keeps in each range it may draw from.
const PoolWant = 8

// PoolNetwork describes one engine network for TopUp.
type PoolNetwork struct {
	// CIDR is the network's range; Ranges are where placements draw from: the automatic range (the first
	// subnet) and every other subnet. Empty Ranges means the whole CIDR.
	CIDR   string
	Ranges []string
	// Machines are the machines that need a pool here.
	Machines []string
	// Pooled is who has each pooled address now; Used who has a container on it now (observed).
	Pooled map[string]string
	Used   map[string]string
}

// PoolChanges is what TopUp decided: addresses to give, by address, and addresses whose container sits on
// another machine than the pool says (they must be fixed, not given twice).
type PoolChanges struct {
	Give      map[string]string
	Conflicts []string
}

// TopUp gives every machine up to PoolWant free addresses in each range, never one that is pooled to
// another machine or used by a container. An address a container already has goes to its machine's pool
// first, so what runs keeps its address when the second machine joins. Small ranges are shared fairly:
// each machine keeps at most half its share, and at least one.
func TopUp(n PoolNetwork) PoolChanges {
	ch := PoolChanges{Give: map[string]string{}}
	cidr, err := netip.ParsePrefix(n.CIDR)
	if err != nil {
		return ch
	}
	owner := func(a string) string {
		if m, ok := ch.Give[a]; ok {
			return m
		}
		return n.Pooled[a]
	}
	used := make([]string, 0, len(n.Used))
	for a := range n.Used {
		used = append(used, a)
	}
	slices.Sort(used)
	for _, a := range used {
		m := n.Used[a]
		switch o := owner(a); {
		case o == "":
			ch.Give[a] = m
		case o != m:
			ch.Conflicts = append(ch.Conflicts, a)
		}
	}
	ranges := n.Ranges
	if len(ranges) == 0 {
		ranges = []string{n.CIDR}
	}
	gateway := cidr.Addr().Next()
	last := broadcast(cidr)
	machines := slices.Clone(n.Machines)
	slices.Sort(machines)
	for _, r := range ranges {
		rng, err := netip.ParsePrefix(r)
		if err != nil {
			continue
		}
		var candidates []netip.Addr
		for a := rng.Addr().Next(); rng.Contains(a) && a != broadcast(rng); a = a.Next() {
			if a != gateway && a != cidr.Addr() && a != last {
				candidates = append(candidates, a)
			}
		}
		want := PoolWant
		if len(machines) > 0 {
			if share := len(candidates) / (2 * len(machines)); share < want {
				want = max(share, 1)
			}
		}
		for _, m := range machines {
			free := 0
			for _, a := range candidates {
				if owner(a.String()) == m && n.Used[a.String()] == "" {
					free++
				}
			}
			for _, a := range candidates {
				if free >= want {
					break
				}
				s := a.String()
				if owner(s) == "" && n.Used[s] == "" {
					ch.Give[s] = m
					free++
				}
			}
		}
	}
	return ch
}

func broadcast(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v |= (1 << (32 - p.Bits())) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
