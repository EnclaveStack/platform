package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The v0.7 contract (L3-002 §10, L2-009): projects, people and roles, scoped tokens, metrics, dashboards,
// alerts, notification channels, log exports and external secret stores. Rules that need other resources
// (existence, roles held) are checked by the service.

// DefaultProject holds everything made before projects existed (ADR-035).
const DefaultProject = "default"

// Roles, weakest first (ADR-036).
const (
	RoleViewer = "viewer"
	RoleEditor = "editor"
	RoleAdmin  = "admin"
)

// Roles lists every role, weakest first.
var Roles = []string{RoleViewer, RoleEditor, RoleAdmin}

// RoleRank orders roles; "" (no role) is 0.
func RoleRank(role string) int { return slices.Index(Roles, role) + 1 }

// RoleAtLeast reports whether have grants need.
func RoleAtLeast(have, need string) bool {
	return RoleRank(have) >= RoleRank(need) && RoleRank(need) > 0
}

// ValidRole reports whether role is one of the three.
func ValidRole(role string) bool { return slices.Contains(Roles, role) }

// Project is a namespace of resources (PRJ-01).
type Project struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	Resources   int       `json:"resources"`
}

// ProjectList wraps projects.
type ProjectList struct {
	Items []Project `json:"items"`
}

// CreateProject is the body of POST /projects.
type CreateProject struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// UpdateProject is the body of PATCH /projects/{name}.
type UpdateProject struct {
	Description string `json:"description"`
}

// ProjectDescriptionMaxLen bounds a project's description.
const ProjectDescriptionMaxLen = 280

// ValidateProject checks a new project.
func ValidateProject(p CreateProject) []FieldError {
	return append(ValidateName(p.Name), validateDescription(p.Description)...)
}

func validateDescription(d string) []FieldError {
	if utf8.RuneCountInString(d) > ProjectDescriptionMaxLen {
		return []FieldError{{Field: "description", Code: FieldTooLong, Message: "Use at most 280 characters."}}
	}
	return nil
}

// ValidateUpdateProject checks a project change.
func ValidateUpdateProject(p UpdateProject) []FieldError { return validateDescription(p.Description) }

// User statuses.
const (
	UserActive  = "active"
	UserInvited = "invited"
)

// UserSummary describes a person and their roles (IAM-02).
type UserSummary struct {
	ID           string            `json:"id"`
	Email        string            `json:"email"`
	MachineRole  *string           `json:"machineRole"`
	ProjectRoles map[string]string `json:"projectRoles"`
	Status       string            `json:"status"`
	CreatedAt    time.Time         `json:"createdAt"`
	LastSignInAt *time.Time        `json:"lastSignInAt"`
}

// UserList wraps users.
type UserList struct {
	Items []UserSummary `json:"items"`
}

// InviteUser is the body of POST /users.
type InviteUser struct {
	Email        string            `json:"email"`
	MachineRole  *string           `json:"machineRole,omitempty"`
	ProjectRoles map[string]string `json:"projectRoles,omitempty"`
}

// UpdateUser is the body of PATCH /users/{id}. MachineRoleSet tells an absent machineRole (keep) from a
// null one (remove); a null project role removes it.
type UpdateUser struct {
	MachineRole    *string            `json:"machineRole"`
	MachineRoleSet bool               `json:"-"`
	ProjectRoles   map[string]*string `json:"projectRoles,omitempty"`
}

// Invitation carries the one-time link to send a person (IAM-04, IAM-07).
type Invitation struct {
	User      UserSummary `json:"user"`
	URL       string      `json:"url"`
	ExpiresAt time.Time   `json:"expiresAt"`
	// Emailed says the link was also sent by email; EmailError why it wasn't (IAM-04).
	Emailed    bool   `json:"emailed"`
	EmailError string `json:"emailError,omitempty"`
}

// Invite kinds.
const (
	InviteKindInvite = "invite"
	InviteKindReset  = "reset"
)

// InviteTTL is how long an invite or reset link works.
const InviteTTL = 7 * 24 * time.Hour

// InviteInfo is the body of GET /invite.
type InviteInfo struct {
	Email string `json:"email"`
	Kind  string `json:"kind"`
}

// AcceptInvite is the body of POST /invite.
type AcceptInvite struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

// ChangePassword is the body of PUT /session/password.
type ChangePassword struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// TokenLimits is the token behind a request, when there is one.
type TokenLimits struct {
	Role     string   `json:"role"`
	Projects []string `json:"projects"`
}

// AuditEntry is one line of the audit list (IAM-09).
type AuditEntry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Detail   string    `json:"detail"`
	ClientIP string    `json:"clientIp"`
}

// AuditList is a page of audit entries, newest first.
type AuditList struct {
	Items      []AuditEntry `json:"items"`
	NextCursor *string      `json:"nextCursor"`
}

// ValidateInvite checks an invitation's shape.
func ValidateInvite(r InviteUser) []FieldError {
	errs := ValidateEmail(strings.TrimSpace(r.Email))
	if r.MachineRole != nil && !ValidRole(*r.MachineRole) {
		errs = append(errs, FieldError{Field: "machineRole", Code: FieldNotAllowed, Message: "Choose viewer, editor or admin."})
	}
	errs = append(errs, validateProjectRoles(r.ProjectRoles)...)
	if r.MachineRole == nil && len(r.ProjectRoles) == 0 {
		errs = append(errs, FieldError{Field: "projectRoles", Code: FieldRequired, Message: "Give them a role on the machine or on at least one project."})
	}
	return errs
}

func validateProjectRoles(roles map[string]string) []FieldError {
	var errs []FieldError
	for _, p := range sortedKeys(roles) {
		if ValidateName(p) != nil {
			errs = append(errs, FieldError{Field: "projectRoles." + p, Code: FieldInvalidFormat, Message: "Name an existing project."})
		} else if !ValidRole(roles[p]) {
			errs = append(errs, FieldError{Field: "projectRoles." + p, Code: FieldNotAllowed, Message: "Choose viewer, editor or admin."})
		}
	}
	return errs
}

// ValidateUpdateUser checks a role change's shape.
func ValidateUpdateUser(r UpdateUser) []FieldError {
	var errs []FieldError
	if r.MachineRole != nil && !ValidRole(*r.MachineRole) {
		errs = append(errs, FieldError{Field: "machineRole", Code: FieldNotAllowed, Message: "Choose viewer, editor or admin, or null to remove it."})
	}
	for p, role := range r.ProjectRoles {
		if ValidateName(p) != nil {
			errs = append(errs, FieldError{Field: "projectRoles." + p, Code: FieldInvalidFormat, Message: "Name an existing project."})
		} else if role != nil && !ValidRole(*role) {
			errs = append(errs, FieldError{Field: "projectRoles." + p, Code: FieldNotAllowed, Message: "Choose viewer, editor or admin, or null to remove it."})
		}
	}
	return errs
}

// TokenProjectsMax bounds a token's project list.
const TokenProjectsMax = 50

// MetricInfo describes one metric (L3-002 §10.4).
type MetricInfo struct {
	Name  string `json:"name"`
	Unit  string `json:"unit"`
	Title string `json:"title"`
	Kinds []Kind `json:"kinds,omitempty"`
}

// Metric units.
const (
	UnitCores          = "cores"
	UnitBytes          = "bytes"
	UnitBytesPerSecond = "bytesPerSecond"
	UnitPerSecond      = "perSecond"
	UnitCount          = "count"
	UnitSeconds        = "seconds"
	UnitMilliseconds   = "milliseconds"
	UnitPercent        = "percent"
)

// Metric names.
const (
	MetricCPU              = "cpu"
	MetricMemory           = "memory"
	MetricMemoryLimit      = "memoryLimit"
	MetricNetRx            = "netRx"
	MetricNetTx            = "netTx"
	MetricDiskRead         = "diskRead"
	MetricDiskWrite        = "diskWrite"
	MetricRestarts         = "restarts"
	MetricRequests         = "requests"
	MetricErrors5xx        = "errors5xx"
	MetricLatency          = "latency"
	MetricQueueDepth       = "queueDepth"
	MetricInFlight         = "inFlight"
	MetricOldestAge        = "oldestAge"
	MetricDelivered        = "delivered"
	MetricDeliveryFailures = "deliveryFailures"
	MetricUsedBytes        = "usedBytes"
	MetricPercentFull      = "percentFull"
	MetricCachedBytes      = "cachedBytes"
	MetricDiskFree         = "diskFree"
	MetricDiskPercentFull  = "diskPercentFull"
	MetricResourcesFailed  = "resourcesFailed"
)

// ContainerKinds run containers the sampler reads stats of (MET-01).
var ContainerKinds = []Kind{KindApp, KindDatabase, KindCache, KindSearch, KindEmail, KindCDN, KindLoadBalancer}

// RestartingKinds can crash-loop (ALR-02): the container kinds and VMs.
var RestartingKinds = append(slices.Clone(ContainerKinds), KindVM)

// RunKinds have runs that can fail (ALR-02).
var RunKinds = []Kind{KindJob, KindBackup, KindTranscoder}

// LogKinds have logs a log export can read (LOG-01).
var LogKinds = append(slices.Clone(ContainerKinds), KindVM)

// Metrics is the catalogue of resource metrics, in display order.
var Metrics = []MetricInfo{
	{MetricCPU, UnitCores, "CPU", RestartingKinds},
	{MetricMemory, UnitBytes, "Memory", RestartingKinds},
	{MetricMemoryLimit, UnitBytes, "Memory limit", ContainerKinds},
	{MetricNetRx, UnitBytesPerSecond, "Network in", ContainerKinds},
	{MetricNetTx, UnitBytesPerSecond, "Network out", ContainerKinds},
	{MetricDiskRead, UnitBytesPerSecond, "Disk reads", ContainerKinds},
	{MetricDiskWrite, UnitBytesPerSecond, "Disk writes", ContainerKinds},
	{MetricRestarts, UnitCount, "Restarts", RestartingKinds},
	{MetricRequests, UnitPerSecond, "Requests", []Kind{KindApp, KindCDN}},
	{MetricErrors5xx, UnitPerSecond, "5xx responses", []Kind{KindApp, KindCDN}},
	{MetricLatency, UnitMilliseconds, "Response time", []Kind{KindApp, KindCDN}},
	{MetricQueueDepth, UnitCount, "Messages waiting", []Kind{KindQueue}},
	{MetricInFlight, UnitCount, "Messages in flight", []Kind{KindQueue}},
	{MetricOldestAge, UnitSeconds, "Oldest message", []Kind{KindQueue}},
	{MetricDelivered, UnitPerSecond, "Deliveries", []Kind{KindTopic}},
	{MetricDeliveryFailures, UnitPerSecond, "Failed deliveries", []Kind{KindTopic}},
	{MetricUsedBytes, UnitBytes, "Used", []Kind{KindVolume}},
	{MetricPercentFull, UnitPercent, "Full", []Kind{KindVolume}},
	{MetricCachedBytes, UnitBytes, "Cached", []Kind{KindCDN}},
}

// MachineMetrics is the machine's own catalogue (MET-03).
var MachineMetrics = []MetricInfo{
	{MetricCPU, UnitCores, "CPU used by EnclaveStack", nil},
	{MetricMemory, UnitBytes, "Memory used by EnclaveStack", nil},
	{MetricDiskFree, UnitBytes, "Disk free", nil},
	{MetricDiskPercentFull, UnitPercent, "Disk full", nil},
	{MetricResourcesFailed, UnitCount, "Failed resources", nil},
}

// MetricFor returns a kind's metric by name; ok is false when the kind doesn't have it.
func MetricFor(kind Kind, name string) (MetricInfo, bool) {
	for _, m := range Metrics {
		if m.Name == name && slices.Contains(m.Kinds, kind) {
			return m, true
		}
	}
	return MetricInfo{}, false
}

// MetricsOf lists a kind's metrics in display order.
func MetricsOf(kind Kind) []MetricInfo {
	var out []MetricInfo
	for _, m := range Metrics {
		if slices.Contains(m.Kinds, kind) {
			out = append(out, m)
		}
	}
	return out
}

// MachineMetric returns a machine metric by name.
func MachineMetric(name string) (MetricInfo, bool) {
	for _, m := range MachineMetrics {
		if m.Name == name {
			return m, true
		}
	}
	return MetricInfo{}, false
}

// Metric sampling and retention (ADR-037).
const (
	MetricInterval      = 30 * time.Second
	MetricRollup        = 15 * time.Minute
	MetricPointsKept    = 24 * time.Hour
	MetricRollupsKept   = 30 * 24 * time.Hour
	MetricMaxPoints     = 500
	MetricMaxRange      = 31 * 24 * time.Hour
	MetricDefaultWindow = time.Hour
)

// MetricPoint is one point; Max is set on 15-minute rollups.
type MetricPoint struct {
	T   int64    `json:"t"`
	V   float64  `json:"v"`
	Max *float64 `json:"max,omitempty"`
}

// MetricSeries is one metric over time.
type MetricSeries struct {
	Metric string        `json:"metric"`
	Unit   string        `json:"unit"`
	Points []MetricPoint `json:"points"`
}

// MetricData answers a metrics query.
type MetricData struct {
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	StepSeconds int            `json:"stepSeconds"`
	Items       []MetricSeries `json:"items"`
}

// ParseRef splits "kind/name".
func ParseRef(s string) (Kind, string, bool) {
	kind, name, ok := strings.Cut(s, "/")
	if !ok || !Kind(kind).Valid() || ValidateName(name) != nil {
		return "", "", false
	}
	return Kind(kind), name, true
}

// Widget is one graph on a dashboard (DSH-01).
type Widget struct {
	Title     string   `json:"title"`
	Metric    string   `json:"metric"`
	Resources []string `json:"resources"`
}

// DashboardSpec is a named page of graphs (ADR-037).
type DashboardSpec struct {
	Widgets []Widget `json:"widgets"`
}

// Dashboard limits.
const (
	WidgetsMax        = 24
	WidgetTitleMaxLen = 80
	// WidgetResourceMax is the chart's palette: eight validated series colours, never generated.
	WidgetResourceMax = 8
	AlertResourceMax  = 10
)

func validateDashboard(s DashboardSpec) []FieldError {
	var errs []FieldError
	if len(s.Widgets) == 0 || len(s.Widgets) > WidgetsMax {
		errs = append(errs, FieldError{Field: "spec.widgets", Code: FieldOutOfRange, Message: "Use 1–24 widgets."})
	}
	for i, w := range s.Widgets {
		f := fmt.Sprintf("spec.widgets[%d]", i)
		if n := utf8.RuneCountInString(strings.TrimSpace(w.Title)); n == 0 || n > WidgetTitleMaxLen {
			errs = append(errs, FieldError{Field: f + ".title", Code: FieldOutOfRange, Message: "Use a title of 1–80 characters."})
		}
		errs = append(errs, validateMetricRefs(w.Metric, w.Resources, f+".metric", f+".resources", WidgetResourceMax)...)
	}
	return errs
}

// MachineRef names the machine itself where a resource is expected (alerts and widgets on machine
// metrics, ALR-03).
const MachineRef = "machine"

// validateMetricRefs checks 1–10 distinct kind/name strings whose kinds all have the metric, or the
// machine alone for a machine metric.
func validateMetricRefs(metric string, refs []string, metricField, refsField string, maxRefs int) []FieldError {
	var errs []FieldError
	if len(refs) == 1 && refs[0] == MachineRef {
		if _, ok := MachineMetric(metric); !ok {
			errs = append(errs, FieldError{Field: metricField, Code: FieldNotAllowed, Message: "The machine's metrics are cpu, memory, diskFree, diskPercentFull and resourcesFailed."})
		}
		return errs
	}
	if !slices.ContainsFunc(Metrics, func(m MetricInfo) bool { return m.Name == metric }) {
		errs = append(errs, FieldError{Field: metricField, Code: FieldNotAllowed, Message: "Choose a metric from the list, like cpu, memory or requests."})
	}
	errs = append(errs, validateRefs(refs, refsField, 1, maxRefs, nil)...)
	for i, r := range refs {
		if kind, _, ok := ParseRef(r); ok && len(errs) == 0 {
			if _, has := MetricFor(kind, metric); !has {
				errs = append(errs, FieldError{Field: fmt.Sprintf("%s[%d]", refsField, i), Code: FieldNotAllowed, Message: fmt.Sprintf("A %s has no %s metric.", kind, metric)})
			}
		}
	}
	return errs
}

// validateRefs checks a list of distinct kind/name strings, of the given kinds when kinds is not nil.
func validateRefs(refs []string, field string, minN, maxN int, kinds []Kind) []FieldError {
	if len(refs) < minN || len(refs) > maxN {
		if minN == 0 {
			return []FieldError{{Field: field, Code: FieldOutOfRange, Message: fmt.Sprintf("Name at most %d resources.", maxN)}}
		}
		return []FieldError{{Field: field, Code: FieldOutOfRange, Message: fmt.Sprintf("Name %d–%d resources as kind/name, like app/api.", minN, maxN)}}
	}
	var errs []FieldError
	seen := map[string]bool{}
	for i, r := range refs {
		f := fmt.Sprintf("%s[%d]", field, i)
		kind, _, ok := ParseRef(r)
		switch {
		case !ok:
			errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Use kind/name, like app/api."})
		case kinds != nil && !slices.Contains(kinds, kind):
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "Choose one of: " + joinKinds(kinds) + "."})
		case seen[r]:
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: r + " is listed twice."})
		}
		seen[r] = true
	}
	return errs
}

func joinKinds(kinds []Kind) string {
	s := make([]string, len(kinds))
	for i, k := range kinds {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

// Alert condition types (ALR-02).
const (
	ConditionMetric    = "metric"
	ConditionCrashLoop = "crash_loop"
	ConditionFailed    = "failed"
	ConditionRunFailed = "run_failed"
	// ConditionMachineOffline watches machines, named "machine/<name>" (L2-010 MCH-06).
	ConditionMachineOffline = "machine_offline"
)

// ConditionTypes lists the condition types.
var ConditionTypes = []string{ConditionMetric, ConditionCrashLoop, ConditionFailed, ConditionRunFailed, ConditionMachineOffline}

// MachineRefPrefix starts a machine in a machine_offline condition's resources.
const MachineRefPrefix = "machine/"

// Operators for metric conditions.
const (
	OperatorAbove = "above"
	OperatorBelow = "below"
)

// Alert states (ALR-04).
const (
	AlertOK       = "ok"
	AlertFiring   = "firing"
	AlertNoData   = "no_data"
	AlertDisabled = "disabled"
)

// Alert limits and defaults.
const (
	AlertForMinutesMin, AlertForMinutesMax, AlertForMinutesDefault = 1, 60, 5
	AlertRestartsMin, AlertRestartsMax, AlertRestartsDefault       = 1, 100, 3
	AlertWindowMin, AlertWindowMax, AlertWindowDefault             = 1, 120, 10
	AlertRepeatMax                                                 = 1440
	AlertChannelsMax                                               = 10
)

// AlertCondition is what an alert watches.
type AlertCondition struct {
	Type          string   `json:"type"`
	Resources     []string `json:"resources"`
	Metric        string   `json:"metric,omitempty"`
	Operator      string   `json:"operator,omitempty"`
	Threshold     *float64 `json:"threshold,omitempty"`
	ForMinutes    int      `json:"forMinutes,omitempty"`
	Restarts      int      `json:"restarts,omitempty"`
	WindowMinutes int      `json:"windowMinutes,omitempty"`
}

// AlertSpec is a rule that fires and resolves (ADR-038).
type AlertSpec struct {
	Condition     AlertCondition `json:"condition"`
	Channels      []string       `json:"channels"`
	RepeatMinutes int            `json:"repeatMinutes,omitempty"`
	Enabled       *bool          `json:"enabled,omitempty"`
}

// IsEnabled reports whether the alert is on (default true).
func (s AlertSpec) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

func normalizeAlert(s AlertSpec) AlertSpec {
	c := &s.Condition
	switch c.Type {
	case ConditionMetric:
		if c.ForMinutes == 0 {
			c.ForMinutes = AlertForMinutesDefault
		}
	case ConditionMachineOffline:
		if c.ForMinutes == 0 {
			c.ForMinutes = 1
		}
	case ConditionCrashLoop:
		if c.Restarts == 0 {
			c.Restarts = AlertRestartsDefault
		}
		if c.WindowMinutes == 0 {
			c.WindowMinutes = AlertWindowDefault
		}
	}
	return s
}

func validateAlert(s AlertSpec) []FieldError {
	var errs []FieldError
	c := s.Condition
	switch c.Type {
	case ConditionMetric:
		errs = append(errs, validateMetricRefs(c.Metric, c.Resources, "spec.condition.metric", "spec.condition.resources", AlertResourceMax)...)
		if c.Operator != OperatorAbove && c.Operator != OperatorBelow {
			errs = append(errs, FieldError{Field: "spec.condition.operator", Code: FieldNotAllowed, Message: "Choose above or below."})
		}
		if c.Threshold == nil || math.IsNaN(*c.Threshold) || math.IsInf(*c.Threshold, 0) {
			errs = append(errs, FieldError{Field: "spec.condition.threshold", Code: FieldRequired, Message: "Enter the threshold, a number."})
		}
		if c.ForMinutes < AlertForMinutesMin || c.ForMinutes > AlertForMinutesMax {
			errs = append(errs, FieldError{Field: "spec.condition.forMinutes", Code: FieldOutOfRange, Message: "Use 1–60 minutes."})
		}
	case ConditionCrashLoop:
		errs = append(errs, validateRefs(c.Resources, "spec.condition.resources", 1, AlertResourceMax, RestartingKinds)...)
		if c.Restarts < AlertRestartsMin || c.Restarts > AlertRestartsMax {
			errs = append(errs, FieldError{Field: "spec.condition.restarts", Code: FieldOutOfRange, Message: "Use 1–100 restarts."})
		}
		if c.WindowMinutes < AlertWindowMin || c.WindowMinutes > AlertWindowMax {
			errs = append(errs, FieldError{Field: "spec.condition.windowMinutes", Code: FieldOutOfRange, Message: "Use 1–120 minutes."})
		}
	case ConditionFailed:
		errs = append(errs, validateRefs(c.Resources, "spec.condition.resources", 1, AlertResourceMax, nil)...)
	case ConditionRunFailed:
		errs = append(errs, validateRefs(c.Resources, "spec.condition.resources", 1, AlertResourceMax, RunKinds)...)
	case ConditionMachineOffline:
		if len(c.Resources) < 1 || len(c.Resources) > AlertResourceMax {
			errs = append(errs, FieldError{Field: "spec.condition.resources", Code: FieldOutOfRange, Message: fmt.Sprintf("Name 1–%d machines, as machine/NAME.", AlertResourceMax)})
		}
		for i, r := range c.Resources {
			name, ok := strings.CutPrefix(r, MachineRefPrefix)
			if !ok || ValidateName(name) != nil {
				errs = append(errs, FieldError{Field: fmt.Sprintf("spec.condition.resources[%d]", i), Code: FieldInvalidFormat, Message: "Name a machine as machine/NAME."})
			}
		}
		if c.ForMinutes < AlertForMinutesMin || c.ForMinutes > AlertForMinutesMax {
			errs = append(errs, FieldError{Field: "spec.condition.forMinutes", Code: FieldOutOfRange, Message: "Use 1–60 minutes."})
		}
	default:
		errs = append(errs, FieldError{Field: "spec.condition.type", Code: FieldNotAllowed, Message: "Choose metric, crash_loop, failed, run_failed or machine_offline."})
	}
	if c.Type != ConditionMetric && (c.Metric != "" || c.Operator != "" || c.Threshold != nil || c.ForMinutes != 0 && c.Type != ConditionMachineOffline) {
		errs = append(errs, FieldError{Field: "spec.condition.metric", Code: FieldNotAllowed, Message: "Metric, operator, threshold and duration are for metric conditions only."})
	}
	if c.Type != ConditionCrashLoop && (c.Restarts != 0 || c.WindowMinutes != 0) {
		errs = append(errs, FieldError{Field: "spec.condition.restarts", Code: FieldNotAllowed, Message: "Restarts and window are for crash_loop conditions only."})
	}
	errs = append(errs, validateNames(s.Channels, "spec.channels", 1, AlertChannelsMax, "channels")...)
	if s.RepeatMinutes < 0 || s.RepeatMinutes > AlertRepeatMax {
		errs = append(errs, FieldError{Field: "spec.repeatMinutes", Code: FieldOutOfRange, Message: "Use 0 (never) to 1440 minutes."})
	}
	return errs
}

// validateNames checks a list of distinct resource names.
func validateNames(names []string, field string, minN, maxN int, what string) []FieldError {
	if len(names) < minN || len(names) > maxN {
		return []FieldError{{Field: field, Code: FieldOutOfRange, Message: fmt.Sprintf("Choose %d–%d %s.", minN, maxN, what)}}
	}
	var errs []FieldError
	seen := map[string]bool{}
	for i, n := range names {
		f := fmt.Sprintf("%s[%d]", field, i)
		if ValidateName(n) != nil {
			errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Name an existing one."})
		} else if seen[n] {
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: n + " is listed twice."})
		}
		seen[n] = true
	}
	return errs
}

// Channel types (NCH-01).
const (
	ChannelEmail    = "email"
	ChannelWebhook  = "webhook"
	ChannelSlack    = "slack"
	ChannelDiscord  = "discord"
	ChannelTelegram = "telegram"
)

// ChannelTypes lists the channel types.
var ChannelTypes = []string{ChannelEmail, ChannelWebhook, ChannelSlack, ChannelDiscord, ChannelTelegram}

// ChannelEmailSettings sends through an email service of the project.
type ChannelEmailSettings struct {
	Service string   `json:"service"`
	From    string   `json:"from"`
	To      []string `json:"to"`
}

// ChannelWebhook posts JSON to a URL, signed when it has a secret.
type WebhookTarget struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

// ChannelSpec is where alerts go (ADR-039).
type ChannelSpec struct {
	Type    string                `json:"type"`
	Email   *ChannelEmailSettings `json:"email,omitempty"`
	Webhook *WebhookTarget        `json:"webhook,omitempty"`
	// Secret holds the Slack or Discord webhook URL, or the Telegram bot token.
	Secret string `json:"secret,omitempty"`
	ChatID string `json:"chatId,omitempty"`
}

// Channel limits.
const (
	ChannelRecipientsMax = 20
	ChatIDMaxLen         = 64
)

func validateChannel(s ChannelSpec) []FieldError {
	var errs []FieldError
	usesSecret := s.Type == ChannelSlack || s.Type == ChannelDiscord || s.Type == ChannelTelegram
	switch s.Type {
	case ChannelEmail:
		if s.Email == nil {
			errs = append(errs, FieldError{Field: "spec.email", Code: FieldRequired, Message: "Choose the email service, the sender and the recipients."})
			break
		}
		if ValidateName(s.Email.Service) != nil {
			errs = append(errs, FieldError{Field: "spec.email.service", Code: FieldRequired, Message: "Choose an email service of this project."})
		}
		if !ValidAddress(s.Email.From) {
			errs = append(errs, FieldError{Field: "spec.email.from", Code: FieldInvalidFormat, Message: "Enter the sender, like alerts@example.com."})
		}
		if len(s.Email.To) == 0 || len(s.Email.To) > ChannelRecipientsMax {
			errs = append(errs, FieldError{Field: "spec.email.to", Code: FieldOutOfRange, Message: "Add 1–20 recipients."})
		}
		for i, to := range s.Email.To {
			if !ValidAddress(to) {
				errs = append(errs, FieldError{Field: fmt.Sprintf("spec.email.to[%d]", i), Code: FieldInvalidFormat, Message: "Enter an address like oncall@example.com."})
			}
		}
	case ChannelWebhook:
		if s.Webhook == nil || !ValidWebhookURL(s.Webhook.URL) {
			errs = append(errs, FieldError{Field: "spec.webhook.url", Code: FieldInvalidFormat, Message: "Use an https:// URL (http:// only for localhost)."})
		} else if s.Webhook.Secret != "" && ValidateName(s.Webhook.Secret) != nil {
			errs = append(errs, FieldError{Field: "spec.webhook.secret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
		}
	case ChannelSlack, ChannelDiscord, ChannelTelegram:
		if ValidateName(s.Secret) != nil {
			what := "the incoming-webhook URL"
			if s.Type == ChannelTelegram {
				what = "the bot token"
			}
			errs = append(errs, FieldError{Field: "spec.secret", Code: FieldRequired, Message: "Name the secret holding " + what + "."})
		}
		if s.Type == ChannelTelegram && (s.ChatID == "" || len(s.ChatID) > ChatIDMaxLen || strings.ContainsAny(s.ChatID, " \t\r\n")) {
			errs = append(errs, FieldError{Field: "spec.chatId", Code: FieldRequired, Message: "Enter the chat ID, like -1001234567890 or @channelname."})
		}
	default:
		return []FieldError{{Field: "spec.type", Code: FieldNotAllowed, Message: "Choose email, webhook, slack, discord or telegram."}}
	}
	if s.Type != ChannelEmail && s.Email != nil {
		errs = append(errs, FieldError{Field: "spec.email", Code: FieldNotAllowed, Message: "Only email channels take email settings."})
	}
	if s.Type != ChannelWebhook && s.Webhook != nil {
		errs = append(errs, FieldError{Field: "spec.webhook", Code: FieldNotAllowed, Message: "Only webhook channels take a webhook."})
	}
	if !usesSecret && s.Secret != "" {
		errs = append(errs, FieldError{Field: "spec.secret", Code: FieldNotAllowed, Message: "Only Slack, Discord and Telegram channels take a secret here."})
	}
	if s.Type != ChannelTelegram && s.ChatID != "" {
		errs = append(errs, FieldError{Field: "spec.chatId", Code: FieldNotAllowed, Message: "Only Telegram channels take a chat ID."})
	}
	return errs
}

// ValidAddress loosely checks an email address.
func ValidAddress(a string) bool { return ValidateEmail(strings.TrimSpace(a)) == nil }

// Log export limits and defaults (LOG-*).
const (
	LogSourcesMax                                       = 50
	LogFilterMaxLen                                     = 512
	LogFlushMin, LogFlushMax                            = 5, 300
	LogFlushDefault, LogFlushBucketDefault              = 10, 60
	LogBatchMaxBytes                                    = 1 << 20
	LogLabelsMax                                        = 10
	LogURLMaxLen                                        = 2048
	LogDestinationBucket, LogDestinationS3              = "bucket", "s3"
	LogDestinationHTTP, LogDestinationOTLP, LogDestLoki = "http", "otlp", "loki"
)

var lokiLabelPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// LogHTTP is an HTTP, OTLP or Loki destination.
type LogHTTP struct {
	URL           string            `json:"url,omitempty"`
	Endpoint      string            `json:"endpoint,omitempty"`
	HeadersSecret string            `json:"headersSecret,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// LogS3 is any S3-compatible bucket, as a backup's destination plus a folder.
type LogS3 struct {
	S3Destination
	Prefix string `json:"prefix,omitempty"`
}

// LogDestination is exactly one place logs go (LOG-02).
type LogDestination struct {
	Bucket *BucketFolder `json:"bucket,omitempty"`
	S3     *LogS3        `json:"s3,omitempty"`
	HTTP   *LogHTTP      `json:"http,omitempty"`
	OTLP   *LogHTTP      `json:"otlp,omitempty"`
	Loki   *LogHTTP      `json:"loki,omitempty"`
}

// Type names the destination that is set.
func (d LogDestination) Type() string {
	switch {
	case d.Bucket != nil:
		return LogDestinationBucket
	case d.S3 != nil:
		return LogDestinationS3
	case d.HTTP != nil:
		return LogDestinationHTTP
	case d.OTLP != nil:
		return LogDestinationOTLP
	case d.Loki != nil:
		return LogDestLoki
	}
	return ""
}

// LogExportSpec sends logs somewhere else (ADR-040).
type LogExportSpec struct {
	Sources      []string       `json:"sources,omitempty"`
	Filter       string         `json:"filter,omitempty"`
	FlushSeconds int            `json:"flushSeconds,omitempty"`
	Destination  LogDestination `json:"destination"`
}

func normalizeLogExport(s LogExportSpec) LogExportSpec {
	if s.FlushSeconds == 0 {
		s.FlushSeconds = LogFlushDefault
		if t := s.Destination.Type(); t == LogDestinationBucket || t == LogDestinationS3 {
			s.FlushSeconds = LogFlushBucketDefault
		}
	}
	if b := s.Destination.Bucket; b != nil {
		b.Prefix = NormalizePrefix(b.Prefix)
	}
	if b := s.Destination.S3; b != nil {
		b.Prefix = NormalizePrefix(b.Prefix)
	}
	return s
}

func validateLogExport(s LogExportSpec) []FieldError {
	errs := validateRefs(s.Sources, "spec.sources", 0, LogSourcesMax, LogKinds)
	if len(s.Filter) > LogFilterMaxLen {
		errs = append(errs, FieldError{Field: "spec.filter", Code: FieldTooLong, Message: "Use at most 512 characters."})
	} else if _, err := regexp.Compile(s.Filter); err != nil {
		errs = append(errs, FieldError{Field: "spec.filter", Code: FieldInvalidFormat, Message: "That isn't a valid regular expression: " + err.Error() + "."})
	}
	if s.FlushSeconds < LogFlushMin || s.FlushSeconds > LogFlushMax {
		errs = append(errs, FieldError{Field: "spec.flushSeconds", Code: FieldOutOfRange, Message: "Use 5–300 seconds."})
	}
	d := s.Destination
	set := 0
	for _, p := range []bool{d.Bucket != nil, d.S3 != nil, d.HTTP != nil, d.OTLP != nil, d.Loki != nil} {
		if p {
			set++
		}
	}
	if set != 1 {
		return append(errs, FieldError{Field: "spec.destination", Code: FieldExactlyOne, Message: "Choose one destination: a bucket, S3, HTTP, OpenTelemetry or Loki."})
	}
	switch {
	case d.Bucket != nil:
		errs = append(errs, validateFolder(*d.Bucket, "spec.destination.bucket")...)
	case d.S3 != nil:
		errs = append(errs, validateS3(d.S3.S3Destination, "spec.destination.s3")...)
		errs = append(errs, validatePrefix(d.S3.Prefix, "spec.destination.s3.prefix")...)
	case d.HTTP != nil:
		errs = append(errs, validateLogHTTP(*d.HTTP, "spec.destination.http", d.HTTP.URL, "url", false)...)
	case d.OTLP != nil:
		errs = append(errs, validateLogHTTP(*d.OTLP, "spec.destination.otlp", d.OTLP.Endpoint, "endpoint", false)...)
	case d.Loki != nil:
		errs = append(errs, validateLogHTTP(*d.Loki, "spec.destination.loki", d.Loki.URL, "url", true)...)
	}
	return errs
}

func validateLogHTTP(h LogHTTP, field, raw, urlField string, labels bool) []FieldError {
	var errs []FieldError
	if !ValidHTTPURL(raw) {
		errs = append(errs, FieldError{Field: field + "." + urlField, Code: FieldInvalidFormat, Message: "Use an http:// or https:// URL."})
	}
	if urlField == "url" && h.Endpoint != "" || urlField == "endpoint" && h.URL != "" {
		errs = append(errs, FieldError{Field: field, Code: FieldNotAllowed, Message: "Use " + urlField + " for this destination."})
	}
	if h.HeadersSecret != "" && ValidateName(h.HeadersSecret) != nil {
		errs = append(errs, FieldError{Field: field + ".headersSecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
	}
	if !labels && len(h.Labels) > 0 {
		errs = append(errs, FieldError{Field: field + ".labels", Code: FieldNotAllowed, Message: "Only Loki takes labels."})
	}
	if len(h.Labels) > LogLabelsMax {
		errs = append(errs, FieldError{Field: field + ".labels", Code: FieldOutOfRange, Message: "Use at most 10 labels."})
	}
	for _, k := range sortedKeys(h.Labels) {
		if !lokiLabelPattern.MatchString(k) || len(h.Labels[k]) > EnvValueMaxLen {
			errs = append(errs, FieldError{Field: field + ".labels." + k, Code: FieldInvalidFormat, Message: "Label names use letters, digits and _, starting with a letter or _."})
		}
	}
	return errs
}

// ValidHTTPURL reports an http:// or https:// URL with a host and no credentials.
func ValidHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && len(raw) <= LogURLMaxLen
}

// Secret store providers (EXT-01).
const (
	ProviderVault       = "vault"
	ProviderOnePassword = "onepassword"
	ProviderAWS         = "aws"
	ProviderDoppler     = "doppler"
	ProviderInfisical   = "infisical"
)

// SecretStoreProviders lists the providers.
var SecretStoreProviders = []string{ProviderVault, ProviderOnePassword, ProviderAWS, ProviderDoppler, ProviderInfisical}

// Secret store limits and defaults.
const (
	StoreRefreshMin, StoreRefreshMax, StoreRefreshDefault = 60, 86400, 300
	VaultMountDefault                                     = "secret"
	InfisicalSiteDefault                                  = "https://app.infisical.com"
	SecretKeyMaxLen, SecretFieldMaxLen                    = 512, 128
)

var (
	storePathPattern = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
	awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
)

// VaultStore is HashiCorp Vault or OpenBao, KV version 2.
type VaultStore struct {
	Address     string `json:"address"`
	Mount       string `json:"mount,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	TokenSecret string `json:"tokenSecret"`
}

// OnePasswordStore is a 1Password Connect server.
type OnePasswordStore struct {
	ConnectURL  string `json:"connectUrl"`
	Vault       string `json:"vault"`
	TokenSecret string `json:"tokenSecret"`
}

// AWSStore is AWS Secrets Manager; the credentials secret holds {"accessKey","secretKey"}.
type AWSStore struct {
	Region            string `json:"region"`
	CredentialsSecret string `json:"credentialsSecret"`
	Endpoint          string `json:"endpoint,omitempty"`
}

// DopplerStore reads with a service token.
type DopplerStore struct {
	TokenSecret string `json:"tokenSecret"`
	Endpoint    string `json:"endpoint,omitempty"`
}

// InfisicalStore signs in as a machine identity; the credentials secret holds {"clientId","clientSecret"}.
type InfisicalStore struct {
	SiteURL           string `json:"siteUrl,omitempty"`
	ProjectID         string `json:"projectId"`
	Environment       string `json:"environment"`
	Path              string `json:"path,omitempty"`
	CredentialsSecret string `json:"credentialsSecret"`
}

// SecretStoreSpec connects to an outside secret manager (ADR-041).
type SecretStoreSpec struct {
	Provider       string            `json:"provider"`
	RefreshSeconds int               `json:"refreshSeconds,omitempty"`
	Vault          *VaultStore       `json:"vault,omitempty"`
	OnePassword    *OnePasswordStore `json:"onepassword,omitempty"`
	AWS            *AWSStore         `json:"aws,omitempty"`
	Doppler        *DopplerStore     `json:"doppler,omitempty"`
	Infisical      *InfisicalStore   `json:"infisical,omitempty"`
}

// CredentialSecret names the secret a store signs in with.
func (s SecretStoreSpec) CredentialSecret() (field, name string) {
	switch {
	case s.Vault != nil:
		return "spec.vault.tokenSecret", s.Vault.TokenSecret
	case s.OnePassword != nil:
		return "spec.onepassword.tokenSecret", s.OnePassword.TokenSecret
	case s.AWS != nil:
		return "spec.aws.credentialsSecret", s.AWS.CredentialsSecret
	case s.Doppler != nil:
		return "spec.doppler.tokenSecret", s.Doppler.TokenSecret
	case s.Infisical != nil:
		return "spec.infisical.credentialsSecret", s.Infisical.CredentialsSecret
	}
	return "", ""
}

func normalizeSecretStore(s SecretStoreSpec) SecretStoreSpec {
	if s.RefreshSeconds == 0 {
		s.RefreshSeconds = StoreRefreshDefault
	}
	if v := s.Vault; v != nil {
		v.Address = strings.TrimRight(strings.TrimSpace(v.Address), "/")
		if v.Mount == "" {
			v.Mount = VaultMountDefault
		}
		v.Mount = strings.Trim(v.Mount, "/")
	}
	if o := s.OnePassword; o != nil {
		o.ConnectURL = strings.TrimRight(strings.TrimSpace(o.ConnectURL), "/")
	}
	if i := s.Infisical; i != nil {
		if i.SiteURL == "" {
			i.SiteURL = InfisicalSiteDefault
		}
		i.SiteURL = strings.TrimRight(strings.TrimSpace(i.SiteURL), "/")
		if i.Path == "" {
			i.Path = "/"
		}
	}
	return s
}

func validateSecretStore(s SecretStoreSpec) []FieldError {
	var errs []FieldError
	blocks := map[string]bool{ProviderVault: s.Vault != nil, ProviderOnePassword: s.OnePassword != nil, ProviderAWS: s.AWS != nil,
		ProviderDoppler: s.Doppler != nil, ProviderInfisical: s.Infisical != nil}
	if !slices.Contains(SecretStoreProviders, s.Provider) {
		return []FieldError{{Field: "spec.provider", Code: FieldNotAllowed, Message: "Choose vault, onepassword, aws, doppler or infisical."}}
	}
	for _, p := range SecretStoreProviders {
		if p == s.Provider && !blocks[p] {
			errs = append(errs, FieldError{Field: "spec." + p, Code: FieldRequired, Message: "Fill in the " + p + " settings."})
		}
		if p != s.Provider && blocks[p] {
			errs = append(errs, FieldError{Field: "spec." + p, Code: FieldNotAllowed, Message: "Only the " + s.Provider + " settings belong here."})
		}
	}
	if s.RefreshSeconds < StoreRefreshMin || s.RefreshSeconds > StoreRefreshMax {
		errs = append(errs, FieldError{Field: "spec.refreshSeconds", Code: FieldOutOfRange, Message: "Use 60 seconds to a day (86400)."})
	}
	url := func(raw, field string, optional bool) {
		if (raw != "" || !optional) && !ValidHTTPURL(raw) {
			errs = append(errs, FieldError{Field: field, Code: FieldInvalidFormat, Message: "Use an http:// or https:// URL."})
		}
	}
	secret := func(name, field string) {
		if ValidateName(name) != nil {
			errs = append(errs, FieldError{Field: field, Code: FieldRequired, Message: "Name the secret EnclaveStack signs in with."})
		}
	}
	path := func(p, field string) {
		if p != "" && (!storePathPattern.MatchString(p) || slices.Contains(strings.Split(p, "/"), "..")) {
			errs = append(errs, FieldError{Field: field, Code: FieldInvalidFormat, Message: "Use printable characters without spaces or ..; up to 512."})
		}
	}
	switch {
	case s.Provider == ProviderVault && s.Vault != nil:
		url(s.Vault.Address, "spec.vault.address", false)
		path(s.Vault.Mount, "spec.vault.mount")
		path(s.Vault.Namespace, "spec.vault.namespace")
		secret(s.Vault.TokenSecret, "spec.vault.tokenSecret")
	case s.Provider == ProviderOnePassword && s.OnePassword != nil:
		url(s.OnePassword.ConnectURL, "spec.onepassword.connectUrl", false)
		if n := utf8.RuneCountInString(s.OnePassword.Vault); n == 0 || n > 128 {
			errs = append(errs, FieldError{Field: "spec.onepassword.vault", Code: FieldRequired, Message: "Enter the vault's name or ID."})
		}
		secret(s.OnePassword.TokenSecret, "spec.onepassword.tokenSecret")
	case s.Provider == ProviderAWS && s.AWS != nil:
		if !awsRegionPattern.MatchString(s.AWS.Region) {
			errs = append(errs, FieldError{Field: "spec.aws.region", Code: FieldInvalidFormat, Message: "Use a region like eu-west-1."})
		}
		url(s.AWS.Endpoint, "spec.aws.endpoint", true)
		secret(s.AWS.CredentialsSecret, "spec.aws.credentialsSecret")
	case s.Provider == ProviderDoppler && s.Doppler != nil:
		url(s.Doppler.Endpoint, "spec.doppler.endpoint", true)
		secret(s.Doppler.TokenSecret, "spec.doppler.tokenSecret")
	case s.Provider == ProviderInfisical && s.Infisical != nil:
		url(s.Infisical.SiteURL, "spec.infisical.siteUrl", false)
		if s.Infisical.ProjectID == "" || len(s.Infisical.ProjectID) > 128 {
			errs = append(errs, FieldError{Field: "spec.infisical.projectId", Code: FieldRequired, Message: "Enter the Infisical project ID."})
		}
		if s.Infisical.Environment == "" || len(s.Infisical.Environment) > 64 {
			errs = append(errs, FieldError{Field: "spec.infisical.environment", Code: FieldRequired, Message: "Enter the environment slug, like prod."})
		}
		path(s.Infisical.Path, "spec.infisical.path")
		secret(s.Infisical.CredentialsSecret, "spec.infisical.credentialsSecret")
	}
	return errs
}

// SecretSource reads a secret's value from a secret store (EXT-02).
type SecretSource struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Field string `json:"field,omitempty"`
}

func validateSecretSource(src SecretSource) []FieldError {
	var errs []FieldError
	if ValidateName(src.Store) != nil {
		errs = append(errs, FieldError{Field: "spec.source.store", Code: FieldRequired, Message: "Choose a secret store of this project."})
	}
	if n := utf8.RuneCountInString(src.Key); n == 0 || n > SecretKeyMaxLen || strings.ContainsAny(src.Key, "\r\n") {
		errs = append(errs, FieldError{Field: "spec.source.key", Code: FieldRequired, Message: "Enter the key: a path, item or secret name, up to 512 characters."})
	}
	if utf8.RuneCountInString(src.Field) > SecretFieldMaxLen || strings.ContainsAny(src.Field, "\r\n") {
		errs = append(errs, FieldError{Field: "spec.source.field", Code: FieldTooLong, Message: "Use at most 128 characters."})
	}
	return errs
}

// ActionTest and ActionRefresh are the v0.7 actions (L3-002 §10.5).
const (
	ActionTest    = "test"
	ActionRefresh = "refresh"
)

// TestOutcome is one target's answer to a test.
type TestOutcome struct {
	Target  string `json:"target"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// TestResult answers the test action.
type TestResult struct {
	OK    bool          `json:"ok"`
	Items []TestOutcome `json:"items"`
}

// Notification statuses and events.
const (
	NotificationPending = "pending"
	NotificationSent    = "sent"
	NotificationFailed  = "failed"

	EventFiring   = "firing"
	EventResolved = "resolved"
	EventRepeat   = "repeat"
	EventTest     = "test"
)

// IncidentNotification is one channel's outcome for an incident.
type IncidentNotification struct {
	Channel string `json:"channel"`
	Event   string `json:"event"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

// Incident is one firing of an alert (ALR-05).
type Incident struct {
	ID            string                 `json:"id"`
	Subject       string                 `json:"subject"`
	StartedAt     time.Time              `json:"startedAt"`
	ResolvedAt    *time.Time             `json:"resolvedAt"`
	Value         *float64               `json:"value"`
	Message       string                 `json:"message"`
	Notifications []IncidentNotification `json:"notifications"`
}

// IncidentList wraps incidents.
type IncidentList struct {
	Items []Incident `json:"items"`
}

// Notification is one delivery of a channel (NCH-04).
type Notification struct {
	ID        string     `json:"id"`
	Event     string     `json:"event"`
	Alert     string     `json:"alert"`
	Subject   string     `json:"subject"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"lastError"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt"`
}

// NotificationList wraps notifications.
type NotificationList struct {
	Items []Notification `json:"items"`
}

// TokenRole resolves a token request's role: the role, or viewer for the v0.6 scope read. "" means the
// owner's strongest role, which is also what the v0.6 scope write meant.
func (r CreateTokenRequest) TokenRole() string {
	switch {
	case r.Role != "":
		return r.Role
	case r.Scope == ScopeRead:
		return RoleViewer
	}
	return ""
}

// ScopeForRole is the v0.6 scope a role reads as.
func ScopeForRole(role string) string {
	if role == RoleViewer {
		return ScopeRead
	}
	return ScopeWrite
}

// UnmarshalJSON tells an absent machineRole from a null one.
func (u *UpdateUser) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k := range raw {
		if k != "machineRole" && k != "projectRoles" {
			return fmt.Errorf("json: unknown field %q", k)
		}
	}
	if m, ok := raw["machineRole"]; ok {
		u.MachineRoleSet = true
		if err := json.Unmarshal(m, &u.MachineRole); err != nil {
			return err
		}
	}
	if p, ok := raw["projectRoles"]; ok {
		if err := json.Unmarshal(p, &u.ProjectRoles); err != nil {
			return err
		}
	}
	return nil
}

// FormatMetric writes a metric value in its unit, as people read it: "1.5 GiB", "51.6%", "0.500 cores".
// The CLI and notifications share it (CLI-40, NCH-02).
func FormatMetric(v float64, unit string) string {
	switch unit {
	case UnitBytes:
		return humanBytes(v)
	case UnitBytesPerSecond:
		return humanBytes(v) + "/s"
	case UnitPercent:
		return fmt.Sprintf("%.1f%%", v)
	case UnitCores:
		return fmt.Sprintf("%.3f cores", v)
	case UnitPerSecond:
		return fmt.Sprintf("%.2f/s", v)
	case UnitMilliseconds:
		return fmt.Sprintf("%.1f ms", v)
	case UnitSeconds:
		return fmt.Sprintf("%.0f s", v)
	}
	return strconv.FormatFloat(v, 'g', 4, 64)
}

// ByteSize writes a size in bytes for people: "512 B", "48.2 MiB" (events, messages, the CLI).
func ByteSize(n int64) string { return humanBytes(float64(n)) }

func humanBytes(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", v)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// RefText names a "kind/name" ref, or the machine, in a sentence.
func RefText(ref string) string {
	if ref == MachineRef {
		return "this machine"
	}
	return ref
}

// Minutes is "1 minute" or "n minutes".
func Minutes(n int) string {
	if n == 1 {
		return "1 minute"
	}
	return strconv.Itoa(n) + " minutes"
}
