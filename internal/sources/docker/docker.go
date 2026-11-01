// Package docker reads containers from the feed file a host cron writes each
// minute (the JSON lines of `docker ps -a --format '{{json .}}'`, see
// scripts/docker-feed.sh), or from the Engine API when docker.host is set
// (api.go). The feed needs no Docker socket in the container.
package docker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Container struct {
	ID        string
	Name      string
	Image     string
	State     string // running | exited | created | paused | restarting | dead
	Status    string // "Up 20 hours", "Exited (0) 6 days ago"
	CreatedAt time.Time
	Labels    map[string]string
}

type line struct {
	ID        string `json:"ID"`
	Names     string `json:"Names"`
	Image     string `json:"Image"`
	State     string `json:"State"`
	Status    string `json:"Status"`
	CreatedAt string `json:"CreatedAt"`
	Labels    string `json:"Labels"`
}

// Feed is one read of the feed file.
type Feed struct {
	Containers []Container
	ModTime    time.Time
	// Skipped counts lines that were not valid JSON.
	Skipped int
}

// Parse reads JSON lines. Bad lines are counted, not fatal.
func Parse(r io.Reader) ([]Container, int, error) {
	var out []Container
	skipped := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil || l.Names == "" {
			skipped++
			continue
		}
		name, _, _ := strings.Cut(l.Names, ",")
		c := Container{
			ID:     l.ID,
			Name:   strings.TrimPrefix(name, "/"),
			Image:  l.Image,
			State:  l.State,
			Status: l.Status,
			Labels: ParseLabels(l.Labels),
		}
		if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", l.CreatedAt); err == nil {
			c.CreatedAt = t.UTC()
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, err
	}
	return out, skipped, nil
}

var labelKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*=`)

// ParseLabels splits docker's "k=v,k2=v2" label string. Values may contain
// commas (JSON blobs), so a segment only starts a new label when it begins
// with key=.
func ParseLabels(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	var cur string
	flush := func() {
		if k, v, ok := strings.Cut(cur, "="); ok && k != "" {
			out[k] = v
		}
	}
	for i, seg := range strings.Split(s, ",") {
		if i > 0 && labelKeyRe.MatchString(seg) {
			flush()
			cur = seg
			continue
		}
		if i == 0 {
			cur = seg
			continue
		}
		cur += "," + seg
	}
	flush()
	return out
}

// ReadFeed reads and parses the feed file.
func ReadFeed(path string) (*Feed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	cs, skipped, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cs) == 0 && skipped > 0 {
		return nil, fmt.Errorf("%s: no valid lines (%d unreadable)", path, skipped)
	}
	return &Feed{Containers: cs, ModTime: st.ModTime().UTC(), Skipped: skipped}, nil
}

// StatusParts is docker's human status split into what it says and how long
// ago that began. The age is the part that reads differently every minute.
type StatusParts struct {
	// Steady is the status without its age, e.g. "Up (healthy)" or
	// "Exited (1)": it holds while the container stays as it is. A status
	// of a shape this package does not know is kept whole.
	Steady string
	// Age is how long the container has been in this state, as docker
	// rounds it ("3 days" is 3 days, "About an hour" one hour). Aged is
	// false when the status gives none ("Created", "Dead"), and for a paused
	// container, whose "Up 3 days (Paused)" counts from the start.
	Age  time.Duration
	Aged bool
	// ExitCode is the N of "Exited (N)" or "Restarting (N)".
	ExitCode    int
	HasExitCode bool
}

// statusRe matches docker's State.String(): "Up <age>", "Exited (N) <age>
// ago" or "Restarting (N) <age> ago", then an optional note such as
// "(healthy)", "(health: starting)" or "(Paused)". <age> is go-units'
// HumanDuration.
var statusRe = regexp.MustCompile(`^(Up|(?:Exited|Restarting) \((-?\d+)\)) (Less than a second|About a minute|About an hour|(\d+) (second|minute|hour|day|week|month|year)s?)(?: ago)?(?: \(([^()]+)\))?$`)

var ageUnit = map[string]time.Duration{
	"second": time.Second, "minute": time.Minute, "hour": time.Hour,
	"day": 24 * time.Hour, "week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour,
}

// ParseStatus splits a container's Status.
func ParseStatus(s string) StatusParts {
	m := statusRe.FindStringSubmatch(s)
	if m == nil {
		return StatusParts{Steady: s}
	}
	p := StatusParts{Steady: m[1], Aged: true}
	if m[6] != "" {
		p.Steady += " (" + m[6] + ")"
	}
	if m[2] != "" {
		p.ExitCode, _ = strconv.Atoi(m[2])
		p.HasExitCode = true
	}
	switch m[3] {
	case "Less than a second":
	case "About a minute":
		p.Age = time.Minute
	case "About an hour":
		p.Age = time.Hour
	default:
		n, _ := strconv.Atoi(m[4])
		p.Age = time.Duration(n) * ageUnit[m[5]]
	}
	if m[6] == "Paused" {
		p.Age, p.Aged = 0, false
	}
	return p
}

// Get returns the container with that name, or nil.
func (f *Feed) Get(name string) *Container {
	if f == nil {
		return nil
	}
	for i := range f.Containers {
		if f.Containers[i].Name == name {
			return &f.Containers[i]
		}
	}
	return nil
}
