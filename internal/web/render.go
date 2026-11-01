package web

import (
	"bytes"
	"compress/gzip"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

var pageNames = []string{"overview", "project", "ideas", "ships", "map", "notfound"}

type views struct {
	base  *template.Template
	pages map[string]*template.Template
}

// parseViews loads the layout and partials once, then clones them per page so
// each page can define its own "main" block. Fragments run on base.
func parseViews(fsys fs.FS, funcs template.FuncMap) (*views, error) {
	base, err := template.New("base").Funcs(funcs).ParseFS(fsys, "templates/layout.html", "templates/parts/*.html")
	if err != nil {
		return nil, err
	}
	v := &views{pages: map[string]*template.Template{}}
	for _, name := range pageNames {
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(fsys, "templates/"+name+".html"); err != nil {
			return nil, err
		}
		v.pages[name] = t
	}
	v.base = base
	return v, nil
}

// page is what the layout sees. Data carries the page-specific view.
type page struct {
	Title string // the whole <title>
	Brand string // the site's title (config site.title)
	Desc  string
	Nav   string
	Class string
	Site  *model.Site
	// Sources are the site's source statuses less the unconfigured ones;
	// Failing the ones of them that are failing.
	Sources []sourceRow
	Failing []sourceRow
	Static  string
	DynCSS  string
	Sprite  template.HTML
	Theme   string
	Version string
	Data    any
}

// sourceRow is a source status with its name in the page's words.
type sourceRow struct {
	model.SourceStatus
	Label string
}

// newPage starts the page called name ("" for the overview): its title is
// "<name> · <site title>".
func (s *server) newPage(r *http.Request, d *derived, nav, name string) *page {
	brand := siteTitle(d.site)
	p := &page{
		Title:   brand,
		Brand:   brand,
		Nav:     nav,
		Site:    d.site,
		Static:  "/static/" + s.assets.hash,
		DynCSS:  "/dyn/" + d.cssHash + "/site.css",
		Sprite:  s.sprite,
		Version: s.opts.Version,
	}
	if name != "" {
		p.Title = name + " · " + brand
	}
	if p.Version == "" {
		p.Version = "dev"
	}
	if c, err := r.Cookie("theme"); err == nil && (c.Value == "light" || c.Value == "dark") {
		p.Theme = c.Value
	}
	for _, src := range shownSources(d.site) {
		row := sourceRow{SourceStatus: src, Label: sourceLabel(src.Name, d.site.Meta)}
		p.Sources = append(p.Sources, row)
		if !src.OK {
			p.Failing = append(p.Failing, row)
		}
	}
	return p
}

// render executes into a buffer first, so a template error is a clean 500
// rather than half a page.
func (s *server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	t := s.views.pages[name]
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, r, status, buf.Bytes(), "no-cache")
}

func (s *server) fragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	var buf bytes.Buffer
	if err := s.views.base.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeHTML(w, r, http.StatusOK, buf.Bytes(), "no-store")
}

func (s *server) writeHTML(w http.ResponseWriter, r *http.Request, status int, b []byte, cache string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", cache)
	h.Set("Vary", "Accept-Encoding, Cookie")
	writeMaybeGzip(w, r, status, b)
}

func writeMaybeGzip(w http.ResponseWriter, r *http.Request, status int, b []byte) {
	if len(b) > 1400 && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(status)
		if r.Method == http.MethodHead {
			return
		}
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		zw.Write(b)
		zw.Close()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}

// ---- pages -----------------------------------------------------------------

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	p := s.newPage(r, d, "overview", "")
	p.Desc = "Every project, what it is and what state it is in."
	p.Data = d.overview(s.now())
	s.render(w, r, http.StatusOK, "overview", p)
}

func (s *server) project(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	slug := r.PathValue("slug")
	pr := d.proj[slug]
	if pr == nil {
		s.renderNotFound(w, r, d, slug)
		return
	}
	p := s.newPage(r, d, "", pr.Name)
	p.Desc = pr.Summary
	p.Class = "page-project"
	p.Data = d.project(pr)
	s.render(w, r, http.StatusOK, "project", p)
}

func (s *server) card(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	pr := d.proj[r.PathValue("slug")]
	if pr == nil {
		http.NotFound(w, r)
		return
	}
	s.fragment(w, r, "card", pr)
}

func (s *server) live(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	pr := d.proj[r.PathValue("slug")]
	if pr == nil {
		http.NotFound(w, r)
		return
	}
	s.fragment(w, r, "project-live", d.project(pr))
}

func (s *server) ideas(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	if !d.site.Meta.Ideas {
		s.renderNotFound(w, r, d, "")
		return
	}
	p := s.newPage(r, d, "ideas", "Ideas")
	v := d.ideas()
	p.Desc = "Feature ideas from the ideas file, ready to file as issues."
	if !v.FromFile {
		p.Desc = "Feature ideas from labelled issues."
	}
	p.Data = v
	s.render(w, r, http.StatusOK, "ideas", p)
}

func (s *server) ships(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	p := s.newPage(r, d, "ships", "Ship log")
	p.Desc = "Every release across the projects, and a year of commits."
	p.Data = d.ships()
	s.render(w, r, http.StatusOK, "ships", p)
}

func (s *server) mapPage(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	p := s.newPage(r, d, "map", "Map")
	p.Desc = "All projects as a constellation, grouped by category and joined by their relations."
	p.Class = "page-map"
	p.Data = d.graph
	s.render(w, r, http.StatusOK, "map", p)
}

type notFoundView struct {
	Path    string
	Slug    string
	Suggest []*model.Project
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	d := s.derived(s.src.Snapshot())
	s.renderNotFound(w, r, d, "")
}

func (s *server) renderNotFound(w http.ResponseWriter, r *http.Request, d *derived, slug string) {
	p := s.newPage(r, d, "", "Not found")
	p.Data = notFoundView{Path: r.URL.Path, Slug: slug, Suggest: suggest(d.site, slug)}
	s.render(w, r, http.StatusNotFound, "notfound", p)
}

// suggest returns up to three projects whose slug or name is close to q.
func suggest(site *model.Site, q string) []*model.Project {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	type scored struct {
		p *model.Project
		d int
	}
	var all []scored
	for i := range site.Projects {
		p := &site.Projects[i]
		d := min(editDistance(q, p.Slug), editDistance(q, strings.ToLower(p.Name)))
		if strings.Contains(p.Slug, q) || strings.Contains(strings.ToLower(p.Name), q) {
			d = 0
		}
		all = append(all, scored{p, d})
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].d < all[b].d })
	var out []*model.Project
	for _, sc := range all {
		if len(out) == 3 || sc.d > max(3, len(q)/2) {
			break
		}
		out = append(out, sc.p)
	}
	return out
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	site := s.src.Snapshot()
	type src struct {
		Name        string    `json:"name"`
		OK          bool      `json:"ok"`
		RefreshedAt time.Time `json:"refreshed_at"`
		Error       string    `json:"error,omitempty"`
		Note        string    `json:"note,omitempty"`
	}
	out := struct {
		OK          bool      `json:"ok"`
		GeneratedAt time.Time `json:"generated_at"`
		Sources     []src     `json:"sources"`
	}{OK: true, GeneratedAt: site.GeneratedAt, Sources: []src{}}
	for _, x := range site.Sources {
		out.Sources = append(out.Sources, src{x.Name, x.OK, x.RefreshedAt, x.Error, x.Note})
		out.OK = out.OK && x.OK
	}
	writeJSON(w, r, http.StatusOK, out)
}
