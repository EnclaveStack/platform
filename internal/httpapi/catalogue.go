package httpapi

import (
	"net/http"

	"github.com/enclavestack/platform/pkg/api"
)

// The catalogue (L3-002 §11.5).

func (s *Server) listCatalogue(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.Catalogue(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getCatalogueEntry(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.CatalogueEntry(r.Context(), r.PathValue("name"), r.URL.Query().Get("version"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTemplateSource(w http.ResponseWriter, r *http.Request) {
	out, err := s.resources.CatalogueSource(r.Context(), r.PathValue("name"), r.URL.Query().Get("version"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = w.Write([]byte(out))
}

func (s *Server) addTemplate(w http.ResponseWriter, r *http.Request) {
	var req api.AddTemplate
	if _, err := decode(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.AddTemplate(r.Context(), principal(r.Context()), req)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) removeTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.resources.RemoveTemplate(r.Context(), principal(r.Context()), r.PathValue("name")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
