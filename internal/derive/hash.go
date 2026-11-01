package derive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/sources/docker"
)

// Hash is sha256 over the project's JSON without the fields that move on
// every probe or poll: Probes[].CheckedAt/LatencyMS/History/UptimePct,
// Deploy.CheckedAt, Sources and Hash itself. Probe Up stays in. The probe
// chips carry the latency in their text, so they are reduced to up/down too.
// Deploy.Status is docker's text, whose age ("Up 21 hours", "Exited (1) 3
// minutes ago") moves with the clock: only its steady part counts ("Up",
// "Exited (1)", health), so a restart the feed never saw down shows on the
// next change or page load rather than re-publishing the card each hour.
// No other text in the project counts finer than whole days.
func Hash(p model.Project) string {
	c := p
	c.Hash = ""
	c.Sources = nil
	if p.Deploy != nil {
		d := *p.Deploy
		d.CheckedAt = time.Time{}
		d.Status = docker.ParseStatus(d.Status).Steady
		c.Deploy = &d
	}
	if p.Probes != nil {
		c.Probes = make([]model.ProbeInfo, len(p.Probes))
		for i, pr := range p.Probes {
			pr.CheckedAt, pr.LatencyMS, pr.History, pr.UptimePct = time.Time{}, 0, nil, 0
			c.Probes[i] = pr
		}
	}
	if p.Signals != nil {
		c.Signals = make([]model.Signal, len(p.Signals))
		for i, s := range p.Signals {
			if s.Kind == "probe" {
				s.Text = string(s.Level)
			}
			c.Signals[i] = s
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
