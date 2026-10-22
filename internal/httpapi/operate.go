package httpapi

import (
	"net/http"
	"time"

	"github.com/enclavestack/platform/internal/service"
	"github.com/enclavestack/platform/pkg/api"
)

// Metrics, incidents and notifications (L3-002 §10.4, §10.5).

// metricQuery reads ?metric=…&metric=…&from=…&to=… (RFC 3339).
func metricQuery(r *http.Request) (service.MetricQuery, error) {
	q := service.MetricQuery{Metrics: r.URL.Query()["metric"]}
	for name, dst := range map[string]*time.Time{"from": &q.From, "to": &q.To} {
		v := r.URL.Query().Get(name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return q, api.Validation([]api.FieldError{{Field: name, Code: api.FieldInvalidFormat, Message: "Use an RFC 3339 time like 2026-10-22T09:00:00Z."}})
		}
		*dst = t
	}
	return q, nil
}

func (s *Server) resourceMetrics(w http.ResponseWriter, r *http.Request) {
	q, err := metricQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.ResourceMetrics(r.Context(), r.PathValue("id"), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) machineMetrics(w http.ResponseWriter, r *http.Request) {
	q, err := metricQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.MachineMetrics(r.Context(), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Incidents(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50, 1, 100)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.resources.Notifications(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
