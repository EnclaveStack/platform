package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func codes(errs []FieldError) map[string]FieldCode {
	m := map[string]FieldCode{}
	for _, e := range errs {
		m[e.Field] = e.Code
	}
	return m
}

func TestValidateName(t *testing.T) {
	ok := []string{"abc", "orders", "orders-db", "a1-b2", strings.Repeat("a", 40)}
	for _, n := range ok {
		if errs := ValidateName(n); errs != nil {
			t.Errorf("%q should be valid, got %v", n, errs)
		}
	}
	bad := map[string]FieldCode{"": FieldRequired, "ab": FieldTooShort, strings.Repeat("a", 41): FieldTooLong,
		"Orders": FieldInvalidFormat, "1abc": FieldInvalidFormat, "abc-": FieldInvalidFormat, "a_b_c": FieldInvalidFormat, "ab c": FieldInvalidFormat}
	for n, want := range bad {
		errs := ValidateName(n)
		if len(errs) != 1 || errs[0].Code != want || errs[0].Field != "name" {
			t.Errorf("%q: want %s, got %v", n, want, errs)
		}
	}
}

func TestNormalizeAppDefaultsAndRules(t *testing.T) {
	s, errs, err := NormalizeSpec(KindApp, json.RawMessage(`{"image":"nginx:1.27"}`))
	if err != nil || len(errs) != 0 {
		t.Fatalf("valid app rejected: %v %v", err, errs)
	}
	if s.(AppSpec).MemoryMiB != AppMemoryDefaultMiB {
		t.Errorf("memory default not applied: %+v", s)
	}
	cases := map[string]map[string]FieldCode{
		`{}`:                                   {"spec.image": FieldRequired},
		`{"image":"NGINX"}`:                    {"spec.image": FieldInvalidFormat},
		`{"image":"nginx","port":70000}`:       {"spec.port": FieldOutOfRange},
		`{"image":"nginx","publish":true}`:     {"spec.port": FieldRequired},
		`{"image":"nginx","memoryMiB":32}`:     {"spec.memoryMiB": FieldOutOfRange},
		`{"image":"nginx","env":{"1BAD":"x"}}`: {"spec.env.1BAD": FieldInvalidFormat},
		`{"image":"nginx","env":{"A":"x"},"secretEnv":{"A":"sec"}}`: {"spec.secretEnv.A": FieldNotAllowed},
		`{"image":"nginx","secretEnv":{"K":"Not_A_Name"}}`:          {"spec.secretEnv.K": FieldInvalidFormat},
	}
	for spec, want := range cases {
		_, errs, err := NormalizeSpec(KindApp, json.RawMessage(spec))
		if err != nil {
			t.Fatalf("%s: decode error %v", spec, err)
		}
		got := codes(errs)
		for f, c := range want {
			if got[f] != c {
				t.Errorf("%s: field %s want %s, got %v", spec, f, c, errs)
			}
		}
	}
	for _, img := range []string{"nginx", "nginx:1.27-alpine", "ghcr.io/acme/api:1.4", "localhost:5000/team/app", "redis@sha256:" + strings.Repeat("a", 64)} {
		if _, errs, _ := NormalizeSpec(KindApp, json.RawMessage(`{"image":"`+img+`"}`)); len(errs) > 0 {
			t.Errorf("image %s should be valid: %v", img, errs)
		}
	}
}

func TestNormalizeRejectsUnknownFields(t *testing.T) {
	if _, _, err := NormalizeSpec(KindApp, json.RawMessage(`{"image":"nginx","imag":"x"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, _, err := NormalizeSpec(KindDatabase, json.RawMessage(`{"version":"16"} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestNormalizeDatabaseCacheBucketSecret(t *testing.T) {
	s, errs, _ := NormalizeSpec(KindDatabase, nil)
	if len(errs) != 0 || !reflect.DeepEqual(s, DatabaseSpec{Engine: "postgres", Version: "16"}) {
		t.Errorf("database defaults: %+v %v", s, errs)
	}
	if _, errs, _ := NormalizeSpec(KindDatabase, json.RawMessage(`{"version":"15"}`)); codes(errs)["spec.version"] != FieldNotAllowed {
		t.Errorf("version 15 accepted: %v", errs)
	}
	if _, errs, _ := NormalizeSpec(KindDatabase, json.RawMessage(`{"engine":"oracle"}`)); codes(errs)["spec.engine"] != FieldNotAllowed {
		t.Errorf("oracle accepted: %v", errs)
	}
	c, errs, _ := NormalizeSpec(KindCache, json.RawMessage(`{}`))
	if len(errs) != 0 || c.(CacheSpec).MaxMemoryMiB != CacheMemoryDefault {
		t.Errorf("cache defaults: %+v %v", c, errs)
	}
	if _, errs, _ := NormalizeSpec(KindCache, json.RawMessage(`{"maxMemoryMiB":5000}`)); codes(errs)["spec.maxMemoryMiB"] != FieldOutOfRange {
		t.Errorf("cache 5000 MiB accepted: %v", errs)
	}
	if _, errs, _ := NormalizeSpec(KindBucket, json.RawMessage(`{"versioning":true}`)); len(errs) != 0 {
		t.Errorf("bucket rejected: %v", errs)
	}
	if _, errs, _ := NormalizeSpec(KindSecret, json.RawMessage(`{}`)); codes(errs)["spec.value"] != FieldRequired {
		t.Errorf("empty secret accepted: %v", errs)
	}
	big, _ := json.Marshal(SecretSpec{Value: strings.Repeat("x", SecretValueMaxBytes+1)})
	if _, errs, _ := NormalizeSpec(KindSecret, big); codes(errs)["spec.value"] != FieldTooLong {
		t.Errorf("oversized secret accepted: %v", errs)
	}
	if _, errs, _ := NormalizeSpec("lambda", nil); codes(errs)["kind"] != FieldNotAllowed {
		t.Errorf("unknown kind accepted: %v", errs)
	}
}

func TestMergeSpecReplacesTopLevelFieldsAndMaps(t *testing.T) {
	stored := json.RawMessage(`{"image":"nginx:1","env":{"A":"1","B":"2"},"port":80}`)
	merged, err := MergeSpec(stored, json.RawMessage(`{"image":"nginx:2","env":{"C":"3"},"port":null}`))
	if err != nil {
		t.Fatal(err)
	}
	var s AppSpec
	if err := json.Unmarshal(merged, &s); err != nil {
		t.Fatal(err)
	}
	if s.Image != "nginx:2" || len(s.Env) != 1 || s.Env["C"] != "3" || s.Port != 0 {
		t.Errorf("merge result %+v", s)
	}
	if _, err := MergeSpec(stored, json.RawMessage(`[1]`)); err == nil {
		t.Error("non-object patch accepted")
	}
}

func TestImmutableViolations(t *testing.T) {
	if v := ImmutableViolations(KindDatabase, DatabaseSpec{Engine: "postgres", Version: "16"}, DatabaseSpec{Engine: "postgres", Version: "17"}); len(v) != 1 || v[0].Field != "spec.version" {
		t.Errorf("version change not flagged: %v", v)
	}
	if v := ImmutableViolations(KindDatabase, DatabaseSpec{Engine: "postgres", Version: "16"}, DatabaseSpec{Engine: "postgres", Version: "16"}); v != nil {
		t.Errorf("no change flagged: %v", v)
	}
	if v := ImmutableViolations(KindCache, CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 256}, CacheSpec{Engine: "redis", Version: "7", MaxMemoryMiB: 512}); v != nil {
		t.Errorf("memory change should be allowed: %v", v)
	}
	if v := ImmutableViolations(KindApp, AppSpec{Image: "a"}, AppSpec{Image: "b"}); v != nil {
		t.Errorf("app image change should be allowed: %v", v)
	}
}

func TestSetupAndTokenValidation(t *testing.T) {
	if errs := ValidateSetup(SetupRequest{MachineName: "M", Email: "a@b.c", Password: "123456789012"}); errs != nil {
		t.Errorf("valid setup rejected: %v", errs)
	}
	got := codes(ValidateSetup(SetupRequest{Email: "nope", Password: "short"}))
	if got["machineName"] == "" || got["email"] == "" || got["password"] == "" {
		t.Errorf("invalid setup not fully reported: %v", got)
	}
	if errs := ValidateCreateToken(CreateTokenRequest{Name: "ci", Scope: "write", ExpiresInDays: 30}); errs != nil {
		t.Errorf("valid token rejected: %v", errs)
	}
	got = codes(ValidateCreateToken(CreateTokenRequest{Scope: "admin", ExpiresInDays: 400}))
	if got["name"] == "" || got["scope"] == "" || got["expiresInDays"] == "" {
		t.Errorf("invalid token not fully reported: %v", got)
	}
}

func TestEveryErrorCodeHasAStatusAndIsUpperSnake(t *testing.T) {
	for code, status := range ErrorCodes {
		if strings.ToUpper(string(code)) != string(code) || strings.ContainsAny(string(code), " -") {
			t.Errorf("%s is not UPPER_SNAKE (L0-003)", code)
		}
		if http.StatusText(status) == "" {
			t.Errorf("%s maps to unknown status %d", code, status)
		}
	}
	if (&Error{Code: "MADE_UP"}).Status() != 500 {
		t.Error("unknown code should be 500")
	}
}

func TestNormalizeHealthCheckAndStrategy(t *testing.T) {
	s, errs, err := NormalizeSpec(KindApp, json.RawMessage(`{"image":"nginx","port":80,"healthCheck":{"type":"http"}}`))
	if err != nil || len(errs) != 0 {
		t.Fatalf("valid health check rejected: %v %v", err, errs)
	}
	app := s.(AppSpec)
	h := app.HealthCheck
	if app.Strategy != StrategyRolling || h.Path != "/" || h.IntervalSeconds != HealthIntervalDefault || h.TimeoutSeconds != HealthTimeoutDefault || h.Retries != HealthRetriesDefault {
		t.Errorf("defaults not applied: %+v %+v", app, h)
	}
	if s, _, _ := NormalizeSpec(KindApp, json.RawMessage(`{"image":"nginx"}`)); s.(AppSpec).HealthCheck != nil {
		t.Error("no health check given: the implicit default is decided by the engine, not stored")
	}
	cases := map[string]map[string]FieldCode{
		`{"image":"nginx","strategy":"blue-green"}`:                                                       {"spec.strategy": FieldNotAllowed},
		`{"image":"nginx","healthCheck":{"type":"ping"}}`:                                                 {"spec.healthCheck.type": FieldNotAllowed},
		`{"image":"nginx","healthCheck":{"type":"tcp"}}`:                                                  {"spec.healthCheck.port": FieldRequired},
		`{"image":"nginx","port":80,"healthCheck":{"type":"http","path":"healthz"}}`:                      {"spec.healthCheck.path": FieldInvalidFormat},
		`{"image":"nginx","healthCheck":{"type":"command"}}`:                                              {"spec.healthCheck.command": FieldRequired},
		`{"image":"nginx","port":80,"healthCheck":{"type":"tcp","path":"/x"}}`:                            {"spec.healthCheck.path": FieldNotAllowed},
		`{"image":"nginx","port":80,"healthCheck":{"type":"tcp","intervalSeconds":301}}`:                  {"spec.healthCheck.intervalSeconds": FieldOutOfRange},
		`{"image":"nginx","port":80,"healthCheck":{"type":"tcp","intervalSeconds":2,"timeoutSeconds":5}}`: {"spec.healthCheck.timeoutSeconds": FieldOutOfRange},
		`{"image":"nginx","port":80,"healthCheck":{"type":"tcp","retries":101}}`:                          {"spec.healthCheck.retries": FieldOutOfRange},
		`{"image":"nginx","port":80,"healthCheck":{"type":"tcp","startPeriodSeconds":601}}`:               {"spec.healthCheck.startPeriodSeconds": FieldOutOfRange},
		`{"image":"nginx","port":80,"healthCheck":{"type":"tcp","intervalSeconds":60,"retries":20}}`:      {"spec.healthCheck.retries": FieldOutOfRange},
	}
	for spec, want := range cases {
		_, errs, err := NormalizeSpec(KindApp, json.RawMessage(spec))
		if err != nil {
			t.Fatalf("%s: decode error %v", spec, err)
		}
		got := codes(errs)
		for f, c := range want {
			if got[f] != c {
				t.Errorf("%s: field %s want %s, got %v", spec, f, c, errs)
			}
		}
	}
	for _, ok := range []string{
		`{"image":"nginx","healthCheck":{"type":"tcp","port":8080}}`,
		`{"image":"nginx","healthCheck":{"type":"command","command":["pg_isready"]}}`,
		`{"image":"nginx","healthCheck":{"type":"none"},"strategy":"recreate"}`,
	} {
		if _, errs, _ := NormalizeSpec(KindApp, json.RawMessage(ok)); len(errs) > 0 {
			t.Errorf("%s should be valid: %v", ok, errs)
		}
	}
}

func TestParseScheduleAndNext(t *testing.T) {
	s, err := ParseSchedule("0 3 * * *", "Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) // 05:30 IST: today's 03:00 IST has passed
	if got, want := s.Next(from), time.Date(2026, 10, 21, 21, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("next 03:00 IST = %s, want %s", got, want)
	}
	for _, d := range Descriptors {
		if _, err := ParseSchedule(d, ""); err != nil {
			t.Errorf("%s rejected: %v", d, err)
		}
	}
	for _, bad := range []string{"", "@every 5m", "* * * *", "61 * * * *", "0 3 * * * *", "@reboot"} {
		if _, err := ParseSchedule(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseSchedule("@daily", "Mars/Olympus"); err == nil {
		t.Error("unknown time zone accepted")
	}
}

func TestNormalizeJobDefaultsAndRules(t *testing.T) {
	s, errs, err := NormalizeSpec(KindJob, json.RawMessage(`{"schedule":" @daily ","app":"api"}`))
	if err != nil || len(errs) != 0 {
		t.Fatalf("valid job rejected: %v %v", err, errs)
	}
	if j := s.(JobSpec); j.Schedule != "@daily" || j.TimeoutSeconds != JobTimeoutDefault || j.Overlap != OverlapForbid {
		t.Errorf("defaults not applied: %+v", j)
	}
	for spec, field := range map[string]string{
		`{"schedule":"@daily"}`:                                          "spec.app",
		`{"schedule":"@daily","app":"api","image":"busybox"}`:            "spec.app",
		`{"schedule":"@every 5m","image":"busybox"}`:                     "spec.schedule",
		`{"schedule":"@daily","image":"busybox","timeZone":"Nowhere/X"}`: "spec.timeZone",
		`{"schedule":"@daily","image":"busybox","timeoutSeconds":90000}`: "spec.timeoutSeconds",
		`{"schedule":"@daily","image":"busybox","overlap":"queue"}`:      "spec.overlap",
	} {
		_, errs, _ := NormalizeSpec(KindJob, json.RawMessage(spec))
		if _, ok := codes(errs)[field]; !ok {
			t.Errorf("%s: want an error on %s, got %v", spec, field, errs)
		}
	}
}

func TestNormalizeBackupDefaultsAndRules(t *testing.T) {
	s, errs, err := NormalizeSpec(KindBackup, json.RawMessage(`{"source":{"database":"shop"},"destination":{"bucket":"vault"}}`))
	if err != nil || len(errs) != 0 {
		t.Fatalf("valid backup rejected: %v %v", err, errs)
	}
	b := s.(BackupSpec)
	if b.Prefix != BackupPrefixDefault || b.Schedule != BackupScheduleDefault || b.Keep != BackupKeepDefault || b.TimeoutSeconds != BackupTimeoutDefault || b.KeepDays != 0 {
		t.Errorf("defaults not applied: %+v", b)
	}
	s3 := `"s3":{"endpoint":"https://s3.eu-central-1.amazonaws.com","bucket":"acme-backups","credentialsSecret":"offsite"}`
	for _, ok := range []string{
		`{"source":{"cache":"sessions"},"destination":{` + s3 + `},"keep":30,"keepDays":90,"schedule":"@hourly","timeZone":"Asia/Kolkata"}`,
		`{"source":{"cache":"sessions"},"destination":{"s3":{"endpoint":"http://127.0.0.1:9000","bucket":"local","credentialsSecret":"k-ey"}}}`,
	} {
		if _, errs, err := NormalizeSpec(KindBackup, json.RawMessage(ok)); err != nil || len(errs) != 0 {
			t.Errorf("%s rejected: %v %v", ok, err, errs)
		}
	}
	for spec, want := range map[string]struct {
		field string
		code  FieldCode
	}{
		`{"source":{},"destination":{"bucket":"vault"}}`:                                                                                        {"spec.source", FieldExactlyOne},
		`{"source":{"database":"a-db","cache":"c-c"},"destination":{"bucket":"vault"}}`:                                                         {"spec.source", FieldExactlyOne},
		`{"source":{"database":"Bad_Name"},"destination":{"bucket":"vault"}}`:                                                                   {"spec.source", FieldInvalidFormat},
		`{"source":{"database":"shop"},"destination":{}}`:                                                                                       {"spec.destination", FieldExactlyOne},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault",` + s3 + `}}`:                                                            {"spec.destination", FieldExactlyOne},
		`{"source":{"database":"shop"},"destination":{"s3":{"endpoint":"http://s3.example.com","bucket":"abc","credentialsSecret":"k-ey"}}}`:    {"spec.destination.s3.endpoint", FieldInvalidFormat},
		`{"source":{"database":"shop"},"destination":{"s3":{"endpoint":"https://s3.example.com/x","bucket":"abc","credentialsSecret":"k-ey"}}}`: {"spec.destination.s3.endpoint", FieldInvalidFormat},
		`{"source":{"database":"shop"},"destination":{"s3":{"endpoint":"https://s3.example.com","bucket":"A","credentialsSecret":"k-ey"}}}`:     {"spec.destination.s3.bucket", FieldInvalidFormat},
		`{"source":{"database":"shop"},"destination":{"s3":{"endpoint":"https://s3.example.com","bucket":"abc"}}}`:                              {"spec.destination.s3.credentialsSecret", FieldRequired},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault"},"prefix":"a/../b"}`:                                                     {"spec.prefix", FieldInvalidFormat},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault"},"prefix":"a//b"}`:                                                       {"spec.prefix", FieldInvalidFormat},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault"},"schedule":"@every 1h"}`:                                                {"spec.schedule", FieldInvalidSchedule},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault"},"keep":366}`:                                                            {"spec.keep", FieldOutOfRange},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault"},"keepDays":3651}`:                                                       {"spec.keepDays", FieldOutOfRange},
		`{"source":{"database":"shop"},"destination":{"bucket":"vault"},"timeoutSeconds":59}`:                                                   {"spec.timeoutSeconds", FieldOutOfRange},
	} {
		_, errs, _ := NormalizeSpec(KindBackup, json.RawMessage(spec))
		if got := codes(errs)[want.field]; got != want.code {
			t.Errorf("%s: want %s on %s, got %v", spec, want.code, want.field, errs)
		}
	}
	before, after := BackupSpec{Source: BackupSource{Database: "shop"}}, BackupSpec{Source: BackupSource{Database: "other"}}
	if v := ImmutableViolations(KindBackup, before, after); len(v) != 1 || v[0].Field != "spec.source" {
		t.Errorf("source change not flagged: %v", v)
	}
	after = BackupSpec{Source: BackupSource{Database: "shop"}, Keep: 30}
	if v := ImmutableViolations(KindBackup, before, after); v != nil {
		t.Errorf("retention change flagged: %v", v)
	}
}

func TestValidateRestore(t *testing.T) {
	if errs := ValidateRestore(RestoreRequest{RunID: "run_1", Mode: RestoreNew, Name: "shop-copy"}); errs != nil {
		t.Errorf("valid new restore: %v", errs)
	}
	if errs := ValidateRestore(RestoreRequest{RunID: "run_1", Mode: RestoreReplace, ConfirmName: "shop"}); errs != nil {
		t.Errorf("valid replace restore: %v", errs)
	}
	for _, tc := range []struct {
		req   RestoreRequest
		field string
	}{
		{RestoreRequest{Mode: RestoreNew, Name: "shop-copy"}, "runId"},
		{RestoreRequest{RunID: "run_1", Mode: RestoreNew, Name: "X"}, "name"},
		{RestoreRequest{RunID: "run_1", Mode: RestoreReplace}, "confirmName"},
		{RestoreRequest{RunID: "run_1", Mode: "merge"}, "mode"},
	} {
		if _, ok := codes(ValidateRestore(tc.req))[tc.field]; !ok {
			t.Errorf("%+v: want an error on %s", tc.req, tc.field)
		}
	}
}

func TestNormalizeAppFromGit(t *testing.T) {
	s, errs, err := NormalizeSpec(KindApp, json.RawMessage(`{"source":{"git":"https://github.com/acme/api.git","ref":"main"}}`))
	if err != nil || len(errs) != 0 {
		t.Fatalf("valid source app rejected: %v %v", err, errs)
	}
	if g := s.(AppSpec).Source; g.Dockerfile != GitDockerfileDefault || g.Context != GitContextDefault {
		t.Errorf("defaults not applied: %+v", g)
	}
	for _, ok := range []string{
		`{"source":{"git":"ssh://git@github.com/acme/api.git","ref":"v1.2","dockerfile":"docker/Dockerfile","context":"svc/api","credentialsSecret":"git-key","buildArgs":{"NODE_ENV":"production"}}}`,
		`{"source":{"git":"http://127.0.0.1:3000/acme/api.git","ref":"0123456789abcdef0123456789abcdef01234567"}}`,
	} {
		if _, errs, err := NormalizeSpec(KindApp, json.RawMessage(ok)); err != nil || len(errs) != 0 {
			t.Errorf("%s rejected: %v %v", ok, err, errs)
		}
	}
	for spec, want := range map[string]struct {
		field string
		code  FieldCode
	}{
		`{"image":"nginx","source":{"git":"https://github.com/a/b.git","ref":"main"}}`: {"spec.image", FieldExactlyOne},
		`{}`: {"spec.image", FieldRequired},
		`{"source":{"git":"http://github.com/a/b.git","ref":"main"}}`:                            {"spec.source.git", FieldInvalidFormat},
		`{"source":{"git":"file:///etc","ref":"main"}}`:                                          {"spec.source.git", FieldInvalidFormat},
		`{"source":{"git":"https://github.com/a/b.git","ref":""}}`:                               {"spec.source.ref", FieldInvalidFormat},
		`{"source":{"git":"https://github.com/a/b.git","ref":"a b"}}`:                            {"spec.source.ref", FieldInvalidFormat},
		`{"source":{"git":"https://github.com/a/b.git","ref":"main","dockerfile":"../x"}}`:       {"spec.source.dockerfile", FieldInvalidFormat},
		`{"source":{"git":"https://github.com/a/b.git","ref":"main","context":"/abs"}}`:          {"spec.source.context", FieldInvalidFormat},
		`{"source":{"git":"https://github.com/a/b.git","ref":"main","credentialsSecret":"Bad"}}`: {"spec.source.credentialsSecret", FieldInvalidFormat},
		`{"source":{"git":"https://github.com/a/b.git","ref":"main","buildArgs":{"1A":"x"}}}`:    {"spec.source.buildArgs.1A", FieldInvalidFormat},
	} {
		_, errs, _ := NormalizeSpec(KindApp, json.RawMessage(spec))
		if got := codes(errs)[want.field]; got != want.code {
			t.Errorf("%s: want %s on %s, got %v", spec, want.code, want.field, errs)
		}
	}
}

// MachineSettings.inviteEmail: absent keeps it, null turns it off, a value is checked (IAM-04, L3-002 §10.3).
func TestMachineSettingsInviteEmailKeepsClearsAndValidates(t *testing.T) {
	var m MachineSettings
	if err := json.Unmarshal([]byte(`{"publicAddress":"1.2.3.4"}`), &m); err != nil || m.InviteEmail != nil || m.ClearInviteEmail {
		t.Errorf("absent: %+v %v", m, err)
	}
	m = MachineSettings{}
	if err := json.Unmarshal([]byte(`{"inviteEmail":null}`), &m); err != nil || !m.ClearInviteEmail {
		t.Errorf("null: %+v %v", m, err)
	}
	m = MachineSettings{}
	if err := json.Unmarshal([]byte(`{"inviteEmail":{"service":"mail","from":"hc@example.com"}}`), &m); err != nil || m.InviteEmail == nil || m.InviteEmail.Service != "mail" {
		t.Errorf("set: %+v %v", m, err)
	}
	if b, _ := json.Marshal(MachineSettings{ClearInviteEmail: true}); !strings.Contains(string(b), `"inviteEmail":null`) {
		t.Errorf("marshal clear: %s", b)
	}
	if b, _ := json.Marshal(MachineSettings{}); strings.Contains(string(b), "inviteEmail") {
		t.Errorf("marshal keep: %s", b)
	}
	errs := ValidateMachineSettings(MachineSettings{InviteEmail: &InviteEmail{Project: "No!", Service: "", From: "nope"}})
	fields := map[string]bool{}
	for _, e := range errs {
		fields[e.Field] = true
	}
	if !fields["inviteEmail.project"] || !fields["inviteEmail.service"] || !fields["inviteEmail.from"] {
		t.Errorf("validation: %+v", errs)
	}
}

func TestFormatMetricAndWords(t *testing.T) {
	for _, c := range []struct {
		v    float64
		unit string
		want string
	}{
		{1.5 * (1 << 30), UnitBytes, "1.5 GiB"}, {512, UnitBytesPerSecond, "512 B/s"}, {51.58, UnitPercent, "51.6%"},
		{0.5, UnitCores, "0.500 cores"}, {12.345, UnitPerSecond, "12.35/s"}, {3, UnitCount, "3"},
	} {
		if got := FormatMetric(c.v, c.unit); got != c.want {
			t.Errorf("FormatMetric(%v, %s) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
	if Minutes(1) != "1 minute" || Minutes(5) != "5 minutes" || RefText(MachineRef) != "this machine" || RefText("app/api") != "app/api" {
		t.Error("words")
	}
}
