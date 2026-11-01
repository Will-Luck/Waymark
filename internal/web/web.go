// Package web serves the pages, the live fragments, the JSON API and the SSE
// stream. It reads only model.Source.Snapshot() and never reaches a source
// directly. Templates and static files are embedded from web/.
package web

import (
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the site's time zone in a scratch container

	"github.com/Will-Luck/waymark/internal/model"
	assets "github.com/Will-Luck/waymark/web"
)

type Options struct {
	// Version is shown in the footer.
	Version string
	// Dev disables immutable caching of static assets.
	Dev bool
	// Now overrides the clock for relative times (tests). Nil means time.Now.
	Now func() time.Time
}

// CSP is the Content-Security-Policy sent with every response.
const CSP = "default-src 'self'; script-src 'self' 'inline-speculation-rules'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'"

type server struct {
	src    model.Source
	opts   Options
	now    func() time.Time
	assets *assetSet
	sprite template.HTML
	icons  map[string]bool
	views  *views

	mu  sync.Mutex
	der *derived
}

// New returns the web handler. It panics if the embedded templates or assets
// are broken, which is a build defect the handler tests catch.
func New(src model.Source, opts Options) http.Handler {
	s, err := newServer(src, opts, assets.FS)
	if err != nil {
		panic("web: " + err.Error())
	}
	return s.routes()
}

func newServer(src model.Source, opts Options, fsys fs.FS) (*server, error) {
	s := &server{src: src, opts: opts, now: opts.Now}
	if s.now == nil {
		s.now = time.Now
	}
	var err error
	if s.assets, err = loadAssets(fsys); err != nil {
		return nil, err
	}
	if s.sprite, s.icons, err = inlineSprite(s.assets.files["icons.svg"]); err != nil {
		return nil, err
	}
	if s.views, err = parseViews(fsys, s.funcs()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /p/{slug}", s.project)
	mux.HandleFunc("GET /p/{slug}/card", s.card)
	mux.HandleFunc("GET /p/{slug}/live", s.live)
	mux.HandleFunc("GET /ideas", s.ideas)
	mux.HandleFunc("GET /ships", s.ships)
	mux.HandleFunc("GET /map", s.mapPage)
	mux.HandleFunc("GET /api/site.json", s.apiSite)
	mux.HandleFunc("GET /api/p/{file}", s.apiProject)
	mux.HandleFunc("GET /api/search.json", s.apiSearch)
	mux.HandleFunc("GET /api/live.json", s.apiLive)
	mux.HandleFunc("POST /api/refresh", s.apiRefresh)
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /static/{hash}/{path...}", s.staticFile)
	mux.HandleFunc("GET /dyn/{hash}/site.css", s.dynCSS)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		// Browsers ask for this on pages without a <link rel=icon> (the JSON ones).
		http.Redirect(w, r, "/static/"+s.assets.hash+"/favicon.svg", http.StatusFound)
	})
	mux.HandleFunc("/", s.notFound)
	return secure(mux)
}

// secure adds the headers every response carries.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// inlineSprite turns static/icons.svg into the hidden block each page carries
// and records which symbol names exist, for the icon fallback.
func inlineSprite(a *asset) (template.HTML, map[string]bool, error) {
	if a == nil {
		return "", nil, errMissing("icons.svg")
	}
	src := string(a.body)
	start := strings.Index(src, "<symbol")
	end := strings.LastIndex(src, "</svg>")
	if start < 0 || end < start {
		return "", nil, errMissing("symbols in icons.svg")
	}
	body := src[start:end]
	names := map[string]bool{}
	for rest := body; ; {
		i := strings.Index(rest, `id="i-`)
		if i < 0 {
			break
		}
		rest = rest[i+6:]
		j := strings.IndexByte(rest, '"')
		names[rest[:j]] = true
		rest = rest[j:]
	}
	if !names[fallbackIcon] {
		return "", nil, errMissing("fallback icon " + fallbackIcon)
	}
	return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" class="sprite" aria-hidden="true" focusable="false">` + body + `</svg>`), names, nil
}

type errMissing string

func (e errMissing) Error() string { return "missing " + string(e) }
