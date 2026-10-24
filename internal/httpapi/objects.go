package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Bucket objects (L2-013, L3-002 §14). Bodies stream; the server's read and write deadlines are lifted
// for these requests so a large transfer isn't cut off (OBJ-06).

func noDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", api.ObjectListMax, 1, api.ObjectListMax)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	out, err := s.resources.ListObjects(r.Context(), r.PathValue("id"), q.Get("prefix"), q.Get("cursor"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// tooLarge records whether a body went past its limit, so the answer can be 413 whatever the object
// store made of the cut-off stream.
type tooLarge struct {
	r    io.Reader
	left int64
	hit  bool
}

func (t *tooLarge) Read(p []byte) (int, error) {
	if t.left == 0 { // all that is allowed was read: the body must end here
		var one [1]byte
		if n, err := t.r.Read(one[:]); n == 0 && err != nil {
			return 0, err
		}
		t.hit = true
		return 0, errors.New("the upload is larger than the limit")
	}
	if int64(len(p)) > t.left {
		p = p[:t.left]
	}
	n, err := t.r.Read(p)
	t.left -= int64(n)
	return n, err
}

func uploadTooLarge() error {
	return api.NewError(api.CodePayloadTooLarge, fmt.Sprintf("Upload at most %s through EnclaveStack; use an S3 client for larger files.", api.ByteSize(api.ObjectUploadMax)))
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request) {
	noDeadlines(w)
	if r.ContentLength > api.ObjectUploadMax {
		s.writeError(w, r, uploadTooLarge())
		return
	}
	tags := map[string]string{}
	for k, v := range r.Header {
		if name, ok := strings.CutPrefix(http.CanonicalHeaderKey(k), api.ObjectTagHeaderPrefix); ok && len(v) > 0 {
			tags[strings.ToLower(name)] = v[0]
		}
	}
	body := &tooLarge{r: r.Body, left: api.ObjectUploadMax}
	out, err := s.resources.PutObject(r.Context(), principal(r.Context()), r.PathValue("id"), r.PathValue("key"), tags, body)
	if body.hit {
		err = uploadTooLarge()
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request) {
	noDeadlines(w)
	info, body, err := s.resources.GetObject(r.Context(), r.PathValue("id"), r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer body.Close()
	h := w.Header()
	h.Set("Content-Type", info.ContentType)
	h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(info.Key)}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Last-Modified", info.Modified.UTC().Format(http.TimeFormat))
	for k, v := range info.Tags {
		h.Set(api.ObjectTagHeaderPrefix+k, v)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead { // the GET route answers HEAD too: headers only
		return
	}
	if _, err := io.Copy(w, body); err != nil && r.Context().Err() == nil {
		s.log.Warn("object download cut short", "resource_id", r.PathValue("id"), "error", err)
	}
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	if err := s.resources.DeleteObject(r.Context(), principal(r.Context()), r.PathValue("id"), r.PathValue("key")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
