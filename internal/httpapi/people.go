package httpapi

import (
	"net/http"

	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/pkg/api"
)

// Projects, people, invites and the audit list (L3-002 §10.3).

// inviteBase is where invite links point: the console URL the server was given, else the console as
// the caller reached it.
func (s *Server) inviteBase(r *http.Request) string {
	if s.auth.ConsoleURL != nil {
		if base := s.auth.ConsoleURL(r.Context()); base != "" {
			return base
		}
	}
	return consoleBase(r)
}

// consoleBase is the console's URL as the caller reached it.
func consoleBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req api.ChangePassword
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.auth.ChangePassword(r.Context(), principal(r.Context()), req); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getInvite(w http.ResponseWriter, r *http.Request) {
	out, err := s.auth.InviteInfo(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req api.AcceptInvite
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	sess, sid, err := s.auth.AcceptInvite(r.Context(), req, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, sid, int(service.SessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	out, err := s.auth.ListProjects(r.Context(), principal(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req api.CreateProject
	body, err := decode(r, &req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.idempotent(w, r, body, func() (int, any, error) {
		out, err := s.auth.CreateProject(r.Context(), principal(r.Context()), req)
		return http.StatusCreated, out, err
	})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	out, err := s.auth.GetProject(r.Context(), principal(r.Context()), r.PathValue("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateProject
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.auth.UpdateProject(r.Context(), principal(r.Context()), r.PathValue("name"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.DeleteProject(r.Context(), principal(r.Context()), r.PathValue("name")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	out, err := s.auth.ListUsers(r.Context(), principal(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request) {
	var req api.InviteUser
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.auth.InviteUser(r.Context(), principal(r.Context()), req, s.inviteBase(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	out, err := s.auth.GetUser(r.Context(), principal(r.Context()), r.PathValue("userId"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateUser
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.auth.UpdateUser(r.Context(), principal(r.Context()), r.PathValue("userId"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.DeleteUser(r.Context(), principal(r.Context()), r.PathValue("userId")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetUser(w http.ResponseWriter, r *http.Request) {
	if hasBody(r) {
		s.writeError(w, r, api.NewError(api.CodeMalformedRequest, "This request takes no body."))
		return
	}
	out, err := s.auth.ResetUser(r.Context(), principal(r.Context()), r.PathValue("userId"), s.inviteBase(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.auth.Audit(r.Context(), principal(r.Context()), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
