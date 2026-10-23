package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/enclavestack/platform/pkg/api"
)

// Candidate is a machine as placement sees it (ADR-044, DAR-039).
type Candidate struct {
	ID          string
	Name        string
	Main        bool
	Online      bool
	Schedulable bool
	// VersionOK is false when the agent's minor version differs from the control plane's (MCH-10).
	VersionOK bool
	Labels    map[string]string
	VMs       bool
	// MemoryTotalMiB is the engine's memory; ReservedMiB what the resources placed there reserve.
	MemoryTotalMiB int
	ReservedMiB    int
}

// FreeMiB is what is left to reserve.
func (c Candidate) FreeMiB() int { return c.MemoryTotalMiB - c.ReservedMiB }

// Need is what a resource asks of its machine.
type Need struct {
	MemoryMiB int
	Labels    map[string]string
	VMs       bool
	// Machine is the one it must run on: its own spec.machine or its volumes' machine (PLC-04); "" for any.
	Machine string
}

// ChooseMachine picks the machine for a resource: among online, schedulable machines of the right version
// that have the labels and the support the kind needs and the memory free, the one with the most free memory,
// the main machine on a tie (PLC-02). It returns the machine's ID, or "" and why none fits.
func ChooseMachine(machines []Candidate, need Need) (string, string) {
	var fits []Candidate
	reasons := map[string]bool{}
	for _, m := range machines {
		switch {
		case need.Machine != "" && m.Name != need.Machine && m.ID != need.Machine:
			continue
		case !m.Online:
			reasons[m.Name+" is offline."] = true
		case !m.Schedulable:
			reasons[m.Name+" isn't schedulable."] = true
		case !m.VersionOK:
			reasons[m.Name+" runs another version of EnclaveStack."] = true
		case !api.HasLabels(m.Labels, need.Labels):
			reasons["No machine has the labels "+FormatLabels(need.Labels)+"."] = true
		case need.VMs && !m.VMs:
			reasons["No machine can run VMs."] = true
		case m.FreeMiB() < need.MemoryMiB:
			reasons[fmt.Sprintf("No machine has %d MiB free.", need.MemoryMiB)] = true
		default:
			fits = append(fits, m)
		}
	}
	if len(fits) == 0 {
		if need.Machine != "" && len(reasons) == 0 {
			return "", "There's no machine named " + need.Machine + "."
		}
		out := make([]string, 0, len(reasons))
		for r := range reasons {
			out = append(out, r)
		}
		slices.Sort(out)
		return "", strings.Join(out, " ")
	}
	best := fits[0]
	for _, m := range fits[1:] {
		if m.FreeMiB() > best.FreeMiB() || (m.FreeMiB() == best.FreeMiB() && m.Main && !best.Main) {
			best = m
		}
	}
	return best.ID, ""
}

// ActionMove is the requested action of a resource a drain sends elsewhere (MCH-08): it moves to another
// machine without its spec changing.
const ActionMove = "move"

// FormatLabels prints labels as "k=v,k2=v2" in key order.
func FormatLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for _, k := range sortedKeys(labels) {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// PlacementMemoryMiB is what a resource needs free to be placed: its reservation (CP-16), and for a VM its
// memory even while it is stopped, since it may be started where it lands.
func PlacementMemoryMiB(r *Resource) int {
	if r.Kind == api.KindVM {
		var s api.VMSpec
		if err := json.Unmarshal(r.Spec, &s); err == nil {
			return s.MemoryMiB + VMLinkMemoryMiB
		}
	}
	return MemoryReservedMiB(r)
}

// Placed reports whether a kind runs on a machine of its own choosing (PLC-01); every other kind stays on
// the main machine (PLC-06).
func Placed(k api.Kind) bool { return slices.Contains(api.PlacedKinds, k) }

// ReservedByMachine sums what resources reserve on each machine (CP-16), in MiB, with the broker on the
// main machine when any queue or topic exists. Resources being deleted and not yet placed reserve nothing.
func ReservedByMachine(all []*Resource, mainID string) map[string]int {
	out := map[string]int{}
	broker := false
	for _, r := range all {
		if r.Phase == api.PhaseDeleting {
			continue
		}
		broker = broker || UsesBroker(r.Kind)
		if Placed(r.Kind) && r.Machine == "" {
			continue
		}
		out[r.MachineOn(mainID)] += MemoryReservedMiB(r)
	}
	if broker { // one broker serves every queue and topic (ADR-029)
		out[mainID] += BrokerMemoryMiB
	}
	return out
}
