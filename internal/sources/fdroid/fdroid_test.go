package fdroid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestParseIndexFixture(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/fdroid/index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := ParseIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := pkgs["com.example.beacon"]
	if !ok {
		t.Fatal("package missing")
	}
	// The index lists its versions oldest first.
	if p.VersionCode != 10600 || p.VersionName != "1.6.0" || !p.Added.Equal(time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("package = %+v (highest versionCode wins)", p)
	}
	if len(pkgs) != 1 {
		t.Errorf("packages = %v", pkgs)
	}
	// Neither the first nor the last listed: the highest code.
	mixed, err := ParseIndex([]byte(`{"packages":{"a":[{"versionName":"1.5.2","versionCode":10502},{"versionName":"1.6.0","versionCode":10600},{"versionName":"1.5.0","versionCode":10500}]}}`))
	if err != nil || mixed["a"].VersionName != "1.6.0" || !mixed["a"].Added.IsZero() {
		t.Errorf("mixed order = %+v %v", mixed["a"], err)
	}
}

func TestFetch(t *testing.T) {
	b, _ := os.ReadFile("../../../testdata/fdroid/index-v1.json")
	uas := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas <- r.UserAgent()
		if r.URL.Path != "/repo/index-v1.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	pkgs, err := Fetch(context.Background(), srv.Client(), srv.URL+"/repo/index-v1.json", "waymark/1.2.3")
	if err != nil || pkgs["com.example.beacon"].VersionName != "1.6.0" {
		t.Errorf("fetch = %v %v", pkgs, err)
	}
	// The request names its client.
	if ua := <-uas; ua != "waymark/1.2.3" {
		t.Errorf("User-Agent = %q", ua)
	}
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/nope", "waymark/1.2.3"); err == nil {
		t.Error("404 must fail")
	}
	if _, err := ParseIndex([]byte("{")); err == nil {
		t.Error("bad JSON must fail")
	}
}
