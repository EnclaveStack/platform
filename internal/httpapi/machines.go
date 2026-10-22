package httpapi

import (
	"net/http"
	"strconv"

	"github.com/enclavestack/platform/pkg/api"
)

// Machines (L3-002 §11.2).

func (s *Server) listMachines(w http.ResponseWriter, r *http.Request) {
	out, err := s.cfg.Machines.List(r.Context(), principal(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addMachine(w http.ResponseWriter, r *http.Request) {
	var req api.AddMachine
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.cfg.Machines.Add(r.Context(), principal(r.Context()), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getMachineByName(w http.ResponseWriter, r *http.Request) {
	out, err := s.cfg.Machines.Get(r.Context(), principal(r.Context()), r.PathValue("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateMachine(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateMachine
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.cfg.Machines.Update(r.Context(), principal(r.Context()), r.PathValue("name"), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) removeMachine(w http.ResponseWriter, r *http.Request) {
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	if err := s.cfg.Machines.Remove(r.Context(), principal(r.Context()), r.PathValue("name"), force); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) machineJoinCode(w http.ResponseWriter, r *http.Request) {
	out, err := s.cfg.Machines.NewJoinCode(r.Context(), principal(r.Context()), r.PathValue("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) machineEvents(w http.ResponseWriter, r *http.Request) {
	out, err := s.cfg.Machines.Events(r.Context(), r.PathValue("name"), 100)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) joinMachine(w http.ResponseWriter, r *http.Request) {
	var req api.JoinRequest
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.cfg.Machines.Join(r.Context(), req, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) drainMachine(w http.ResponseWriter, r *http.Request) {
	out, err := s.cfg.Machines.Drain(r.Context(), principal(r.Context()), r.PathValue("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}
