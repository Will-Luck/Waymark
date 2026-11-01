package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Release builds: "latest" (the image tag and the GitHub release) moves only
// for a release, never a prerelease (a tag with a "-", such as v0.3.0-rc.1),
// and the image's Go build runs on the build machine, cross-compiling,
// rather than under emulation.

const notPrerelease = "${{ !contains(github.ref_name, '-') }}"

func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// steps are a workflow's steps across its jobs: what each uses and its with.
func steps(t *testing.T, name string) []map[string]any {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(repoFile(t, name)), &wf); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, j := range wf.Jobs {
		out = append(out, j.Steps...)
	}
	return out
}

// with is the with: of the first step using action, nil when no step does.
func with(ss []map[string]any, action string) map[string]any {
	for _, s := range ss {
		if u, _ := s["uses"].(string); strings.HasPrefix(u, action+"@") {
			w, _ := s["with"].(map[string]any)
			if w == nil {
				w = map[string]any{}
			}
			return w
		}
	}
	return nil
}

func TestReleaseLatestOnlyForReleases(t *testing.T) {
	ss := steps(t, ".github/workflows/release.yml")
	meta := with(ss, "docker/metadata-action")
	if meta == nil {
		t.Fatal("no docker/metadata-action step")
	}
	// No automatic latest; the one latest tag is gated.
	if flavor := fmt.Sprint(meta["flavor"]); !strings.Contains(flavor, "latest=false") {
		t.Errorf("metadata flavor %q, want latest=false", flavor)
	}
	gated := 0
	for _, line := range strings.Split(fmt.Sprint(meta["tags"]), "\n") {
		if !strings.Contains(line, "latest") {
			continue
		}
		if !strings.Contains(line, "enable="+notPrerelease) {
			t.Errorf("image tag %q moves latest on a prerelease", strings.TrimSpace(line))
		}
		gated++
	}
	if gated != 1 {
		t.Errorf("%d latest image tags, want one", gated)
	}
	rel := with(ss, "softprops/action-gh-release")
	if rel == nil {
		t.Fatal("no softprops/action-gh-release step")
	}
	if rel["make_latest"] != notPrerelease || rel["prerelease"] != "${{ contains(github.ref_name, '-') }}" {
		t.Errorf("GitHub release make_latest %v, prerelease %v", rel["make_latest"], rel["prerelease"])
	}
}

func TestImageGoBuildIsNative(t *testing.T) {
	var stages [][]string // each FROM line and the lines after it
	for _, l := range strings.Split(repoFile(t, "Dockerfile"), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "FROM ") {
			stages = append(stages, nil)
		}
		if len(stages) > 0 && l != "" && !strings.HasPrefix(l, "#") {
			stages[len(stages)-1] = append(stages[len(stages)-1], l)
		}
	}
	if len(stages) < 2 {
		t.Fatalf("%d stages, want a builder and a final stage", len(stages))
	}
	builder := strings.Join(stages[0], "\n")
	if !strings.HasPrefix(builder, "FROM --platform=$BUILDPLATFORM ") {
		t.Errorf("the builder runs on the target platform: %s", stages[0][0])
	}
	for _, w := range []string{"ARG TARGETOS", "ARG TARGETARCH", "GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build"} {
		if !strings.Contains(builder, w) {
			t.Errorf("the builder lacks %q", w)
		}
	}
	// Emulation is set up exactly while the final stage runs a command in
	// the target platform's image (apk, adduser).
	runs := false
	for _, l := range stages[len(stages)-1] {
		runs = runs || strings.HasPrefix(l, "RUN ")
	}
	if qemu := with(steps(t, ".github/workflows/release.yml"), "docker/setup-qemu-action") != nil; qemu != runs {
		t.Errorf("setup-qemu %v, but the final stage runs commands: %v", qemu, runs)
	}
}
