package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadFeedFixture(t *testing.T) {
	f, err := ReadFeed("../../../testdata/docker/docker_ps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Containers) != 9 || f.Skipped != 0 {
		t.Fatalf("containers = %d skipped = %d", len(f.Containers), f.Skipped)
	}
	cases := []struct {
		name, image, state, status string
	}{
		{"beacon", "registry.example.com/acme/beacon:v2.3.1", "running", "Up 4 days"},
		{"beacon-prev", "registry.example.com/acme/beacon:v2.3.0", "exited", "Exited (0) 6 days ago"},
		{"fdroid-repo", "5d1e0c2a9b47", "running", "Up 20 hours"},
		{"harbour", "registry.example.com/acme/harbour:3.1.0", "running", "Up 19 hours"},
	}
	for _, tc := range cases {
		c := f.Get(tc.name)
		if c == nil {
			t.Errorf("%s missing", tc.name)
			continue
		}
		if c.Image != tc.image || c.State != tc.state || c.Status != tc.status {
			t.Errorf("%s = %+v", tc.name, c)
		}
	}
	if f.Get("nope") != nil {
		t.Error("unknown name found")
	}
	px := f.Get("proxy")
	if want := time.Date(2026, 10, 1, 19, 30, 17, 0, time.UTC); !px.CreatedAt.Equal(want) {
		t.Errorf("created = %v", px.CreatedAt)
	}
	// A label value that is a JSON blob full of commas stays whole.
	if v := px.Labels["com.example.build"]; !strings.HasPrefix(v, `{"ref"`) || !strings.HasSuffix(v, `]}`) || !strings.Contains(v, `"ok": true}, "tags"`) {
		t.Errorf("json label split: %q", v)
	}
	if px.Labels["org.opencontainers.image.source"] != "https://github.com/traefik/traefik" || px.Labels["traefik.enable"] != "true" {
		t.Errorf("labels = %v", px.Labels)
	}
	if v := f.Get("ledger-api").Labels["org.opencontainers.image.version"]; v != "1.8.0" {
		t.Errorf("ledger-api version label = %q", v)
	}
}

// Docker's status says how long ago the state began, so it reads differently
// every minute; ParseStatus separates that age from what the status says.
func TestParseStatus(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		in, steady string
		age        time.Duration
		aged       bool
		code       int // -1: none
	}{
		{"Up 20 hours", "Up", 20 * time.Hour, true, -1},
		{"Up 19 hours (healthy)", "Up (healthy)", 19 * time.Hour, true, -1},
		// Docker's paused age counts from the start, not the pause.
		{"Up 3 days (Paused)", "Up (Paused)", 0, false, -1},
		{"Up About a minute (health: starting)", "Up (health: starting)", time.Minute, true, -1},
		{"Up Less than a second", "Up", 0, true, -1},
		{"Up 2 weeks", "Up", 14 * day, true, -1},
		{"Exited (0) 6 days ago", "Exited (0)", 6 * day, true, 0},
		{"Exited (137) 3 months ago", "Exited (137)", 90 * day, true, 137},
		{"Exited (1) About an hour ago", "Exited (1)", time.Hour, true, 1},
		{"Exited (2) 3 weeks ago", "Exited (2)", 21 * day, true, 2},
		{"Exited (0) 2 years ago", "Exited (0)", 730 * day, true, 0},
		{"Restarting (1) 5 seconds ago", "Restarting (1)", 5 * time.Second, true, 1},
		{"Exited (1) 1 second ago", "Exited (1)", time.Second, true, 1},
		{"Created", "Created", 0, false, -1},
		{"Dead", "Dead", 0, false, -1},
		{"Removal In Progress", "Removal In Progress", 0, false, -1},
		{"", "", 0, false, -1},
		{"Up for a while", "Up for a while", 0, false, -1}, // an unknown shape is kept whole
	}
	for _, tc := range cases {
		got := ParseStatus(tc.in)
		code := -1
		if got.HasExitCode {
			code = got.ExitCode
		}
		if got.Steady != tc.steady || got.Age != tc.age || got.Aged != tc.aged || code != tc.code {
			t.Errorf("ParseStatus(%q) = %+v, want steady %q age %v aged %v code %d", tc.in, got, tc.steady, tc.age, tc.aged, tc.code)
		}
	}
	// Every status in a feed of running and exited containers carries an age.
	f, err := ReadFeed("../../../testdata/docker/docker_ps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Containers {
		if st := ParseStatus(c.Status); !st.Aged || strings.Contains(st.Steady, "ago") || strings.ContainsAny(st.Steady, "0123456789") && !st.HasExitCode {
			t.Errorf("%s: %q parsed as %+v", c.Name, c.Status, st)
		}
	}
}

func TestParseLabels(t *testing.T) {
	cases := map[string]map[string]string{
		"":                       {},
		"a=1":                    {"a": "1"},
		"a=1,b=2":                {"a": "1", "b": "2"},
		"a=x,y,b=2":              {"a": "x,y", "b": "2"},
		"a=,b=":                  {"a": "", "b": ""},
		`j={"k": 1, "l": 2},z=9`: {"j": `{"k": 1, "l": 2}`, "z": "9"},
	}
	for in, want := range cases {
		got := ParseLabels(in)
		if len(got) != len(want) {
			t.Errorf("%q = %v", in, got)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%q [%s] = %q, want %q", in, k, got[k], v)
			}
		}
	}
}

func TestParseSkipsBadLines(t *testing.T) {
	in := `{"Names":"a","Image":"x:1","State":"running","Status":"Up"}
not json
{"Image":"no name"}

{"Names":"b","Image":"y","State":"exited","Status":"Exited (1)"}
`
	cs, skipped, err := Parse(strings.NewReader(in))
	if err != nil || len(cs) != 2 || skipped != 2 {
		t.Errorf("got %d skipped %d err %v", len(cs), skipped, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "docker.json")
	if err := os.WriteFile(p, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFeed(p); err == nil {
		t.Error("an all-garbage feed must be an error")
	}
	if _, err := ReadFeed(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing feed must be an error")
	}
}
