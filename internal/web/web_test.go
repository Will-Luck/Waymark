package web

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
	assets "github.com/Will-Luck/waymark/web"
)

// The CSP exactly as the spec writes it; compared literally, not via the const.
const specCSP = "default-src 'self'; script-src 'self' 'inline-speculation-rules'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'"

var fixedNow = time.Date(2026, 10, 31, 23, 0, 30, 0, time.UTC)

func loadSite(t *testing.T, name string) *model.Site {
	t.Helper()
	src, err := model.LoadFixture(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return src.Snapshot()
}

func handler(t *testing.T, name string, dev bool) (http.Handler, *model.StaticSource) {
	t.Helper()
	src := model.NewStaticSource(loadSite(t, name))
	return New(src, Options{Version: "v-test", Dev: dev, Now: func() time.Time { return fixedNow }}), src
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	return do(t, h, http.MethodGet, path, nil)
}

func TestRoutes(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	landmarks := []string{`<html lang="en-GB"`, `<a class="skip" href="#main">`, `<header class="site-head">`, `<nav class="nav" aria-label="Main">`,
		`<main id="main"`, `<footer class="site-foot">`, `<dialog id="palette"`, `type="speculationrules"`, `id="i-circle-dashed"`}
	cases := []struct {
		path   string
		status int
		want   []string
	}{
		{"/", 200, append([]string{`<a href="/" aria-current="page">`, `id="needs-you"`, `data-since`, `id="board"`, `id="cat-services"`,
			`data-live-card="beacon"`, `id="tier-active"`, `id="tier-quiet"`, `<svg class="weeks"`}, landmarks...)},
		{"/p/beacon", 200, append([]string{`data-live-panels="beacon"`, `class="pname">Beacon</h1>`, `id="pn-runs"`, `id="pn-ver"`,
			`id="pn-needs"`, `id="pn-issues"`, `id="pn-ci"`, `id="pn-commits"`, `id="pn-notes"`, `id="pn-rel"`, `id="pn-src"`, `id="pn-ideas"`,
			`class="ubar"`, `badge good`, `v2.4.0</b> is ready`}, landmarks...)},
		{"/p/atlas", 200, append([]string{`data-live-panels="atlas"`, `id="pn-runs"`}, landmarks...)},
		{"/ideas", 200, append([]string{`<a href="/ideas" aria-current="page">`, `data-ideas`, `id="k-quick-win"`, `id="s-S"`,
			`Ideas from the roundup on <b>30 Oct 2026</b> and from issues labelled idea or enhancement. File one as an issue to make it real.`,
			`data-surprise`, `id="idea-project"`}, landmarks...)},
		{"/ships", 200, append([]string{`<a href="/ships" aria-current="page">`, `class="heat"`, `class="rel-line"`, `Current streak`}, landmarks...)},
		{"/map", 200, append([]string{`<a href="/map" aria-current="page">`, `id="map-svg"`, `<script type="application/json" id="graph">`,
			`class="legend wrap"`, `map-list"`}, landmarks...)},
		{"/p/no-such-project", 404, append([]string{`No project called <span class="mono">no-such-project</span>.`, `Search instead?`}, landmarks...)},
		{"/nothing/here", 404, []string{`Nothing lives at`}},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := get(t, h, c.path)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d", rec.Code, c.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("content type %q", ct)
			}
			body := rec.Body.String()
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q", w)
				}
			}
		})
	}
}

func TestJSONRoutes(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	for _, c := range []struct {
		path   string
		status int
	}{
		{"/api/site.json", 200}, {"/api/p/beacon.json", 200}, {"/api/search.json", 200}, {"/healthz", 200},
		{"/api/p/nope.json", 404}, {"/api/p/beacon", 404},
	} {
		rec := get(t, h, c.path)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.status)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control %q", c.path, got)
		}
		if !json.Valid(rec.Body.Bytes()) {
			t.Errorf("%s: invalid JSON", c.path)
		}
	}
	var ix searchIndex
	if err := json.Unmarshal(get(t, h, "/api/search.json").Body.Bytes(), &ix); err != nil {
		t.Fatal(err)
	}
	if len(ix.Projects) != 11 || len(ix.Ideas) != 12 || len(ix.Commands) != 7 || len(ix.Issues) == 0 {
		t.Errorf("search index: %d projects, %d ideas, %d issues, %d commands", len(ix.Projects), len(ix.Ideas), len(ix.Issues), len(ix.Commands))
	}
	var p model.Project
	if err := json.Unmarshal(get(t, h, "/api/p/beacon.json").Body.Bytes(), &p); err != nil || p.Slug != "beacon" {
		t.Errorf("project JSON: %v %q", err, p.Slug)
	}
}

// /api/live.json is what live.js reads after the stream drops or the page
// comes back from the back/forward cache: the tick payload plus each
// project's hash, so missed changes can be swapped in.
func TestLiveState(t *testing.T) {
	h, src := handler(t, "site.json", false)
	rec := get(t, h, "/api/live.json")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("live.json: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got struct {
		RefreshedAt time.Time `json:"refreshed_at"`
		Sources     []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"sources"`
		Hashes map[string]string `json:"hashes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	site := src.Snapshot()
	if !got.RefreshedAt.Equal(site.GeneratedAt) || len(got.Sources) != len(site.Sources) || len(got.Hashes) != len(site.Projects) {
		t.Errorf("live.json: %v, %d sources, %d hashes", got.RefreshedAt, len(got.Sources), len(got.Hashes))
	}
	if p := site.ProjectBySlug("beacon"); p == nil || got.Hashes["beacon"] != p.Hash {
		t.Errorf("beacon hash %q", got.Hashes["beacon"])
	}
}

// A source that is OK but shows less than it might (a forge with no token)
// says why on its row, never as a failure; the live state and /healthz carry
// the same words.
func TestSourceNoteShown(t *testing.T) {
	site := loadSite(t, "site.json")
	const note = "listing is off without a token: owners are not listed"
	found := false
	for i := range site.Sources {
		if site.Sources[i].Name == "github" {
			site.Sources[i].OK, site.Sources[i].Error, site.Sources[i].Note = true, "", note
			found = true
		}
	}
	if !found {
		t.Fatal("the fixture has no github source")
	}
	h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
	home := get(t, h, "/").Body.String()
	if !strings.Contains(home, `<span class="src-note">`+note+`</span>`) {
		t.Error("the overview's source list does not show the note")
	}
	if strings.Contains(home, "GitHub failing") {
		t.Error("a source with a note reads as failing")
	}
	for _, path := range []string{"/api/live.json", "/healthz"} {
		if body := get(t, h, path).Body.String(); !strings.Contains(body, `"note":"`+note+`"`) {
			t.Errorf("%s does not carry the note: %s", path, body)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	for _, path := range []string{"/", "/p/beacon", "/ideas", "/ships", "/map", "/p/nope", "/api/site.json", "/p/beacon/card", "/healthz"} {
		rec := get(t, h, path)
		if got := rec.Header().Get("Content-Security-Policy"); got != specCSP {
			t.Errorf("%s: CSP %q", path, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: nosniff %q", path, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("%s: Referrer-Policy %q", path, got)
		}
	}
}

// With style-src 'self' and no 'unsafe-inline', a style attribute or an inline
// script would silently do nothing in the browser.
func TestNoInlineStyleOrScript(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	scriptRe := regexp.MustCompile(`<script\b[^>]*>`)
	for _, path := range []string{"/", "/p/beacon", "/p/atlas", "/ideas", "/ships", "/map", "/p/nope"} {
		body := get(t, h, path).Body.String()
		if strings.Contains(body, " style=") {
			t.Errorf("%s: inline style attribute", path)
		}
		if strings.Contains(strings.ToLower(body), " on") && regexp.MustCompile(`\son[a-z]+="`).MatchString(body) {
			t.Errorf("%s: inline event handler", path)
		}
		for _, tag := range scriptRe.FindAllString(body, -1) {
			ok := strings.Contains(tag, ` src="/static/`) || strings.Contains(tag, `type="speculationrules"`) || strings.Contains(tag, `type="application/json"`)
			if !ok {
				t.Errorf("%s: inline script %s", path, tag)
			}
		}
	}
}

func TestEveryProjectRenders(t *testing.T) {
	for _, fx := range []string{"site.json", "site.live.json", "site-awkward.json"} {
		h, src := handler(t, fx, false)
		for _, p := range src.Snapshot().Projects {
			for _, path := range []string{"/p/" + p.Slug, "/p/" + p.Slug + "/card", "/p/" + p.Slug + "/live"} {
				if rec := get(t, h, path); rec.Code != 200 {
					t.Errorf("%s %s: %d %s", fx, path, rec.Code, firstLine(rec.Body.String()))
				}
			}
		}
		for _, path := range []string{"/", "/ideas", "/ships", "/map"} {
			if rec := get(t, h, path); rec.Code != 200 {
				t.Errorf("%s %s: %d %s", fx, path, rec.Code, firstLine(rec.Body.String()))
			}
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func TestFragments(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	for path, marker := range map[string]string{"/p/beacon/card": `data-live-card="beacon"`, "/p/beacon/live": `data-live-panels="beacon"`} {
		rec := get(t, h, path)
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		low := strings.ToLower(body)
		if strings.Contains(low, "<html") || strings.Contains(low, "<!doctype") || strings.Contains(low, "<head>") || strings.Contains(low, "<body") {
			t.Errorf("%s: fragment carries a document wrapper", path)
		}
		if !strings.Contains(body, marker) {
			t.Errorf("%s: missing %s", path, marker)
		}
		if strings.Contains(body, `<symbol`) {
			t.Errorf("%s: fragment repeats the sprite", path)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
	}
	for _, path := range []string{"/p/nope/card", "/p/nope/live"} {
		if rec := get(t, h, path); rec.Code != 404 {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
}

var staticRe = regexp.MustCompile(`/static/([0-9a-f]{12})/css/app\.css`)
var dynRe = regexp.MustCompile(`/dyn/([0-9a-f]{12})/site\.css`)

func TestStaticCaching(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	page := get(t, h, "/").Body.String()
	m := staticRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no hashed stylesheet link in the page")
	}
	hash := m[1]
	for path, ctype := range map[string]string{
		"/static/" + hash + "/css/app.css":                                             "text/css; charset=utf-8",
		"/static/" + hash + "/js/app.js":                                               "text/javascript; charset=utf-8",
		"/static/" + hash + "/fonts/atkinson-hyperlegible-next-latin-400-normal.woff2": "font/woff2",
		"/static/" + hash + "/icons.svg":                                               "image/svg+xml",
		"/static/" + hash + "/LICENSES.txt":                                            "text/plain; charset=utf-8",
	} {
		rec := get(t, h, path)
		if rec.Code != 200 {
			t.Errorf("%s: %d", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
		if got := rec.Header().Get("Content-Type"); got != ctype {
			t.Errorf("%s: Content-Type %q, want %q", path, got, ctype)
		}
	}
	if rec := get(t, h, "/favicon.ico"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/static/"+hash+"/favicon.svg" {
		t.Errorf("favicon.ico: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(t, h, "/static/000000000000/css/app.css"); rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("stale hash: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get(t, h, "/static/"+hash+"/css/nope.css"); rec.Code != 404 {
		t.Errorf("missing asset: %d", rec.Code)
	}
	first := get(t, h, "/static/"+hash+"/css/app.css")
	again := do(t, h, http.MethodGet, "/static/"+hash+"/css/app.css", map[string]string{"If-None-Match": first.Header().Get("ETag")})
	if again.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", again.Code)
	}
	gz := do(t, h, http.MethodGet, "/static/"+hash+"/css/app.css", map[string]string{"Accept-Encoding": "gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("no gzip for css")
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if string(plain) != first.Body.String() {
		t.Errorf("gzip body differs")
	}

	dev, _ := handler(t, "site.json", true)
	if got := get(t, dev, "/static/"+hash+"/css/app.css").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("dev Cache-Control %q", got)
	}
}

func TestDynCSS(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	m := dynRe.FindStringSubmatch(get(t, h, "/").Body.String())
	if m == nil {
		t.Fatal("no dyn stylesheet link")
	}
	rec := get(t, h, m[0])
	body := rec.Body.String()
	for _, w := range []string{`[data-cat="mobile"]{--hue:300}`, `body:has(#cat-services:checked) .card-wrap:not([data-cat="services"]){display:none}`,
		`view-transition-name:p-beacon`, `view-transition-name:t-beacon`, `[data-pct="43"]{--pv:43}`} {
		if !strings.Contains(body, w) {
			t.Errorf("dyn css missing %q", w)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("dyn Cache-Control %q", got)
	}
}

func TestEventsStream(t *testing.T) {
	h, src := handler(t, "site.json", false)
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for k, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	rd := bufio.NewReader(resp.Body)
	readUntil := func(prefix string) string {
		t.Helper()
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("waiting for %q: %v", prefix, err)
			}
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(line)
			}
		}
	}
	readUntil(": connected") // the subscription exists once this arrives

	src.Publish(model.Change{Kind: "tick", At: fixedNow})
	readUntil("event: tick")
	var tick struct {
		RefreshedAt time.Time `json:"refreshed_at"`
		Sources     []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(readUntil("data: "), "data: ")), &tick); err != nil {
		t.Fatal(err)
	}
	if !tick.RefreshedAt.Equal(fixedNow) || len(tick.Sources) != 9 {
		t.Errorf("tick %+v", tick)
	}

	src.Publish(model.Change{Kind: "project", Slug: "beacon", Hash: "abc"})
	readUntil("event: project")
	if got := readUntil("data: "); got != `data: {"hash":"abc","slug":"beacon"}` {
		t.Errorf("project data %s", got)
	}
	ev := model.Event{ID: "x", Project: "beacon", ProjectName: "Beacon", Kind: "release", Text: "v2.5.0 tagged", At: fixedNow}
	src.Publish(model.Change{Kind: "activity", Event: &ev})
	readUntil("event: activity")
	var got model.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(readUntil("data: "), "data: ")), &got); err != nil || got.Text != ev.Text {
		t.Errorf("activity %+v %v", got, err)
	}
}

func TestRefresh(t *testing.T) {
	h, src := handler(t, "site.json", false)
	ch, cancel := src.Subscribe()
	defer cancel()
	if rec := do(t, h, http.MethodPost, "/api/refresh", nil); rec.Code != http.StatusAccepted {
		t.Errorf("refresh: %d", rec.Code)
	}
	select {
	case c := <-ch:
		if c.Kind != "tick" {
			t.Errorf("change %q", c.Kind)
		}
	case <-time.After(time.Second):
		t.Error("refresh did not reach the source")
	}
	if rec := do(t, h, http.MethodPost, "/api/refresh", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site refresh: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/refresh", map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusAccepted {
		t.Errorf("same-origin refresh: %d", rec.Code)
	}
}

func TestEscaping(t *testing.T) {
	h, _ := handler(t, "site-awkward.json", false)
	for _, path := range []string{"/", "/p/odd-repo", "/p/odd-repo/card", "/map"} {
		body := get(t, h, path).Body.String()
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<b>bold</b>") {
			t.Errorf("%s: raw markup from data", path)
		}
	}
	card := get(t, h, "/p/odd-repo/card").Body.String()
	if !strings.Contains(card, "&lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt; &amp; &#34;quotes&#34;") {
		t.Errorf("summary not escaped as text: %s", card)
	}
	broken := get(t, h, "/p/broken-thing").Body.String()
	if strings.Contains(broken, "<img src=x") || !strings.Contains(broken, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Error("issue title not escaped")
	}
	// mdlite: bold and code render, raw tags in changelog items stay text.
	for _, w := range []string{"<strong>Fixed</strong>", "<code>config.go</code>", `<a href="https://example.com/docs" rel="noopener noreferrer">docs</a>`, "&lt;b&gt;tags&lt;/b&gt;"} {
		if !strings.Contains(broken, w) {
			t.Errorf("markdown-lite output missing %q", w)
		}
	}
	// A bad label colour falls back instead of reaching the attribute.
	if strings.Contains(broken, "zzzzzz") {
		t.Error("unvalidated label colour reached the page")
	}
}

func TestAwkwardStates(t *testing.T) {
	h, _ := handler(t, "site-awkward.json", false)
	home := get(t, h, "/").Body.String()
	for _, w := range []string{`data-status="warn"`, `Gitea failing`, `gitea /user/repos: HTTP 502 Bad Gateway (showing the last good data)`, `<span class="tag">unsorted</span>`,
		`class="need lv-alert"`, `>Fix</span>`, `>Confirm</span>`, `>Review</span>`} {
		if !strings.Contains(home, w) {
			t.Errorf("overview missing %q", w)
		}
	}
	odd := get(t, h, "/p/odd-repo").Body.String()
	if !strings.Contains(odd, `href="#i-circle-dashed"`) || strings.Contains(odd, "#i-no-such-icon") {
		t.Error("unknown icon did not fall back to circle-dashed")
	}
	if strings.Contains(odd, `id="pn-runs"`) || strings.Contains(odd, `id="pn-ideas"`) || strings.Contains(odd, `id="pn-ver"`) {
		t.Error("empty panels rendered for a project with no data")
	}
	if !strings.Contains(odd, "No commits seen") && !strings.Contains(get(t, h, "/p/odd-repo/card").Body.String(), "No commits seen") {
		t.Error("zero last activity not worded")
	}
	broken := get(t, h, "/p/broken-thing").Body.String()
	for _, w := range []string{`st-alert`, `<b>Stopped</b>`, `class="probe down"`, `HTTP 502`, `class="u down"`, `Parked since 11 Sep 2026.`,
		`12 commits since then.`, `<h3 class="sub"><svg class="i" aria-hidden="true" focusable="false"><use href="#i-github"></use></svg>GitHub</h3>`,
		`id="pn-prs"`, `ci-failed`, `ci-running`, `data-pct="58"`, `7/12 done, 58%`, `9 unreleased changes`, `Possibly stale.`, `depends on`, `monitored by`} {
		if !strings.Contains(broken, w) {
			t.Errorf("broken-thing missing %q", w)
		}
	}
	if strings.Contains(broken, `href="/p/gone-project"`) {
		t.Error("relation to an unknown project became a link")
	}
	if rec := get(t, h, "/map"); !strings.Contains(rec.Body.String(), `href="#i-circle-dashed"`) {
		t.Error("map node did not fall back to circle-dashed")
	}
}

// Spec: tier colours each come with an icon and a word, never colour alone.
// The project icon fills the star, so each star also carries its tier glyph.
func TestMapNodesShowTierGlyph(t *testing.T) {
	h, src := handler(t, "site.json", false)
	body := get(t, h, "/map").Body.String()
	nodeRe := regexp.MustCompile(`<a class="node (t-[a-z]+)" href="/p/([^"]+)"[^>]*>(.*?)</a>`)
	nodes := nodeRe.FindAllStringSubmatch(body, -1)
	if len(nodes) != len(src.Snapshot().Graph.Nodes) {
		t.Fatalf("%d nodes on the map, want %d", len(nodes), len(src.Snapshot().Graph.Nodes))
	}
	glyph := map[string]string{"t-active": "zap", "t-live": "radio", "t-quiet": "moon", "t-dormant": "hourglass", "t-hold": "pause", "t-archived": "archive"}
	for _, n := range nodes {
		want := `<g class="node-tier"`
		if !strings.Contains(n[3], want) || !strings.Contains(n[3], `href="#i-`+glyph[n[1]]+`"`) {
			t.Errorf("%s (%s): no %s tier glyph", n[2], n[1], glyph[n[1]])
		}
	}
	if strings.Contains(body, "colour and icon show its tier") {
		t.Error("map copy still says the icon shows the tier")
	}
}

// .meta is a flex row with a gap, so loose text runs and <time> each become
// a flex item and the gap lands before the comma ("opened 4 days ago , 1
// comment"). The text after the icon must be one item.
func TestIssueMetaIsOneFlexItem(t *testing.T) {
	h, _ := handler(t, "site-awkward.json", false)
	body := get(t, h, "/p/broken-thing").Body.String()
	const head = `<span class="meta"><svg class="i" aria-hidden="true" focusable="false"><use href="#i-user"></use></svg>`
	metas := strings.Split(body, head)[1:]
	if len(metas) == 0 {
		t.Fatal("no issue meta rows on broken-thing")
	}
	comments := 0
	for _, m := range metas {
		end := strings.Index(m, "</span></span>")
		if !strings.HasPrefix(m, "<span>") || end < 0 {
			t.Fatalf("meta text is not wrapped in one span: %.120s", m)
		}
		if strings.Contains(m[:end], " comment") {
			comments++
		}
	}
	if comments == 0 {
		t.Error("no meta row with a comment count, so the comma case is untested")
	}
}

// Under a category filter each tier header must count the cards it shows:
// every tier carries one count per category, and the dyn CSS shows the one
// matching the checked chip.
func TestTierCountsPerCategory(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	body := get(t, h, "/").Body.String()
	cardRe := regexp.MustCompile(`<article class="card-wrap [^"]*" data-live-card="[^"]*" data-slug="[^"]*" data-cat="([^"]*)"`)
	blocks := strings.Split(body, `<details class="tier `)[1:]
	if len(blocks) == 0 {
		t.Fatal("no tier sections")
	}
	for _, b := range blocks {
		b, _, _ = strings.Cut(b, "</details>")
		summary, cards, _ := strings.Cut(b, "</summary>")
		per := map[string]int{}
		all := 0
		for _, m := range cardRe.FindAllStringSubmatch(cards, -1) {
			per[m[1]]++
			all++
		}
		id := firstLine(summary)
		if !strings.Contains(summary, fmt.Sprintf(`<span class="count" data-n="all">%d</span>`, all)) {
			t.Errorf("%s: no all-count of %d", id, all)
		}
		for cat, n := range per {
			if !strings.Contains(summary, fmt.Sprintf(`<span class="count" data-n="%s">%d</span>`, cat, n)) {
				t.Errorf("%s: no %s count of %d", id, cat, n)
			}
		}
	}
	m := dynRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no dyn stylesheet link")
	}
	css := get(t, h, m[0]).Body.String()
	if w := `body:has(#cat-services:checked) .tier .count[data-n="services"]{display:inline-block}`; !strings.Contains(css, w) {
		t.Errorf("dyn css missing %q", w)
	}
}

// A kind and size pair with no ideas says so (CSS only, so without JS too),
// and each chip's count narrows to the other filter's choice.
func TestIdeasEmptyState(t *testing.T) {
	h, src := handler(t, "site.json", false)
	body := get(t, h, "/ideas").Body.String()
	if !strings.Contains(body, `<p class="ideas-empty" data-ideas-empty>`) || !strings.Contains(body, "No ideas match these filters.") {
		t.Fatal("no empty-state message on /ideas")
	}
	n := map[[2]string]int{}
	for _, i := range src.Snapshot().Ideas {
		n[[2]string{i.Kind, i.Size}]++
		n[[2]string{i.Kind, "all"}]++
		n[[2]string{"all", i.Size}]++
	}
	m := dynRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no dyn stylesheet link")
	}
	css := get(t, h, m[0]).Body.String()
	zero := 0
	for _, k := range []string{"all", "quick-win", "bold", "connective", "hardening"} {
		for _, s := range []string{"all", "S", "M", "L"} {
			if k == "all" && s == "all" {
				continue
			}
			rule := fmt.Sprintf(`body:has(#k-%s:checked):has(#s-%s:checked) .ideas-empty{display:flex}`, k, s)
			if got := strings.Contains(css, rule); got != (n[[2]string{k, s}] == 0) {
				t.Errorf("k-%s + s-%s: %d ideas, empty rule present %v", k, s, n[[2]string{k, s}], got)
			}
			if n[[2]string{k, s}] == 0 {
				zero++
			}
		}
	}
	if zero == 0 {
		t.Error("the fixture has no empty pair, so the rule is untested")
	}
	sizeL, _, _ := strings.Cut(strings.SplitN(body, `<label for="s-L">`, 2)[1], "</label>")
	if w := fmt.Sprintf(`<span class="n" data-n="k-quick-win">%d</span>`, n[[2]string{"quick-win", "L"}]); !strings.Contains(sizeL, w) {
		t.Errorf("Large chip has no quick-win count %s: %s", w, sizeL)
	}
	kindQW, _, _ := strings.Cut(strings.SplitN(body, `<label for="k-quick-win" class="k-quick-win">`, 2)[1], "</label>")
	if w := fmt.Sprintf(`<span class="n" data-n="s-L">%d</span>`, n[[2]string{"quick-win", "L"}]); !strings.Contains(kindQW, w) {
		t.Errorf("Quick win chip has no Large count %s: %s", w, kindQW)
	}
}

// live.js keeps open disclosures open across a swap by the nearest id inside
// the panels, so every note needs its own stable id.
func TestNotesHaveStableIDs(t *testing.T) {
	h, src := handler(t, "site.json", false)
	var p *model.Project
	for i := range src.Snapshot().Projects {
		if len(src.Snapshot().Projects[i].Notes) > 1 {
			p = &src.Snapshot().Projects[i]
			break
		}
	}
	if p == nil {
		t.Fatal("no project with several notes in the fixture")
	}
	body := get(t, h, "/p/"+p.Slug+"/live").Body.String()
	ids := regexp.MustCompile(`<div class="note" id="(note-[a-z0-9-]+)">`).FindAllStringSubmatch(body, -1)
	if len(ids) != len(p.Notes) {
		t.Fatalf("%s: %d note ids for %d notes", p.Slug, len(ids), len(p.Notes))
	}
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("duplicate note id %s", m[1])
		}
		seen[m[1]] = true
	}
}

// An idea that is an open issue links that issue itself, which need not be in
// the project's primary repo.
func TestIssueIdeaLinksItsIssue(t *testing.T) {
	site := loadSite(t, "site.json")
	var p *model.Project
	for i := range site.Projects {
		if site.Projects[i].Repo != nil {
			p = &site.Projects[i]
			break
		}
	}
	if p == nil {
		t.Fatal("no project with a repo in the fixture")
	}
	issue := "https://git.example.com/acme/tool-docs/issues/4"
	site.Ideas = append(site.Ideas, model.Idea{ID: "gitea:acme/tool-docs#4", Project: p.Slug, ProjectName: p.Name, Title: "Dark mode",
		Size: "S", Kind: "quick-win", ExistingIssue: 4, Source: "issue", URL: issue})
	h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
	body := get(t, h, "/ideas").Body.String()
	parts := strings.SplitN(body, `id="idea-gitea-acme-tool-docs-4"`, 2)
	if len(parts) != 2 {
		t.Fatal("no card for the issue idea on /ideas")
	}
	card, _, _ := strings.Cut(parts[1], "</article>")
	if !strings.Contains(card, `href="`+issue+`"`) || !strings.Contains(card, "Issue #4") {
		t.Errorf("issue idea card does not link its issue:\n%s", card)
	}
}

// A hero tile links only where the page lists what it counts; the board
// lists no issues, so "Open issues" is a plain stat.
func TestOpenIssuesTileIsNotALink(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	body := get(t, h, "/").Body.String()
	m := regexp.MustCompile(`<(a|span) class="stat[^"]*"[^>]*>\s*<span class="stat-label"><svg[^>]*><use href="#i-circle-dot"></use></svg>Open issues</span>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no Open issues tile")
	}
	if m[1] != "span" {
		t.Errorf("Open issues tile is a link: %s", m[0])
	}
}

// A Needs-you card is one <a>; action text that carries a markdown link must
// not put an <a> inside it (the parser would close the card early).
func TestNeedCardHasNoNestedLink(t *testing.T) {
	site := loadSite(t, "site.json")
	if len(site.NeedsYou) == 0 {
		t.Fatal("fixture has no needs")
	}
	const extra = " See [the docs](https://example.com/docs)."
	site.NeedsYou[0].Text += extra
	slug := site.NeedsYou[0].Project
	p := site.ProjectBySlug(slug)
	if p == nil || len(p.Actions) == 0 {
		t.Fatalf("project %q has no actions", slug)
	}
	p.Actions[0].Text += extra
	h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
	for _, path := range []string{"/", "/p/" + slug} {
		body := get(t, h, path).Body.String()
		cards := strings.Split(body, `<a class="need `)[1:]
		found := false
		for _, c := range cards {
			c, _, _ = strings.Cut(c, "</a>")
			if strings.Contains(c, "<a ") {
				t.Errorf("%s: link inside a need card: %.200s", path, c)
			}
			if strings.Contains(c, "See the docs.") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the link text did not stay as text in its card", path)
		}
	}
}

func TestMdTextDropsOnlyItsOwnLinks(t *testing.T) {
	in := "**Fix** `<a href=x>` <a href=\"y\">raw</a> and [the docs](https://example.com/d)"
	want := "<strong>Fix</strong> <code>&lt;a href=x&gt;</code> &lt;a href=&#34;y&#34;&gt;raw&lt;/a&gt; and the docs"
	if got := string(mdText(in)); got != want {
		t.Errorf("mdText:\n got %s\nwant %s", got, want)
	}
}

func TestNotFoundSuggests(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	body := get(t, h, "/p/beacn").Body.String()
	if !strings.Contains(body, "Did you mean") || !strings.Contains(body, `href="/p/beacon"`) {
		t.Error("no suggestion for a near-miss slug")
	}
}

func TestGzipPages(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	rec := do(t, h, http.MethodGet, "/ideas", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("page not compressed")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if !strings.Contains(string(b), `data-ideas`) {
		t.Error("compressed body is not the page")
	}
}

func TestThemeCookie(t *testing.T) {
	h, _ := handler(t, "site.json", false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "light"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `<html lang="en-GB" data-theme="light">`) {
		t.Error("theme cookie not applied")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "sepia"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "data-theme=") {
		t.Error("unknown theme value reached the page")
	}
}

// UK copy rule: no em or en dashes in anything we wrote.
func TestNoDashesInCopy(t *testing.T) {
	err := fs.WalkDir(assets.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".html") && !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".css") {
			return nil
		}
		b, err := fs.ReadFile(assets.FS, p)
		if err != nil {
			return err
		}
		if strings.ContainsAny(string(b), "–—") {
			t.Errorf("%s contains an en or em dash", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpriteHasEveryFixtureIcon(t *testing.T) {
	s, err := newServer(model.NewStaticSource(&model.Site{}), Options{}, assets.FS)
	if err != nil {
		t.Fatal(err)
	}
	site := loadSite(t, "site.json")
	var missing []string
	check := func(n string) {
		if n != "" && !s.icons[n] {
			missing = append(missing, n)
		}
	}
	for _, c := range site.Categories {
		check(c.Icon)
	}
	for _, p := range site.Projects {
		check(p.Icon)
		for _, sg := range p.Signals {
			check(sg.Icon)
		}
	}
	for _, n := range []string{"zap", "radio", "moon", "hourglass", "pause", "snowflake", "archive", "check", "info", "triangle-alert",
		"octagon-alert", "rocket", "link", "shield", "github", "folder-git-2", "circle-arrow-up"} {
		check(n)
	}
	if len(missing) > 0 {
		t.Errorf("sprite lacks %v", missing)
	}
}

func serve(site *model.Site) http.Handler {
	return New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
}

func TestTitleFromMeta(t *testing.T) {
	site := acmeSite()
	site.Meta.Title, site.Meta.Ideas = "Acme status", true
	site.Ideas = []model.Idea{{ID: "gh:acme/app#3", Project: "app", ProjectName: "App", Icon: "box", Title: "Dark mode", ExistingIssue: 3,
		Source: "issue", URL: "https://github.com/acme/app/issues/3"}}
	h := serve(site)
	for path, title := range map[string]string{
		"/": "<title>Acme status</title>", "/p/app": "<title>App · Acme status</title>", "/ideas": "<title>Ideas · Acme status</title>",
		"/ships": "<title>Ship log · Acme status</title>", "/map": "<title>Map · Acme status</title>", "/p/nope": "<title>Not found · Acme status</title>",
	} {
		body := get(t, h, path).Body.String()
		for _, w := range []string{title, `aria-label="Acme status, overview"`, `<span class="brand-word">Acme status</span>`, `<p class="foot-brand"><b>Acme status</b>`} {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", path, w)
			}
		}
		if strings.Contains(body, "Roadmap") || strings.Contains(body, "Waymark") {
			t.Errorf("%s: names a site the data does not", path)
		}
	}
	site.Meta.Title = ""
	if body := get(t, serve(site), "/").Body.String(); !strings.Contains(body, "<title>Waymark</title>") {
		t.Error("an empty title does not fall back to Waymark")
	}
}

// liveLabels are /api/live.json's source labels by name.
func liveLabels(t *testing.T, h http.Handler) map[string]string {
	t.Helper()
	var v struct {
		Sources []struct {
			Name  string `json:"name"`
			Label string `json:"label"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(get(t, h, "/api/live.json").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range v.Sources {
		out[s.Name] = s.Label
	}
	return out
}

func srcPanel(body string) string {
	return regexp.MustCompile(`(?s)<section class="panel" aria-labelledby="pn-src">.*?</section>`).FindString(body)
}

func TestNotesLabelFromMeta(t *testing.T) {
	site := acmeSite()
	site.Meta.Notes, site.Meta.NotesLabel = true, "Field notes"
	site.Sources = append(site.Sources, model.SourceStatus{Name: "notes", OK: true, RefreshedAt: fixedNow})
	p := &site.Projects[0]
	p.Notes = []model.NoteInfo{{File: "app.md", Name: "app", Modified: fixedNow, Body: "Ship it"}}
	p.Sources = append(p.Sources, model.SourceStamp{Name: "notes", At: fixedNow})
	h := serve(site)
	body := get(t, h, "/p/app").Body.String()
	heading := regexp.MustCompile(`<h2 id="pn-notes" class="panel-h">.*?</h2>`).FindString(body)
	if !strings.HasSuffix(heading, "</svg>Field notes</h2>") {
		t.Errorf("notes panel heading %q", heading)
	}
	if !strings.Contains(body, `<b>Field notes</b> refreshed`) {
		t.Error("the header's source row does not use the notes label")
	}
	if !strings.Contains(srcPanel(body), "<b>Field notes</b>") {
		t.Errorf("sources panel: %s", srcPanel(body))
	}
	if got := liveLabels(t, h)["notes"]; got != "Field notes" {
		t.Errorf("live.json notes label %q", got)
	}
	// No other notes label anywhere: every text that names notes is the
	// configured label, and with none set it is "Notes".
	for label, want := range map[string]string{"Field notes": "Field notes", "": "Notes"} {
		site.Meta.NotesLabel = label
		h := serve(site)
		for _, path := range []string{"/", "/p/app", "/p/app/live", "/ships", "/map"} {
			names := notesTexts(get(t, h, path).Body.String())
			if len(names) == 0 {
				t.Errorf("label %q: %s names no notes at all", label, path)
			}
			for _, n := range names {
				if n != want {
					t.Errorf("label %q: %s says %q", label, path, n)
				}
			}
		}
	}
	// Set here, not left over from the loop: map order is random.
	site.Meta.NotesLabel = ""
	if body := get(t, serve(site), "/p/app").Body.String(); !strings.Contains(body, "</svg>Notes</h2>") {
		t.Error("an empty notes label does not fall back to Notes")
	}
}

// notesTexts are the page's text runs that contain the word "notes".
func notesTexts(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?i)>([^<>]*\bnotes\b[^<>]*)<`).FindAllStringSubmatch(body, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// Review focus 4: a source that is not configured hides its panels, its nav
// link and its status rows instead of showing them empty or failing.
func TestOptionalSourcesHide(t *testing.T) {
	site := acmeSite()
	for _, n := range []string{"notes", "docker", "fdroid", "ideas"} {
		site.Sources = append(site.Sources, model.SourceStatus{Name: n, OK: false, Error: "first read in progress"})
	}
	running := func(name string) *model.DeployInfo {
		return &model.DeployInfo{Source: "docker", Where: "Docker", Container: name, State: "running", Version: "v1.0.0", CheckedAt: fixedNow}
	}
	app := &site.Projects[0]
	app.Deploy = running("app")
	app.Notes = []model.NoteInfo{{File: "app.md", Name: "app", Modified: fixedNow, Body: "Ship it"}}
	app.Sources = append(app.Sources, model.SourceStamp{Name: "docker", At: fixedNow}, model.SourceStamp{Name: "notes", At: fixedNow})
	site.Ideas = []model.Idea{{ID: "app-01", Project: "app", ProjectName: "App", Icon: "box", Title: "Export", Size: "S", Kind: "quick-win", Source: "file"}}
	app.Ideas = site.Ideas
	site.Events = []model.Event{{ID: "app:note:x", At: fixedNow, Project: "app", ProjectName: "App", Icon: "box", Kind: "note",
		Level: model.LevelInfo, Text: "Note app.md was updated"}}
	site.Projects = append(site.Projects, model.Project{Slug: "cli", Name: "Cli", Category: "tools", Icon: "box", Tier: model.TierLive, Forge: "GitHub",
		Hash: "h2", Deploy: running("cli"), Probes: []model.ProbeInfo{{Label: "Live", URL: "https://cli.example.com", Up: true, CheckedAt: fixedNow}}})

	// Configured, everything shows: the checks below can see each thing.
	on := *site
	on.Meta.Notes, on.Meta.Docker, on.Meta.Ideas, on.Meta.FDroid = true, true, true, true
	h := serve(&on)
	if home := get(t, h, "/").Body.String(); !strings.Contains(home, "app.md was updated") || !strings.Contains(home, `href="/ideas"`) || !strings.Contains(home, `<li class="bad">`) {
		t.Fatal("control: with every source configured the overview lacks the note event, the Ideas link or the failing rows")
	}
	if a := get(t, h, "/p/app").Body.String(); !strings.Contains(a, `id="pn-notes"`) || !strings.Contains(a, `class="deploy"`) || !strings.Contains(a, `id="pn-ideas"`) {
		t.Fatal("control: with every source configured /p/app lacks notes, the deploy or ideas")
	}

	h = serve(site)
	home := get(t, h, "/").Body.String()
	for _, w := range []string{`href="/ideas"`, `<li class="bad">`, "app.md was updated", "<b>Docker</b>", "<b>F-Droid</b>", "<b>Ideas</b>", "<b>Notes</b>"} {
		if strings.Contains(home, w) {
			t.Errorf("overview shows %q", w)
		}
	}
	if !strings.Contains(home, `data-status="ok"`) || !strings.Contains(home, `data-status-word>Live</span>`) {
		t.Error("the header is not green with only unconfigured sources failing")
	}
	a := get(t, h, "/p/app").Body.String()
	for _, w := range []string{`id="pn-notes"`, `id="pn-runs"`, `id="pn-ideas"`, `href="/ideas"`} {
		if strings.Contains(a, w) {
			t.Errorf("/p/app shows %q", w)
		}
	}
	if s := srcPanel(a); s == "" || strings.Contains(s, "<b>Docker</b>") || strings.Contains(s, "<b>Notes</b>") {
		t.Errorf("/p/app sources panel names an unconfigured source: %s", s)
	}
	cli := get(t, h, "/p/cli").Body.String()
	if !strings.Contains(cli, `id="pn-runs"`) || !strings.Contains(cli, `class="probe `) || strings.Contains(cli, `class="deploy"`) {
		t.Error("a project with a probe and a container: want Where it runs with the probe only")
	}
	if rec := get(t, h, "/ideas"); rec.Code != http.StatusNotFound {
		t.Errorf("/ideas: %d, want 404", rec.Code)
	}
	for n := range liveLabels(t, h) {
		if n == "notes" || n == "docker" || n == "fdroid" || n == "ideas" {
			t.Errorf("live.json lists %s", n)
		}
	}
	var ix searchIndex
	if err := json.Unmarshal(get(t, h, "/api/search.json").Body.Bytes(), &ix); err != nil {
		t.Fatal(err)
	}
	if len(ix.Ideas) != 0 {
		t.Errorf("search has %d ideas", len(ix.Ideas))
	}
	for _, c := range ix.Commands {
		if c.ID == "go-ideas" {
			t.Error("search offers the Ideas page")
		}
	}
}

// Source rows and stamps carry each forge's name on the page, not its id:
// in the header, the project's sources and the live state.
func TestForgeSourceLabels(t *testing.T) {
	site := acmeSite()
	site.Sources[1].OK, site.Sources[1].Error = false, "boom"
	site.Projects[0].Sources = []model.SourceStamp{{Name: "gh", At: fixedNow}, {Name: "cb", At: fixedNow}, {Name: "cb files", At: fixedNow}}
	h := serve(site)
	body := get(t, h, "/p/app").Body.String()
	for _, w := range []string{`<b>GitHub</b> refreshed`, `<b>Codeberg</b> failing`, `data-status-word>Codeberg failing</span>`} {
		if !strings.Contains(body, w) {
			t.Errorf("header missing %q", w)
		}
	}
	if s := srcPanel(body); !strings.Contains(s, "<b>GitHub</b>") || !strings.Contains(s, "<b>Codeberg</b>") || !strings.Contains(s, "<b>Codeberg files</b>") {
		t.Errorf("sources panel: %s", s)
	}
	for _, w := range []string{"<b>Gh", "<b>Cb", "Cb failing"} {
		if strings.Contains(body, w) {
			t.Errorf("a forge is named by its id: %q", w)
		}
	}
	if got := liveLabels(t, h); got["gh"] != "GitHub" || got["cb"] != "Codeberg" {
		t.Errorf("live.json labels %v", got)
	}

	// Two forges of one kind are told apart.
	site.Meta.Forges = map[string]string{"work": "Gitea (work)", "home": "Gitea (home)"}
	site.Sources = []model.SourceStatus{{Name: "work", OK: true, RefreshedAt: fixedNow}, {Name: "home", OK: true, RefreshedAt: fixedNow}}
	h = serve(site)
	if home := get(t, h, "/").Body.String(); !strings.Contains(home, "<b>Gitea (work)</b>") || !strings.Contains(home, "<b>Gitea (home)</b>") {
		t.Error("two Gitea forges are not told apart")
	}
	if got := liveLabels(t, h); got["work"] != "Gitea (work)" || got["home"] != "Gitea (home)" {
		t.Errorf("live.json labels %v", got)
	}
}

// A homepage the server would not probe (a local or private address) says
// so, with an icon and words; it is never shown as down.
func TestNotProbedHomepage(t *testing.T) {
	site := acmeSite()
	site.Projects[0].Probes = []model.ProbeInfo{{Label: "Homepage", URL: "http://app.example.com", ProbeURL: "http://app.example.com", Homepage: true,
		NotProbed: "local or private address"}}
	body := get(t, serve(site), "/p/app").Body.String()
	row := regexp.MustCompile(`(?s)<div class="probe skipped">.*?</div>`).FindString(body)
	for _, w := range []string{`<use href="#i-circle-slash"></use></svg>Not probed</span>`, `http://app.example.com</a>, local or private address`} {
		if !strings.Contains(row, w) {
			t.Errorf("not-probed row missing %q: %s", w, row)
		}
	}
	if strings.Contains(body, `class="probe down"`) || strings.Contains(body, "Down</span>") || strings.Contains(body, `class="ubar"`) {
		t.Error("a homepage that was not probed shows as down")
	}
}

// acmeSite is one project, acme/app, with a GitHub primary (forge id gh) and a
// Codeberg mirror (forge id cb), and nothing else configured.
func acmeSite() *model.Site {
	p := model.Project{
		Slug: "app", Name: "App", Category: "tools", Icon: "box", Tier: model.TierActive, Forge: "GitHub", Hash: "h1",
		Repo:   &model.RepoInfo{FullName: "acme/app", HTMLURL: "https://github.com/acme/app", OpenIssues: 5},
		GitHub: &model.GitHubInfo{FullName: "acme/app", HTMLURL: "https://codeberg.org/acme/app", OpenIssues: 1},
		Issues: []model.IssueInfo{
			{Number: 1, Title: "Primary bug", URL: "https://github.com/acme/app/issues/1", Source: "gh", Forge: "GitHub"},
			{Number: 2, Title: "Mirror bug", URL: "https://codeberg.org/acme/app/issues/2", Source: "cb", Mirror: true, Forge: "Codeberg"},
		},
		Links:   []model.Link{{Label: "GitHub", URL: "https://github.com/acme/app", Kind: "repo"}, {Label: "Codeberg", URL: "https://codeberg.org/acme/app", Kind: "mirror"}},
		Sources: []model.SourceStamp{{Name: "gh", At: fixedNow}, {Name: "gh files", At: fixedNow}, {Name: "cb", At: fixedNow}},
	}
	return &model.Site{
		GeneratedAt: fixedNow, Projects: []model.Project{p},
		Sources: []model.SourceStatus{{Name: "gh", OK: true, RefreshedAt: fixedNow}, {Name: "cb", OK: true, RefreshedAt: fixedNow}},
		Meta:    model.Meta{Title: "Acme", Forges: map[string]string{"gh": "GitHub", "cb": "Codeberg"}},
	}
}

// Issues name the forge they came from; nothing says Gitea on a site with no
// Gitea forge.
func TestIssuesNameTheirForge(t *testing.T) {
	h := serve(acmeSite())
	body := get(t, h, "/p/app").Body.String()
	for _, w := range []string{`All 5 open issues on GitHub</a>`, `<use href="#i-github"></use></svg>Codeberg</h3>`, `Mirror bug`, `Primary bug`} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q", w)
		}
	}
	for _, path := range []string{"/", "/p/app", "/p/app/live", "/api/search.json"} {
		if b := get(t, h, path).Body.String(); strings.Contains(b, "Gitea") {
			t.Errorf("%s says Gitea", path)
		}
	}
	var ix searchIndex
	if err := json.Unmarshal(get(t, h, "/api/search.json").Body.Bytes(), &ix); err != nil {
		t.Fatal(err)
	}
	if len(ix.Issues) != 2 || ix.Issues[0].Forge != "GitHub" || ix.Issues[0].Mirror || ix.Issues[1].Forge != "Codeberg" || !ix.Issues[1].Mirror {
		t.Errorf("search issues %+v", ix.Issues)
	}
}

// A markdown note's body renders through mdlite, the first 40 lines open and
// the rest behind a disclosure.
func TestNoteBodyRenders(t *testing.T) {
	site := acmeSite()
	site.Meta.Notes, site.Meta.NotesLabel = true, "Notes"
	var lines []string
	for i := 1; i <= 45; i++ {
		lines = append(lines, fmt.Sprintf("line %d with **bold**", i))
	}
	lines[0] = "# App <script>alert(1)</script>"
	site.Projects[0].Notes = []model.NoteInfo{
		{File: "app.md", Name: "app", Modified: fixedNow, Body: strings.Join(lines, "\n")},
		{File: "short.md", Name: "short", Modified: fixedNow, Body: "Just **one** line"},
	}
	body := get(t, serve(site), "/p/app").Body.String()
	notes := strings.Split(body, `<div class="note" id="`)[1:]
	if len(notes) != 2 || !strings.HasPrefix(notes[0], `note-app-md"`) || !strings.HasPrefix(notes[1], `note-short-md"`) {
		t.Fatalf("%d note blocks", len(notes))
	}
	note := notes[0]
	short, _, _ := strings.Cut(notes[1], "</section>")
	open, more, ok := strings.Cut(note, "<details")
	if !ok || !strings.Contains(open, "line 40 with <strong>bold</strong>") || strings.Contains(open, "line 41 ") ||
		!strings.Contains(more, "5 more lines") || !strings.Contains(more, "line 45 with <strong>bold</strong>") {
		t.Errorf("body not split at 40 lines:\n%s", note)
	}
	if strings.Contains(note, "<script>") || !strings.Contains(note, "&lt;script&gt;") {
		t.Error("note body markup not escaped")
	}
	if !strings.Contains(short, "Just <strong>one</strong> line") || strings.Contains(short, "more line") {
		t.Errorf("short note: %s", short)
	}
}

// Ideas from labelled issues are issues already: the page says where they
// come from, shows no kind or size it does not know, and counts none as filed.
func TestLabelIdeasCopy(t *testing.T) {
	site := acmeSite()
	site.Meta.Ideas, site.Meta.IdeaLabels = true, []string{"idea", "enhancement"}
	issue := model.Idea{ID: "github:acme/app#3", Project: "app", ProjectName: "App", Icon: "box", Title: "Dark mode", ExistingIssue: 3,
		Source: "issue", URL: "https://github.com/acme/app/issues/3"}
	site.Ideas = []model.Idea{issue}
	site.Projects[0].Ideas = site.Ideas
	body := get(t, serve(site), "/ideas").Body.String()
	for _, w := range []string{`<p class="lead">From issues labelled idea or enhancement.</p>`, "Issue #3"} {
		if !strings.Contains(body, w) {
			t.Errorf("label ideas only: missing %q", w)
		}
	}
	if strings.Contains(body, "Already filed") {
		t.Error("label ideas only: an Already filed count for ideas that are issues already")
	}
	card, _, _ := strings.Cut(strings.SplitN(body, `id="idea-github-acme-app-3"`, 2)[1], "</article>")
	for _, w := range []string{`class="kind"`, `class="size"`, "Unsized", "roundup", "Already #3"} {
		if strings.Contains(card, w) || (w == "roundup" && strings.Contains(body, w)) {
			t.Errorf("label idea shows %q", w)
		}
	}

	file := model.Idea{ID: "app-01", Project: "app", ProjectName: "App", Icon: "box", Title: "Export", Pitch: "CSV out", Size: "S", Kind: "quick-win",
		Source: "file", FileURL: "https://github.com/acme/app/issues/new?title=Export"}
	site.Ideas = append(site.Ideas, file)
	site.Projects[0].Ideas = site.Ideas
	site.RoundupDate = "2026-10-31"
	body = get(t, serve(site), "/ideas").Body.String()
	for _, w := range []string{
		`<p class="lead">Ideas from the roundup on <b>31 Oct 2026</b> and from issues labelled idea or enhancement. File one as an issue to make it real.</p>`,
		`(opens GitHub in a new tab)`, `Quick win`, `Small`,
		// The label idea is an issue already: neither filed nor unfiled.
		`<span class="kc-num">0</span><span class="kc-label">Already filed</span>`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("both kinds: missing %q", w)
		}
	}
	// A project with no repo names the forge it would file on.
	site.Projects[0].Repo, site.Ideas[1].FileURL = nil, ""
	site.Projects[0].Ideas = site.Ideas
	if body := get(t, serve(site), "/ideas").Body.String(); !strings.Contains(body, "No GitHub repo to file against") {
		t.Error("no repo: the idea does not name the forge")
	}
}

// Demo mode's data is invented, and every page says so in one line; real
// data never carries the line.
func TestDemoBanner(t *testing.T) {
	site := acmeSite()
	for _, demo := range []bool{true, false} {
		site.Meta.Demo = demo
		h := serve(site)
		for _, path := range []string{"/", "/p/app", "/ships", "/map", "/p/nope"} {
			body := get(t, h, path).Body.String()
			shown := strings.Contains(body, `<p class="demo-banner" role="note">`) && strings.Contains(body, "<b>Demo data.</b>")
			if shown != demo {
				t.Errorf("demo %v: %s shows the banner: %v", demo, path, shown)
			}
		}
	}
}

// Dates and times are written in the configured time zone: in the titles
// of relative times, in the ship log's months, and for live.js to use.
func TestTimesInSiteZone(t *testing.T) {
	site := acmeSite()
	site.Releases = []model.Release{{Project: "app", ProjectName: "App", Icon: "box", Tag: "v1.0.0", Date: time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)}}
	for zone, want := range map[string][]string{
		"":           {`data-tz="UTC"`, `title="31 Oct 2026, 23:00"`, "October 2026"},
		"Asia/Tokyo": {`data-tz="Asia/Tokyo"`, `title="1 Nov 2026, 08:00"`, "November 2026"},
	} {
		site.Meta.Timezone = zone
		h := serve(site)
		body := get(t, h, "/").Body.String() + get(t, h, "/ships").Body.String()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("zone %q: missing %q", zone, w)
			}
		}
	}
}
