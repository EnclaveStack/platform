package api

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The v0.6 contract (L3-002 §9, L2-008): database engines, queues, topics, search, transcoders, CDNs and
// email. Rules that need other resources (existence, hostname uniqueness) are checked by the service.

// Database engines (DBE-01).
const (
	EnginePostgres   = "postgres"
	EngineMySQL      = "mysql"
	EngineMongoDB    = "mongodb"
	EngineClickHouse = "clickhouse"
)

// DatabaseEngines lists the engines in display order; DatabaseEngineVersions their versions, default
// first except for PostgreSQL, whose default stays 16 (DBE-01).
var (
	DatabaseEngines        = []string{EnginePostgres, EngineMySQL, EngineMongoDB, EngineClickHouse}
	DatabaseEngineVersions = map[string][]string{
		EnginePostgres: {"16", "17"},
		EngineMySQL:    {"8.4"},
		// 7.0 is the default: MongoDB 8 refuses Linux kernels 6.19 – 7.0.13, which Docker Desktop ran in
		// October 2026 (SERVER-121912).
		EngineMongoDB:    {"7.0", "8.0"},
		EngineClickHouse: {"26.3", "25.8"},
	}
	DatabaseDefaultVersions = map[string]string{EnginePostgres: "16", EngineMySQL: "8.4", EngineMongoDB: "7.0", EngineClickHouse: "26.3"}
)

func validateDatabaseEngine(s DatabaseSpec) []FieldError {
	versions, ok := DatabaseEngineVersions[s.Engine]
	if !ok {
		return []FieldError{{Field: "spec.engine", Code: FieldNotAllowed, Message: "Choose postgres, mysql, mongodb or clickhouse."}}
	}
	if !slices.Contains(versions, s.Version) {
		return []FieldError{{Field: "spec.version", Code: FieldNotAllowed, Message: fmt.Sprintf("Choose a %s version: %s.", s.Engine, strings.Join(versions, " or "))}}
	}
	return nil
}

// QueueSpec is a queue of messages (ADR-029, QUE-*).
type QueueSpec struct {
	VisibilityTimeoutSeconds int    `json:"visibilityTimeoutSeconds,omitempty"`
	RetentionHours           int    `json:"retentionHours,omitempty"`
	MaxMessageBytes          int    `json:"maxMessageBytes,omitempty"`
	MaxReceives              int    `json:"maxReceives,omitempty"`
	DeadLetterQueue          string `json:"deadLetterQueue,omitempty"`
}

// Subscription is one of a topic's targets: exactly one of a queue or a webhook URL (TOP-01).
type Subscription struct {
	Queue string `json:"queue,omitempty"`
	URL   string `json:"url,omitempty"`
	// Secret names a secret whose value signs webhook bodies (TOP-02).
	Secret string `json:"secret,omitempty"`
}

// Target names a subscription the way counters and events do: "queue/<name>" or the URL.
func (s Subscription) Target() string {
	if s.Queue != "" {
		return string(KindQueue) + "/" + s.Queue
	}
	return s.URL
}

// TopicSpec fans messages out to subscriptions (ADR-029, TOP-*).
type TopicSpec struct {
	RetentionHours int            `json:"retentionHours,omitempty"`
	Subscriptions  []Subscription `json:"subscriptions,omitempty"`
}

// SearchSpec is an OpenSearch service (ADR-031, SRC-*).
type SearchSpec struct {
	Version   string `json:"version,omitempty"`
	MemoryMiB int    `json:"memoryMiB,omitempty"`
	Network   string `json:"network,omitempty"`
	Subnet    string `json:"subnet,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// BucketFolder is a bucket and a folder in it ("" or "a/b/").
type BucketFolder struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix,omitempty"`
}

// TranscoderSpec converts media written to a folder (ADR-034, TRN-*).
type TranscoderSpec struct {
	Input          BucketFolder `json:"input"`
	Output         BucketFolder `json:"output"`
	Presets        []string     `json:"presets,omitempty"`
	Watch          *bool        `json:"watch,omitempty"`
	TimeoutSeconds int          `json:"timeoutSeconds,omitempty"`
	Concurrency    int          `json:"concurrency,omitempty"`
	MemoryMiB      int          `json:"memoryMiB,omitempty"`
	// Renditions, HLS and Notify (L2-012 TRX-*): outputs of the user's own and the "done" message.
	Renditions []Rendition      `json:"renditions,omitempty"`
	HLS        *HLSLadder       `json:"hls,omitempty"`
	Notify     *TranscodeNotify `json:"notify,omitempty"`
}

// Watching reports whether new objects are converted automatically (default true).
func (s TranscoderSpec) Watching() bool { return s.Watch == nil || *s.Watch }

// CDNSpec serves an app or a bucket through a cache (ADR-033, CDN-*). Hostname, TLS, RedirectHTTP and
// Zone mean what they mean on a domain.
type CDNSpec struct {
	Hostname          string `json:"hostname"`
	Origin            string `json:"origin"`
	OriginPort        int    `json:"originPort,omitempty"`
	DefaultTTLSeconds *int   `json:"defaultTtlSeconds,omitempty"`
	MaxSizeMiB        int    `json:"maxSizeMiB,omitempty"`
	TLS               string `json:"tls,omitempty"`
	RedirectHTTP      *bool  `json:"redirectHttp,omitempty"`
	Zone              string `json:"zone,omitempty"`
}

// AsDomain returns the hostname settings a CDN shares with domains, for the edge and DNS-01.
func (s CDNSpec) AsDomain() DomainSpec {
	return DomainSpec{Hostname: s.Hostname, TLS: s.TLS, RedirectHTTP: s.RedirectHTTP, Zone: s.Zone}
}

// TTL is the default cache time in seconds.
func (s CDNSpec) TTL() int {
	if s.DefaultTTLSeconds == nil {
		return CDNTTLDefault
	}
	return *s.DefaultTTLSeconds
}

// EmailRelay is where a relaying email service delivers (EML-02).
type EmailRelay struct {
	Host              string `json:"host"`
	Port              int    `json:"port,omitempty"`
	TLS               string `json:"tls,omitempty"`
	CredentialsSecret string `json:"credentialsSecret"`
}

// EmailSpec is an SMTP service for apps (ADR-032, EML-*).
type EmailSpec struct {
	Mode         string      `json:"mode,omitempty"`
	Relay        *EmailRelay `json:"relay,omitempty"`
	Domain       string      `json:"domain,omitempty"`
	KeepMessages int         `json:"keepMessages,omitempty"`
	Network      string      `json:"network,omitempty"`
	Subnet       string      `json:"subnet,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// Search versions (SRC-01).
const (
	SearchVersion2 = "2"
	SearchVersion3 = "3"
)

// Transcoder presets (TRN-02).
const (
	PresetMP4480p   = "mp4-480p"
	PresetMP4720p   = "mp4-720p"
	PresetMP41080p  = "mp4-1080p"
	PresetWebM720p  = "webm-720p"
	PresetHLS720p   = "hls-720p"
	PresetMP3       = "mp3"
	PresetThumbnail = "thumbnail"
)

// Email modes and relay TLS (EML-02).
const (
	EmailCapture  = "capture"
	EmailRelaying = "relay"
	RelaySTARTTLS = "starttls"
	RelayTLS      = "tls"
	RelayNone     = "none"
)

// Email relay results (L3-002 §9.3): kept only, or also handed to the relay. Mailpit records no
// per-message relay outcome, so a refused relay shows in the service's logs (ADR-032).
const (
	RelayCaptured = "captured"
	RelayRelayed  = "relayed"
)

// ActionPurge empties a queue or a CDN's cache (QUE-07, CDN-05).
const ActionPurge = "purge"

// TriggerWatch starts a transcoder run for an object that appeared (TRN-03).
const TriggerWatch = "watch"

// Limits (L3-002 §9.4), exported for the console.
const (
	QueueVisibilityMin, QueueVisibilityMax, QueueVisibilityDefault = 1, 43200, 30
	RetentionHoursMin, RetentionHoursMax                           = 1, 336
	QueueRetentionDefault, TopicRetentionDefault                   = 96, 24
	MessageBytesMin, MessageBytesMax, MessageBytesDefault          = 1024, 1 << 20, 256 << 10
	MaxReceivesMax                                                 = 1000
	SubscriptionsMax, WebhookURLMaxLen                             = 20, 2048
	MessageAttributesMax, AttributeValueMaxLen, DedupIDMaxLen      = 10, 1024, 128
	ReceiveMaxMessages, ReceiveWaitMaxSeconds, DeleteReceiptsMax   = 10, 20, 10
	PeekLimitMax, PeekLimitDefault                                 = 50, 20
	WebhookAttempts                                                = 10
	SearchMemoryMinMiB, SearchMemoryMaxMiB, SearchMemoryDefaultMiB = 1024, 16384, 2048
	SearchMemoryStepMiB                                            = 256
	SearchPort                                                     = 9200
	PrefixMaxLen                                                   = 255
	TranscodeTimeoutMin, TranscodeTimeoutMax                       = 60, 86400
	TranscodeTimeoutDefault                                        = 3600
	ConcurrencyMin, ConcurrencyMax, ConcurrencyDefault             = 1, 4, 1
	TranscodeMemoryMin, TranscodeMemoryMax, TranscodeMemoryDefault = 256, 8192, 1024
	TranscodeOutputDefault                                         = "transcoded/"
	ObjectKeyMaxLen                                                = 1024
	CDNTTLMin, CDNTTLMax, CDNTTLDefault                            = 0, 31536000, 3600
	CDNSizeMinMiB, CDNSizeMaxMiB, CDNSizeDefaultMiB                = 64, 102400, 1024
	KeepMessagesMin, KeepMessagesMax, KeepMessagesDefault          = 10, 10000, 500
	RelayPortDefault                                               = 587
	SMTPPort                                                       = 1025
	EmailRecipientsMax, EmailSubjectMaxLen                         = 50, 998
	EmailBodyMaxBytes                                              = 1 << 20
	EmailListMax, EmailListDefault                                 = 200, 50
)

// Allowed values (L3-002 §9.2).
var (
	SearchVersions = []string{SearchVersion3, SearchVersion2}
	Presets        = []string{PresetMP4480p, PresetMP4720p, PresetMP41080p, PresetWebM720p, PresetHLS720p, PresetMP3, PresetThumbnail}
	EmailModes     = []string{EmailCapture, EmailRelaying}
	RelayTLSModes  = []string{RelaySTARTTLS, RelayTLS, RelayNone}
	CDNOriginKinds = []Kind{KindApp, KindBucket}
	// PresetFiles is where each preset's main output lands under a run's folder (TRN-06).
	PresetFiles = map[string]string{PresetMP4480p: "mp4-480p.mp4", PresetMP4720p: "mp4-720p.mp4", PresetMP41080p: "mp4-1080p.mp4",
		PresetWebM720p: "webm-720p.webm", PresetHLS720p: "hls-720p/index.m3u8", PresetMP3: "mp3.mp3", PresetThumbnail: "thumbnail.jpg"}
	prefixCharacters = regexp.MustCompile(`^[A-Za-z0-9!_.*'()/ +=,@-]*$`)
)

func normalizeQueue(s QueueSpec) QueueSpec {
	if s.VisibilityTimeoutSeconds == 0 {
		s.VisibilityTimeoutSeconds = QueueVisibilityDefault
	}
	if s.RetentionHours == 0 {
		s.RetentionHours = QueueRetentionDefault
	}
	if s.MaxMessageBytes == 0 {
		s.MaxMessageBytes = MessageBytesDefault
	}
	return s
}

func validateQueue(s QueueSpec) []FieldError {
	var errs []FieldError
	if s.VisibilityTimeoutSeconds < QueueVisibilityMin || s.VisibilityTimeoutSeconds > QueueVisibilityMax {
		errs = append(errs, FieldError{Field: "spec.visibilityTimeoutSeconds", Code: FieldOutOfRange, Message: "Use 1 second to 12 hours (43200 seconds)."})
	}
	errs = append(errs, validateRetention(s.RetentionHours)...)
	if s.MaxMessageBytes < MessageBytesMin || s.MaxMessageBytes > MessageBytesMax {
		errs = append(errs, FieldError{Field: "spec.maxMessageBytes", Code: FieldOutOfRange, Message: "Use 1 KiB to 1 MiB (1024–1048576 bytes)."})
	}
	if s.MaxReceives < 0 || s.MaxReceives > MaxReceivesMax {
		errs = append(errs, FieldError{Field: "spec.maxReceives", Code: FieldOutOfRange, Message: "Use 1–1000, or 0 for no limit."})
	}
	if s.DeadLetterQueue != "" {
		switch {
		case ValidateName(s.DeadLetterQueue) != nil:
			errs = append(errs, FieldError{Field: "spec.deadLetterQueue", Code: FieldInvalidFormat, Message: "Name an existing queue."})
		case s.MaxReceives == 0:
			errs = append(errs, FieldError{Field: "spec.maxReceives", Code: FieldRequired, Message: "Set how many receives move a message to the dead-letter queue."})
		}
	}
	return errs
}

func validateRetention(hours int) []FieldError {
	if hours < RetentionHoursMin || hours > RetentionHoursMax {
		return []FieldError{{Field: "spec.retentionHours", Code: FieldOutOfRange, Message: "Use 1–336 hours (two weeks)."}}
	}
	return nil
}

func normalizeTopic(s TopicSpec) TopicSpec {
	if s.RetentionHours == 0 {
		s.RetentionHours = TopicRetentionDefault
	}
	for i := range s.Subscriptions {
		s.Subscriptions[i].URL = strings.TrimSpace(s.Subscriptions[i].URL)
	}
	return s
}

func validateTopic(s TopicSpec) []FieldError {
	errs := validateRetention(s.RetentionHours)
	if len(s.Subscriptions) > SubscriptionsMax {
		errs = append(errs, FieldError{Field: "spec.subscriptions", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d subscriptions.", SubscriptionsMax)})
	}
	seen := map[string]bool{}
	for i, sub := range s.Subscriptions {
		f := fmt.Sprintf("spec.subscriptions[%d]", i)
		switch {
		case (sub.Queue == "") == (sub.URL == ""):
			errs = append(errs, FieldError{Field: f, Code: FieldExactlyOne, Message: "Subscribe a queue or a webhook URL — choose one."})
			continue
		case sub.Queue != "":
			if ValidateName(sub.Queue) != nil {
				errs = append(errs, FieldError{Field: f + ".queue", Code: FieldInvalidFormat, Message: "Name an existing queue."})
			}
			if sub.Secret != "" {
				errs = append(errs, FieldError{Field: f + ".secret", Code: FieldNotAllowed, Message: "Only webhooks are signed."})
			}
		default:
			if !ValidWebhookURL(sub.URL) {
				errs = append(errs, FieldError{Field: f + ".url", Code: FieldInvalidFormat, Message: "Use an https:// URL (http:// only for localhost), up to 2048 characters."})
			}
			if sub.Secret != "" && ValidateName(sub.Secret) != nil {
				errs = append(errs, FieldError{Field: f + ".secret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
			}
		}
		if seen[sub.Target()] {
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "This subscription is listed twice."})
		}
		seen[sub.Target()] = true
	}
	return errs
}

// ValidWebhookURL reports whether a webhook may be called (TOP-03).
func ValidWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || len(raw) > WebhookURLMaxLen || u.User != nil || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	return u.Scheme == "https" || (u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1"))
}

func normalizeSearch(s SearchSpec) SearchSpec {
	if s.Version == "" {
		s.Version = SearchVersion3
	}
	if s.MemoryMiB == 0 {
		s.MemoryMiB = SearchMemoryDefaultMiB
	}
	return s
}

func validateSearch(s SearchSpec) []FieldError {
	var errs []FieldError
	if !slices.Contains(SearchVersions, s.Version) {
		errs = append(errs, FieldError{Field: "spec.version", Code: FieldNotAllowed, Message: "Choose version 3 or 2."})
	}
	if s.MemoryMiB < SearchMemoryMinMiB || s.MemoryMiB > SearchMemoryMaxMiB || s.MemoryMiB%SearchMemoryStepMiB != 0 {
		errs = append(errs, FieldError{Field: "spec.memoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB in steps of %d.", SearchMemoryMinMiB, SearchMemoryMaxMiB, SearchMemoryStepMiB)})
	}
	return append(errs, validatePlacement(s.Network, s.Subnet)...)
}

// NormalizePrefix turns a folder into "" or "a/b/": no leading slash, one trailing slash.
func NormalizePrefix(p string) string {
	p = strings.TrimLeft(strings.TrimSpace(p), "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func normalizeTranscoder(s TranscoderSpec) TranscoderSpec {
	s.Input.Prefix = NormalizePrefix(s.Input.Prefix)
	if s.Output.Prefix == "" {
		s.Output.Prefix = TranscodeOutputDefault
	}
	s.Output.Prefix = NormalizePrefix(s.Output.Prefix)
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = TranscodeTimeoutDefault
	}
	if s.Concurrency == 0 {
		s.Concurrency = ConcurrencyDefault
	}
	if s.MemoryMiB == 0 {
		s.MemoryMiB = TranscodeMemoryDefault
	}
	return normalizeTranscodeOutputs(s)
}

func validateFolder(f BucketFolder, field string) []FieldError {
	var errs []FieldError
	if f.Bucket == "" {
		errs = append(errs, FieldError{Field: field + ".bucket", Code: FieldRequired, Message: "Choose a bucket."})
	} else if ValidateName(f.Bucket) != nil {
		errs = append(errs, FieldError{Field: field + ".bucket", Code: FieldInvalidFormat, Message: "Name an existing bucket."})
	}
	return append(errs, validatePrefix(f.Prefix, field+".prefix")...)
}

// validatePrefix checks a folder inside a bucket.
func validatePrefix(prefix, field string) []FieldError {
	if len(prefix) > PrefixMaxLen || !prefixCharacters.MatchString(prefix) || slices.Contains(strings.Split(prefix, "/"), "..") || strings.Contains(prefix, "//") {
		return []FieldError{{Field: field, Code: FieldInvalidFormat, Message: "Use a folder like videos/ or media/2026/: letters, digits and - _ . / only, up to 255 characters."}}
	}
	return nil
}

func validateTranscoder(s TranscoderSpec) []FieldError {
	output := s.Output
	output.Prefix = placeholder.ReplaceAllString(output.Prefix, "x") // {name} and {meta:<tag>} are checked by validateTranscodeOutputs
	errs := append(validateFolder(s.Input, "spec.input"), validateFolder(output, "spec.output")...)
	if out := staticPrefix(s.Output.Prefix); s.Input.Bucket == s.Output.Bucket && (strings.HasPrefix(s.Input.Prefix, out) || strings.HasPrefix(out, s.Input.Prefix)) {
		errs = append(errs, FieldError{Field: "spec.output.prefix", Code: FieldNotAllowed, Message: "On the same bucket, the output folder can't be inside the input folder or contain it; the transcoder would convert its own files."})
	}
	for i, p := range s.Presets {
		f := fmt.Sprintf("spec.presets[%d]", i)
		switch {
		case !slices.Contains(Presets, p):
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "Choose from " + strings.Join(Presets, ", ") + "."})
		case slices.Index(s.Presets, p) < i:
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "This preset is listed twice."})
		}
	}
	errs = append(errs, validateTranscodeOutputs(s)...)
	if s.TimeoutSeconds < TranscodeTimeoutMin || s.TimeoutSeconds > TranscodeTimeoutMax {
		errs = append(errs, FieldError{Field: "spec.timeoutSeconds", Code: FieldOutOfRange, Message: "Use 60 seconds to 24 hours."})
	}
	if s.Concurrency < ConcurrencyMin || s.Concurrency > ConcurrencyMax {
		errs = append(errs, FieldError{Field: "spec.concurrency", Code: FieldOutOfRange, Message: "Use 1–4 files at once."})
	}
	if s.MemoryMiB < TranscodeMemoryMin || s.MemoryMiB > TranscodeMemoryMax {
		errs = append(errs, FieldError{Field: "spec.memoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB.", TranscodeMemoryMin, TranscodeMemoryMax)})
	}
	return errs
}

// ParseOrigin splits a CDN origin into its kind (app or bucket) and name.
func ParseOrigin(s string) (Kind, string, bool) {
	kind, name, ok := strings.Cut(s, "/")
	if !ok || !slices.Contains(CDNOriginKinds, Kind(kind)) || ValidateName(name) != nil {
		return "", "", false
	}
	return Kind(kind), name, true
}

func normalizeCDN(s CDNSpec) CDNSpec {
	s.Hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Hostname)), ".")
	s.Origin = strings.TrimSpace(s.Origin)
	if s.DefaultTTLSeconds == nil {
		ttl := CDNTTLDefault
		s.DefaultTTLSeconds = &ttl
	}
	if s.MaxSizeMiB == 0 {
		s.MaxSizeMiB = CDNSizeDefaultMiB
	}
	d := normalizeDomainTLS(s.AsDomain())
	s.TLS, s.RedirectHTTP = d.TLS, d.RedirectHTTP
	return s
}

func validateCDN(s CDNSpec) []FieldError {
	d := s.AsDomain()
	d.App = "-" // a domain needs an app; a CDN's origin is checked below
	var errs []FieldError
	for _, e := range validateDomain(d) {
		if e.Field != "spec.app" {
			errs = append(errs, e)
		}
	}
	if IsWildcard(s.Hostname) {
		errs = append(errs, FieldError{Field: "spec.hostname", Code: FieldNotAllowed, Message: "A CDN serves one hostname; wildcards are for domains."})
	}
	kind, _, ok := ParseOrigin(s.Origin)
	switch {
	case s.Origin == "":
		errs = append(errs, FieldError{Field: "spec.origin", Code: FieldRequired, Message: "Choose what the CDN serves: app/<name> or bucket/<name>."})
	case !ok:
		errs = append(errs, FieldError{Field: "spec.origin", Code: FieldInvalidFormat, Message: "Use app/<name> or bucket/<name>."})
	case kind == KindBucket && s.OriginPort != 0:
		errs = append(errs, FieldError{Field: "spec.originPort", Code: FieldNotAllowed, Message: "Only an app origin has a port."})
	}
	if s.OriginPort != 0 && (s.OriginPort < PortMin || s.OriginPort > PortMax) {
		errs = append(errs, FieldError{Field: "spec.originPort", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
	}
	if ttl := s.TTL(); ttl < CDNTTLMin || ttl > CDNTTLMax {
		errs = append(errs, FieldError{Field: "spec.defaultTtlSeconds", Code: FieldOutOfRange, Message: "Use 0 seconds to a year (31536000 seconds)."})
	}
	if s.MaxSizeMiB < CDNSizeMinMiB || s.MaxSizeMiB > CDNSizeMaxMiB {
		errs = append(errs, FieldError{Field: "spec.maxSizeMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB.", CDNSizeMinMiB, CDNSizeMaxMiB)})
	}
	return errs
}

// normalizeDomainTLS fills a hostname's TLS mode and redirect defaults, as domains have them.
func normalizeDomainTLS(s DomainSpec) DomainSpec {
	if s.TLS == "" {
		s.TLS = TLSAuto
		if strings.HasSuffix(s.Hostname, LocalhostSuffix) {
			s.TLS = TLSInternal
		}
	}
	if s.RedirectHTTP == nil {
		t := true
		s.RedirectHTTP = &t
	}
	return s
}

func normalizeEmail(s EmailSpec) EmailSpec {
	if s.Mode == "" {
		s.Mode = EmailCapture
	}
	if s.KeepMessages == 0 {
		s.KeepMessages = KeepMessagesDefault
	}
	s.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Domain)), ".")
	if r := s.Relay; r != nil {
		r.Host = strings.TrimSpace(r.Host)
		if r.Port == 0 {
			r.Port = RelayPortDefault
		}
		if r.TLS == "" {
			r.TLS = RelaySTARTTLS
		}
	}
	return s
}

func validateEmail(s EmailSpec) []FieldError {
	var errs []FieldError
	switch {
	case !slices.Contains(EmailModes, s.Mode):
		errs = append(errs, FieldError{Field: "spec.mode", Code: FieldNotAllowed, Message: "Choose capture or relay."})
	case s.Mode == EmailRelaying && s.Relay == nil:
		errs = append(errs, FieldError{Field: "spec.relay", Code: FieldRequired, Message: "Give the relay's host and credentials secret."})
	case s.Mode == EmailCapture && s.Relay != nil:
		errs = append(errs, FieldError{Field: "spec.relay", Code: FieldNotAllowed, Message: "A capturing email service doesn't relay; choose relay mode to use one."})
	}
	if r := s.Relay; r != nil {
		if r.Host == "" || len(r.Host) > HostnameMaxLen || (net.ParseIP(r.Host) == nil && ValidateHostname(r.Host) != nil) {
			errs = append(errs, FieldError{Field: "spec.relay.host", Code: FieldInvalidFormat, Message: "Use the provider's SMTP host, like smtp.postmarkapp.com."})
		}
		if r.Port < PortMin || r.Port > PortMax {
			errs = append(errs, FieldError{Field: "spec.relay.port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
		}
		if !slices.Contains(RelayTLSModes, r.TLS) {
			errs = append(errs, FieldError{Field: "spec.relay.tls", Code: FieldNotAllowed, Message: "Choose starttls, tls or none."})
		}
		if r.CredentialsSecret == "" {
			errs = append(errs, FieldError{Field: "spec.relay.credentialsSecret", Code: FieldRequired, Message: `Name a secret holding {"username": "…", "password": "…"}.`})
		} else if ValidateName(r.CredentialsSecret) != nil {
			errs = append(errs, FieldError{Field: "spec.relay.credentialsSecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
		}
	}
	if s.Domain != "" && ValidateHostname(s.Domain) != nil {
		errs = append(errs, FieldError{Field: "spec.domain", Code: FieldInvalidFormat, Message: "Use a domain like example.com."})
	}
	if s.KeepMessages < KeepMessagesMin || s.KeepMessages > KeepMessagesMax {
		errs = append(errs, FieldError{Field: "spec.keepMessages", Code: FieldOutOfRange, Message: fmt.Sprintf("Keep %d–%d messages.", KeepMessagesMin, KeepMessagesMax)})
	}
	return append(errs, validatePlacement(s.Network, s.Subnet)...)
}

// ─── data endpoints (L3-002 §9.3) ─────────────────────────────────────────

// SendMessage is the body of POST /resources/{id}/messages (queues and topics).
type SendMessage struct {
	Body            string            `json:"body"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	DeduplicationID string            `json:"deduplicationId,omitempty"`
}

// SentMessage answers a send or publish.
type SentMessage struct {
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate"`
}

// QueueStats is a queue's live state (QUE-06).
type QueueStats struct {
	Messages         int64  `json:"messages"`
	InFlight         int64  `json:"inFlight"`
	OldestAgeSeconds *int64 `json:"oldestAgeSeconds"`
	DeadLettered     int64  `json:"deadLettered"`
}

// PeekedMessage is a waiting message, read without receiving it.
type PeekedMessage struct {
	ID         string            `json:"id"`
	Body       string            `json:"body"`
	Attributes map[string]string `json:"attributes"`
	SentAt     time.Time         `json:"sentAt"`
}

// MessageList is the body of GET /resources/{id}/messages.
type MessageList struct {
	Stats QueueStats      `json:"stats"`
	Items []PeekedMessage `json:"items"`
}

// ReceiveMessages is the body of POST /resources/{id}/messages/receive.
type ReceiveMessages struct {
	Max         int `json:"max,omitempty"`
	WaitSeconds int `json:"waitSeconds,omitempty"`
}

// ReceivedMessage is a message taken from a queue; Receipt deletes it.
type ReceivedMessage struct {
	PeekedMessage
	Receipt      string `json:"receipt"`
	ReceiveCount int    `json:"receiveCount"`
}

// ReceivedList wraps received messages.
type ReceivedList struct {
	Items []ReceivedMessage `json:"items"`
}

// DeleteMessages is the body of POST /resources/{id}/messages/delete.
type DeleteMessages struct {
	Receipts []string `json:"receipts"`
}

// ReceiptFailure is a receipt that deleted nothing.
type ReceiptFailure struct {
	Receipt string `json:"receipt"`
	Message string `json:"message"`
}

// DeletedMessages answers a delete.
type DeletedMessages struct {
	Deleted int              `json:"deleted"`
	Failed  []ReceiptFailure `json:"failed"`
}

// SubscriptionStats counts one subscription's deliveries (TOP-07).
type SubscriptionStats struct {
	Target    string `json:"target"`
	Delivered int64  `json:"delivered"`
	Pending   int64  `json:"pending"`
	Failed    int64  `json:"failed"`
	LastError string `json:"lastError"`
}

// DeliveryStats is the body of GET /resources/{id}/deliveries.
type DeliveryStats struct {
	Published int64               `json:"published"`
	Items     []SubscriptionStats `json:"items"`
}

// SendEmail is the body of POST /resources/{id}/emails.
type SendEmail struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	ReplyTo string   `json:"replyTo,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
}

// SentEmail answers a send.
type SentEmail struct {
	ID string `json:"id"`
}

// EmailSummary is one kept message (EML-03).
type EmailSummary struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Subject   string    `json:"subject"`
	SentAt    time.Time `json:"sentAt"`
	SizeBytes int64     `json:"sizeBytes"`
	Relay     string    `json:"relay"`
}

// EmailList is the body of GET /resources/{id}/emails.
type EmailList struct {
	Total int            `json:"total"`
	Items []EmailSummary `json:"items"`
}

// Email is one kept message in full.
type Email struct {
	EmailSummary
	Cc      []string            `json:"cc"`
	Bcc     []string            `json:"bcc"`
	ReplyTo string              `json:"replyTo"`
	Text    string              `json:"text"`
	HTML    string              `json:"html"`
	Headers map[string][]string `json:"headers"`
}

// TranscodeRequest is the body of POST /resources/{transcoder}/actions/run (TRN-04).
type TranscodeRequest struct {
	Key string `json:"key"`
}

// ValidateSendMessage checks a message against a queue's size limit (topics: MessageBytesMax).
func ValidateSendMessage(m SendMessage, maxBytes int) []FieldError {
	var errs []FieldError
	switch {
	case !utf8.ValidString(m.Body):
		errs = append(errs, FieldError{Field: "body", Code: FieldInvalidFormat, Message: "The body must be text (UTF-8)."})
	case len(m.Body) > maxBytes:
		errs = append(errs, FieldError{Field: "body", Code: FieldTooLong, Message: fmt.Sprintf("The body can be at most %d bytes here.", maxBytes)})
	}
	if len(m.Attributes) > MessageAttributesMax {
		errs = append(errs, FieldError{Field: "attributes", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d attributes.", MessageAttributesMax)})
	}
	for _, k := range sortedKeys(m.Attributes) {
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, FieldError{Field: "attributes." + k, Code: FieldInvalidFormat, Message: "Attribute names use letters, digits and underscores and can't start with a digit."})
		} else if v := m.Attributes[k]; len(v) > AttributeValueMaxLen || !utf8.ValidString(v) || strings.ContainsAny(v, "\r\n") {
			errs = append(errs, FieldError{Field: "attributes." + k, Code: FieldInvalidFormat, Message: "Attribute values are one line of text, up to 1024 characters."})
		}
	}
	if len(m.DeduplicationID) > DedupIDMaxLen || strings.ContainsAny(m.DeduplicationID, " \t\r\n") {
		errs = append(errs, FieldError{Field: "deduplicationId", Code: FieldInvalidFormat, Message: "Use up to 128 characters without spaces."})
	}
	return errs
}

// NormalizeReceive applies defaults and checks a receive.
func NormalizeReceive(r ReceiveMessages) (ReceiveMessages, []FieldError) {
	if r.Max == 0 {
		r.Max = 1
	}
	var errs []FieldError
	if r.Max < 1 || r.Max > ReceiveMaxMessages {
		errs = append(errs, FieldError{Field: "max", Code: FieldOutOfRange, Message: "Receive 1–10 messages at a time."})
	}
	if r.WaitSeconds < 0 || r.WaitSeconds > ReceiveWaitMaxSeconds {
		errs = append(errs, FieldError{Field: "waitSeconds", Code: FieldOutOfRange, Message: "Wait 0–20 seconds."})
	}
	return r, errs
}

// ValidateDeleteMessages checks a delete.
func ValidateDeleteMessages(d DeleteMessages) []FieldError {
	if len(d.Receipts) == 0 || len(d.Receipts) > DeleteReceiptsMax {
		return []FieldError{{Field: "receipts", Code: FieldOutOfRange, Message: "Delete 1–10 messages at a time."}}
	}
	return nil
}

// ValidateSendEmail checks a message for an email service; domain ("" for any) bounds the sender (EML-05).
func ValidateSendEmail(m SendEmail, domain string) []FieldError {
	var errs []FieldError
	addr := func(field, a string) {
		if ValidateEmail(a) != nil || strings.ContainsAny(a, "<>,;\"") {
			errs = append(errs, FieldError{Field: field, Code: FieldInvalidFormat, Message: "Use an address like name@example.com."})
		}
	}
	addr("from", m.From)
	if domain != "" && ValidateEmail(m.From) == nil {
		if _, d, _ := strings.Cut(strings.ToLower(m.From), "@"); d != domain {
			errs = append(errs, FieldError{Field: "from", Code: FieldNotAllowed, Message: "This email service sends from @" + domain + " only."})
		}
	}
	n := len(m.To) + len(m.Cc) + len(m.Bcc)
	if len(m.To) == 0 {
		errs = append(errs, FieldError{Field: "to", Code: FieldRequired, Message: "Add at least one recipient."})
	} else if n > EmailRecipientsMax {
		errs = append(errs, FieldError{Field: "to", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d recipients in all.", EmailRecipientsMax)})
	}
	for field, list := range map[string][]string{"to": m.To, "cc": m.Cc, "bcc": m.Bcc} {
		for i, a := range list {
			addr(fmt.Sprintf("%s[%d]", field, i), a)
		}
	}
	if m.ReplyTo != "" {
		addr("replyTo", m.ReplyTo)
	}
	switch {
	case strings.TrimSpace(m.Subject) == "":
		errs = append(errs, FieldError{Field: "subject", Code: FieldRequired, Message: "Enter a subject."})
	case len(m.Subject) > EmailSubjectMaxLen || strings.ContainsAny(m.Subject, "\r\n"):
		errs = append(errs, FieldError{Field: "subject", Code: FieldInvalidFormat, Message: "Use one line of up to 998 characters."})
	}
	switch {
	case m.Text == "" && m.HTML == "":
		errs = append(errs, FieldError{Field: "text", Code: FieldRequired, Message: "Write a text or an HTML body."})
	case len(m.Text)+len(m.HTML) > EmailBodyMaxBytes:
		errs = append(errs, FieldError{Field: "text", Code: FieldTooLong, Message: "The bodies can be at most 1 MiB together."})
	case !utf8.ValidString(m.Text) || !utf8.ValidString(m.HTML):
		errs = append(errs, FieldError{Field: "text", Code: FieldInvalidFormat, Message: "Bodies must be text (UTF-8)."})
	}
	slices.SortFunc(errs, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return errs
}

// ValidateTranscodeKey checks a key asked for by hand against the input folder (TRN-04).
func ValidateTranscodeKey(key, prefix string) []FieldError {
	switch {
	case key == "":
		return []FieldError{{Field: "key", Code: FieldRequired, Message: "Give the object's key, like videos/intro.mov."}}
	case len(key) > ObjectKeyMaxLen || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n"):
		return []FieldError{{Field: "key", Code: FieldInvalidFormat, Message: "Use an object key of up to 1024 characters."}}
	case !strings.HasPrefix(key, prefix) || strings.HasSuffix(key, "/"):
		return []FieldError{{Field: "key", Code: FieldNotAllowed, Message: "Choose a file inside the input folder " + prefix + "."}}
	}
	return nil
}
