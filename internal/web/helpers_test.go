package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

func TestAgoText(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Second, "just now"},
		{12 * time.Second, "12 s ago"},
		{time.Minute, "1 minute ago"},
		{5 * time.Minute, "5 minutes ago"},
		{3 * time.Hour, "3 hours ago"},
		{30 * time.Hour, "yesterday"},
		{5 * 24 * time.Hour, "5 days ago"},
		{59 * 24 * time.Hour, "59 days ago"},
		{90 * 24 * time.Hour, "3 months ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	} {
		if got := agoText(now.Add(-c.d), now); got != c.want {
			t.Errorf("%v: %q, want %q", c.d, got, c.want)
		}
	}
	if got := agoText(time.Time{}, now); got != "never" {
		t.Errorf("zero: %q", got)
	}
}

func TestTierWords(t *testing.T) {
	frozen := &model.Project{Tier: model.TierHold, Hold: &model.Hold{Kind: "frozen"}}
	parked := &model.Project{Tier: model.TierHold, Hold: &model.Hold{Kind: "parked"}}
	if tierLabel(frozen) != "Frozen" || tierIcon(frozen) != "snowflake" {
		t.Error("frozen")
	}
	if tierLabel(parked) != "Parked" || tierIcon(parked) != "pause" {
		t.Error("parked")
	}
	if tierLabel(model.TierHold) != "On hold" || tierLabel(model.TierLive) != "Live" || tierIcon(model.TierLive) != "radio" {
		t.Error("plain tiers")
	}
}

func TestSparklineIsNumbersOnly(t *testing.T) {
	out := string(sparkline([]int{0, 0, 3, 9, 0, 1, 0, 0, 48, 22, 75, 0}))
	if !strings.HasPrefix(out, `<svg class="spark"`) || !strings.Contains(out, "158 commits in 12 weeks") {
		t.Errorf("sparkline %s", out)
	}
	if flat := string(sparkline(make([]int, 12))); !strings.Contains(flat, `class="spark flat"`) {
		t.Error("all-zero sparkline not marked flat")
	}
	if sparkline(nil) != "" {
		t.Error("nil activity drew a sparkline")
	}
}

func TestUptimeCells(t *testing.T) {
	end := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	var hist []model.ProbePoint
	for i := 0; i < 10; i++ {
		hist = append(hist, model.ProbePoint{At: end.Add(-time.Duration(9-i) * 10 * time.Minute), Up: i != 4, LatencyMS: 20})
	}
	cells := uptimeCells(hist, time.UTC)
	if len(cells) != 144 {
		t.Fatalf("%d cells", len(cells))
	}
	if cells[0].Class != "none" || cells[143].Class != "up" || cells[138].Class != "down" {
		t.Errorf("cells: first %q, last %q, down slot %q", cells[0].Class, cells[143].Class, cells[138].Class)
	}
	if !strings.Contains(cells[143].Tip, "31 Oct 23:00, up, 20 ms") {
		t.Errorf("tip %q", cells[143].Tip)
	}
	if got := uptimeCells(nil, time.UTC); len(got) != 144 || got[0].Class != "none" {
		t.Error("empty history")
	}
	// The tips are in the site's time zone, not the server's or UTC.
	if tip := uptimeCells(hist, time.FixedZone("", 3600))[143].Tip; !strings.HasPrefix(tip, "1 Nov 00:00, up") {
		t.Errorf("tip in UTC+1: %q", tip)
	}
}

func TestHeatFromFixture(t *testing.T) {
	site := loadSite(t, "site.json")
	h := buildHeat(site.Heatmap)
	if len(h.Cells) != 53*7 || len(h.Months) != 53 || len(h.Weeks) != 53 {
		t.Fatalf("cells %d months %d weeks %d", len(h.Cells), len(h.Months), len(h.Weeks))
	}
	first, _ := time.Parse("2006-01-02", h.Cells[0].Date)
	if first.Weekday() != time.Monday {
		t.Errorf("first cell is %s", first.Weekday())
	}
	last := site.Heatmap.Days[len(site.Heatmap.Days)-1]
	var today *heatCell
	sum := 0
	for i := range h.Cells {
		if h.Cells[i].Today {
			today = &h.Cells[i]
		}
		sum += h.Cells[i].Count
	}
	if today == nil || today.Date != last.Date {
		t.Fatalf("today cell %+v, want %s", today, last.Date)
	}
	if sum != h.Total || h.Total == 0 {
		t.Errorf("total %d, sum %d", h.Total, sum)
	}
	if h.Busiest.Count != site.Heatmap.Max {
		t.Errorf("busiest %d, heatmap max %d", h.Busiest.Count, site.Heatmap.Max)
	}
	// Streak: walk back from today by hand.
	counts := map[string]int{}
	for _, d := range site.Heatmap.Days {
		counts[d.Date] = d.Count
	}
	day, _ := time.Parse("2006-01-02", last.Date)
	if counts[last.Date] == 0 {
		day = day.AddDate(0, 0, -1)
	}
	want := 0
	for counts[day.Format("2006-01-02")] > 0 {
		want++
		day = day.AddDate(0, 0, -1)
	}
	if h.Streak != want {
		t.Errorf("streak %d, want %d", h.Streak, want)
	}
}

func TestMapLayoutInBounds(t *testing.T) {
	site := loadSite(t, "site.json")
	v := buildMap(site)
	if len(v.Nodes) != len(site.Graph.Nodes) {
		t.Fatalf("%d nodes, want %d", len(v.Nodes), len(site.Graph.Nodes))
	}
	for _, n := range v.Nodes {
		if n.X-n.R < 0 || n.X+n.R > v.W || n.Y-n.R < 0 || n.Y+n.R > v.H {
			t.Errorf("%s out of bounds at %.0f,%.0f r %.0f", n.ID, n.X, n.Y, n.R)
		}
		if n.R < 14 || n.R > 32 {
			t.Errorf("%s radius %.1f", n.ID, n.R)
		}
	}
	if len(v.Edges) == 0 || len(v.Anchors) != len(site.Categories) {
		t.Errorf("%d edges, %d anchors", len(v.Edges), len(v.Anchors))
	}
	if strings.Contains(string(v.JSON), "<") {
		t.Error("graph island contains a raw <")
	}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{"beacon": "beacon", "Quick Win": "quick-win", "<x>": "x", "": "x", "a_b": "a-b"} {
		if got := ident(in); got != want {
			t.Errorf("ident(%q) = %q, want %q", in, got, want)
		}
	}
}
