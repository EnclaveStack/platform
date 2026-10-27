package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Functions (L2-016 FN-01 – FN-12, L3-002 §18.1, ADR-055): a container image started once per event.

// Function limits and defaults.
const (
	FunctionMemoryDefaultMiB = 256
	FunctionMemoryMaxMiB     = 8192
	FunctionTimeoutMin       = 1
	FunctionTimeoutMax       = 900
	FunctionTimeoutDefault   = 60
	FunctionConcurrencyMin   = 1
	FunctionConcurrencyMax   = 20
	FunctionConcurrencyDef   = 5
	FunctionTriggersMax      = 10
	// InvokeMaxBytes bounds an invocation's body and so a run's event (FN-06).
	InvokeMaxBytes = 256 << 10
	// InvokeWaitMax is how long ?wait=true waits for a run to end.
	InvokeWaitMax = 60 * time.Second
	// QueueVisibilityMargin is how much longer than a function's timeout a queue it reads must hide a
	// received message (FN-09).
	QueueVisibilityMargin = 30
	// FunctionBusyRetrySeconds is the Retry-After of FUNCTION_BUSY.
	FunctionBusyRetrySeconds = 5
)

// The environment a function's container gets (FN-05). EventPath is where the event is written.
const (
	EventPath        = "/enclave/event.json"
	EnvEventFile     = "ENCLAVE_EVENT_FILE"
	EnvEventSource   = "ENCLAVE_EVENT_SOURCE"
	EnvRunID         = "ENCLAVE_RUN_ID"
	EnvFunction      = "ENCLAVE_FUNCTION"
	ReservedEnvStart = "ENCLAVE_"
)

// Event sources (FN-05): a run's trigger, and "manual" for the run action.
const (
	SourceSchedule = TriggerSchedule
	SourceQueue    = TriggerQueue
	SourceTopic    = TriggerTopic
	SourceBucket   = TriggerBucket
	SourceInvoke   = TriggerInvoke
	SourceManual   = TriggerManual
)

// FunctionTrigger is exactly one of a schedule, a queue or a bucket folder (FN-02).
type FunctionTrigger struct {
	Schedule string `json:"schedule,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
	Queue    string `json:"queue,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

// FunctionSpec is the desired state of a function (FN-01).
type FunctionSpec struct {
	Image          string            `json:"image"`
	Command        []string          `json:"command,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	SecretEnv      map[string]string `json:"secretEnv,omitempty"`
	MemoryMiB      int               `json:"memoryMiB,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	Concurrency    int               `json:"concurrency,omitempty"`
	Triggers       []FunctionTrigger `json:"triggers,omitempty"`
	Network        string            `json:"network,omitempty"`
	Subnet         string            `json:"subnet,omitempty"`
	BucketAccess   []BucketGrant     `json:"bucketAccess,omitempty"`
	// Placed chooses the machine it runs on (ADR-044).
	Placed
}

// Schedule is the function's schedule trigger, if it has one.
func (s FunctionSpec) Schedule() (FunctionTrigger, bool) {
	for _, t := range s.Triggers {
		if t.Schedule != "" {
			return t, true
		}
	}
	return FunctionTrigger{}, false
}

// QueueMessage, TopicMessage and the event types are a run's event as written to EventPath (L3-002 §18.1).
type QueueMessage struct {
	ID           string            `json:"id"`
	Body         string            `json:"body"`
	Attributes   map[string]string `json:"attributes,omitempty"`
	SentAt       time.Time         `json:"sentAt"`
	ReceiveCount int               `json:"receiveCount"`
}

type TopicMessage struct {
	Sequence    uint64            `json:"sequence"`
	Body        string            `json:"body"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	PublishedAt time.Time         `json:"publishedAt"`
	Attempt     int               `json:"attempt"`
}

// FunctionEvent is every source's event in one shape; fields that don't belong to Source are left out.
type FunctionEvent struct {
	Source       string          `json:"source"`
	ScheduledFor *time.Time      `json:"scheduledFor,omitempty"`
	Queue        string          `json:"queue,omitempty"`
	Topic        string          `json:"topic,omitempty"`
	Message      any             `json:"message,omitempty"`
	Bucket       string          `json:"bucket,omitempty"`
	Key          string          `json:"key,omitempty"`
	Size         *int64          `json:"size,omitempty"`
	ModifiedAt   *time.Time      `json:"modifiedAt,omitempty"`
	Body         json.RawMessage `json:"body,omitempty"`
}

func normalizeFunction(s FunctionSpec) FunctionSpec {
	s.Image = strings.TrimSpace(s.Image)
	if s.MemoryMiB == 0 {
		s.MemoryMiB = FunctionMemoryDefaultMiB
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = FunctionTimeoutDefault
	}
	if s.Concurrency == 0 {
		s.Concurrency = FunctionConcurrencyDef
	}
	for i := range s.Triggers {
		t := &s.Triggers[i]
		t.Schedule, t.TimeZone, t.Queue, t.Bucket = strings.TrimSpace(t.Schedule), strings.TrimSpace(t.TimeZone), strings.TrimSpace(t.Queue), strings.TrimSpace(t.Bucket)
	}
	return s
}

func validateFunction(s FunctionSpec) []FieldError {
	errs := validateImage(s.Image, "spec.image")
	errs = append(errs, validateCommand(s.Command)...)
	errs = append(errs, validateEnv(s.Env, s.SecretEnv)...)
	for _, k := range append(sortedKeys(s.Env), sortedKeys(s.SecretEnv)...) {
		if strings.HasPrefix(k, ReservedEnvStart) {
			field := "spec.env." + k
			if _, secret := s.SecretEnv[k]; secret {
				field = "spec.secretEnv." + k
			}
			errs = append(errs, FieldError{Field: field, Code: FieldNotAllowed, Message: "Names starting with ENCLAVE_ are set by EnclaveStack for each run."})
		}
	}
	errs = append(errs, validateBucketAccess(s.BucketAccess, s.Env, s.SecretEnv)...)
	if s.MemoryMiB < AppMemoryMinMiB || s.MemoryMiB > FunctionMemoryMaxMiB {
		errs = append(errs, FieldError{Field: "spec.memoryMiB", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d MiB.", AppMemoryMinMiB, FunctionMemoryMaxMiB)})
	}
	if s.TimeoutSeconds < FunctionTimeoutMin || s.TimeoutSeconds > FunctionTimeoutMax {
		errs = append(errs, FieldError{Field: "spec.timeoutSeconds", Code: FieldOutOfRange, Message: "Use 1–900 seconds. Longer work belongs in a job."})
	}
	if s.Concurrency < FunctionConcurrencyMin || s.Concurrency > FunctionConcurrencyMax {
		errs = append(errs, FieldError{Field: "spec.concurrency", Code: FieldOutOfRange, Message: fmt.Sprintf("Use %d–%d runs at once.", FunctionConcurrencyMin, FunctionConcurrencyMax)})
	}
	if len(s.Triggers) > FunctionTriggersMax {
		errs = append(errs, FieldError{Field: "spec.triggers", Code: FieldTooLong, Message: fmt.Sprintf("Use at most %d triggers.", FunctionTriggersMax)})
	}
	schedules := 0
	seen := map[string]bool{}
	for i, t := range s.Triggers {
		f := fmt.Sprintf("spec.triggers[%d]", i)
		kinds := 0
		for _, v := range []string{t.Schedule, t.Queue, t.Bucket} {
			if v != "" {
				kinds++
			}
		}
		if kinds != 1 {
			errs = append(errs, FieldError{Field: f, Code: FieldExactlyOne, Message: "A trigger is a schedule, a queue or a bucket — choose one."})
			continue
		}
		if t.Schedule == "" && t.TimeZone != "" {
			errs = append(errs, FieldError{Field: f + ".timeZone", Code: FieldNotAllowed, Message: "Only a schedule has a time zone."})
		}
		if t.Bucket == "" && t.Prefix != "" {
			errs = append(errs, FieldError{Field: f + ".prefix", Code: FieldNotAllowed, Message: "Only a bucket trigger has a folder."})
		}
		switch {
		case t.Schedule != "":
			schedules++
			if schedules > 1 {
				errs = append(errs, FieldError{Field: f + ".schedule", Code: FieldNotAllowed, Message: "A function has at most one schedule. Put the times in one cron expression, or make a second function."})
				continue
			}
			tzOK := true
			if t.TimeZone != "" {
				if _, err := time.LoadLocation(t.TimeZone); err != nil {
					tzOK = false
					errs = append(errs, FieldError{Field: f + ".timeZone", Code: FieldInvalidFormat, Message: "Use an IANA time zone like Asia/Kolkata or Europe/Berlin."})
				}
			}
			if tzOK {
				if _, err := ParseSchedule(t.Schedule, t.TimeZone); err != nil {
					errs = append(errs, FieldError{Field: f + ".schedule", Code: FieldInvalidSchedule, Message: "Use 5 cron fields (minute hour day month weekday) or @hourly, @daily, @weekly, @monthly: " + err.Error() + "."})
				}
			}
		case t.Queue != "":
			if ValidateName(t.Queue) != nil {
				errs = append(errs, FieldError{Field: f + ".queue", Code: FieldInvalidFormat, Message: "Name an existing queue."})
			} else if seen["queue/"+t.Queue] {
				errs = append(errs, FieldError{Field: f + ".queue", Code: FieldNotAllowed, Message: t.Queue + " is a trigger twice."})
			}
			seen["queue/"+t.Queue] = true
		default:
			errs = append(errs, validateFolder(BucketFolder{Bucket: t.Bucket, Prefix: t.Prefix}, f)...)
			key := "bucket/" + t.Bucket + "/" + t.Prefix
			if seen[key] {
				errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "This folder is a trigger twice."})
			}
			seen[key] = true
		}
	}
	return append(errs, validatePlacement(s.Network, s.Subnet)...)
}
