package domain

import (
	"strings"
	"testing"
)

func cand(id string, freeMiB int, opts ...func(*Candidate)) Candidate {
	c := Candidate{ID: id, Name: id, Main: id == "main", Online: true, Schedulable: true, VersionOK: true, MemoryTotalMiB: 16384, ReservedMiB: 16384 - freeMiB}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func TestChooseMachinePicksTheMostFreeMemory(t *testing.T) {
	ms := []Candidate{cand("main", 2048), cand("rack-1", 8192), cand("rack-2", 4096)}
	if id, why := ChooseMachine(ms, Need{MemoryMiB: 512}); id != "rack-1" || why != "" {
		t.Errorf("got %q %q", id, why)
	}
	// The main machine wins a tie (PLC-02).
	tie := []Candidate{cand("rack-1", 4096), cand("main", 4096)}
	if id, _ := ChooseMachine(tie, Need{MemoryMiB: 512}); id != "main" {
		t.Errorf("tie went to %q", id)
	}
}

func TestChooseMachineFiltersAndSaysWhy(t *testing.T) {
	offline := func(c *Candidate) { c.Online = false }
	cordoned := func(c *Candidate) { c.Schedulable = false }
	oldVersion := func(c *Candidate) { c.VersionOK = false }
	ssd := func(c *Candidate) { c.Labels = map[string]string{"disk": "ssd"} }
	vms := func(c *Candidate) { c.VMs = true }
	cases := []struct {
		name string
		ms   []Candidate
		need Need
		want string
		why  string
	}{
		{"memory", []Candidate{cand("main", 256), cand("rack-1", 1024)}, Need{MemoryMiB: 512}, "rack-1", ""},
		{"no memory", []Candidate{cand("main", 256)}, Need{MemoryMiB: 512}, "", "No machine has 512 MiB free."},
		{"offline", []Candidate{cand("main", 256), cand("rack-1", 8192, offline)}, Need{MemoryMiB: 512}, "", "rack-1 is offline."},
		{"not schedulable", []Candidate{cand("rack-1", 8192, cordoned), cand("main", 1024)}, Need{MemoryMiB: 512}, "main", ""},
		{"version", []Candidate{cand("rack-1", 8192, oldVersion)}, Need{}, "", "rack-1 runs another version of EnclaveStack."},
		{"labels", []Candidate{cand("main", 8192), cand("rack-1", 1024, ssd)}, Need{Labels: map[string]string{"disk": "ssd"}}, "rack-1", ""},
		{"no labels", []Candidate{cand("main", 8192)}, Need{Labels: map[string]string{"disk": "ssd"}}, "", "No machine has the labels disk=ssd."},
		{"vms", []Candidate{cand("main", 8192), cand("rack-1", 1024, vms)}, Need{VMs: true}, "rack-1", ""},
		{"no vms", []Candidate{cand("main", 8192)}, Need{VMs: true}, "", "No machine can run VMs."},
		{"pinned", []Candidate{cand("main", 8192), cand("rack-1", 1024)}, Need{Machine: "rack-1"}, "rack-1", ""},
		{"pinned and offline", []Candidate{cand("main", 8192), cand("rack-1", 1024, offline)}, Need{Machine: "rack-1"}, "", "rack-1 is offline."},
		{"pinned and unknown", []Candidate{cand("main", 8192)}, Need{Machine: "nowhere"}, "", "There's no machine named nowhere."},
	}
	for _, c := range cases {
		id, why := ChooseMachine(c.ms, c.need)
		if id != c.want || (c.why != "" && !strings.Contains(why, c.why)) {
			t.Errorf("%s: got %q %q, want %q %q", c.name, id, why, c.want, c.why)
		}
	}
}

func TestPlacementMemoryCountsAStoppedVM(t *testing.T) {
	r := &Resource{Kind: "vm", Spec: []byte(`{"memoryMiB":2048,"power":"stopped"}`)}
	if got := PlacementMemoryMiB(r); got != 2048+VMLinkMemoryMiB {
		t.Errorf("stopped VM needs %d", got)
	}
	if MemoryReservedMiB(r) != 0 {
		t.Error("a stopped VM reserves memory")
	}
}
