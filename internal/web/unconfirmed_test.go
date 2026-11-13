package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

// probeRow is the page from its first probe row on, for a short failure text.
func probeRow(page string) string {
	i := strings.Index(page, `<div class="probe`)
	if i < 0 {
		return "(no probe row)"
	}
	return page[i:min(len(page), i+600)]
}

// A probe whose failure is not confirmed yet shows as warn, icon plus word:
// "Failed once, checking again", not "Down" (it may have answered, with
// an error code).
func TestUnconfirmedProbeShowsCheckingAgain(t *testing.T) {
	site := loadSite(t, "site.json")
	at := site.ProjectBySlug("atlas")
	if at == nil || len(at.Probes) != 1 || at.Probes[0].Up {
		t.Fatal("the fixture's atlas has no failing probe")
	}
	at.Probes[0].Unconfirmed = true
	h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
	page := get(t, h, "/p/atlas").Body.String()
	if !strings.Contains(page, `<div class="probe warn">`) ||
		!strings.Contains(page, `<use href="#i-triangle-alert"></use></svg>Failed once, checking again</span> <span class="mono">HTTP 503</span>`) {
		t.Errorf("atlas's probe row does not say it is checking again:\n%s", probeRow(page))
	}
	if strings.Contains(page, `</svg>Down</span>`) {
		t.Error("an unconfirmed probe still reads Down")
	}
}

// A container not confirmed down yet shows its state word with "checking
// again", in the warn style.
func TestUnconfirmedContainerShowsCheckingAgain(t *testing.T) {
	site := loadSite(t, "site.json")
	v := site.ProjectBySlug("vault")
	if v == nil || v.Deploy == nil || v.Deploy.State != "exited" {
		t.Fatal("the fixture's vault has no stopped container")
	}
	v.Deploy.Unconfirmed = true
	h := New(model.NewStaticSource(site), Options{Version: "v-test", Now: func() time.Time { return fixedNow }})
	page := get(t, h, "/p/vault").Body.String()
	if !strings.Contains(page, `<p class="state st-warn">`) || !strings.Contains(page, `<b>Stopped, checking again</b>`) {
		t.Error("vault's deploy row does not say it is checking again")
	}
}
