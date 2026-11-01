package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsUp(t *testing.T) {
	for code, want := range map[int]bool{200: true, 204: true, 301: true, 302: true, 304: true, 401: true, 403: true, 404: false, 500: false, 502: false, 0: false} {
		if IsUp(code) != want {
			t.Errorf("IsUp(%d) = %v", code, !want)
		}
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "/locked":
			w.WriteHeader(http.StatusUnauthorized)
		case "/broken":
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	c := NewClient()
	cases := map[string]struct {
		up   bool
		code int
	}{
		"/":         {true, 200},
		"/redirect": {true, 302},
		"/locked":   {true, 401},
		"/broken":   {false, 502},
	}
	for path, want := range cases {
		r := Check(context.Background(), c, srv.URL+path, "dev")
		if r.Up != want.up || r.Code != want.code || r.Err != "" || r.At.IsZero() {
			t.Errorf("%s = %+v", path, r)
		}
	}
	srv.Close()
	r := Check(context.Background(), c, srv.URL+"/", "dev")
	if r.Up || r.Err == "" || r.Code != 0 {
		t.Errorf("closed server = %+v", r)
	}
}

// A probe names itself and its version, so a site owner can tell it from a
// browser in their logs.
func TestCheckUserAgent(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.UserAgent() }))
	defer srv.Close()
	Check(context.Background(), NewClient(), srv.URL+"/", "1.2.3")
	if ua := <-got; ua != "waymark-probe/1.2.3" {
		t.Errorf("User-Agent = %q", ua)
	}
}
