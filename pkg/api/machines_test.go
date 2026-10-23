package api

import (
	"encoding/json"
	"testing"
)

func TestPlacedKindsTakeAMachineAndLabels(t *testing.T) {
	specs := map[Kind]string{
		KindApp: `{"image":"nginx:1.27"}`, KindDatabase: `{}`, KindCache: `{}`, KindJob: `{"schedule":"@daily","image":"busybox:1.36"}`,
		KindLoadBalancer: `{"protocol":"tcp","port":80,"targets":[{"app":"web","port":80}]}`, KindVM: `{"image":"debian-12"}`,
		KindVolume: `{"type":"filesystem"}`, KindSearch: `{}`, KindEmail: `{}`,
	}
	for _, k := range PlacedKinds {
		raw := specs[k]
		if raw == "" {
			t.Fatalf("no sample spec for %s", k)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(raw), &m)
		m["machine"], m["machineLabels"] = "rack-1", map[string]string{"disk": "ssd"}
		b, _ := json.Marshal(m)
		spec, errs, err := NormalizeSpec(k, b)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		p, ok := PlacementOf(spec)
		if !ok || p.Machine != "rack-1" || p.MachineLabels["disk"] != "ssd" {
			t.Errorf("%s: placement %+v %v", k, p, ok)
		}
		for _, e := range errs {
			if e.Field == "spec.machine" || e.Field == "spec.machineLabels" || e.Field == "spec.machineLabels.disk" {
				t.Errorf("%s: placement refused: %v", k, e)
			}
		}
	}
	if _, ok := PlacementOf(BucketSpec{}); ok {
		t.Error("a bucket is placed")
	}
	if _, _, err := NormalizeSpec(KindBucket, json.RawMessage(`{"machine":"rack-1"}`)); err == nil {
		t.Error("a bucket accepted a machine")
	}
}

func TestPlacementValidation(t *testing.T) {
	cases := map[string]string{
		`{"machine":"Rack One"}`:             "spec.machine",
		`{"machineLabels":{}}`:               "spec.machineLabels",
		`{"machineLabels":{"Disk":"ssd"}}`:   "spec.machineLabels.Disk",
		`{"machineLabels":{"disk":"s s d"}}`: "spec.machineLabels.disk",
		`{"machineLabels":{"a":"1","b":"1","c":"1","d":"1","e":"1","f":"1","g":"1","h":"1","i":"1","j":"1","k":"1"}}`: "spec.machineLabels",
	}
	for raw, field := range cases {
		_, errs, err := NormalizeSpec(KindDatabase, json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := codes(errs)[field]; !ok {
			t.Errorf("%s: no error on %s: %v", raw, field, errs)
		}
	}
	_, errs, _ := NormalizeSpec(KindJob, json.RawMessage(`{"schedule":"@daily","app":"web","machine":"rack-1"}`))
	if codes(errs)["spec.machine"] != FieldNotAllowed {
		t.Errorf("an app job took a machine: %v", errs)
	}
}

func TestMachineRequests(t *testing.T) {
	if errs := ValidateAddMachine(AddMachine{Name: "rack-1", Labels: map[string]string{"disk": "ssd"}}); len(errs) != 0 {
		t.Errorf("valid machine refused: %v", errs)
	}
	if len(ValidateAddMachine(AddMachine{Name: "main"})) != 0 {
		t.Error("main is an ordinary name now")
	}
	if codes(ValidateAddMachine(AddMachine{Name: "Bad Name"}))["name"] != FieldInvalidFormat {
		t.Error("a machine named main accepted")
	}
	ok := JoinRequest{Code: "C", CSR: "pem", Advertise: "192.168.1.20", AgentPort: 7071, TunnelPort: 51820}
	if errs := ValidateJoin(ok); len(errs) != 0 {
		t.Errorf("valid join refused: %v", errs)
	}
	for _, adv := range []string{"rack-1.lan", "10.0.0.5"} {
		j := ok
		j.Advertise = adv
		if errs := ValidateJoin(j); len(errs) != 0 {
			t.Errorf("%s refused: %v", adv, errs)
		}
	}
	bad := JoinRequest{Advertise: "http://x", AgentPort: 0, TunnelPort: 70000}
	got := codes(ValidateJoin(bad))
	for _, f := range []string{"code", "csr", "advertise", "agentPort", "tunnelPort"} {
		if _, ok := got[f]; !ok {
			t.Errorf("no error on %s: %v", f, got)
		}
	}
	if !HasLabels(map[string]string{"disk": "ssd", "zone": "a"}, map[string]string{"disk": "ssd"}) || HasLabels(map[string]string{}, map[string]string{"disk": "ssd"}) {
		t.Error("HasLabels")
	}
}

func TestMachineDisplayName(t *testing.T) {
	for host, want := range map[string]string{
		"sunnys-mbp.local": "sunnys-mbp", "Sunny's MacBook Pro": "Sunny's MacBook Pro", "rack-1": "rack-1", "  ": "This computer", "": "This computer", ".local": "This computer",
	} {
		if got := MachineNameFromHostname(host); got != want {
			t.Errorf("MachineNameFromHostname(%q) = %q, want %q", host, got, want)
		}
	}
	if got := MachineNameFromHostname(string(make([]rune, 65))); got != "This computer" {
		t.Errorf("a host name over the limit gave %q", got)
	}
	for title, ok := range map[string]bool{"Home lab": true, "x": true, "": false, "   ": false} {
		if got := len(ValidateMachineTitle(title)) == 0; got != ok {
			t.Errorf("ValidateMachineTitle(%q) valid = %v, want %v", title, got, ok)
		}
	}
}

func TestMachineHandleFromHostname(t *testing.T) {
	for host, want := range map[string]string{
		"sunnys-mbp.local": "sunnys-mbp", "Sunny's MacBook Pro": "sunny-s-macbook-pro", "RACK_01": "rack-01", "9lives": "m-9lives",
		"": "computer", "...": "computer", "--": "computer", "ab": "computer",
	} {
		if got := MachineHandleFromHostname(host); got != want {
			t.Errorf("MachineHandleFromHostname(%q) = %q, want %q", host, got, want)
		}
		if got := MachineHandleFromHostname(host); ValidateName(got) != nil {
			t.Errorf("%q gives %q, which is not a valid name", host, got)
		}
	}
	long := MachineHandleFromHostname("a-very-long-computer-name-that-goes-on-and-on-and-on-forever")
	if len(long) > NameMaxLen || ValidateName(long) != nil {
		t.Errorf("a long name gave %q", long)
	}
}
