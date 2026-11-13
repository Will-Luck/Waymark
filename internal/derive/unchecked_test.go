package derive

import (
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/sources/probe"
)

// A projects-file probe with no result yet (after a restart, before the
// first probe pass reaches it) could make the project Live: a lower tier is
// shown but not fingerprinted until it is checked, so no tier event crosses
// it. A homepage never makes a project Live, and an active project is
// active whatever its probe says: both are fingerprinted as before.
func TestTierNotFingerprintedUntilTheProbeIsChecked(t *testing.T) {
	day := 24 * time.Hour
	unchecked := []ProbeData{{Label: "Live", URL: "https://app.example", ProbeURL: "http://10.0.0.1/"}}
	in := synth{lastCommit: 40 * day, commits: 1}.input()
	in.Probes = unchecked
	if b := BuildProject(env(t), in); b.Project.Tier != model.TierQuiet || b.FP.Tier != "" {
		t.Errorf("probe not checked: tier %s, fingerprint tier %q; want quiet shown and not fingerprinted", b.Project.Tier, b.FP.Tier)
	}
	in.Probes = []ProbeData{{Label: "Live", URL: "https://app.example", ProbeURL: "http://10.0.0.1/",
		Last: &probe.Result{Up: true, Code: 200, LatencyMS: 12, At: testNow}}}
	if b := BuildProject(env(t), in); b.FP.Tier != model.TierLive {
		t.Errorf("probe checked and up: fingerprint tier %q, want live", b.FP.Tier)
	}
	in.Probes = []ProbeData{{Label: "Homepage", URL: "https://app.example", ProbeURL: "https://app.example", Homepage: true}}
	if b := BuildProject(env(t), in); b.FP.Tier != model.TierQuiet {
		t.Errorf("homepage not checked: fingerprint tier %q, want quiet", b.FP.Tier)
	}
	in = synth{lastCommit: 2 * day, commits: 1}.input()
	in.Probes = unchecked
	if b := BuildProject(env(t), in); b.FP.Tier != model.TierActive {
		t.Errorf("active, probe not checked: fingerprint tier %q, want active", b.FP.Tier)
	}
}
