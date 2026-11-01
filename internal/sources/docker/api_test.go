package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const apiBody = `[
 {"Id":"abc123","Names":["/tool"],"Image":"ghcr.io/acme/tool:1.2.0","State":"running","Status":"Up 3 hours","Created":1793000000,
  "Labels":{"org.opencontainers.image.source":"https://github.com/acme/tool","note":"a=b,c"}},
 {"Id":"def456","Names":["/tool-prev","/alias"],"Image":"ghcr.io/acme/tool:1.1.0","State":"exited","Status":"Exited (0) 2 days ago","Created":1792000000,"Labels":null},
 {"Id":"nameless","Names":[],"Image":"x","State":"running","Status":"Up"}
]`

func TestFetchAPIUnixSocket(t *testing.T) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.RequestURI()+" "+r.UserAgent())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(apiBody))
	}))
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()

	f, err := FetchAPI(context.Background(), "unix://"+sock, "waymark/1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 1 || reqs[0] != "GET /containers/json?all=1 waymark/1.2.3" {
		t.Fatalf("requests = %v, want one unversioned GET naming its client", reqs)
	}
	if len(f.Containers) != 2 {
		t.Fatalf("containers = %+v", f.Containers)
	}
	c := f.Get("tool")
	if c == nil || c.Image != "ghcr.io/acme/tool:1.2.0" || c.State != "running" || c.Status != "Up 3 hours" || c.ID != "abc123" {
		t.Fatalf("tool = %+v", c)
	}
	if c.Labels["org.opencontainers.image.source"] != "https://github.com/acme/tool" || c.Labels["note"] != "a=b,c" {
		t.Errorf("labels = %v", c.Labels)
	}
	if want := time.Unix(1793000000, 0).UTC(); !c.CreatedAt.Equal(want) {
		t.Errorf("created = %v", c.CreatedAt)
	}
	if p := f.Get("tool-prev"); p == nil || p.State != "exited" || p.Labels == nil {
		t.Errorf("tool-prev = %+v", p)
	}
	if f.ModTime.IsZero() {
		t.Error("ModTime unset")
	}
}

func TestFetchAPITCPAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			_, _ = w.Write([]byte(apiBody))
			return
		}
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	f, err := FetchAPI(context.Background(), "tcp://"+strings.TrimPrefix(srv.URL, "http://"), "waymark/dev")
	if err != nil || len(f.Containers) != 2 {
		t.Fatalf("tcp: %v %+v", err, f)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer bad.Close()
	if _, err := FetchAPI(context.Background(), "tcp://"+strings.TrimPrefix(bad.URL, "http://"), "waymark/dev"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("403 err = %v", err)
	}
	if _, err := FetchAPI(context.Background(), "http://example.com", "waymark/dev"); err == nil {
		t.Error("a host without unix:// or tcp:// must fail")
	}
	if _, err := FetchAPI(context.Background(), "unix://"+filepath.Join(t.TempDir(), "none.sock"), "waymark/dev"); err == nil {
		t.Error("a missing socket must fail")
	}
}

func TestFeedLabelsParsed(t *testing.T) {
	line := `{"ID":"a1","Names":"tool","Image":"ghcr.io/acme/tool:1.2.0","State":"running","Status":"Up 1 hour","CreatedAt":"2026-10-31 02:30:17 +0000 UTC",` +
		`"Labels":"org.opencontainers.image.source=https://github.com/acme/tool?a=b,org.opencontainers.image.version=1.2.0,empty="}`
	cs, skipped, err := Parse(strings.NewReader(line))
	if err != nil || skipped != 0 || len(cs) != 1 {
		t.Fatalf("%v %d %+v", err, skipped, cs)
	}
	l := cs[0].Labels
	if l["org.opencontainers.image.source"] != "https://github.com/acme/tool?a=b" || l["org.opencontainers.image.version"] != "1.2.0" {
		t.Errorf("labels = %v", l)
	}
	if v, ok := l["empty"]; !ok || v != "" {
		t.Errorf("empty label = %q, %v", v, ok)
	}
}

func TestHostCredentialsNeverInErrors(t *testing.T) {
	for _, host := range []string{"tcp://u:secret@127.0.0.1:1", "tcp://127.0.0.1:1/secret", "tcp://127.0.0.1:1?k=secret", "tcp://%zzsecret", "ftp://u:secret@h:1"} {
		_, err := FetchAPI(context.Background(), host, "waymark/dev")
		if err == nil {
			t.Errorf("%s: want an error", host)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: error leaks: %v", host, err)
		}
	}
	// A reachable-looking host that refuses the connection still names only scheme and host.
	_, err := FetchAPI(context.Background(), "tcp://127.0.0.1:1", "waymark/dev")
	if err == nil || !strings.Contains(err.Error(), "tcp://127.0.0.1:1") {
		t.Errorf("err = %v", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/containers/stop", http.StatusFound)
	}))
	defer srv.Close()
	_, err := FetchAPI(context.Background(), "tcp://"+strings.TrimPrefix(srv.URL, "http://"), "waymark/dev")
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("err = %v, want the 302 status error", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
}

func TestOwnName(t *testing.T) {
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"/web"}, "web"},
		{[]string{"/app/db", "/db"}, "db"},
		{[]string{"/a/b/c"}, "a/b/c"},
	} {
		if got := ownName(tc.names); got != tc.want {
			t.Errorf("ownName(%v) = %q, want %q", tc.names, got, tc.want)
		}
	}
}
