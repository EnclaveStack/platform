package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/pkg/api"
)

// invoke starts a function's run with the body as its event (L3-002 §18.1, FN-06).
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	wait := false
	switch r.URL.Query().Get("wait") {
	case "", "false":
	case "true":
		wait = true
	default:
		s.writeError(w, r, api.Validation([]api.FieldError{{Field: "wait", Code: api.FieldInvalidFormat, Message: "Use wait=true to wait for the run to end."}}))
		return
	}
	body := json.RawMessage("null")
	if hasBody(r) {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			s.writeError(w, r, api.NewError(api.CodeUnsupportedMediaType, "Send the body as JSON with Content-Type: application/json."))
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, api.InvokeMaxBytes))
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			s.writeError(w, r, api.NewError(api.CodePayloadTooLarge, "An invocation's body can be at most 256 KiB. Put bigger inputs in a bucket and send the key."))
			return
		case err != nil:
			s.writeError(w, r, api.NewError(api.CodeMalformedRequest, "The request body couldn't be read."))
			return
		case !json.Valid(raw):
			s.writeError(w, r, api.NewError(api.CodeMalformedRequest, "The body isn't JSON. Send any JSON value: an object, a list, a string or a number."))
			return
		}
		body = raw
	}
	run, err := s.resources.Invoke(r.Context(), principal(r.Context()), r.PathValue("id"), body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !wait {
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	detail, ended, err := s.resources.WaitRun(r.Context(), r.PathValue("id"), run.ID, api.InvokeWaitMax)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !ended {
		writeJSON(w, http.StatusAccepted, detail.Run)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.ListPreviews(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	var req api.CreatePreview
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.CreatePreview(r.Context(), principal(r.Context()), r.PathValue("id"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeResource(w, http.StatusAccepted, out)
}

// gitHook takes a GitHub or GitLab webhook for an app (L3-002 §18.2).
func (s *Server) gitHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, api.HookBodyMax))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.writeError(w, r, api.NewError(api.CodePayloadTooLarge, "A webhook's body can be at most 1 MiB."))
			return
		}
		s.writeError(w, r, api.NewError(api.CodeMalformedRequest, "The request body couldn't be read."))
		return
	}
	out, err := s.resources.Hook(r.Context(), r.PathValue("id"), service.GitHook{Header: r.Header, Body: body})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
