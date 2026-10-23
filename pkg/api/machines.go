package api

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// v0.8: many machines (L2-010, L3-002 §11).

// Placed chooses the machine a resource runs on (ADR-044). It is embedded in every placed kind's spec; empty
// lets EnclaveStack choose.
type Placed struct {
	Machine       string            `json:"machine,omitempty"`
	MachineLabels map[string]string `json:"machineLabels,omitempty"`
}

// PlacedKinds run on one machine each (PLC-01).
var PlacedKinds = []Kind{KindApp, KindDatabase, KindCache, KindJob, KindLoadBalancer, KindVM, KindVolume, KindSearch, KindEmail}

// DataKinds keep data on their machine's disk: they wait for it when it is offline and copy their data
// when they move (FAIL-03, MOVE-02 – MOVE-04).
var DataKinds = []Kind{KindDatabase, KindCache, KindSearch, KindEmail, KindVolume, KindVM}

// Machine states (L3-002 §11.2).
const (
	MachineWaiting = "waiting"
	MachineOnline  = "online"
	MachineOffline = "offline"
)

// MachineStates lists every state.
var MachineStates = []string{MachineWaiting, MachineOnline, MachineOffline}

// Machine limits and ports (L3-002 §11.9).
const (
	MachineLabelsMax  = 10
	AgentPortDefault  = 7071
	TunnelPortDefault = 51820
	JoinCodeTTL       = time.Hour
)

// Label rules, shared with the console (L3-002 §11.9).
const (
	LabelKeyPatternSource   = `^[a-z0-9][a-z0-9._-]{0,62}$`
	LabelValuePatternSource = `^[A-Za-z0-9._-]{0,63}$`
)

var (
	labelKeyPattern   = regexp.MustCompile(LabelKeyPatternSource)
	labelValuePattern = regexp.MustCompile(LabelValuePatternSource)
)

// PlacementOf returns the placement of a placed kind's spec, and false for any other spec.
func PlacementOf(spec any) (Placed, bool) {
	switch s := spec.(type) {
	case AppSpec:
		return s.Placed, true
	case DatabaseSpec:
		return s.Placed, true
	case CacheSpec:
		return s.Placed, true
	case JobSpec:
		return s.Placed, true
	case LoadBalancerSpec:
		return s.Placed, true
	case VMSpec:
		return s.Placed, true
	case VolumeSpec:
		return s.Placed, true
	case SearchSpec:
		return s.Placed, true
	case EmailSpec:
		return s.Placed, true
	}
	return Placed{}, false
}

// ValidateLabels checks machine labels; field is where they were sent ("labels", "spec.machineLabels").
func ValidateLabels(labels map[string]string, field string) []FieldError {
	if len(labels) > MachineLabelsMax {
		return []FieldError{{Field: field, Code: FieldOutOfRange, Message: fmt.Sprintf("Use at most %d labels.", MachineLabelsMax)}}
	}
	var errs []FieldError
	for _, k := range sortedKeys(labels) {
		if !labelKeyPattern.MatchString(k) {
			errs = append(errs, FieldError{Field: field + "." + k, Code: FieldInvalidFormat,
				Message: "A label's key is lowercase letters, digits, dots, hyphens and underscores, up to 63 characters, starting with a letter or digit."})
		} else if !labelValuePattern.MatchString(labels[k]) {
			errs = append(errs, FieldError{Field: field + "." + k, Code: FieldInvalidFormat,
				Message: "A label's value is letters, digits, dots, hyphens and underscores, up to 63 characters."})
		}
	}
	return errs
}

// validatePlaced checks a spec's machine and labels. The machine's existence is the service's to check.
func validatePlaced(p Placed) []FieldError {
	var errs []FieldError
	if p.Machine != "" {
		if e := ValidateName(p.Machine); e != nil {
			errs = append(errs, FieldError{Field: "spec.machine", Code: e[0].Code, Message: "Name a machine: " + e[0].Message})
		}
	}
	if p.MachineLabels != nil && len(p.MachineLabels) == 0 {
		errs = append(errs, FieldError{Field: "spec.machineLabels", Code: FieldRequired, Message: "Give at least one label, or leave machineLabels out."})
	}
	return append(errs, ValidateLabels(p.MachineLabels, "spec.machineLabels")...)
}

// HasLabels reports whether have carries every label of want.
func HasLabels(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// IsDataKind reports whether a kind keeps data on its machine.
func IsDataKind(k Kind) bool { return slices.Contains(DataKinds, k) }

// MachineLink is one machine's tunnel link to another (XNET-06).
type MachineLink struct {
	Machine         string     `json:"machine"`
	Connected       bool       `json:"connected"`
	LastHandshakeAt *time.Time `json:"lastHandshakeAt"`
}

// MachineSummary is one machine of GET /machines (L3-002 §11.2).
type MachineSummary struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Title               string            `json:"title"`
	Main                bool              `json:"main"`
	State               string            `json:"state"`
	Since               time.Time         `json:"since"`
	Labels              map[string]string `json:"labels"`
	Schedulable         bool              `json:"schedulable"`
	Address             *string           `json:"address"`
	OS                  string            `json:"os"`
	Arch                string            `json:"arch"`
	AgentVersion        string            `json:"agentVersion"`
	Engine              Engine            `json:"engine"`
	CPUs                int               `json:"cpus"`
	MemoryTotalBytes    int64             `json:"memoryTotalBytes"`
	MemoryReservedBytes int64             `json:"memoryReservedBytes"`
	VMs                 VMSupport         `json:"vms"`
	LastSeenAt          *time.Time        `json:"lastSeenAt"`
	Resources           int               `json:"resources"`
	VersionMismatch     bool              `json:"versionMismatch"`
	Links               []MachineLink     `json:"links"`
	Message             string            `json:"message"`
}

// MachineList wraps machines.
type MachineList struct {
	Items []MachineSummary `json:"items"`
}

// AddMachine is the body of POST /machines.
type AddMachine struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// AddedMachine is returned with a new join code, shown once.
type AddedMachine struct {
	Machine     MachineSummary `json:"machine"`
	JoinCode    string         `json:"joinCode"`
	JoinCommand string         `json:"joinCommand"`
	ExpiresAt   time.Time      `json:"expiresAt"`
}

// UpdateMachine is the body of PATCH /machines/{name}; absent fields stay.
type UpdateMachine struct {
	// Labels replace the machine's when not null; {} clears them.
	Labels      map[string]string `json:"labels"`
	Schedulable *bool             `json:"schedulable,omitempty"`
	// Title is the display name people read (MCH-10). Only the main machine has one of its own; absent keeps it.
	Title *string `json:"title,omitempty"`
}

// JoinRequest is the body of POST /machines/join, sent by `enclave agent join` (ADR-043).
type JoinRequest struct {
	Code         string `json:"code"`
	CSR          string `json:"csr"`
	Advertise    string `json:"advertise"`
	AgentPort    int    `json:"agentPort"`
	TunnelPort   int    `json:"tunnelPort"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agentVersion"`
}

// JoinResponse is what a joining agent gets back.
type JoinResponse struct {
	MachineID   string `json:"machineId"`
	Name        string `json:"name"`
	Certificate string `json:"certificate"`
	CA          string `json:"ca"`
	Server      string `json:"server"`
}

// DrainResult lists what a drain moves and what stays because it is pinned (MCH-08).
type DrainResult struct {
	Moving []string `json:"moving"`
	Pinned []string `json:"pinned"`
}

// ValidateAddMachine checks POST /machines.
func ValidateAddMachine(m AddMachine) []FieldError {
	return append(ValidateName(m.Name), ValidateLabels(m.Labels, "labels")...)
}

// ValidateJoin checks POST /machines/join.
func ValidateJoin(r JoinRequest) []FieldError {
	var errs []FieldError
	if r.Code == "" {
		errs = append(errs, FieldError{Field: "code", Code: FieldRequired, Message: "Give the join code."})
	}
	if r.CSR == "" {
		errs = append(errs, FieldError{Field: "csr", Code: FieldRequired, Message: "Send a certificate request."})
	}
	if e := ValidateHostname(r.Advertise); r.Advertise == "" || (e != nil && !isIPv4(r.Advertise)) {
		errs = append(errs, FieldError{Field: "advertise", Code: FieldInvalidFormat, Message: "Advertise an IPv4 address or a host name the main machine can reach."})
	}
	for field, port := range map[string]int{"agentPort": r.AgentPort, "tunnelPort": r.TunnelPort} {
		if port < PortMin || port > PortMax {
			errs = append(errs, FieldError{Field: field, Code: FieldOutOfRange, Message: fmt.Sprintf("Use a port from %d to %d.", PortMin, PortMax)})
		}
	}
	slices.SortFunc(errs, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return errs
}

func isIPv4(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4()
}

// MountedVolumes are the volumes an app or job spec mounts: what uses a volume runs where it is (PLC-04).
func MountedVolumes(spec any) []string {
	var mounts []VolumeMount
	switch s := spec.(type) {
	case AppSpec:
		mounts = s.Volumes
	case JobSpec:
		mounts = s.Volumes
	}
	out := make([]string, len(mounts))
	for i, m := range mounts {
		out[i] = m.Volume
	}
	return out
}

// ValidateMachineTitle checks a display name: 1–MachineNameMaxLen characters once trimmed.
func ValidateMachineTitle(title string) []FieldError {
	if n := utf8.RuneCountInString(strings.TrimSpace(title)); n < 1 || n > MachineNameMaxLen {
		return []FieldError{{Field: "title", Code: FieldOutOfRange, Message: "Use 1–64 characters."}}
	}
	return nil
}

// MachineNameFromHostname turns a computer's own name into the display name setup suggests: the first label
// of its host name ("sunnys-mbp.local" → "sunnys-mbp"), or "This computer" when it has none (MCH-10).
func MachineNameFromHostname(hostname string) string {
	h := strings.TrimSpace(hostname)
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	if h == "" || utf8.RuneCountInString(h) > MachineNameMaxLen {
		return "This computer"
	}
	return h
}

// MachineHandleFromHostname turns a computer's own name into the name commands use for the main machine
// (MCH-01): the first label of its host name, lowercase letters, digits and hyphens, starting with a letter,
// within the resource name rules; "computer" when nothing usable is left.
func MachineHandleFromHostname(hostname string) string {
	h := strings.ToLower(strings.TrimSpace(hostname))
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	var b strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-') // anything else separates words
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > NameMaxLen {
		out = strings.TrimRight(out[:NameMaxLen], "-")
	}
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = strings.TrimLeft("m-"+out, "-")
		out = strings.TrimRight(out, "-")
	}
	if ValidateName(out) != nil {
		return "computer"
	}
	return out
}
