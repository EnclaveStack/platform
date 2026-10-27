package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The agent protocol (L3-002 §11.11, ADR-042).
const (
	Protocol = 1

	pathInfo     = "/agent/v1/info"
	pathDriver   = "/agent/v1/driver/"
	pathHyper    = "/agent/v1/hypervisor/"
	pathContinue = "/agent/v1/calls/"
	pathFiles    = "/agent/v1/files/"
	pathRenew    = "/agent/v1/renew"
	pathCert     = "/agent/v1/certificate"
	pathSweep    = "/agent/v1/sweep"

	headerArgs     = "X-Enclave-Args"
	headerDeadline = "X-Enclave-Deadline"
	trailerResult  = "X-Enclave-Result"
	trailerError   = "X-Enclave-Error"
)

// Error classes keep an engine error's meaning across the wire.
const (
	classPermanent   = "permanent"
	classUnavailable = "unavailable"
	classNoVMs       = "no_vms"
	classJobMissing  = "job_missing"
	classNoCA        = "no_ca"
	classTransient   = "transient"
)

func init() { classStatus[classFenced] = http.StatusConflict }

var classStatus = map[string]int{classPermanent: http.StatusUnprocessableEntity, classUnavailable: http.StatusServiceUnavailable,
	classNoVMs: http.StatusServiceUnavailable, classJobMissing: http.StatusNotFound, classNoCA: http.StatusNotFound, classTransient: http.StatusInternalServerError}

// wireError is an error as it crosses the wire.
type wireError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

func toWire(err error) wireError {
	class := classTransient
	switch {
	case errors.Is(err, engine.ErrNoVMs):
		class = classNoVMs
	case errors.Is(err, engine.ErrUnavailable):
		class = classUnavailable
	case errors.Is(err, engine.ErrJobMissing):
		class = classJobMissing
	case errors.Is(err, engine.ErrNoCA):
		class = classNoCA
	case errors.Is(err, ErrFenced):
		class = classFenced
	case engine.IsPermanent(err):
		class = classPermanent
	}
	return wireError{Class: class, Message: err.Error()}
}

// remoteError is an error the agent sent back: its message as the agent wrote it, and Is() for the engine's
// sentinel errors, so the reconciler classifies it as it does a local one.
type remoteError struct {
	class string
	msg   string
}

func (e *remoteError) Error() string { return e.msg }

func (e *remoteError) Is(target error) bool {
	switch e.class {
	case classNoVMs:
		return target == engine.ErrNoVMs
	case classUnavailable:
		return target == engine.ErrUnavailable
	case classJobMissing:
		return target == engine.ErrJobMissing
	case classNoCA:
		return target == engine.ErrNoCA
	case classFenced:
		return target == ErrFenced
	}
	return false
}

func (w wireError) err() error {
	if w.Class == classPermanent {
		return engine.Permanent(&remoteError{class: w.Class, msg: w.Message})
	}
	return &remoteError{class: w.Class, msg: w.Message}
}

// Info is GET /agent/v1/info: the agent and its machine (L3-002 §11.11).
type Info struct {
	Version          string        `json:"version"`
	Protocol         int           `json:"protocol"`
	MachineID        string        `json:"machineId"`
	OS               string        `json:"os"`
	Arch             string        `json:"arch"`
	Engine           api.Engine    `json:"engine"`
	CPUs             int           `json:"cpus"`
	MemoryTotalBytes int64         `json:"memoryTotalBytes"`
	VMs              api.VMSupport `json:"vms"`
	TunnelPublicKey  string        `json:"tunnelPublicKey"`
}

// event is one line of a callback stream (Ensure, EnsureVM, Build).
type event struct {
	Step    string          `json:"step,omitempty"`
	State   api.StepState   `json:"state,omitempty"`
	Note    *string         `json:"note,omitempty"`
	Event   string          `json:"event,omitempty"`
	Message string          `json:"message,omitempty"`
	Started string          `json:"started,omitempty"`
	Log     *string         `json:"log,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

// continueBody answers a started callback.
type continueBody struct {
	Error string `json:"error,omitempty"`
}

func encodeArgs(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeArgs(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("arguments: %w", err)
	}
	return json.Unmarshal(b, v)
}

func formatDeadline(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseDeadline(s string) (time.Time, bool) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}
