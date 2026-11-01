package derive

import (
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
)

// zone is a site time zone that is UTC in winter and UTC+1 in summer,
// changing on the last Sundays of March and October (Lisbon's rules).
func zone(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in            string
		ok            bool
		maj, min, pat int
		pre           string
	}{
		{"v1.25.0", true, 1, 25, 0, ""},
		{"1.9.1", true, 1, 9, 1, ""},
		{"v2.18.2-ls250", true, 2, 18, 2, "ls250"},
		{"3.0.0-rc.1+build.5", true, 3, 0, 0, "rc.1"},
		{"1.2.3+meta", true, 1, 2, 3, ""},
		{"v2026.10.07.1", false, 0, 0, 0, ""},
		{"28.2", false, 0, 0, 0, ""},
		{"latest", false, 0, 0, 0, ""},
		{"", false, 0, 0, 0, ""},
		{"v01.2.3", true, 1, 2, 3, ""},
		{"1.2.x", false, 0, 0, 0, ""},
	}
	for _, tc := range cases {
		v, ok := ParseSemver(tc.in)
		if ok != tc.ok || (ok && (v.Major != tc.maj || v.Minor != tc.min || v.Patch != tc.pat || v.Pre != tc.pre)) {
			t.Errorf("ParseSemver(%q) = %+v %v", tc.in, v, ok)
		}
	}
}

func TestLagging(t *testing.T) {
	cases := []struct {
		latest, deployed string
		want             bool
	}{
		{"v1.25.0", "v1.24.0", true},
		{"v1.9.2", "1.9.1", true},
		{"v1.9.1", "1.9.1", false},
		{"v1.9.1", "1.9.2", false},
		{"v2.0.0", "v2.0.0-rc1", false}, // -suffix ignored
		{"v2.0.0-rc1", "v1.9.9", true},
		{"v1.10.0", "v1.9.0", true}, // numeric, not lexical
		{"v2026.10.07.1", "v2026.10.01.1", false},
		{"v1.0.0", "", false},
		{"", "v1.0.0", false},
		{"v1.0.0", "latest", false},
	}
	for _, tc := range cases {
		if got := Lagging(tc.latest, tc.deployed); got != tc.want {
			t.Errorf("Lagging(%q, %q) = %v", tc.latest, tc.deployed, got)
		}
	}
}

func tag(name, created string) forge.Tag {
	var tg forge.Tag
	tg.Name = name
	tm, _ := time.Parse(time.RFC3339, created)
	tg.Commit.Created = forge.Time{Time: tm}
	return tg
}

func TestPickLatestTag(t *testing.T) {
	cases := []struct {
		name string
		tags []forge.Tag
		want string
	}{
		{"none", nil, ""},
		{"highest semver wins over order", []forge.Tag{tag("v1.9.9", "2026-10-02T00:00:00Z"), tag("v2.0.0", "2026-09-01T00:00:00Z")}, "v2.0.0"},
		{"release beats prerelease of same core", []forge.Tag{tag("v2.0.0-rc1", "2026-09-01T00:00:00Z"), tag("v2.0.0", "2026-09-02T00:00:00Z")}, "v2.0.0"},
		{"no semver: first listed", []forge.Tag{tag("v2026.10.07.1", "2026-10-07T00:00:00Z"), tag("v2026.09.01.1", "2026-09-01T00:00:00Z")}, "v2026.10.07.1"},
		{"newest semver: an older non-semver tag is ignored", []forge.Tag{tag("nightly", "2026-09-01T00:00:00Z"), tag("v0.1.0", "2026-10-07T00:00:00Z")}, "v0.1.0"},
		{"calendar versions: the newest wins even if an older one parses", []forge.Tag{tag("v2026.10.07", "2026-10-07T09:00:00Z"), tag("v2026.10.07.1", "2026-10-07T10:00:00Z")}, "v2026.10.07.1"},
		{"equal dates: first listed", []forge.Tag{tag("b-build", "2026-10-07T09:00:00Z"), tag("a-build", "2026-10-07T09:00:00Z")}, "b-build"},
	}
	for _, tc := range cases {
		got := PickLatestTag(tc.tags)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, name, tc.want)
		}
	}
}

func TestDeployedVersion(t *testing.T) {
	ver := map[string]string{"org.opencontainers.image.version": "v1.1.1"}
	cases := []struct {
		image  string
		labels map[string]string
		want   string
	}{
		{"beacon:v2.3.1", nil, "v2.3.1"},
		{"ghcr.io/acme/harbour:3.1.0", map[string]string{"org.opencontainers.image.version": "3.1.0"}, "3.1.0"},
		{"ghcr.io/acme/badge:latest", ver, "v1.1.1"},
		{"5d1e0c2a9b47", nil, ""},
		{"ccdb5660ecdf", map[string]string{"org.opencontainers.image.version": "1.11.0"}, "1.11.0"},
		{"sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", ver, "v1.1.1"},
		{"atlas", ver, "v1.1.1"},
		{"registry.example.com:5443/team/app:2.0.1", nil, "2.0.1"},
		{"registry.example.com:5443/team/app", ver, "v1.1.1"},
		{"nginx:alpine", nil, ""},
		{"n8nio/n8n:stable", map[string]string{"org.opencontainers.image.version": "2.44.6"}, "2.44.6"},
		{"app:edge", ver, "v1.1.1"},
		{"app:2026.10.07", nil, "2026.10.07"},
		{"app:1.2.3@sha256:abcd", nil, "1.2.3"},
		{"ghcr.io/acme/tile-cache:latest", map[string]string{"org.opencontainers.image.version": "latest"}, ""},
	}
	for _, tc := range cases {
		if got := DeployedVersion(tc.image, tc.labels); got != tc.want {
			t.Errorf("DeployedVersion(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

func TestTier(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	days := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	hold := &registry.Hold{Kind: "parked", Since: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Reason: "Waiting on a decision"}
	frozen := &registry.Hold{Kind: "frozen", Since: time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC), Reason: "Production is frozen"}
	cases := []struct {
		name   string
		in     TierInput
		tier   model.Tier
		reason string
	}{
		{"retired beats everything", TierInput{Retired: true, Hold: hold, LastCommit: days(1), Running: true}, model.TierArchived, "Retired in the projects file"},
		{"archived repo", TierInput{RepoArchived: true, LastCommit: days(1), Forge: "Gitea"}, model.TierArchived, "Archived on Gitea"},
		{"hold beats active", TierInput{Hold: hold, LastCommit: days(1)}, model.TierHold, "Parked since 7 Aug: Waiting on a decision"},
		{"frozen", TierInput{Hold: frozen}, model.TierHold, "Frozen since 29 May: Production is frozen"},
		{"active at 20 days", TierInput{LastCommit: days(20), Running: true}, model.TierActive, "Commits in the last 3 weeks"},
		{"live by container", TierInput{LastCommit: days(40), Running: true, ProbeUp: true}, model.TierLive, "Deployed and answering, no commits for 40 days"},
		{"live by container only", TierInput{LastCommit: days(40), Running: true}, model.TierLive, "Deployed and running, no commits for 40 days"},
		{"live by probe only", TierInput{LastCommit: days(200), ProbeUp: true}, model.TierLive, "Answering, no commits for 6 months"},
		{"quiet at 22 days", TierInput{LastCommit: days(22)}, model.TierQuiet, "No commits for 22 days"},
		{"quiet at 120 days", TierInput{LastCommit: days(120)}, model.TierQuiet, "No commits for 4 months"},
		{"dormant", TierInput{LastCommit: days(245)}, model.TierDormant, "No commits for 8 months"},
		{"dormant, no commits", TierInput{}, model.TierDormant, "No commits yet"},
	}
	for _, tc := range cases {
		tier, reason := Tier(tc.in, now)
		if tier != tc.tier || reason != tc.reason {
			t.Errorf("%s: got %s %q, want %s %q", tc.name, tier, reason, tc.tier, tc.reason)
		}
	}
}

func TestSpan(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cases := map[time.Duration]string{
		10 * time.Minute:     "less than a day",
		20 * time.Second:     "less than a day",
		3 * time.Hour:        "less than a day",
		30 * time.Hour:       "1 day",
		5 * 24 * time.Hour:   "5 days",
		59 * 24 * time.Hour:  "59 days",
		61 * 24 * time.Hour:  "2 months",
		800 * 24 * time.Hour: "2 years",
	}
	for d, want := range cases {
		if got := For(now.Add(-d), now); got != want {
			t.Errorf("For(%v) = %q, want %q", d, got, want)
		}
	}
}

// DaysAgo counts calendar days in the site's zone, so its text holds all day.
func TestDaysAgo(t *testing.T) {
	loc := zone(t)
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC) // Sat 31 Oct, winter time (UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-5 * time.Minute), "today"},
		{time.Date(2026, 10, 31, 0, 30, 0, 0, time.UTC), "today"},
		{now.Add(time.Hour), "today"}, // a clock a little ahead
		{time.Date(2026, 10, 30, 23, 59, 0, 0, time.UTC), "yesterday"},
		{time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC), "2 days ago"},
		{time.Date(2026, 10, 24, 23, 30, 0, 0, time.UTC), "6 days ago"}, // 25 Oct 00:30 summer time
		{now.Add(-59 * 24 * time.Hour), "59 days ago"},
		{now.Add(-61 * 24 * time.Hour), "2 months ago"},
		{now.Add(-800 * 24 * time.Hour), "2 years ago"},
	}
	for _, tc := range cases {
		if got := DaysAgo(tc.at, now, loc); got != tc.want {
			t.Errorf("DaysAgo(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
	// Every minute of the day reads the same.
	for m := 0; m < 24*60; m += 7 {
		at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
		if got := DaysAgo(time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC), at, loc); got != "2 days ago" {
			t.Fatalf("at %v: %q", at, got)
		}
	}
}

func TestActivityWeeksInZone(t *testing.T) {
	loc := zone(t)
	// Saturday 31 Oct 2026 22:00 UTC; summer time ended on Sunday 25 Oct.
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	times := []time.Time{
		at("2026-10-26T00:30:00Z"), // Mon 26 Oct 00:30 winter time: current week
		at("2026-10-25T23:30:00Z"), // Sun 25 Oct 23:30 winter time: previous week
		at("2026-10-25T23:30:00Z"),
		at("2026-10-18T23:30:00Z"), // Mon 19 Oct 00:30 summer time (Sunday in UTC): index 10
		at("2026-10-18T22:30:00Z"), // Sun 18 Oct 23:30 summer time: index 9
		at("2026-08-10T12:00:00Z"), // Mon 10 Aug: oldest bucket (index 0)
		at("2026-08-09T12:00:00Z"), // before the 12 weeks: dropped
		at("2026-11-01T12:00:00Z"), // future: dropped
	}
	got := Activity(times, now, loc)
	want := []int{1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 3, 1}
	if len(got) != 12 {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("activity = %v, want %v", got, want)
		}
	}
}

func TestHeatmapInZone(t *testing.T) {
	loc := zone(t)
	now := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC) // still Sat 31 Oct in the zone (winter time)
	pts := []forge.HeatPoint{
		{Timestamp: time.Date(2026, 10, 31, 23, 15, 0, 0, time.UTC).Unix(), Contributions: 3},
		{Timestamp: time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).Unix(), Contributions: 2},  // 01:30 summer time Sun 25 Oct
		{Timestamp: time.Date(2026, 10, 24, 23, 30, 0, 0, time.UTC).Unix(), Contributions: 5}, // 00:30 summer time Sun 25 Oct
		{Timestamp: time.Date(2025, 10, 26, 12, 0, 0, 0, time.UTC).Unix(), Contributions: 9},  // first day (today - 370)
		{Timestamp: time.Date(2025, 10, 25, 12, 0, 0, 0, time.UTC).Unix(), Contributions: 9},  // too old
	}
	h := Heatmap(pts, now, loc)
	if len(h.Days) != 371 {
		t.Fatalf("days = %d", len(h.Days))
	}
	first, last := h.Days[0], h.Days[370]
	if first.Date != "2025-10-26" || first.Count != 9 || first.Weekday != 6 {
		t.Errorf("first = %+v (Sunday is weekday 6)", first)
	}
	if last.Date != "2026-10-31" || last.Count != 3 || last.Weekday != 5 {
		t.Errorf("last = %+v", last)
	}
	if d := h.Days[364]; d.Date != "2026-10-25" || d.Count != 7 {
		t.Errorf("25 Oct = %+v", d)
	}
	if h.Max != 9 || h.Total != 19 {
		t.Errorf("max %d total %d", h.Max, h.Total)
	}
}

func TestCountText(t *testing.T) {
	if s := CountText(12, false); s != "12" {
		t.Error(s)
	}
	if s := CountText(150, true); s != "150+" {
		t.Error(s)
	}
}
