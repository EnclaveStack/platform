package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Validation limits (L3-002 §4). They are exported so cmd/gencontracts can hand the same numbers and
// patterns to the console — the rules exist once.
const (
	NamePatternSource   = `^[a-z][a-z0-9-]{1,38}[a-z0-9]$`
	NameMinLen          = 3
	NameMaxLen          = 40
	ImagePatternSource  = `^(?:[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]{1,5})?/)?[a-z0-9]+(?:(?:[._]|__|[-]+|/)[a-z0-9]+)*(?::[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`
	ImageMaxLen         = 255
	EnvKeyPatternSource = `^[A-Za-z_][A-Za-z0-9_]{0,127}$`
	EnvMaxEntries       = 100
	EnvValueMaxLen      = 4096
	CommandMaxItems     = 32
	CommandItemMaxLen   = 1024
	PortMin             = 1
	PortMax             = 65535
	AppMemoryMinMiB     = 64
	AppMemoryMaxMiB     = 16384
	AppMemoryDefaultMiB = 256
	CacheMemoryMinMiB   = 64
	CacheMemoryMaxMiB   = 4096
	CacheMemoryDefault  = 256
	SecretValueMaxBytes = 65536
	PasswordMinLen      = 12
	PasswordMaxLen      = 256
	MachineNameMaxLen   = 64
	EmailMaxLen         = 254
	TokenNameMaxLen     = 64
	TokenMaxExpiryDays  = 365
	DatabaseEngine      = EnginePostgres // the default engine
	DefaultDBVersion    = "16"           // PostgreSQL's default version
	CacheEngine         = "redis"
	CacheVersion        = "7"

	HealthIntervalMin, HealthIntervalMax, HealthIntervalDefault = 1, 300, 5
	HealthTimeoutMin, HealthTimeoutMax, HealthTimeoutDefault    = 1, 60, 3
	HealthRetriesMin, HealthRetriesMax, HealthRetriesDefault    = 1, 100, 12
	HealthStartPeriodMax                                        = 600
	HealthBudgetMaxSeconds                                      = 900
	HealthPathMaxLen                                            = 255

	HostnameMaxLen         = 253
	HostLabelPatternSource = `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	LocalhostSuffix        = ".localhost"

	JobTimeoutMin, JobTimeoutMax, JobTimeoutDefault = 1, 86400, 3600

	GitURLMaxLen, GitRefMaxLen, BuildArgsMax = 512, 255, 50
	GitDockerfileDefault, GitContextDefault  = "Dockerfile", "."

	BackupPrefixDefault, BackupScheduleDefault               = "enclave-backups", "0 3 * * *"
	BackupKeepMin, BackupKeepMax, BackupKeepDefault          = 1, 365, 7
	BackupKeepDaysMax                                        = 3650
	BackupTimeoutMin, BackupTimeoutMax, BackupTimeoutDefault = 60, 86400, 7200
	BackupPrefixPatternSource                                = `^[a-z0-9][a-z0-9/_-]{0,99}$`
	S3BucketPatternSource                                    = `^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`
)

var (
	backupPrefixPattern = regexp.MustCompile(BackupPrefixPatternSource)
	s3BucketPattern     = regexp.MustCompile(S3BucketPatternSource)
)

// OverlapPolicies are the allowed job overlap policies.
var OverlapPolicies = []string{OverlapForbid, OverlapAllow}

// TLSModes are the allowed domain TLS modes.
var TLSModes = []string{TLSAuto, TLSInternal, TLSOff}

var hostLabelPattern = regexp.MustCompile(HostLabelPatternSource)

// HealthCheckTypes and Strategies are the allowed values (L3-002 §6.5).
var (
	HealthCheckTypes = []string{HealthHTTP, HealthTCP, HealthCommand, HealthNone}
	Strategies       = []string{StrategyRolling, StrategyRecreate}
)

// DatabaseVersions are the supported PostgreSQL major versions.
var DatabaseVersions = DatabaseEngineVersions[EnginePostgres]

var (
	namePattern   = regexp.MustCompile(NamePatternSource)
	imagePattern  = regexp.MustCompile(ImagePatternSource)
	envKeyPattern = regexp.MustCompile(EnvKeyPatternSource)
)

// Messages are written for people (L0-003 §3): they name the field and the rule.
const (
	msgName = "Use 3–40 lowercase letters, numbers or hyphens, starting with a letter and not ending with a hyphen."
	// KindChoiceMessage answers an unknown kind.
	KindChoiceMessage = "Choose one of app, database, cache, bucket, secret, domain, job, backup, network, firewall, loadbalancer, zone, record, vm, volume, snapshot, queue, topic, search, transcoder, cdn, email, dashboard, alert, channel, logexport, secretstore or tunnel."
)

// ValidateName checks a resource name.
func ValidateName(name string) []FieldError {
	switch {
	case name == "":
		return []FieldError{{Field: "name", Code: FieldRequired, Message: "Enter a name."}}
	case len(name) < NameMinLen:
		return []FieldError{{Field: "name", Code: FieldTooShort, Message: msgName}}
	case len(name) > NameMaxLen:
		return []FieldError{{Field: "name", Code: FieldTooLong, Message: msgName}}
	case !namePattern.MatchString(name):
		return []FieldError{{Field: "name", Code: FieldInvalidFormat, Message: msgName}}
	}
	return nil
}

// DecodeStrict decodes JSON rejecting unknown fields and trailing data.
func DecodeStrict(raw []byte, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected data after the JSON value")
	}
	return nil
}

// NormalizeSpec decodes a kind's spec, applies defaults and validates it. It returns the typed spec
// (AppSpec, DatabaseSpec, …), field errors, or a decode error for malformed JSON.
func NormalizeSpec(kind Kind, raw json.RawMessage) (any, []FieldError, error) {
	spec, errs, err := normalizeSpec(kind, raw)
	if err != nil {
		return spec, errs, err
	}
	if p, ok := PlacementOf(spec); ok {
		errs = append(errs, validatePlaced(p)...)
		if j, isJob := spec.(JobSpec); isJob && j.App != "" && p.Machine != "" {
			errs = append(errs, FieldError{Field: "spec.machine", Code: FieldNotAllowed, Message: "A job of an app runs where the app runs. Leave machine out."})
		}
	}
	return spec, errs, nil
}

func normalizeSpec(kind Kind, raw json.RawMessage) (any, []FieldError, error) {
	switch kind {
	case KindApp:
		var s AppSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		if s.MemoryMiB == 0 {
			s.MemoryMiB = AppMemoryDefaultMiB
		}
		if s.Strategy == "" {
			s.Strategy = StrategyRolling
		}
		if s.Replicas == 0 {
			s.Replicas = ReplicasDefault
		}
		if g := s.Source; g != nil {
			if g.Dockerfile == "" {
				g.Dockerfile = GitDockerfileDefault
			}
			if g.Context == "" {
				g.Context = GitContextDefault
			}
		}
		if h := s.HealthCheck; h != nil {
			if h.IntervalSeconds == 0 {
				h.IntervalSeconds = HealthIntervalDefault
			}
			if h.TimeoutSeconds == 0 {
				h.TimeoutSeconds = min(HealthTimeoutDefault, h.IntervalSeconds)
			}
			if h.Retries == 0 {
				h.Retries = HealthRetriesDefault
			}
			if h.Type == HealthHTTP && h.Path == "" {
				h.Path = "/"
			}
		}
		return s, validateApp(s), nil
	case KindDatabase:
		var s DatabaseSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		if s.Engine == "" {
			s.Engine = DatabaseEngine
		}
		if s.Version == "" {
			s.Version = DatabaseDefaultVersions[s.Engine]
		}
		return s, append(validateDatabaseEngine(s), validatePlacement(s.Network, s.Subnet)...), nil
	case KindCache:
		var s CacheSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		if s.Engine == "" {
			s.Engine = CacheEngine
		}
		if s.Version == "" {
			s.Version = CacheVersion
		}
		if s.MaxMemoryMiB == 0 {
			s.MaxMemoryMiB = CacheMemoryDefault
		}
		var errs []FieldError
		if s.Engine != CacheEngine {
			errs = append(errs, FieldError{Field: "spec.engine", Code: FieldNotAllowed, Message: "Only redis is supported."})
		}
		if s.Version != CacheVersion {
			errs = append(errs, FieldError{Field: "spec.version", Code: FieldNotAllowed, Message: "Only version 7 is supported."})
		}
		if s.MaxMemoryMiB < CacheMemoryMinMiB || s.MaxMemoryMiB > CacheMemoryMaxMiB {
			errs = append(errs, FieldError{Field: "spec.maxMemoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB.", CacheMemoryMinMiB, CacheMemoryMaxMiB)})
		}
		return s, append(errs, validatePlacement(s.Network, s.Subnet)...), nil
	case KindBucket:
		var s BucketSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		return s, nil, nil
	case KindDomain:
		var s DomainSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s.Hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Hostname)), ".")
		s = normalizeDomainTLS(s)
		s.Tunnel = strings.TrimSpace(s.Tunnel)
		if s.Gate != nil {
			g := normalizeGate(*s.Gate)
			s.Gate = &g
		}
		s.CloudflareAccess = normalizeAccess(s.CloudflareAccess)
		return s, append(validateDomain(s), validateDomainAccess(s)...), nil
	case KindJob:
		var s JobSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s.Schedule = strings.TrimSpace(s.Schedule)
		if s.TimeoutSeconds == 0 {
			s.TimeoutSeconds = JobTimeoutDefault
		}
		if s.Overlap == "" {
			s.Overlap = OverlapForbid
		}
		if s.MemoryMiB == 0 {
			s.MemoryMiB = AppMemoryDefaultMiB
		}
		return s, validateJob(s), nil
	case KindBackup:
		var s BackupSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		if s.Prefix == "" {
			s.Prefix = BackupPrefixDefault
		}
		if s.Schedule == "" {
			s.Schedule = BackupScheduleDefault
		}
		s.Schedule = strings.TrimSpace(s.Schedule)
		if s.Keep == 0 {
			s.Keep = BackupKeepDefault
		}
		if s.TimeoutSeconds == 0 {
			s.TimeoutSeconds = BackupTimeoutDefault
		}
		return s, validateBackup(s), nil
	case KindNetwork:
		var s NetworkSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeNetwork(s)
		return s, validateNetwork(s), nil
	case KindFirewall:
		var s FirewallSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeFirewall(s)
		return s, validateFirewall(s), nil
	case KindLoadBalancer:
		var s LoadBalancerSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeLoadBalancer(s)
		return s, validateLoadBalancer(s), nil
	case KindZone:
		var s ZoneSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeZone(s)
		return s, validateZone(s), nil
	case KindRecord:
		var s RecordSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeRecord(s)
		return s, validateRecord(s), nil
	case KindVM:
		var s VMSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeVM(s)
		return s, validateVM(s), nil
	case KindVolume:
		var s VolumeSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeVolume(s)
		return s, validateVolume(s), nil
	case KindSnapshot:
		var s SnapshotSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s.Source = strings.TrimSpace(s.Source)
		return s, validateSnapshot(s), nil
	case KindQueue:
		var s QueueSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeQueue(s)
		return s, validateQueue(s), nil
	case KindTopic:
		var s TopicSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeTopic(s)
		return s, validateTopic(s), nil
	case KindSearch:
		var s SearchSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeSearch(s)
		return s, validateSearch(s), nil
	case KindTranscoder:
		var s TranscoderSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeTranscoder(s)
		return s, validateTranscoder(s), nil
	case KindCDN:
		var s CDNSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeCDN(s)
		return s, validateCDN(s), nil
	case KindEmail:
		var s EmailSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeEmail(s)
		return s, validateEmail(s), nil
	case KindDashboard:
		var s DashboardSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		for i := range s.Widgets {
			s.Widgets[i].Title = strings.TrimSpace(s.Widgets[i].Title)
		}
		return s, validateDashboard(s), nil
	case KindAlert:
		var s AlertSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeAlert(s)
		return s, validateAlert(s), nil
	case KindChannel:
		var s ChannelSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		return s, validateChannel(s), nil
	case KindLogExport:
		var s LogExportSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeLogExport(s)
		return s, validateLogExport(s), nil
	case KindSecretStore:
		var s SecretStoreSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeSecretStore(s)
		return s, validateSecretStore(s), nil
	case KindStack:
		var s StackSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeStack(s)
		return s, validateStack(s), nil
	case KindTunnel:
		var s TunnelSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		s = normalizeTunnel(s)
		return s, validateTunnel(s), nil
	case KindSecret:
		var s SecretSpec
		if err := DecodeStrict(raw, &s); err != nil {
			return nil, nil, err
		}
		var errs []FieldError
		switch {
		case s.Source != nil && s.Value != "":
			errs = append(errs, FieldError{Field: "spec.value", Code: FieldExactlyOne, Message: "A secret has a value or a source in a secret store, not both."})
		case s.Source != nil:
			errs = append(errs, validateSecretSource(*s.Source)...)
		case s.Value == "":
			errs = append(errs, FieldError{Field: "spec.value", Code: FieldRequired, Message: "Enter the secret's value."})
		case len(s.Value) > SecretValueMaxBytes:
			errs = append(errs, FieldError{Field: "spec.value", Code: FieldTooLong, Message: "Secrets can be at most 64 KiB."})
		case !utf8.ValidString(s.Value):
			errs = append(errs, FieldError{Field: "spec.value", Code: FieldInvalidFormat, Message: "The value must be text (UTF-8)."})
		}
		return s, errs, nil
	}
	return nil, []FieldError{{Field: "kind", Code: FieldNotAllowed, Message: KindChoiceMessage}}, nil
}

func validateApp(s AppSpec) []FieldError {
	var errs []FieldError
	switch {
	case s.Image != "" && s.Source != nil:
		errs = append(errs, FieldError{Field: "spec.image", Code: FieldExactlyOne, Message: "Deploy an image or build from Git — choose one."})
	case s.Source != nil:
		errs = append(errs, validateGitSource(*s.Source)...)
	default:
		errs = append(errs, validateImage(s.Image, "spec.image")...)
	}
	if s.Port != 0 && (s.Port < PortMin || s.Port > PortMax) {
		errs = append(errs, FieldError{Field: "spec.port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
	}
	if s.Publish && s.Port == 0 {
		errs = append(errs, FieldError{Field: "spec.port", Code: FieldRequired, Message: "Set the port to publish."})
	}
	if s.MemoryMiB < AppMemoryMinMiB || s.MemoryMiB > AppMemoryMaxMiB {
		errs = append(errs, FieldError{Field: "spec.memoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB.", AppMemoryMinMiB, AppMemoryMaxMiB)})
	}
	errs = append(errs, validateCommand(s.Command)...)
	if !slices.Contains(Strategies, s.Strategy) {
		errs = append(errs, FieldError{Field: "spec.strategy", Code: FieldNotAllowed, Message: "Choose rolling or recreate."})
	}
	errs = append(errs, validateHealthCheck(s)...)
	errs = append(errs, validateEnv(s.Env, s.SecretEnv)...)
	errs = append(errs, validateBucketAccess(s.BucketAccess, s.Env, s.SecretEnv)...)
	errs = append(errs, validatePlacement(s.Network, s.Subnet)...)
	switch {
	case s.Replicas < ReplicasMin || s.Replicas > ReplicasMax:
		errs = append(errs, FieldError{Field: "spec.replicas", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d replicas.", ReplicasMin, ReplicasMax)})
	case s.Replicas > 1 && s.Publish:
		errs = append(errs, FieldError{Field: "spec.replicas", Code: FieldNotAllowed, Message: "An app that publishes a host port runs one replica. Use a load balancer or a domain to serve several."})
	}
	return append(errs, validateMounts(s.Volumes)...)
}

// validateGitSource checks a source app's repository settings (L3-002 §6.5).
func validateGitSource(g GitSource) []FieldError {
	var errs []FieldError
	u, err := url.Parse(g.Git)
	local := err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	if err != nil || u.Host == "" || len(g.Git) > GitURLMaxLen || !(u.Scheme == "https" || u.Scheme == "ssh" || (u.Scheme == "http" && local)) {
		errs = append(errs, FieldError{Field: "spec.source.git", Code: FieldInvalidFormat, Message: "Use an https:// or ssh:// repository URL, like https://github.com/acme/api.git."})
	}
	if g.Ref == "" || len(g.Ref) > GitRefMaxLen || strings.ContainsAny(g.Ref, " \t\n") {
		errs = append(errs, FieldError{Field: "spec.source.ref", Code: FieldInvalidFormat, Message: "Enter a branch, tag or commit, like main."})
	}
	for field, p := range map[string]string{"spec.source.dockerfile": g.Dockerfile, "spec.source.context": g.Context} {
		if !relativePath(p) {
			errs = append(errs, FieldError{Field: field, Code: FieldInvalidFormat, Message: "Use a path inside the repository, like docker/Dockerfile."})
		}
	}
	if g.CredentialsSecret != "" && ValidateName(g.CredentialsSecret) != nil {
		errs = append(errs, FieldError{Field: "spec.source.credentialsSecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
	}
	if len(g.BuildArgs) > BuildArgsMax {
		errs = append(errs, FieldError{Field: "spec.source.buildArgs", Code: FieldTooLong, Message: "Use at most 50 build arguments."})
	}
	for _, k := range sortedKeys(g.BuildArgs) {
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, FieldError{Field: "spec.source.buildArgs." + k, Code: FieldInvalidFormat, Message: "Argument names use letters, digits and underscores and cannot start with a digit."})
		}
	}
	slices.SortFunc(errs, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return errs
}

// relativePath reports whether p stays inside the repository: relative, clean of "..", not empty.
func relativePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || len(p) > 255 {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// validateEnv checks plain and secret environment maps (apps and jobs share the rules).
func validateEnv(env, secretEnv map[string]string) []FieldError {
	var errs []FieldError
	if len(env)+len(secretEnv) > EnvMaxEntries {
		errs = append(errs, FieldError{Field: "spec.env", Code: FieldTooLong, Message: "Use at most 100 environment variables."})
	}
	for _, k := range sortedKeys(env) {
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, FieldError{Field: "spec.env." + k, Code: FieldInvalidFormat, Message: "Variable names use letters, digits and underscores and cannot start with a digit."})
		}
		if len(env[k]) > EnvValueMaxLen {
			errs = append(errs, FieldError{Field: "spec.env." + k, Code: FieldTooLong, Message: "Values can be at most 4096 characters."})
		}
	}
	for _, k := range sortedKeys(secretEnv) {
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, FieldError{Field: "spec.secretEnv." + k, Code: FieldInvalidFormat, Message: "Variable names use letters, digits and underscores and cannot start with a digit."})
		}
		if _, dup := env[k]; dup {
			errs = append(errs, FieldError{Field: "spec.secretEnv." + k, Code: FieldNotAllowed, Message: "This variable is already set as a plain variable."})
		}
		if ValidateName(secretEnv[k]) != nil {
			errs = append(errs, FieldError{Field: "spec.secretEnv." + k, Code: FieldInvalidFormat, Message: "Name an existing secret."})
		}
	}
	return errs
}

func validateCommand(cmd []string) []FieldError {
	var errs []FieldError
	if len(cmd) > CommandMaxItems {
		errs = append(errs, FieldError{Field: "spec.command", Code: FieldTooLong, Message: "Use at most 32 command arguments."})
	}
	for i, c := range cmd {
		if len(c) > CommandItemMaxLen {
			errs = append(errs, FieldError{Field: fmt.Sprintf("spec.command[%d]", i), Code: FieldTooLong, Message: "Each argument can be at most 1024 characters."})
		}
	}
	return errs
}

func validateImage(image, field string) []FieldError {
	switch {
	case image == "":
		return []FieldError{{Field: field, Code: FieldRequired, Message: "Enter an image, for example nginx:1.27."}}
	case len(image) > ImageMaxLen:
		return []FieldError{{Field: field, Code: FieldTooLong, Message: "Images can be at most 255 characters."}}
	case !imagePattern.MatchString(image):
		return []FieldError{{Field: field, Code: FieldInvalidFormat, Message: "Use an image reference like nginx:1.27 or ghcr.io/acme/api:1.4."}}
	}
	return nil
}

func validateJob(s JobSpec) []FieldError {
	var errs []FieldError
	tzOK := true
	if s.TimeZone != "" {
		if _, err := time.LoadLocation(s.TimeZone); err != nil {
			tzOK = false
			errs = append(errs, FieldError{Field: "spec.timeZone", Code: FieldInvalidFormat, Message: "Use an IANA time zone like Asia/Kolkata or Europe/Berlin."})
		}
	}
	if s.Schedule == "" {
		errs = append(errs, FieldError{Field: "spec.schedule", Code: FieldRequired, Message: "Enter a schedule like 0 3 * * * or @daily."})
	} else if tzOK {
		if _, err := ParseSchedule(s.Schedule, s.TimeZone); err != nil {
			errs = append(errs, FieldError{Field: "spec.schedule", Code: FieldInvalidSchedule, Message: "Use 5 cron fields (minute hour day month weekday) or @hourly, @daily, @weekly, @monthly: " + err.Error() + "."})
		}
	}
	switch {
	case (s.App == "") == (s.Image == ""):
		errs = append(errs, FieldError{Field: "spec.app", Code: FieldExactlyOne, Message: "Run the job with an app's image, or with its own image — choose one."})
	case s.App != "" && ValidateName(s.App) != nil:
		errs = append(errs, FieldError{Field: "spec.app", Code: FieldInvalidFormat, Message: "Name an existing app."})
	case s.Image != "":
		errs = append(errs, validateImage(s.Image, "spec.image")...)
	}
	errs = append(errs, validateCommand(s.Command)...)
	errs = append(errs, validateEnv(s.Env, s.SecretEnv)...)
	if s.App != "" && len(s.BucketAccess) > 0 {
		errs = append(errs, FieldError{Field: "spec.bucketAccess", Code: FieldNotAllowed, Message: "A job that runs as an app uses the app's bucket access."})
	} else {
		errs = append(errs, validateBucketAccess(s.BucketAccess, s.Env, s.SecretEnv)...)
	}
	if s.TimeoutSeconds < JobTimeoutMin || s.TimeoutSeconds > JobTimeoutMax {
		errs = append(errs, FieldError{Field: "spec.timeoutSeconds", Code: FieldOutOfRange, Message: "Use 1 second to 24 hours (86400)."})
	}
	if !slices.Contains(OverlapPolicies, s.Overlap) {
		errs = append(errs, FieldError{Field: "spec.overlap", Code: FieldNotAllowed, Message: "Choose forbid or allow."})
	}
	if s.MemoryMiB < AppMemoryMinMiB || s.MemoryMiB > AppMemoryMaxMiB {
		errs = append(errs, FieldError{Field: "spec.memoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB.", AppMemoryMinMiB, AppMemoryMaxMiB)})
	}
	if s.App != "" && s.Network != "" {
		errs = append(errs, FieldError{Field: "spec.network", Code: FieldNotAllowed, Message: "A job that runs with an app's image runs in that app's network."})
	} else {
		errs = append(errs, validatePlacement(s.Network, s.Subnet)...)
	}
	return append(errs, validateMounts(s.Volumes)...)
}

func validateBackup(s BackupSpec) []FieldError {
	var errs []FieldError
	switch src := s.Source; {
	case (src.Database == "") == (src.Cache == ""):
		errs = append(errs, FieldError{Field: "spec.source", Code: FieldExactlyOne, Message: "Back up one database or one cache."})
	case ValidateName(src.Database+src.Cache) != nil:
		errs = append(errs, FieldError{Field: "spec.source", Code: FieldInvalidFormat, Message: "Name an existing database or cache."})
	}
	switch d := s.Destination; {
	case (d.Bucket == "") == (d.S3 == nil):
		errs = append(errs, FieldError{Field: "spec.destination", Code: FieldExactlyOne, Message: "Store backups in one EnclaveStack bucket or one external S3 bucket."})
	case d.Bucket != "" && ValidateName(d.Bucket) != nil:
		errs = append(errs, FieldError{Field: "spec.destination.bucket", Code: FieldInvalidFormat, Message: "Name an existing bucket."})
	case d.S3 != nil:
		errs = append(errs, validateS3(*d.S3, "spec.destination.s3")...)
	}
	if !backupPrefixPattern.MatchString(s.Prefix) || strings.Contains(s.Prefix, "..") || strings.Contains(s.Prefix, "//") {
		errs = append(errs, FieldError{Field: "spec.prefix", Code: FieldInvalidFormat, Message: "Use lowercase letters, digits, /, _ or -, up to 100 characters."})
	}
	if s.TimeZone != "" {
		if _, err := time.LoadLocation(s.TimeZone); err != nil {
			errs = append(errs, FieldError{Field: "spec.timeZone", Code: FieldInvalidFormat, Message: "Use an IANA time zone like Asia/Kolkata or Europe/Berlin."})
		}
	}
	if _, err := ParseSchedule(s.Schedule, ""); err != nil {
		errs = append(errs, FieldError{Field: "spec.schedule", Code: FieldInvalidSchedule, Message: "Use 5 cron fields (minute hour day month weekday) or @hourly, @daily, @weekly, @monthly: " + err.Error() + "."})
	}
	if s.Keep < BackupKeepMin || s.Keep > BackupKeepMax {
		errs = append(errs, FieldError{Field: "spec.keep", Code: FieldOutOfRange, Message: "Keep 1–365 backups."})
	}
	if s.KeepDays < 0 || s.KeepDays > BackupKeepDaysMax {
		errs = append(errs, FieldError{Field: "spec.keepDays", Code: FieldOutOfRange, Message: "Use 1–3650 days, or leave empty."})
	}
	if s.TimeoutSeconds < BackupTimeoutMin || s.TimeoutSeconds > BackupTimeoutMax {
		errs = append(errs, FieldError{Field: "spec.timeoutSeconds", Code: FieldOutOfRange, Message: "Use 60 seconds to 24 hours (86400)."})
	}
	return errs
}

// validateS3 checks an external S3-compatible bucket (backups, log exports).
func validateS3(d S3Destination, field string) []FieldError {
	var errs []FieldError
	u, err := url.Parse(d.Endpoint)
	local := err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && local)) || (u.Path != "" && u.Path != "/") {
		errs = append(errs, FieldError{Field: field + ".endpoint", Code: FieldInvalidFormat, Message: "Use an https:// endpoint like https://s3.eu-central-1.amazonaws.com (http only for localhost)."})
	}
	if !s3BucketPattern.MatchString(d.Bucket) {
		errs = append(errs, FieldError{Field: field + ".bucket", Code: FieldInvalidFormat, Message: "Use an S3 bucket name: 3–63 lowercase letters, digits, dots or hyphens."})
	}
	if ValidateName(d.CredentialsSecret) != nil {
		errs = append(errs, FieldError{Field: field + ".credentialsSecret", Code: FieldRequired, Message: `Name the secret holding {"accessKey","secretKey"}.`})
	}
	return errs
}

// ValidateRestore checks a restore request's shape (the service checks the run and names).
func ValidateRestore(r RestoreRequest) []FieldError {
	var errs []FieldError
	if r.RunID == "" {
		errs = append(errs, FieldError{Field: "runId", Code: FieldRequired, Message: "Choose the backup run to restore."})
	}
	switch r.Mode {
	case RestoreNew:
		errs = append(errs, renameName(ValidateName(r.Name))...)
	case RestoreReplace:
		if r.ConfirmName == "" {
			errs = append(errs, FieldError{Field: "confirmName", Code: FieldRequired, Message: "Type the name of the resource whose data will be replaced."})
		}
	default:
		errs = append(errs, FieldError{Field: "mode", Code: FieldNotAllowed, Message: "Choose new or replace."})
	}
	return errs
}

func renameName(errs []FieldError) []FieldError {
	for i := range errs {
		errs[i].Field = "name"
	}
	return errs
}

// ValidateHostname checks a domain's hostname (L3-002 §6.5): lowercase DNS labels, at least two, no IP
// addresses and no wildcards. Validated hostnames are safe to place in proxy configuration.
func ValidateHostname(h string) []FieldError {
	const f = "spec.hostname"
	msg := "Use a hostname like api.example.com or web.localhost."
	if h == "" {
		return []FieldError{{Field: f, Code: FieldRequired, Message: "Enter the hostname, like api.example.com."}}
	}
	if len(h) > HostnameMaxLen {
		return []FieldError{{Field: f, Code: FieldTooLong, Message: "Hostnames can be at most 253 characters."}}
	}
	if strings.Contains(h, "*") {
		return []FieldError{{Field: f, Code: FieldNotAllowed, Message: "Wildcard hostnames aren't supported yet."}}
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return []FieldError{{Field: f, Code: FieldInvalidFormat, Message: msg}}
	}
	allDigits := true
	for _, l := range labels {
		if !hostLabelPattern.MatchString(l) {
			return []FieldError{{Field: f, Code: FieldInvalidFormat, Message: msg}}
		}
		if strings.Trim(l, "0123456789") != "" {
			allDigits = false
		}
	}
	if allDigits {
		return []FieldError{{Field: f, Code: FieldNotAllowed, Message: "Use a hostname, not an IP address."}}
	}
	return nil
}

func validateDomain(s DomainSpec) []FieldError {
	var errs []FieldError
	if s.Zone != "" && IsWildcard(s.Hostname) {
		errs = ValidateHostname(strings.TrimPrefix(s.Hostname, "*."))
	} else {
		errs = ValidateHostname(s.Hostname)
	}
	if s.Zone != "" {
		switch {
		case ValidateName(s.Zone) != nil:
			errs = append(errs, FieldError{Field: "spec.zone", Code: FieldInvalidFormat, Message: "Name an existing zone."})
		case s.TLS != TLSAuto:
			errs = append(errs, FieldError{Field: "spec.tls", Code: FieldNotAllowed, Message: "A domain that gets its certificate through DNS uses tls auto."})
		}
	}
	if s.App == "" {
		errs = append(errs, FieldError{Field: "spec.app", Code: FieldRequired, Message: "Choose the app this domain serves."})
	} else if ValidateName(s.App) != nil {
		errs = append(errs, FieldError{Field: "spec.app", Code: FieldInvalidFormat, Message: "Name an existing app."})
	}
	if s.Port != 0 && (s.Port < PortMin || s.Port > PortMax) {
		errs = append(errs, FieldError{Field: "spec.port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
	}
	if !slices.Contains(TLSModes, s.TLS) {
		errs = append(errs, FieldError{Field: "spec.tls", Code: FieldNotAllowed, Message: "Choose auto, internal or off."})
	} else if s.TLS == TLSAuto && strings.HasSuffix(s.Hostname, LocalhostSuffix) {
		errs = append(errs, FieldError{Field: "spec.tls", Code: FieldNotAllowed, Message: "Public certificates can't be issued for .localhost names. Use internal."})
	}
	return errs
}

// ValidateMachineSettings checks PATCH /machine.
func ValidateMachineSettings(m MachineSettings) []FieldError {
	var errs []FieldError
	if a := m.PublicAddress; a != nil && *a != "" {
		if len(*a) > HostnameMaxLen || strings.ContainsAny(*a, " /\t\r\n") {
			errs = append(errs, FieldError{Field: "publicAddress", Code: FieldInvalidFormat, Message: "Use an IP address or a hostname."})
		}
	}
	if e := m.AcmeEmail; e != nil && *e != "" {
		if ValidateEmail(*e) != nil {
			errs = append(errs, FieldError{Field: "acmeEmail", Code: FieldInvalidFormat, Message: "Enter an email address like you@example.com."})
		}
	}
	if ie := m.InviteEmail; ie != nil {
		if ie.Project != "" && ValidateName(ie.Project) != nil {
			errs = append(errs, FieldError{Field: "inviteEmail.project", Code: FieldInvalidFormat, Message: "Name an existing project."})
		}
		if ValidateName(ie.Service) != nil {
			errs = append(errs, FieldError{Field: "inviteEmail.service", Code: FieldRequired, Message: "Choose the email service that sends invites."})
		}
		if !ValidAddress(ie.From) {
			errs = append(errs, FieldError{Field: "inviteEmail.from", Code: FieldInvalidFormat, Message: "Enter the sender, like enclave@example.com."})
		}
	}
	return errs
}

func validateHealthCheck(s AppSpec) []FieldError {
	h := s.HealthCheck
	if h == nil {
		return nil
	}
	const f = "spec.healthCheck."
	var errs []FieldError
	if !slices.Contains(HealthCheckTypes, h.Type) {
		return []FieldError{{Field: f + "type", Code: FieldNotAllowed, Message: "Choose http, tcp, command or none."}}
	}
	port := h.Port
	if port == 0 {
		port = s.Port
	}
	switch h.Type {
	case HealthHTTP, HealthTCP:
		if h.Port != 0 && (h.Port < PortMin || h.Port > PortMax) {
			errs = append(errs, FieldError{Field: f + "port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
		} else if port == 0 {
			errs = append(errs, FieldError{Field: f + "port", Code: FieldRequired, Message: "Set the app's port, or a port for the health check."})
		}
		if h.Type == HealthHTTP && (!strings.HasPrefix(h.Path, "/") || len(h.Path) > HealthPathMaxLen || strings.ContainsAny(h.Path, " \t\r\n")) {
			errs = append(errs, FieldError{Field: f + "path", Code: FieldInvalidFormat, Message: "Use a path that starts with /, like /healthz."})
		}
	case HealthCommand:
		if len(h.Command) == 0 {
			errs = append(errs, FieldError{Field: f + "command", Code: FieldRequired, Message: "Enter the command to run inside the container."})
		} else if len(h.Command) > CommandMaxItems {
			errs = append(errs, FieldError{Field: f + "command", Code: FieldTooLong, Message: "Use at most 32 command arguments."})
		}
	}
	if h.Type != HealthHTTP && h.Path != "" {
		errs = append(errs, FieldError{Field: f + "path", Code: FieldNotAllowed, Message: "A path applies to http checks only."})
	}
	if h.Type != HealthCommand && len(h.Command) > 0 {
		errs = append(errs, FieldError{Field: f + "command", Code: FieldNotAllowed, Message: "A command applies to command checks only."})
	}
	if h.IntervalSeconds < HealthIntervalMin || h.IntervalSeconds > HealthIntervalMax {
		errs = append(errs, FieldError{Field: f + "intervalSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds.", HealthIntervalMin, HealthIntervalMax)})
	}
	if h.TimeoutSeconds < HealthTimeoutMin || h.TimeoutSeconds > HealthTimeoutMax {
		errs = append(errs, FieldError{Field: f + "timeoutSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d seconds.", HealthTimeoutMin, HealthTimeoutMax)})
	} else if h.TimeoutSeconds > h.IntervalSeconds {
		errs = append(errs, FieldError{Field: f + "timeoutSeconds", Code: FieldOutOfRange, Message: "The timeout can't be longer than the interval."})
	}
	if h.Retries < HealthRetriesMin || h.Retries > HealthRetriesMax {
		errs = append(errs, FieldError{Field: f + "retries", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d attempts.", HealthRetriesMin, HealthRetriesMax)})
	}
	if h.StartPeriodSeconds < 0 || h.StartPeriodSeconds > HealthStartPeriodMax {
		errs = append(errs, FieldError{Field: f + "startPeriodSeconds", Code: FieldOutOfRange, Message: fmt.Sprintf("Use 0–%d seconds.", HealthStartPeriodMax)})
	}
	if len(errs) == 0 && h.StartPeriodSeconds+h.Retries*h.IntervalSeconds > HealthBudgetMaxSeconds {
		errs = append(errs, FieldError{Field: f + "retries", Code: FieldOutOfRange, Message: "The start period plus retries × interval can be at most 15 minutes."})
	}
	return errs
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// MergeSpec applies a PATCH body to a stored spec: top-level fields present in the patch replace the
// stored ones (maps are replaced whole); absent fields are kept (L3-002 §2).
func MergeSpec(stored, patch json.RawMessage) (json.RawMessage, error) {
	base := map[string]json.RawMessage{}
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &base); err != nil {
			return nil, err
		}
	}
	var over map[string]json.RawMessage
	if err := json.Unmarshal(patch, &over); err != nil {
		return nil, err
	}
	for k, v := range over {
		if string(v) == "null" {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	return json.Marshal(base)
}

// ImmutableViolations lists fields an update tries to change that cannot change in place (CP-04).
func ImmutableViolations(kind Kind, before, after any) []FieldError {
	switch kind {
	case KindDatabase:
		b, a := before.(DatabaseSpec), after.(DatabaseSpec)
		var errs []FieldError
		if b.Engine != a.Engine {
			errs = append(errs, FieldError{Field: "spec.engine", Code: FieldImmutable, Message: "The engine can't change. Create a new database instead."})
		}
		if b.Version != a.Version {
			errs = append(errs, FieldError{Field: "spec.version", Code: FieldImmutable, Message: "Changing the version needs a new database."})
		}
		return errs
	case KindCache:
		b, a := before.(CacheSpec), after.(CacheSpec)
		if b.Engine != a.Engine || b.Version != a.Version {
			return []FieldError{{Field: "spec.version", Code: FieldImmutable, Message: "The engine and version can't change."}}
		}
	case KindStack:
		b, a := before.(StackSpec), after.(StackSpec)
		if b.Template != a.Template || b.TemplateBody != a.TemplateBody {
			return []FieldError{{Field: "spec.template", Code: FieldImmutable, Message: "A stack's template can't change. Change its version or parameters, or make a new stack."}}
		}
	case KindBackup:
		if before.(BackupSpec).Source != after.(BackupSpec).Source {
			return []FieldError{{Field: "spec.source", Code: FieldImmutable, Message: "A backup's source can't change. Create a new backup instead."}}
		}
	case KindDomain:
		if before.(DomainSpec).Hostname != after.(DomainSpec).Hostname {
			return []FieldError{{Field: "spec.hostname", Code: FieldImmutable, Message: "The hostname can't change. Create a new domain instead."}}
		}
	case KindNetwork:
		b, a := before.(NetworkSpec), after.(NetworkSpec)
		if b.CIDR != a.CIDR || !slices.Equal(b.Subnets, a.Subnets) || b.HasInternet() != a.HasInternet() {
			return []FieldError{{Field: "spec", Code: FieldImmutable, Message: "A network's addresses and internet access can't change. Create a new network and move resources to it."}}
		}
	case KindZone:
		b, a := before.(ZoneSpec), after.(ZoneSpec)
		if b.Provider != a.Provider || b.Zone != a.Zone {
			return []FieldError{{Field: "spec.zone", Code: FieldImmutable, Message: "A zone's provider and name can't change. Create a new zone instead."}}
		}
	case KindRecord:
		b, a := before.(RecordSpec), after.(RecordSpec)
		if b.Zone != a.Zone || b.Name != a.Name || b.Type != a.Type {
			return []FieldError{{Field: "spec.name", Code: FieldImmutable, Message: "A record's zone, name and type can't change. Create a new record instead."}}
		}
	case KindVM:
		b, a := before.(VMSpec), after.(VMSpec)
		var errs []FieldError
		if b.Image != a.Image || b.ImageSHA256 != a.ImageSHA256 {
			errs = append(errs, FieldError{Field: "spec.image", Code: FieldImmutable, Message: "A VM's image can't change. Create a new VM instead."})
		}
		if !slices.Equal(b.SSHKeys, a.SSHKeys) || b.SSHKeySecret != a.SSHKeySecret {
			errs = append(errs, FieldError{Field: "spec.sshKeys", Code: FieldImmutable, Message: "SSH keys are set at the first boot and can't change. Add keys inside the VM, or create a new VM."})
		}
		if b.UserData != a.UserData {
			errs = append(errs, FieldError{Field: "spec.userData", Code: FieldImmutable, Message: "The first-boot script ran already and can't change. Create a new VM to run another."})
		}
		return errs
	case KindVolume:
		b, a := before.(VolumeSpec), after.(VolumeSpec)
		if b.Type != a.Type || b.Snapshot != a.Snapshot {
			return []FieldError{{Field: "spec.type", Code: FieldImmutable, Message: "A volume's type and snapshot can't change. Create a new volume instead."}}
		}
	case KindSearch:
		if before.(SearchSpec).Version != after.(SearchSpec).Version {
			return []FieldError{{Field: "spec.version", Code: FieldImmutable, Message: "Changing the version needs a new search service."}}
		}
	case KindCDN:
		if before.(CDNSpec).Hostname != after.(CDNSpec).Hostname {
			return []FieldError{{Field: "spec.hostname", Code: FieldImmutable, Message: "The hostname can't change. Create a new CDN instead."}}
		}
	case KindSecretStore:
		if before.(SecretStoreSpec).Provider != after.(SecretStoreSpec).Provider {
			return []FieldError{{Field: "spec.provider", Code: FieldImmutable, Message: "A store's provider can't change. Create a new store instead."}}
		}
	case KindChannel:
		if before.(ChannelSpec).Type != after.(ChannelSpec).Type {
			return []FieldError{{Field: "spec.type", Code: FieldImmutable, Message: "A channel's type can't change. Create a new channel instead."}}
		}
	case KindTunnel:
		b, a := before.(TunnelSpec), after.(TunnelSpec)
		if b.Provider != a.Provider || b.Machine != a.Machine {
			return []FieldError{{Field: "spec.provider", Code: FieldImmutable, Message: "A tunnel's way in and edge machine can't change. Create a new tunnel instead."}}
		}
	case KindSnapshot:
		if before.(SnapshotSpec) != after.(SnapshotSpec) {
			return []FieldError{{Field: "spec.source", Code: FieldImmutable, Message: "A snapshot never changes. Take a new snapshot instead."}}
		}
	}
	return nil
}

// ValidateSetup checks a first-run request.
func ValidateSetup(r SetupRequest) []FieldError {
	var errs []FieldError
	if strings.TrimSpace(r.MachineName) == "" || len(r.MachineName) > MachineNameMaxLen {
		errs = append(errs, FieldError{Field: "machineName", Code: FieldOutOfRange, Message: "Use 1–64 characters."})
	}
	errs = append(errs, ValidateEmail(r.Email)...)
	errs = append(errs, ValidatePassword(r.Password)...)
	return errs
}

// ValidateEmail checks an email address loosely; delivery is never attempted.
func ValidateEmail(email string) []FieldError {
	at := strings.IndexByte(email, '@')
	if at < 1 || at == len(email)-1 || len(email) > EmailMaxLen || strings.ContainsAny(email, " \t\r\n") {
		return []FieldError{{Field: "email", Code: FieldInvalidFormat, Message: "Enter an email address like you@example.com."}}
	}
	return nil
}

// ValidatePassword checks password length.
func ValidatePassword(pw string) []FieldError {
	if n := utf8.RuneCountInString(pw); n < PasswordMinLen || n > PasswordMaxLen {
		return []FieldError{{Field: "password", Code: FieldOutOfRange, Message: "Use at least 12 characters."}}
	}
	return nil
}

// ValidateCreateToken checks a token request.
func ValidateCreateToken(r CreateTokenRequest) []FieldError {
	var errs []FieldError
	if n := utf8.RuneCountInString(strings.TrimSpace(r.Name)); n == 0 || n > TokenNameMaxLen {
		errs = append(errs, FieldError{Field: "name", Code: FieldOutOfRange, Message: "Use 1–64 characters."})
	}
	if r.Scope != "" && r.Scope != ScopeRead && r.Scope != ScopeWrite {
		errs = append(errs, FieldError{Field: "scope", Code: FieldNotAllowed, Message: "Choose read or write."})
	}
	if r.Role != "" && !ValidRole(r.Role) {
		errs = append(errs, FieldError{Field: "role", Code: FieldNotAllowed, Message: "Choose viewer, editor or admin."})
	}
	if r.Projects != nil && (len(r.Projects) == 0 || len(r.Projects) > TokenProjectsMax) {
		errs = append(errs, FieldError{Field: "projects", Code: FieldOutOfRange, Message: "Choose 1–50 projects, or leave it out for all projects."})
	}
	for i, p := range r.Projects {
		if ValidateName(p) != nil {
			errs = append(errs, FieldError{Field: fmt.Sprintf("projects[%d]", i), Code: FieldInvalidFormat, Message: "Name an existing project."})
		}
	}
	if r.ExpiresInDays < 0 || r.ExpiresInDays > TokenMaxExpiryDays {
		errs = append(errs, FieldError{Field: "expiresInDays", Code: FieldOutOfRange, Message: "Use 1–365 days, or leave empty for no expiry."})
	}
	return errs
}
