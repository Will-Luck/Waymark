package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

// heartbeat keeps proxies from closing an idle stream.
const heartbeat = 25 * time.Second

// events streams source changes as SSE: project (slug + hash), activity (an
// Event) and tick (refresh time + source status).
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	ch, cancel := s.src.Subscribe()
	defer cancel()

	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 5000\n: connected\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	ping := time.NewTicker(heartbeat)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		case c, ok := <-ch:
			if !ok {
				return
			}
			name, data := s.sseMessage(c)
			if name == "" {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

type tickSource struct {
	Name        string    `json:"name"`
	Label       string    `json:"label"` // the name on the page (sourceLabel)
	OK          bool      `json:"ok"`
	RefreshedAt time.Time `json:"refreshed_at"`
	Error       string    `json:"error,omitempty"`
	Note        string    `json:"note,omitempty"`
}

type tickData struct {
	RefreshedAt time.Time    `json:"refreshed_at"`
	Sources     []tickSource `json:"sources"`
}

// tick is the header's state: the refresh time (at, or the snapshot's when
// zero) and the status of every source the page shows.
func tick(site *model.Site, at time.Time) tickData {
	if at.IsZero() {
		at = site.GeneratedAt
	}
	t := tickData{RefreshedAt: at, Sources: []tickSource{}}
	for _, x := range shownSources(site) {
		t.Sources = append(t.Sources, tickSource{x.Name, sourceLabel(x.Name, site.Meta), x.OK, x.RefreshedAt, x.Error, x.Note})
	}
	return t
}

// apiLive serves what live.js reads after the stream drops or the page comes
// back from the back/forward cache: the tick plus each project's hash, so
// the page can swap in what changed while it was not listening.
func (s *server) apiLive(w http.ResponseWriter, r *http.Request) {
	site := s.src.Snapshot()
	v := struct {
		tickData
		Hashes map[string]string `json:"hashes"`
	}{tick(site, time.Time{}), map[string]string{}}
	for _, p := range site.Projects {
		v.Hashes[p.Slug] = p.Hash
	}
	writeJSON(w, r, http.StatusOK, v)
}

func (s *server) sseMessage(c model.Change) (string, []byte) {
	var v any
	switch c.Kind {
	case "project":
		if c.Slug == "" {
			return "", nil
		}
		v = map[string]string{"slug": c.Slug, "hash": c.Hash}
	case "activity":
		if c.Event == nil {
			return "", nil
		}
		v = c.Event
	case "tick":
		v = tick(s.src.Snapshot(), c.At)
	default:
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil
	}
	return c.Kind, b
}
