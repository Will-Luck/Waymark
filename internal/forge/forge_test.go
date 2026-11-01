package forge

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const fixtures = "../../testdata/gitea/"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A real listing mixes dated plans, a dated folder (its plan.md) and
// undated files; the newest dated ones come first.
func TestPlanCandidatesFromAListing(t *testing.T) {
	var entries []Entry
	if err := json.Unmarshal(fixture(t, "beacon_plans_dir.json"), &entries); err != nil {
		t.Fatal(err)
	}
	got := PlanCandidates(entries, 3)
	want := []string{
		"docs/plans/2026-10-22-incident-timeline.md",
		"docs/plans/2026-10-11-status-api/plan.md",
		"docs/plans/2026-09-21-regions.md",
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Errorf("candidate %d = %s, want %s", i, got[i].Path, want[i])
		}
	}
	if !strings.HasSuffix(got[1].URL, "/docs/plans/2026-10-11-status-api/plan.md") {
		t.Errorf("dir plan url = %s", got[1].URL)
	}
	if all := PlanCandidates(entries, 20); len(all) != 10 || all[9].Name != "2026-07-13-audit-log.md" {
		t.Errorf("%d candidates, want the ten dated ones, oldest last (README.md and alerting-notes.md are not plans)", len(all))
	}
}

func TestParseChangelog(t *testing.T) {
	cases := []struct {
		name, in  string
		items     []string
		ver, date string
	}{
		{
			name:  "items with wrapped lines and subheadings",
			in:    "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- **New** thing\n  wraps here\n- second\n  - nested is ignored\n\n### Fixed\n* third\n\n## [1.2.0] - 2026-10-01\n\n- old\n",
			items: []string{"**New** thing wraps here", "second", "third"},
			ver:   "1.2.0", date: "2026-10-01",
		},
		{
			name: "no unreleased section",
			in:   "# Changelog\n\n## [v0.3.0] - 2026-07-25\n- shipped\n## [0.2.0] - 2026-07-01\n",
			ver:  "0.3.0", date: "2026-07-25",
		},
		{
			name:  "plain unreleased heading, no versions",
			in:    "## Unreleased\n- only\n",
			items: []string{"only"},
		},
		{
			name: "version without date",
			in:   "## [Unreleased]\n## [2.0.0]\n",
			ver:  "2.0.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseChangelog(tc.in)
			if fmt.Sprint(got.Unreleased) != fmt.Sprint(tc.items) || got.LastVersion != tc.ver || got.LastDate != tc.date {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestParsePlan(t *testing.T) {
	in := "intro\n# The Plan\n## Task 1\n- [x] done one\n  - [X] nested done\n- [ ] open\n1. [ ] numbered\n```md\n- [ ] in a fence\n```\n> - [ ] quoted is not a task\n# Second H1\n"
	title, done, total := ParsePlan(in)
	if title != "The Plan" || done != 2 || total != 4 {
		t.Errorf("got %q %d/%d", title, done, total)
	}
	if got := PlanTitleFromName("2026-10-23-k8s-update-sweep.md"); got != "K8s update sweep" {
		t.Errorf("title from name = %q", got)
	}
}

func TestFiledIdeas(t *testing.T) {
	ts := func(s string) Time { v, _ := time.Parse(time.RFC3339, s); return Time{v} }
	issues := []Issue{
		{Number: 9, HTMLURL: "u9", Body: "Pitch.\n\nroadmap-idea:beacon-01", CreatedAt: ts("2026-11-02T00:00:00Z")},
		{Number: 7, HTMLURL: "u7", Body: "dup\nroadmap-idea:beacon-01\n", CreatedAt: ts("2026-11-01T00:00:00Z")},
		{Number: 8, HTMLURL: "u8", Body: "roadmap-idea:atlas-02.", CreatedAt: ts("2026-11-01T00:00:00Z")},
		{Number: 10, Body: "mentions roadmap-idea without an id"},
	}
	got := FiledIdeas(issues, "roadmap-idea:")
	if got["beacon-01"].Number != 7 || got["atlas-02"].Number != 8 || len(got) != 2 {
		t.Errorf("filed = %+v", got)
	}
	var empty []Issue
	if err := json.Unmarshal(fixture(t, "issue_search_empty.json"), &empty); err != nil || len(FiledIdeas(empty, "roadmap-idea:")) != 0 {
		t.Errorf("empty search: %v", err)
	}
	// A forge's search answer: the issue an idea was filed as.
	var found []Issue
	if err := json.Unmarshal(fixture(t, "issue_search.json"), &found); err != nil {
		t.Fatal(err)
	}
	if got := FiledIdeas(found, "waymark-idea:"); len(got) != 1 || got["beacon-01"].Number != 15 || !strings.HasSuffix(got["beacon-01"].URL, "/acme/beacon/issues/15") {
		t.Errorf("search answer: filed = %+v", got)
	}
	// Another marker finds only its own lines, taken literally (a regexp
	// character in it is no wildcard); no marker finds nothing.
	other := []Issue{
		{Number: 1, Body: "acme.idea+tool-01"},
		{Number: 2, Body: "acmexidea+tool-02"},
		{Number: 3, Body: "waymark-idea:tool-03"},
	}
	if got := FiledIdeas(other, "acme.idea+"); len(got) != 1 || got["tool-01"].Number != 1 {
		t.Errorf("marker acme.idea+: filed = %+v", got)
	}
	if got := FiledIdeas(other, ""); len(got) != 0 {
		t.Errorf("no marker: filed = %+v", got)
	}
}

func TestTimeTolerance(t *testing.T) {
	var v struct {
		A Time `json:"a"`
		B Time `json:"b"`
		C Time `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":"","b":null,"c":"2026-10-10T11:23:41+01:00"}`), &v); err != nil {
		t.Fatal(err)
	}
	if !v.A.IsZero() || !v.B.IsZero() || v.C.Location() != time.UTC || v.C.Hour() != 10 {
		t.Errorf("times = %+v", v)
	}
}
