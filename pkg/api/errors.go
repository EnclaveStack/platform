package api

import (
	"fmt"
	"net/http"
)

// ErrorCode is a stable machine identifier (L0-003). Clients branch on it, never on the message.
type ErrorCode string

const (
	CodeMalformedRequest     ErrorCode = "MALFORMED_REQUEST"
	CodeUnauthenticated      ErrorCode = "UNAUTHENTICATED"
	CodeInvalidCredentials   ErrorCode = "INVALID_CREDENTIALS"
	CodeForbidden            ErrorCode = "FORBIDDEN"
	CodeCSRFRequired         ErrorCode = "CSRF_REQUIRED"
	CodeInvalidSetupCode     ErrorCode = "INVALID_SETUP_CODE"
	CodeNotFound             ErrorCode = "NOT_FOUND"
	CodeMethodNotAllowed     ErrorCode = "METHOD_NOT_ALLOWED"
	CodeNameTaken            ErrorCode = "NAME_TAKEN"
	CodeHostnameTaken        ErrorCode = "HOSTNAME_TAKEN"
	CodeRunInProgress        ErrorCode = "RUN_IN_PROGRESS"
	CodeRunNotRestorable     ErrorCode = "RUN_NOT_RESTORABLE"
	CodeResourceInUse        ErrorCode = "RESOURCE_IN_USE"
	CodeVolumeAttached       ErrorCode = "VOLUME_ATTACHED"
	CodeResourceBusy         ErrorCode = "RESOURCE_BUSY"
	CodeActionNotSupported   ErrorCode = "ACTION_NOT_SUPPORTED"
	CodeSetupComplete        ErrorCode = "SETUP_COMPLETE"
	CodeSetupRequired        ErrorCode = "SETUP_REQUIRED"
	CodePreconditionFailed   ErrorCode = "PRECONDITION_FAILED"
	CodePayloadTooLarge      ErrorCode = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMediaType ErrorCode = "UNSUPPORTED_MEDIA_TYPE"
	CodeValidationFailed     ErrorCode = "VALIDATION_FAILED"
	CodeImmutableField       ErrorCode = "IMMUTABLE_FIELD"
	CodeIdempotencyKeyReused ErrorCode = "IDEMPOTENCY_KEY_REUSED"
	CodeTooManyAttempts      ErrorCode = "TOO_MANY_ATTEMPTS"
	CodeRateLimited          ErrorCode = "RATE_LIMITED"
	CodeInternal             ErrorCode = "INTERNAL"
	CodeEngineUnavailable    ErrorCode = "ENGINE_UNAVAILABLE"
	// v0.7 (L3-002 §10.6).
	CodeProjectNotEmpty ErrorCode = "PROJECT_NOT_EMPTY"
	CodeLastAdmin       ErrorCode = "LAST_ADMIN"
	CodeEmailTaken      ErrorCode = "EMAIL_TAKEN"
	CodeInviteInvalid   ErrorCode = "INVITE_INVALID"
)

// ErrorCodes maps every code to its HTTP status. It is the complete list (L3-002 §1).
var ErrorCodes = map[ErrorCode]int{
	CodeMalformedRequest:     http.StatusBadRequest,
	CodeUnauthenticated:      http.StatusUnauthorized,
	CodeInvalidCredentials:   http.StatusUnauthorized,
	CodeForbidden:            http.StatusForbidden,
	CodeCSRFRequired:         http.StatusForbidden,
	CodeInvalidSetupCode:     http.StatusForbidden,
	CodeNotFound:             http.StatusNotFound,
	CodeMethodNotAllowed:     http.StatusMethodNotAllowed,
	CodeNameTaken:            http.StatusConflict,
	CodeHostnameTaken:        http.StatusConflict,
	CodeRunInProgress:        http.StatusConflict,
	CodeRunNotRestorable:     http.StatusConflict,
	CodeResourceInUse:        http.StatusConflict,
	CodeVolumeAttached:       http.StatusConflict,
	CodeResourceBusy:         http.StatusConflict,
	CodeActionNotSupported:   http.StatusConflict,
	CodeSetupComplete:        http.StatusConflict,
	CodeSetupRequired:        http.StatusConflict,
	CodePreconditionFailed:   http.StatusPreconditionFailed,
	CodePayloadTooLarge:      http.StatusRequestEntityTooLarge,
	CodeUnsupportedMediaType: http.StatusUnsupportedMediaType,
	CodeValidationFailed:     http.StatusUnprocessableEntity,
	CodeImmutableField:       http.StatusUnprocessableEntity,
	CodeIdempotencyKeyReused: http.StatusUnprocessableEntity,
	CodeTooManyAttempts:      http.StatusTooManyRequests,
	CodeRateLimited:          http.StatusTooManyRequests,
	CodeInternal:             http.StatusInternalServerError,
	CodeEngineUnavailable:    http.StatusServiceUnavailable,
	CodeProjectNotEmpty:      http.StatusConflict,
	CodeLastAdmin:            http.StatusConflict,
	CodeEmailTaken:           http.StatusConflict,
	CodeInviteInvalid:        http.StatusGone,
}

// FieldCode identifies why a single field failed validation.
type FieldCode string

const (
	FieldRequired        FieldCode = "REQUIRED"
	FieldInvalidFormat   FieldCode = "INVALID_FORMAT"
	FieldTooShort        FieldCode = "TOO_SHORT"
	FieldTooLong         FieldCode = "TOO_LONG"
	FieldOutOfRange      FieldCode = "OUT_OF_RANGE"
	FieldNotAllowed      FieldCode = "NOT_ALLOWED"
	FieldNotFound        FieldCode = "NOT_FOUND"
	FieldImmutable       FieldCode = "IMMUTABLE"
	FieldInvalidSchedule FieldCode = "INVALID_SCHEDULE"
	FieldExactlyOne      FieldCode = "EXACTLY_ONE"
	FieldOverlaps        FieldCode = "OVERLAPS"
	FieldGrowOnly        FieldCode = "GROW_ONLY"
)

// FieldCodes is the complete list, exported for generated clients.
var FieldCodes = []FieldCode{FieldRequired, FieldInvalidFormat, FieldTooShort, FieldTooLong, FieldOutOfRange, FieldNotAllowed, FieldNotFound, FieldImmutable, FieldInvalidSchedule, FieldExactlyOne, FieldOverlaps, FieldGrowOnly}

// FieldError is one entry of an error's details.
type FieldError struct {
	Field   string    `json:"field"`
	Code    FieldCode `json:"code"`
	Message string    `json:"message"`
}

// Error is both the wire envelope's inner object and a Go error.
type Error struct {
	Code      ErrorCode    `json:"code"`
	Message   string       `json:"message"`
	RequestID string       `json:"requestId,omitempty"`
	Details   []FieldError `json:"details,omitempty"`
	// RetryAfterSeconds is sent as the Retry-After header for 429/503, not in the body.
	RetryAfterSeconds int `json:"-"`
}

// ErrorEnvelope is the response body for every error (L0-003 §1).
type ErrorEnvelope struct {
	Error *Error `json:"error"`
}

func (e *Error) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("%s: %s (request %s)", e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Status is the HTTP status for the error's code.
func (e *Error) Status() int {
	if s, ok := ErrorCodes[e.Code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// NewError builds an error with a safe, human-readable message.
func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Validation builds a VALIDATION_FAILED error from field errors.
func Validation(details []FieldError) *Error {
	return &Error{Code: CodeValidationFailed, Message: "Check the highlighted fields.", Details: details}
}
