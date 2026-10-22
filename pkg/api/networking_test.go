package api

import (
	"encoding/json"
	"slices"
	"testing"
)

func fieldsOf(errs []FieldError) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Field + ":" + string(e.Code)
	}
	return out
}

func normalize(t *testing.T, kind Kind, spec string) (any, []string) {
	t.Helper()
	s, errs, err := NormalizeSpec(kind, json.RawMessage(spec))
	if err != nil {
		t.Fatalf("decode %s: %v", spec, err)
	}
	return s, fieldsOf(errs)
}

func TestNetworkValidation(t *testing.T) {
	s, errs := normalize(t, KindNetwork, `{"cidr":"10.40.0.0/16"}`)
	if len(errs) != 0 {
		t.Fatalf("valid network refused: %v", errs)
	}
	n := s.(NetworkSpec)
	if !n.HasInternet() || len(n.Subnets) != 1 || n.Subnets[0] != (Subnet{Name: DefaultSubnetName, CIDR: "10.40.0.0/16"}) {
		t.Errorf("defaults not applied: %+v", n)
	}
	cases := map[string]string{
		`{}`:                      "spec.cidr:REQUIRED",
		`{"cidr":"10.40.0.1/16"}`: "spec.cidr:INVALID_FORMAT", // host bits set
		`{"cidr":"10.0.0.0/8"}`:   "spec.cidr:OUT_OF_RANGE",
		`{"cidr":"10.40.0.0/30"}`: "spec.cidr:OUT_OF_RANGE",
		`{"cidr":"8.8.0.0/16"}`:   "spec.cidr:NOT_ALLOWED",
		`{"cidr":"fd00::/64"}`:    "spec.cidr:INVALID_FORMAT",
		`{"cidr":"10.40.0.0/16","subnets":[{"name":"a","cidr":"10.40.1.0/24"}]}`:                                            "spec.subnets[0].name:INVALID_FORMAT",
		`{"cidr":"10.40.0.0/16","subnets":[{"name":"data","cidr":"10.41.1.0/24"}]}`:                                         "spec.subnets[0].cidr:NOT_ALLOWED",
		`{"cidr":"10.40.0.0/16","subnets":[{"name":"data","cidr":"10.40.1.0/24"},{"name":"apps","cidr":"10.40.1.128/25"}]}`: "spec.subnets[1].cidr:OVERLAPS",
		`{"cidr":"10.40.0.0/16","subnets":[{"name":"data","cidr":"10.40.1.0/24"},{"name":"data","cidr":"10.40.2.0/24"}]}`:   "spec.subnets[1].name:NOT_ALLOWED",
		`{"cidr":"10.40.0.0/16","subnets":[{"name":"tiny","cidr":"10.40.1.0/30"}]}`:                                         "spec.subnets[0].cidr:OUT_OF_RANGE",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindNetwork, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	b, _ := normalize(t, KindNetwork, `{"cidr":"10.40.0.0/16"}`)
	a, _ := normalize(t, KindNetwork, `{"cidr":"10.40.0.0/16","internet":false}`)
	if v := ImmutableViolations(KindNetwork, b, a); len(v) != 1 {
		t.Errorf("internet change not flagged immutable: %v", v)
	}
	if v := ImmutableViolations(KindNetwork, b, b); v != nil {
		t.Errorf("no change flagged: %v", v)
	}
}

func TestPlacementAndReplicas(t *testing.T) {
	if _, errs := normalize(t, KindApp, `{"image":"nginx:1.27","subnet":"data"}`); !slices.Contains(errs, "spec.subnet:REQUIRED") {
		t.Errorf("subnet without network accepted: %v", errs)
	}
	if _, errs := normalize(t, KindDatabase, `{"network":"Bad_Name"}`); !slices.Contains(errs, "spec.network:INVALID_FORMAT") {
		t.Errorf("bad network name accepted: %v", errs)
	}
	s, errs := normalize(t, KindApp, `{"image":"nginx:1.27","network":"backend","subnet":"apps"}`)
	if len(errs) != 0 || s.(AppSpec).Replicas != 1 {
		t.Errorf("placed app: errs %v, replicas %d", errs, s.(AppSpec).Replicas)
	}
	if _, errs := normalize(t, KindApp, `{"image":"nginx:1.27","port":80,"publish":true,"replicas":2}`); !slices.Contains(errs, "spec.replicas:NOT_ALLOWED") {
		t.Errorf("published app with 2 replicas accepted: %v", errs)
	}
	if _, errs := normalize(t, KindApp, `{"image":"nginx:1.27","replicas":11}`); !slices.Contains(errs, "spec.replicas:OUT_OF_RANGE") {
		t.Errorf("11 replicas accepted: %v", errs)
	}
	if _, errs := normalize(t, KindJob, `{"schedule":"@daily","app":"api","network":"backend"}`); !slices.Contains(errs, "spec.network:NOT_ALLOWED") {
		t.Errorf("app job with its own network accepted: %v", errs)
	}
	if _, errs := normalize(t, KindJob, `{"schedule":"@daily","image":"busybox:1.37","network":"backend"}`); len(errs) != 0 {
		t.Errorf("placed image job refused: %v", errs)
	}
}

func TestFirewallValidation(t *testing.T) {
	s, errs := normalize(t, KindFirewall, `{"targets":["database/orders"],"ingress":[{"from":["app/api","10.0.0.0/8","network/backend","any","10.1.2.3"],"ports":["5432","8000-8100"]}]}`)
	if len(errs) != 0 {
		t.Fatalf("valid firewall refused: %v", errs)
	}
	if fw := s.(FirewallSpec); fw.EgressDefault != EgressAllow || fw.Ingress[0].Protocol != ProtocolTCP {
		t.Errorf("defaults not applied: %+v", fw)
	}
	cases := map[string]string{
		`{"targets":[]}`:                                                                        "spec.targets:REQUIRED",
		`{"targets":["secret/key"]}`:                                                            "spec.targets[0]:INVALID_FORMAT",
		`{"targets":["app/api","app/api"]}`:                                                     "spec.targets[1]:NOT_ALLOWED",
		`{"targets":["app/api"],"ingress":[{"from":[]}]}`:                                       "spec.ingress[0].from:REQUIRED",
		`{"targets":["app/api"],"ingress":[{"from":["10.0.0.1/8"]}]}`:                           "spec.ingress[0].from[0]:INVALID_FORMAT",
		`{"targets":["app/api"],"ingress":[{"from":["bucket/x"]}]}`:                             "spec.ingress[0].from[0]:INVALID_FORMAT",
		`{"targets":["app/api"],"ingress":[{"from":["any"],"to":["any"]}]}`:                     "spec.ingress[0].to:NOT_ALLOWED",
		`{"targets":["app/api"],"ingress":[{"from":["any"],"protocol":"icmp","ports":["80"]}]}`: "spec.ingress[0].ports:NOT_ALLOWED",
		`{"targets":["app/api"],"ingress":[{"from":["any"],"ports":["90-80"]}]}`:                "spec.ingress[0].ports[0]:INVALID_FORMAT",
		`{"targets":["app/api"],"ingress":[{"from":["any"],"ports":["070"]}]}`:                  "spec.ingress[0].ports[0]:INVALID_FORMAT",
		`{"targets":["app/api"],"ingress":[{"from":["any"],"protocol":"sctp"}]}`:                "spec.ingress[0].protocol:NOT_ALLOWED",
		`{"targets":["app/api"],"egress":[{"to":["any"]}]}`:                                     "spec.egress:NOT_ALLOWED",
		`{"targets":["app/api"],"egressDefault":"block"}`:                                       "spec.egressDefault:NOT_ALLOWED",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindFirewall, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	if _, errs := normalize(t, KindFirewall, `{"targets":["app/api"],"egressDefault":"deny","egress":[{"to":["any"],"ports":["443"]}]}`); len(errs) != 0 {
		t.Errorf("egress deny with rules refused: %v", errs)
	}
}

func TestParsePeerAndPorts(t *testing.T) {
	if p, ok := ParsePeer("10.1.2.3"); !ok || p.CIDR.String() != "10.1.2.3/32" {
		t.Errorf("bare address: %+v %v", p, ok)
	}
	if p, ok := ParsePeer("loadbalancer/edge"); !ok || p.Kind != KindLoadBalancer || p.Name != "edge" {
		t.Errorf("resource peer: %+v %v", p, ok)
	}
	for _, bad := range []string{"", "app/", "app/A", "zone/example", "10.0.0.0/33", "::1"} {
		if _, ok := ParsePeer(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if lo, hi, ok := ParsePortRange("8000-8100"); !ok || lo != 8000 || hi != 8100 {
		t.Errorf("range: %d %d %v", lo, hi, ok)
	}
	for _, bad := range []string{"0", "65536", "-1", "80-", "a", "1-2-3"} {
		if _, _, ok := ParsePortRange(bad); ok {
			t.Errorf("port %q accepted", bad)
		}
	}
}

func TestLoadBalancerValidation(t *testing.T) {
	s, errs := normalize(t, KindLoadBalancer, `{"port":80,"targets":[{"app":"api"}]}`)
	if len(errs) != 0 {
		t.Fatalf("valid load balancer refused: %v", errs)
	}
	lb := s.(LoadBalancerSpec)
	if lb.Protocol != LBProtocolHTTP || lb.Algorithm != AlgorithmRoundRobin || lb.Targets[0].Weight != 1 ||
		lb.HealthCheck == nil || lb.HealthCheck.Type != HealthTCP || lb.HealthCheck.IntervalSeconds != 2 || lb.HealthCheck.UnhealthyThreshold != 3 {
		t.Errorf("defaults not applied: %+v %+v", lb, lb.HealthCheck)
	}
	cases := map[string]string{
		`{"port":0,"targets":[{"app":"api"}]}`:                                                                      "spec.port:OUT_OF_RANGE",
		`{"port":80,"targets":[]}`:                                                                                  "spec.targets:REQUIRED",
		`{"port":80,"targets":[{"app":"api"},{"app":"api"}]}`:                                                       "spec.targets[1].app:NOT_ALLOWED",
		`{"port":80,"targets":[{"app":"api","weight":300}]}`:                                                        "spec.targets[0].weight:OUT_OF_RANGE",
		`{"port":80,"protocol":"udp","targets":[{"app":"api"}]}`:                                                    "spec.protocol:NOT_ALLOWED",
		`{"port":80,"algorithm":"random","targets":[{"app":"api"}]}`:                                                "spec.algorithm:NOT_ALLOWED",
		`{"port":80,"targets":[{"app":"api"}],"healthCheck":{"type":"tcp","path":"/"}}`:                             "spec.healthCheck.path:NOT_ALLOWED",
		`{"port":80,"targets":[{"app":"api"}],"healthCheck":{"type":"http","path":"x"}}`:                            "spec.healthCheck.path:INVALID_FORMAT",
		`{"port":80,"targets":[{"app":"api"}],"healthCheck":{"type":"tcp","intervalSeconds":2,"timeoutSeconds":5}}`: "spec.healthCheck.timeoutSeconds:OUT_OF_RANGE",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindLoadBalancer, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
}

func TestZoneAndRecordValidation(t *testing.T) {
	s, errs := normalize(t, KindZone, `{"provider":"cloudflare","zone":"Example.COM.","credentialsSecret":"cf-token"}`)
	if len(errs) != 0 || s.(ZoneSpec).Zone != "example.com" {
		t.Errorf("zone: %v %+v", errs, s)
	}
	if _, errs := normalize(t, KindZone, `{"provider":"godaddy","zone":"example.com","credentialsSecret":"cf-token"}`); !slices.Contains(errs, "spec.provider:NOT_ALLOWED") {
		t.Errorf("unknown provider accepted: %v", errs)
	}
	ok := []string{
		`{"zone":"example","name":"api","type":"a","machine":true}`,
		`{"zone":"example","name":"@","type":"A","values":["203.0.113.7"]}`,
		`{"zone":"example","name":"*.dev","type":"AAAA","values":["2001:db8::7"]}`,
		`{"zone":"example","name":"www","type":"CNAME","values":["App.Example.NET."]}`,
		`{"zone":"example","name":"_dmarc","type":"TXT","values":["v=DMARC1; p=none"]}`,
	}
	for _, spec := range ok {
		if _, errs := normalize(t, KindRecord, spec); len(errs) != 0 {
			t.Errorf("%s refused: %v", spec, errs)
		}
	}
	if s, _ := normalize(t, KindRecord, ok[3]); s.(RecordSpec).Values[0] != "app.example.net" || s.(RecordSpec).TTL != 300 {
		t.Errorf("CNAME not normalised: %+v", s)
	}
	cases := map[string]string{
		`{"zone":"example","name":"api","type":"A"}`:                                     "spec.values:EXACTLY_ONE",
		`{"zone":"example","name":"api","type":"A","machine":true,"values":["1.2.3.4"]}`: "spec.values:EXACTLY_ONE",
		`{"zone":"example","name":"api","type":"TXT","machine":true}`:                    "spec.machine:NOT_ALLOWED",
		`{"zone":"example","name":"api","type":"A","values":["2001:db8::1"]}`:            "spec.values[0]:INVALID_FORMAT",
		`{"zone":"example","name":"api","type":"AAAA","values":["1.2.3.4"]}`:             "spec.values[0]:INVALID_FORMAT",
		`{"zone":"example","name":"@","type":"CNAME","values":["a.example.net"]}`:        "spec.name:NOT_ALLOWED",
		`{"zone":"example","name":"api","type":"CNAME","values":["a.b.c","d.e.f"]}`:      "spec.values:NOT_ALLOWED",
		`{"zone":"example","name":"api","type":"MX","values":["mail"]}`:                  "spec.type:NOT_ALLOWED",
		`{"zone":"example","name":"a..b","type":"A","machine":true}`:                     "spec.name:INVALID_FORMAT",
		`{"zone":"example","name":"api","type":"A","machine":true,"ttl":30}`:             "spec.ttl:OUT_OF_RANGE",
		`{"zone":"example","name":"api","type":"TXT","values":["has \"quotes\""]}`:       "spec.values[0]:INVALID_FORMAT",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindRecord, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	b, _ := normalize(t, KindRecord, ok[0])
	a, _ := normalize(t, KindRecord, `{"zone":"example","name":"api","type":"AAAA","machine":true}`)
	if v := ImmutableViolations(KindRecord, b, a); len(v) != 1 {
		t.Errorf("type change not flagged immutable: %v", v)
	}
	if got := RecordFQDN("@", "example.com"); got != "example.com" {
		t.Errorf("apex fqdn %q", got)
	}
}

func TestDomainZoneAndWildcard(t *testing.T) {
	if _, errs := normalize(t, KindDomain, `{"hostname":"*.example.com","app":"api"}`); !slices.Contains(errs, "spec.hostname:NOT_ALLOWED") {
		t.Errorf("wildcard without zone accepted: %v", errs)
	}
	if _, errs := normalize(t, KindDomain, `{"hostname":"*.example.com","app":"api","zone":"example"}`); len(errs) != 0 {
		t.Errorf("wildcard with zone refused: %v", errs)
	}
	if _, errs := normalize(t, KindDomain, `{"hostname":"*.*.example.com","app":"api","zone":"example"}`); len(errs) == 0 {
		t.Error("double wildcard accepted")
	}
	if _, errs := normalize(t, KindDomain, `{"hostname":"api.example.com","app":"api","zone":"example","tls":"internal"}`); !slices.Contains(errs, "spec.tls:NOT_ALLOWED") {
		t.Errorf("zone with internal tls accepted: %v", errs)
	}
	if !InZone("a.example.com", "example.com") || InZone("aexample.com", "example.com") || !InZone("example.com", "example.com") {
		t.Error("InZone")
	}
}
