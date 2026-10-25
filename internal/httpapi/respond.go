package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/enclavestack/platform/internal/observability"
	"github.com/enclavestack/platform/pkg/api"
)

// MaxBodyBytes is the request body limit (ADR-012); MaxDataBodyBytes is that of the message and email
// endpoints, whose bodies carry up to 1 MiB of content after JSON escaping (L3-002 §9.3).
const (
	MaxBodyBytes     = 1 << 20
	MaxDataBodyBytes = 3 << 20
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
	}
}

// writeError renders any error as the L0-003 envelope. Unknown errors become INTERNAL and are logged
// with their cause; the client only sees the request ID.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *api.Error
	if !errors.As(err, &ae) {
		observability.Logger(r.Context(), s.log).Error("request failed", "error", err.Error())
		ae = api.NewError(api.CodeInternal, "Something went wrong on our side. Try again in a minute.")
	}
	out := *ae
	out.RequestID = requestID(r.Context())
	if out.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(out.RetryAfterSeconds))
	}
	writeJSON(w, out.Status(), api.ErrorEnvelope{Error: &out})
}

// decode reads a JSON body strictly (L3-002 §1): JSON content type, ≤ 1 MiB, no unknown fields.
func decode(r *http.Request, v any) ([]byte, error) {
	return decodeLimit(r, v, MaxBodyBytes)
}

// decodeLimit is decode with another body limit.
func decodeLimit(r *http.Request, v any, limit int64) ([]byte, error) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return nil, api.NewError(api.CodeUnsupportedMediaType, "Send the body as JSON with Content-Type: application/json.")
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, api.NewError(api.CodePayloadTooLarge, fmt.Sprintf("The request body is larger than %d MiB.", limit>>20))
		}
		return nil, api.NewError(api.CodeMalformedRequest, "The request body couldn't be read.")
	}
	if err := api.DecodeStrict(body, v); err != nil {
		return nil, api.NewError(api.CodeMalformedRequest, "The request body isn't valid: "+cleanJSONError(err))
	}
	return body, nil
}

// hasBody reports whether the request carries any body bytes (chunked bodies have no length up front).
// The byte it peeks at is put back, so the body can still be decoded.
func hasBody(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return false
	}
	var b [1]byte
	n, _ := io.ReadFull(r.Body, b[:])
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(b[:n]), r.Body), r.Body}
	return n > 0
}

func cleanJSONError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "json: ")
	return msg
}

func queryInt(r *http.Request, name string, def, min, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, api.Validation([]api.FieldError{{Field: name, Code: api.FieldOutOfRange, Message: "Use a number from " + strconv.Itoa(min) + " to " + strconv.Itoa(max) + "."}})
	}
	return n, nil
}

// ifMatch parses If-Match: "<generation>" (quotes optional, W/ ignored).
func ifMatch(r *http.Request) (*int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" || raw == "*" {
		return nil, nil
	}
	raw = strings.Trim(strings.TrimPrefix(raw, "W/"), `"`)
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, api.NewError(api.CodeMalformedRequest, `If-Match must be the resource's ETag, like "3".`)
	}
	return &n, nil
}

func etag(gen int64) string { return `"` + strconv.FormatInt(gen, 10) + `"` }
