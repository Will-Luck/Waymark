package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONBytes(w, r, status, b)
}

func writeJSONBytes(w http.ResponseWriter, r *http.Request, status int, b []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	writeMaybeGzip(w, r, status, b)
}

func (s *server) apiSite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, s.src.Snapshot())
}

// apiProject serves /api/p/{slug}.json (a 1.22 wildcard must be a whole
// segment, so the suffix is checked here).
func (s *server) apiProject(w http.ResponseWriter, r *http.Request) {
	slug, ok := strings.CutSuffix(r.PathValue("file"), ".json")
	if !ok {
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	p := s.src.Snapshot().ProjectBySlug(slug)
	if p == nil {
		writeJSON(w, r, http.StatusNotFound, map[string]string{"error": "no project called " + slug})
		return
	}
	writeJSON(w, r, http.StatusOK, p)
}

func (s *server) apiSearch(w http.ResponseWriter, r *http.Request) {
	writeJSONBytes(w, r, http.StatusOK, s.derived(s.src.Snapshot()).search)
}

// apiRefresh asks the source for a cycle. The source rate-limits; a request
// another site makes the browser send is refused.
func (s *server) apiRefresh(w http.ResponseWriter, r *http.Request) {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		writeJSON(w, r, http.StatusForbidden, map[string]string{"error": "cross-site request"})
		return
	}
	if err := s.src.Refresh(r.Context()); err != nil {
		writeJSON(w, r, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, r, http.StatusAccepted, map[string]any{"ok": true})
}
