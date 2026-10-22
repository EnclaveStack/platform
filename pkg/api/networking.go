package api

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The v0.4 networking contract (L3-002 §7, L2-006): networks, firewalls, load balancers, DNS zones and
// records. Rules that need other resources (overlaps, existence, one network per load balancer) are
// checked by the service.

// Subnet is a named range inside a network (ADR-020).
type Subnet struct {
	Name string `json:"name"`
	CIDR string `json:"cidr"`
}

// NetworkSpec is a private network. Every field is immutable: the engine can't change a network's
// addresses after it is created (CP-04, NET-02).
type NetworkSpec struct {
	CIDR    string   `json:"cidr"`
	Subnets []Subnet `json:"subnets,omitempty"`
	// Internet gives the network a route out (default true, NET-05).
	Internet *bool `json:"internet,omitempty"`
}

// HasInternet reports the effective internet setting.
func (s NetworkSpec) HasInternet() bool { return s.Internet == nil || *s.Internet }

// Firewall protocols and egress defaults (L2-006 FW-02, FW-04).
const (
	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
	ProtocolICMP = "icmp"
	ProtocolAll  = "all"

	EgressAllow = "allow"
	EgressDeny  = "deny"

	// PeerAny matches every address.
	PeerAny = "any"
)

// FirewallRule allows traffic from (ingress) or to (egress) peers, on a protocol and ports.
type FirewallRule struct {
	From     []string `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	Ports    []string `json:"ports,omitempty"`
}

// FirewallSpec applies allow rules to its targets (ADR-021).
type FirewallSpec struct {
	Targets       []string       `json:"targets"`
	Ingress       []FirewallRule `json:"ingress,omitempty"`
	EgressDefault string         `json:"egressDefault,omitempty"`
	Egress        []FirewallRule `json:"egress,omitempty"`
}

// Load balancer protocols and algorithms (L2-006 LB-03, LB-04).
const (
	LBProtocolHTTP = "http"
	LBProtocolTCP  = "tcp"

	AlgorithmRoundRobin = "round_robin"
	AlgorithmLeastConn  = "least_conn"
	AlgorithmSource     = "source"
)

// LoadBalancerTarget is one app behind a load balancer.
type LoadBalancerTarget struct {
	App    string `json:"app"`
	Port   int    `json:"port,omitempty"`
	Weight int    `json:"weight,omitempty"`
}

// LoadBalancerCheck decides which replicas get traffic.
type LoadBalancerCheck struct {
	Type               string `json:"type"`
	Path               string `json:"path,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int    `json:"timeoutSeconds,omitempty"`
	HealthyThreshold   int    `json:"healthyThreshold,omitempty"`
	UnhealthyThreshold int    `json:"unhealthyThreshold,omitempty"`
}

// LoadBalancerSpec is a TCP or HTTP load balancer in front of apps' replicas (ADR-022).
type LoadBalancerSpec struct {
	Port        int                  `json:"port"`
	Protocol    string               `json:"protocol,omitempty"`
	Targets     []LoadBalancerTarget `json:"targets"`
	Algorithm   string               `json:"algorithm,omitempty"`
	HealthCheck *LoadBalancerCheck   `json:"healthCheck,omitempty"`
	Publish     bool                 `json:"publish,omitempty"`
}

// DNS providers (ADR-023).
const (
	ProviderCloudflare = "cloudflare"
	ProviderRoute53    = "route53"
)

// ZoneSpec is a DNS zone at a provider. The credentials secret holds {"apiToken"} (Cloudflare) or
// {"accessKeyId","secretAccessKey"} (Route 53).
type ZoneSpec struct {
	Provider          string `json:"provider"`
	Zone              string `json:"zone"`
	CredentialsSecret string `json:"credentialsSecret"`
}

// Record types (L2-006 DNS-02).
const (
	RecordA     = "A"
	RecordAAAA  = "AAAA"
	RecordCNAME = "CNAME"
	RecordTXT   = "TXT"

	// RecordApex is the zone's own name.
	RecordApex = "@"
)

// RecordSpec is a DNS record kept at a zone's provider. Exactly one of Values and Machine.
type RecordSpec struct {
	Zone    string   `json:"zone"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Values  []string `json:"values,omitempty"`
	Machine bool     `json:"machine,omitempty"`
	TTL     int      `json:"ttl,omitempty"`
}

// RecordFQDN is a record's fully-qualified name (without the trailing dot).
func RecordFQDN(name, zone string) string {
	if name == RecordApex {
		return zone
	}
	return name + "." + zone
}

// Validation limits (L3-002 §7.3), exported for the console.
const (
	NetworkPrefixMin, NetworkPrefixMax = 16, 28
	SubnetPrefixMin, SubnetPrefixMax   = 16, 29
	SubnetsMax                         = 16
	DefaultSubnetName                  = "default"

	ReplicasMin, ReplicasMax, ReplicasDefault = 1, 10, 1

	FirewallTargetsMax, FirewallRulesMax, FirewallPeersMax, FirewallPortsMax = 20, 50, 20, 20

	LBTargetsMax = 8
	// LBStatsPort is reserved inside a load balancer's container for its statistics (ADR-022).
	LBStatsPort                                     = 61999
	LBWeightMin, LBWeightMax, LBWeightDefault       = 1, 256, 1
	LBIntervalMin, LBIntervalMax, LBIntervalDefault = 1, 60, 2
	LBTimeoutMin, LBTimeoutMax, LBTimeoutDefault    = 1, 30, 1
	LBThresholdMin, LBThresholdMax                  = 1, 10
	LBHealthyDefault, LBUnhealthyDefault            = 2, 3

	// RecordLabelPatternSource also allows underscores, which service records use (_dmarc, _acme-challenge).
	RecordLabelPatternSource = `^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`

	RecordTTLMin, RecordTTLMax, RecordTTLDefault = 60, 86400, 300
	RecordValuesMax, TXTValueMaxLen              = 20, 255
)

// Allowed values (L3-002 §7.2).
var (
	Protocols            = []string{ProtocolTCP, ProtocolUDP, ProtocolICMP, ProtocolAll}
	EgressDefaults       = []string{EgressAllow, EgressDeny}
	LBProtocols          = []string{LBProtocolHTTP, LBProtocolTCP}
	Algorithms           = []string{AlgorithmRoundRobin, AlgorithmLeastConn, AlgorithmSource}
	LBCheckTypes         = []string{HealthTCP, HealthHTTP}
	DNSProviders         = []string{ProviderCloudflare, ProviderRoute53}
	RecordTypes          = []string{RecordA, RecordAAAA, RecordCNAME, RecordTXT}
	FirewallKinds        = []Kind{KindApp, KindDatabase, KindCache, KindLoadBalancer, KindVM, KindSearch, KindEmail}
	PlaceableKinds       = []Kind{KindApp, KindDatabase, KindCache, KindJob, KindSearch, KindEmail}
	privateNetworkRanges = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
	}
)

var recordLabelPattern = regexp.MustCompile(RecordLabelPatternSource)

// ParseCIDR parses a canonical IPv4 prefix (host bits zero).
func ParseCIDR(s string) (netip.Prefix, bool) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() || p.Masked() != p {
		return netip.Prefix{}, false
	}
	return p, true
}

// Overlaps reports whether two prefixes share any address.
func Overlaps(a, b netip.Prefix) bool { return a.Overlaps(b) }

// Contains reports whether outer holds every address of inner.
func Contains(outer, inner netip.Prefix) bool {
	return outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}

// EffectiveSubnets returns a network's subnets, or the implicit "default" subnet covering it all.
func (s NetworkSpec) EffectiveSubnets() []Subnet {
	if len(s.Subnets) > 0 {
		return s.Subnets
	}
	return []Subnet{{Name: DefaultSubnetName, CIDR: s.CIDR}}
}

// Peer is a parsed firewall source or destination.
type Peer struct {
	Any  bool
	CIDR netip.Prefix // set for a CIDR
	Kind Kind         // network, app, database, cache or loadbalancer
	Name string
}

// ParsePeer parses "any", an IPv4 CIDR (a bare address means /32), "network/<name>" or "<kind>/<name>".
func ParsePeer(s string) (Peer, bool) {
	if s == PeerAny {
		return Peer{Any: true}, true
	}
	if kind, name, ok := strings.Cut(s, "/"); ok && ValidateName(name) == nil {
		k := Kind(kind)
		if k == KindNetwork || slices.Contains(FirewallKinds, k) {
			return Peer{Kind: k, Name: name}, true
		}
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
		return Peer{CIDR: netip.PrefixFrom(a, 32)}, true
	}
	if p, ok := ParseCIDR(s); ok {
		return Peer{CIDR: p}, true
	}
	return Peer{}, false
}

// ParseTarget parses a firewall target "<kind>/<name>".
func ParseTarget(s string) (Kind, string, bool) {
	kind, name, ok := strings.Cut(s, "/")
	if !ok || !slices.Contains(FirewallKinds, Kind(kind)) || ValidateName(name) != nil {
		return "", "", false
	}
	return Kind(kind), name, true
}

// ParsePortRange parses "80" or "8000-8100".
func ParsePortRange(s string) (lo, hi int, ok bool) {
	a, b, isRange := strings.Cut(s, "-")
	lo, err := strconv.Atoi(a)
	if err != nil || lo < PortMin || lo > PortMax || strconv.Itoa(lo) != a {
		return 0, 0, false
	}
	if !isRange {
		return lo, lo, true
	}
	hi, err = strconv.Atoi(b)
	if err != nil || hi < lo || hi > PortMax || strconv.Itoa(hi) != b {
		return 0, 0, false
	}
	return lo, hi, true
}

func normalizeNetwork(s NetworkSpec) NetworkSpec {
	s.CIDR = strings.TrimSpace(s.CIDR)
	if s.Internet == nil {
		t := true
		s.Internet = &t
	}
	if len(s.Subnets) == 0 && s.CIDR != "" {
		s.Subnets = []Subnet{{Name: DefaultSubnetName, CIDR: s.CIDR}}
	}
	return s
}

func validateNetwork(s NetworkSpec) []FieldError {
	var errs []FieldError
	cidr, ok := ParseCIDR(s.CIDR)
	switch {
	case s.CIDR == "":
		return []FieldError{{Field: "spec.cidr", Code: FieldRequired, Message: "Enter the network's address range, like 10.40.0.0/16."}}
	case !ok:
		return []FieldError{{Field: "spec.cidr", Code: FieldInvalidFormat, Message: "Use an IPv4 range like 10.40.0.0/16, with the host part zero."}}
	case cidr.Bits() < NetworkPrefixMin || cidr.Bits() > NetworkPrefixMax:
		return []FieldError{{Field: "spec.cidr", Code: FieldOutOfRange, Message: "Use a range from /16 (65,536 addresses) to /28 (16 addresses)."}}
	case !slices.ContainsFunc(privateNetworkRanges, func(p netip.Prefix) bool { return Contains(p, cidr) }):
		return []FieldError{{Field: "spec.cidr", Code: FieldNotAllowed, Message: "Use a private range: inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 or 100.64.0.0/10."}}
	}
	if len(s.Subnets) > SubnetsMax {
		errs = append(errs, FieldError{Field: "spec.subnets", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d subnets.", SubnetsMax)})
	}
	var seen []netip.Prefix
	names := map[string]bool{}
	for i, sub := range s.Subnets {
		f := fmt.Sprintf("spec.subnets[%d]", i)
		if ValidateName(sub.Name) != nil {
			errs = append(errs, FieldError{Field: f + ".name", Code: FieldInvalidFormat, Message: msgName})
		} else if names[sub.Name] {
			errs = append(errs, FieldError{Field: f + ".name", Code: FieldNotAllowed, Message: "Another subnet already has this name."})
		}
		names[sub.Name] = true
		p, ok := ParseCIDR(sub.CIDR)
		switch {
		case !ok:
			errs = append(errs, FieldError{Field: f + ".cidr", Code: FieldInvalidFormat, Message: "Use an IPv4 range like 10.40.1.0/24, with the host part zero."})
			continue
		case p.Bits() < SubnetPrefixMin || p.Bits() > SubnetPrefixMax:
			errs = append(errs, FieldError{Field: f + ".cidr", Code: FieldOutOfRange, Message: "Use a range from /16 to /29."})
		case !Contains(cidr, p):
			errs = append(errs, FieldError{Field: f + ".cidr", Code: FieldNotAllowed, Message: "The subnet must lie inside the network's range " + s.CIDR + "."})
		}
		for j, o := range seen {
			if o.IsValid() && Overlaps(o, p) {
				errs = append(errs, FieldError{Field: f + ".cidr", Code: FieldOverlaps, Message: fmt.Sprintf("Overlaps subnet %s (%s).", s.Subnets[j].Name, o)})
				break
			}
		}
		seen = append(seen, p)
	}
	return errs
}

// validatePlacement checks the shape of a network/subnet pair; existence is the service's job.
func validatePlacement(network, subnet string) []FieldError {
	var errs []FieldError
	if network != "" && ValidateName(network) != nil {
		errs = append(errs, FieldError{Field: "spec.network", Code: FieldInvalidFormat, Message: "Name an existing network."})
	}
	switch {
	case subnet != "" && network == "":
		errs = append(errs, FieldError{Field: "spec.subnet", Code: FieldRequired, Message: "Choose the network first."})
	case subnet != "" && ValidateName(subnet) != nil:
		errs = append(errs, FieldError{Field: "spec.subnet", Code: FieldInvalidFormat, Message: "Name a subnet of the network."})
	}
	return errs
}

func normalizeFirewall(s FirewallSpec) FirewallSpec {
	if s.EgressDefault == "" {
		s.EgressDefault = EgressAllow
	}
	for _, rules := range [][]FirewallRule{s.Ingress, s.Egress} {
		for i := range rules {
			if rules[i].Protocol == "" {
				rules[i].Protocol = ProtocolTCP
			}
		}
	}
	return s
}

func validateFirewall(s FirewallSpec) []FieldError {
	var errs []FieldError
	switch {
	case len(s.Targets) == 0:
		errs = append(errs, FieldError{Field: "spec.targets", Code: FieldRequired, Message: "Choose the resources this firewall protects."})
	case len(s.Targets) > FirewallTargetsMax:
		errs = append(errs, FieldError{Field: "spec.targets", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d targets.", FirewallTargetsMax)})
	}
	seen := map[string]bool{}
	for i, t := range s.Targets {
		f := fmt.Sprintf("spec.targets[%d]", i)
		if _, _, ok := ParseTarget(t); !ok {
			errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Name an app, database, cache, load balancer, VM, search service or email service like app/api."})
		} else if seen[t] {
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: t + " is listed twice."})
		}
		seen[t] = true
	}
	if !slices.Contains(EgressDefaults, s.EgressDefault) {
		errs = append(errs, FieldError{Field: "spec.egressDefault", Code: FieldNotAllowed, Message: "Choose allow or deny."})
	}
	if len(s.Egress) > 0 && s.EgressDefault != EgressDeny {
		errs = append(errs, FieldError{Field: "spec.egress", Code: FieldNotAllowed, Message: "Outgoing rules apply only when other outgoing traffic is denied (egressDefault: deny)."})
	}
	errs = append(errs, validateRules(s.Ingress, "spec.ingress", true)...)
	errs = append(errs, validateRules(s.Egress, "spec.egress", false)...)
	return errs
}

func validateRules(rules []FirewallRule, field string, ingress bool) []FieldError {
	var errs []FieldError
	if len(rules) > FirewallRulesMax {
		errs = append(errs, FieldError{Field: field, Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d rules.", FirewallRulesMax)})
	}
	for i, r := range rules {
		f := fmt.Sprintf("%s[%d]", field, i)
		peers, peerField, other := r.From, f+".from", r.To
		if !ingress {
			peers, peerField, other = r.To, f+".to", r.From
		}
		if len(other) > 0 {
			word := "from"
			if ingress {
				word = "to"
			}
			errs = append(errs, FieldError{Field: f + "." + word, Code: FieldNotAllowed, Message: "Incoming rules name sources (from); outgoing rules name destinations (to)."})
		}
		switch {
		case len(peers) == 0:
			errs = append(errs, FieldError{Field: peerField, Code: FieldRequired, Message: "Add at least one: any, a CIDR, network/<name> or a resource like app/api."})
		case len(peers) > FirewallPeersMax:
			errs = append(errs, FieldError{Field: peerField, Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d entries.", FirewallPeersMax)})
		}
		for j, p := range peers {
			if _, ok := ParsePeer(p); !ok {
				errs = append(errs, FieldError{Field: fmt.Sprintf("%s[%d]", peerField, j), Code: FieldInvalidFormat, Message: "Use any, an IPv4 CIDR like 10.0.0.0/8, network/<name> or a resource like app/api."})
			}
		}
		if !slices.Contains(Protocols, r.Protocol) {
			errs = append(errs, FieldError{Field: f + ".protocol", Code: FieldNotAllowed, Message: "Choose tcp, udp, icmp or all."})
		}
		if r.Protocol == ProtocolICMP && len(r.Ports) > 0 {
			errs = append(errs, FieldError{Field: f + ".ports", Code: FieldNotAllowed, Message: "ICMP has no ports."})
		}
		if len(r.Ports) > FirewallPortsMax {
			errs = append(errs, FieldError{Field: f + ".ports", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d ports or ranges.", FirewallPortsMax)})
		}
		for j, p := range r.Ports {
			if _, _, ok := ParsePortRange(p); !ok {
				errs = append(errs, FieldError{Field: fmt.Sprintf("%s.ports[%d]", f, j), Code: FieldInvalidFormat, Message: "Use a port like 5432 or a range like 8000-8100."})
			}
		}
	}
	return errs
}

func normalizeLoadBalancer(s LoadBalancerSpec) LoadBalancerSpec {
	if s.Protocol == "" {
		s.Protocol = LBProtocolHTTP
	}
	if s.Algorithm == "" {
		s.Algorithm = AlgorithmRoundRobin
	}
	if s.HealthCheck == nil {
		s.HealthCheck = &LoadBalancerCheck{Type: HealthTCP}
	}
	h := s.HealthCheck
	if h.IntervalSeconds == 0 {
		h.IntervalSeconds = LBIntervalDefault
	}
	if h.TimeoutSeconds == 0 {
		h.TimeoutSeconds = min(LBTimeoutDefault, h.IntervalSeconds)
	}
	if h.HealthyThreshold == 0 {
		h.HealthyThreshold = LBHealthyDefault
	}
	if h.UnhealthyThreshold == 0 {
		h.UnhealthyThreshold = LBUnhealthyDefault
	}
	if h.Type == HealthHTTP && h.Path == "" {
		h.Path = "/"
	}
	for i := range s.Targets {
		if s.Targets[i].Weight == 0 {
			s.Targets[i].Weight = LBWeightDefault
		}
	}
	return s
}

func validateLoadBalancer(s LoadBalancerSpec) []FieldError {
	var errs []FieldError
	if s.Port < PortMin || s.Port > PortMax {
		errs = append(errs, FieldError{Field: "spec.port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
	} else if s.Port == LBStatsPort {
		errs = append(errs, FieldError{Field: "spec.port", Code: FieldNotAllowed, Message: fmt.Sprintf("Port %d is reserved for the load balancer's statistics. Choose another.", LBStatsPort)})
	}
	if !slices.Contains(LBProtocols, s.Protocol) {
		errs = append(errs, FieldError{Field: "spec.protocol", Code: FieldNotAllowed, Message: "Choose http or tcp."})
	}
	if !slices.Contains(Algorithms, s.Algorithm) {
		errs = append(errs, FieldError{Field: "spec.algorithm", Code: FieldNotAllowed, Message: "Choose round_robin, least_conn or source."})
	}
	switch {
	case len(s.Targets) == 0:
		errs = append(errs, FieldError{Field: "spec.targets", Code: FieldRequired, Message: "Choose at least one app to send traffic to."})
	case len(s.Targets) > LBTargetsMax:
		errs = append(errs, FieldError{Field: "spec.targets", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d apps.", LBTargetsMax)})
	}
	seen := map[string]bool{}
	for i, t := range s.Targets {
		f := fmt.Sprintf("spec.targets[%d]", i)
		if ValidateName(t.App) != nil {
			errs = append(errs, FieldError{Field: f + ".app", Code: FieldInvalidFormat, Message: "Name an existing app."})
		} else if seen[t.App] {
			errs = append(errs, FieldError{Field: f + ".app", Code: FieldNotAllowed, Message: "The app " + t.App + " is listed twice."})
		}
		seen[t.App] = true
		if t.Port != 0 && (t.Port < PortMin || t.Port > PortMax) {
			errs = append(errs, FieldError{Field: f + ".port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
		}
		if t.Weight < LBWeightMin || t.Weight > LBWeightMax {
			errs = append(errs, FieldError{Field: f + ".weight", Code: FieldOutOfRange, Message: fmt.Sprintf("Use a weight from %d to %d.", LBWeightMin, LBWeightMax)})
		}
	}
	if h := s.HealthCheck; h != nil {
		const f = "spec.healthCheck."
		if !slices.Contains(LBCheckTypes, h.Type) {
			errs = append(errs, FieldError{Field: f + "type", Code: FieldNotAllowed, Message: "Choose tcp or http."})
		}
		if h.Type == HealthHTTP && (!strings.HasPrefix(h.Path, "/") || len(h.Path) > HealthPathMaxLen || strings.ContainsAny(h.Path, " \t\r\n")) {
			errs = append(errs, FieldError{Field: f + "path", Code: FieldInvalidFormat, Message: "Use a path that starts with /, like /healthz."})
		}
		if h.Type != HealthHTTP && h.Path != "" {
			errs = append(errs, FieldError{Field: f + "path", Code: FieldNotAllowed, Message: "A path applies to http checks only."})
		}
		if h.IntervalSeconds < LBIntervalMin || h.IntervalSeconds > LBIntervalMax {
			errs = append(errs, FieldError{Field: f + "intervalSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds.", LBIntervalMin, LBIntervalMax)})
		}
		if h.TimeoutSeconds < LBTimeoutMin || h.TimeoutSeconds > LBTimeoutMax || h.TimeoutSeconds > h.IntervalSeconds {
			errs = append(errs, FieldError{Field: f + "timeoutSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds, no longer than the interval.", LBTimeoutMin, LBTimeoutMax)})
		}
		for name, v := range map[string]int{"healthyThreshold": h.HealthyThreshold, "unhealthyThreshold": h.UnhealthyThreshold} {
			if v < LBThresholdMin || v > LBThresholdMax {
				errs = append(errs, FieldError{Field: f + name, Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d checks.", LBThresholdMin, LBThresholdMax)})
			}
		}
	}
	slices.SortStableFunc(errs, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return errs
}

func normalizeZone(s ZoneSpec) ZoneSpec {
	s.Zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Zone)), ".")
	return s
}

func validateZone(s ZoneSpec) []FieldError {
	var errs []FieldError
	if !slices.Contains(DNSProviders, s.Provider) {
		errs = append(errs, FieldError{Field: "spec.provider", Code: FieldNotAllowed, Message: "Choose cloudflare or route53."})
	}
	for _, e := range ValidateHostname(s.Zone) {
		e.Field = "spec.zone"
		if e.Code == FieldRequired {
			e.Message = "Enter the zone's name, like example.com."
		}
		errs = append(errs, e)
	}
	if ValidateName(s.CredentialsSecret) != nil {
		errs = append(errs, FieldError{Field: "spec.credentialsSecret", Code: FieldRequired, Message: "Name the secret holding the provider's credentials."})
	}
	return errs
}

func normalizeRecord(s RecordSpec) RecordSpec {
	s.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Name)), ".")
	s.Type = strings.ToUpper(strings.TrimSpace(s.Type))
	if s.TTL == 0 {
		s.TTL = RecordTTLDefault
	}
	if s.Type == RecordCNAME {
		for i, v := range s.Values {
			s.Values[i] = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
		}
	}
	return s
}

func validateRecord(s RecordSpec) []FieldError {
	var errs []FieldError
	if ValidateName(s.Zone) != nil {
		errs = append(errs, FieldError{Field: "spec.zone", Code: FieldRequired, Message: "Choose the zone this record belongs to."})
	}
	if s.Name != RecordApex {
		labels := strings.TrimPrefix(s.Name, "*.")
		if s.Name == "*" {
			labels = ""
		}
		ok := s.Name != "" && len(s.Name) <= HostnameMaxLen
		for _, l := range strings.Split(labels, ".") {
			if labels != "" && !recordLabelPattern.MatchString(l) {
				ok = false
			}
		}
		if !ok {
			errs = append(errs, FieldError{Field: "spec.name", Code: FieldInvalidFormat, Message: "Use a name inside the zone like api, www.api or *.dev, or @ for the zone itself."})
		}
	}
	if !slices.Contains(RecordTypes, s.Type) {
		errs = append(errs, FieldError{Field: "spec.type", Code: FieldNotAllowed, Message: "Choose A, AAAA, CNAME or TXT."})
		return errs
	}
	switch {
	case s.Machine && len(s.Values) > 0, !s.Machine && len(s.Values) == 0:
		errs = append(errs, FieldError{Field: "spec.values", Code: FieldExactlyOne, Message: "Enter the record's values, or point it at this machine — choose one."})
	case s.Machine && s.Type != RecordA && s.Type != RecordAAAA:
		errs = append(errs, FieldError{Field: "spec.machine", Code: FieldNotAllowed, Message: "Only A and AAAA records can point at this machine."})
	case len(s.Values) > RecordValuesMax:
		errs = append(errs, FieldError{Field: "spec.values", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d values.", RecordValuesMax)})
	case s.Type == RecordCNAME && len(s.Values) != 1:
		errs = append(errs, FieldError{Field: "spec.values", Code: FieldNotAllowed, Message: "A CNAME record has exactly one value."})
	case s.Type == RecordCNAME && s.Name == RecordApex:
		errs = append(errs, FieldError{Field: "spec.name", Code: FieldNotAllowed, Message: "The zone itself (@) can't be a CNAME."})
	}
	for i, v := range s.Values {
		f := fmt.Sprintf("spec.values[%d]", i)
		switch s.Type {
		case RecordA:
			if a, err := netip.ParseAddr(v); err != nil || !a.Is4() {
				errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Use an IPv4 address like 203.0.113.7."})
			}
		case RecordAAAA:
			if a, err := netip.ParseAddr(v); err != nil || !a.Is6() || a.Is4In6() {
				errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Use an IPv6 address like 2001:db8::7."})
			}
		case RecordCNAME:
			if ValidateHostname(v) != nil {
				errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Use a hostname like app.example.net."})
			}
		case RecordTXT:
			if v == "" || len(v) > TXTValueMaxLen || strings.ContainsAny(v, "\r\n\"") {
				errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Use 1–255 characters of text, without quotes or line breaks."})
			}
		}
	}
	if s.TTL < RecordTTLMin || s.TTL > RecordTTLMax {
		errs = append(errs, FieldError{Field: "spec.ttl", Code: FieldOutOfRange, Message: "Use a TTL from 60 seconds to 1 day (86400)."})
	}
	return errs
}

// IsWildcard reports whether a hostname starts with a wildcard label.
func IsWildcard(hostname string) bool { return strings.HasPrefix(hostname, "*.") }

// InZone reports whether hostname is the zone or a name below it.
func InZone(hostname, zone string) bool {
	return hostname == zone || strings.HasSuffix(hostname, "."+zone)
}
