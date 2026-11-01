// Package config loads the Waymark configuration: one YAML file with ${VAR}
// expansion, or an env-only quick start. It owns every default; the
// Dockerfile sets none.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ExampleYAML is the documented example file, shipped as waymark.example.yml.
//
//go:embed example.yml
var ExampleYAML []byte

const (
	defaultConfigPath = "/config/waymark.yml"
	githubAPI         = "https://api.github.com"
)

type Config struct {
	Listen, DataDir string
	Site            Site
	IncludePrivate  bool
	Forges          []Forge
	Projects        FileRef
	Ideas           Ideas
	NeedsYou        NeedsYou
	Notes           Notes
	Plans           Plans
	Docker          Docker
	Probes          Probes
	// Demo marks the synthetic world demo mode serves; the page says so.
	// It has no YAML key: only -demo sets it.
	Demo bool
	// Version is the build's version, sent in User-Agent headers ("" reads
	// as dev). It has no YAML key: main sets it.
	Version string
	// Warnings are what Load noticed that is not an error, such as a token
	// variable that is not set; main logs them. They never hold a value.
	Warnings []string
	// badEnv are environment values Load could not use; Validate reports them.
	badEnv []string
	// envOnly: the env-only quick start, so errors name its variables.
	envOnly bool
}

type Site struct {
	Title    string
	Timezone string
	You      []string
}

type Forge struct {
	ID, Type, URL, Token string
	Owners               []string
	Heatmap              bool
}

// FileRef points at a file on disk, or at a file in a forge repo. Empty means
// not set.
type FileRef struct {
	File              string
	Forge, Repo, Path string
}

func (f FileRef) isSet() bool    { return f.File != "" || f.inRepo() }
func (f FileRef) inRepo() bool   { return f.Forge != "" || f.Repo != "" || f.Path != "" }
func (f FileRef) complete() bool { return f.Forge != "" && f.Repo != "" && f.Path != "" }

type Ideas struct {
	Labels []string
	File   FileRef
	Marker string
}

type NeedsYou struct {
	Labels                   []string
	Assigned, ReviewRequests bool
}

type Notes struct {
	Type    string
	Dir     string
	Markers []string
}

type Plans struct{ Dirs []string }

type Docker struct{ Feed, Host, Label string }

type Probes struct{ Homepages bool }

// file* types are the YAML shape. Booleans that default to true are pointers
// so "absent" and "false" differ.
type fileConfig struct {
	Listen         string       `yaml:"listen"`
	DataDir        string       `yaml:"data_dir"`
	Site           fileSite     `yaml:"site"`
	IncludePrivate *bool        `yaml:"include_private"`
	Forges         []fileForge  `yaml:"forges"`
	Projects       fileRef      `yaml:"projects"`
	Ideas          fileIdeas    `yaml:"ideas"`
	NeedsYou       fileNeedsYou `yaml:"needs_you"`
	Notes          fileNotes    `yaml:"notes"`
	Plans          filePlans    `yaml:"plans"`
	Docker         fileDocker   `yaml:"docker"`
	Probes         fileProbes   `yaml:"probes"`
}

type fileSite struct {
	Title    string   `yaml:"title"`
	Timezone string   `yaml:"timezone"`
	You      []string `yaml:"you"`
}

type fileForge struct {
	ID      string   `yaml:"id"`
	Type    string   `yaml:"type"`
	URL     string   `yaml:"url"`
	Token   string   `yaml:"token"`
	Owners  []string `yaml:"owners"`
	Heatmap *bool    `yaml:"heatmap"`
}

type fileRef struct {
	File  string `yaml:"file"`
	Forge string `yaml:"forge"`
	Repo  string `yaml:"repo"`
	Path  string `yaml:"path"`
}

type fileIdeas struct {
	Labels  []string `yaml:"labels"`
	fileRef `yaml:",inline"`
	Marker  string `yaml:"marker"`
}

type fileNeedsYou struct {
	Labels         []string `yaml:"labels"`
	Assigned       *bool    `yaml:"assigned"`
	ReviewRequests *bool    `yaml:"review_requests"`
}

type fileNotes struct {
	Type    string   `yaml:"type"`
	Dir     string   `yaml:"dir"`
	Markers []string `yaml:"markers"`
}

type filePlans struct {
	Dirs []string `yaml:"dirs"`
}

type fileDocker struct {
	Feed  string `yaml:"feed"`
	Host  string `yaml:"host"`
	Label string `yaml:"label"`
}

type fileProbes struct {
	Homepages *bool `yaml:"homepages"`
}

// ResolvePath picks the config file: WAYMARK_CONFIG if set, else
// /config/waymark.yml if it exists, else "" (env-only quick start).
func ResolvePath(getenv func(string) string, exists func(string) bool) string {
	if p := strings.TrimSpace(getenv("WAYMARK_CONFIG")); p != "" {
		return p
	}
	if exists(defaultConfigPath) {
		return defaultConfigPath
	}
	return ""
}

// FileExists reports whether path names an existing file; main passes it to
// ResolvePath.
func FileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Load reads the YAML file at path, or builds the env-only quick start when
// path is "". Unknown keys are an error. It does not call Validate.
func Load(path string, getenv func(string) string) (Config, error) {
	var fc fileConfig
	var forges []Forge
	var warnings []string
	if path == "" {
		forges = envForges(getenv)
	} else {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: %w", err)
		}
		// Decode with ${VAR} still literal, then expand string values only:
		// a type error cannot echo a secret, comments are never expanded, and
		// a value containing "#" or ": " cannot change the YAML structure.
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("config %s: %s", path, redactYAMLError(err))
		}
		warnings = unsetTokenVars(fc.Forges, getenv)
		expandStrings(reflect.ValueOf(&fc).Elem(), func(k string) string { return getenv(k) })
		for _, f := range fc.Forges {
			forges = append(forges, Forge{
				ID: f.ID, Type: f.Type, URL: f.URL, Token: f.Token,
				Owners: f.Owners, Heatmap: boolOr(f.Heatmap, true),
			})
		}
	}
	c := build(fc, forges, getenv)
	c.Warnings, c.envOnly = warnings, path == ""
	return c, nil
}

// unsetTokenVars names each variable in a forge's token that expands to
// nothing (a typo, or not passed to the container): the forge then runs
// without a token, which is legal but rarely meant. Never a value.
func unsetTokenVars(forges []fileForge, getenv func(string) string) []string {
	var out []string
	for i, f := range forges {
		seen := map[string]bool{}
		os.Expand(f.Token, func(k string) string {
			if !seen[k] && strings.TrimSpace(getenv(k)) == "" {
				seen[k] = true
				out = append(out, fmt.Sprintf("forges[%d].token: ${%s} is empty or not set, so forge %s has no token", i, k, strings.TrimSpace(f.ID)))
			}
			return ""
		})
	}
	return out
}

func build(fc fileConfig, forges []Forge, getenv func(string) string) Config {
	env := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	str := func(v, def string) string {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
		return def
	}
	list := func(v, def []string) []string {
		if v == nil {
			return def
		}
		return v
	}
	// Private repos show only when asked for: the page has no login.
	includePrivate, badEnv := false, []string(nil)
	if v := strings.TrimSpace(getenv("WAYMARK_INCLUDE_PRIVATE")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badEnv = append(badEnv, fmt.Sprintf("WAYMARK_INCLUDE_PRIVATE: %q is not true or false", v))
		}
		includePrivate = b
	}
	c := Config{
		Listen:         str(fc.Listen, env("WAYMARK_LISTEN", ":8080")),
		DataDir:        str(fc.DataDir, env("DATA_DIR", "/data")),
		IncludePrivate: boolOr(fc.IncludePrivate, includePrivate),
		badEnv:         badEnv,
		Forges:         forges,
		Projects:       FileRef(fc.Projects),
		Site: Site{
			Title:    str(fc.Site.Title, env("WAYMARK_TITLE", "Waymark")),
			Timezone: str(fc.Site.Timezone, env("TZ", "UTC")),
			You:      fc.Site.You,
		},
		Ideas: Ideas{
			Labels: list(fc.Ideas.Labels, []string{"idea", "enhancement"}),
			File:   FileRef(fc.Ideas.fileRef),
			Marker: str(fc.Ideas.Marker, "waymark-idea:"),
		},
		NeedsYou: NeedsYou{
			Labels:         list(fc.NeedsYou.Labels, []string{"needs-you", "decision"}),
			Assigned:       boolOr(fc.NeedsYou.Assigned, true),
			ReviewRequests: boolOr(fc.NeedsYou.ReviewRequests, true),
		},
		Notes: Notes{
			Type:    str(fc.Notes.Type, "none"),
			Dir:     strings.TrimSpace(fc.Notes.Dir),
			Markers: list(fc.Notes.Markers, []string{"WAITING:", "@me"}),
		},
		Plans:  Plans{Dirs: list(fc.Plans.Dirs, []string{"docs/plans", "docs/superpowers/plans"})},
		Docker: Docker{Feed: strings.TrimSpace(fc.Docker.Feed), Host: strings.TrimSpace(fc.Docker.Host), Label: str(fc.Docker.Label, "Docker")},
		Probes: Probes{Homepages: boolOr(fc.Probes.Homepages, true)},
	}
	for i := range c.Forges {
		f := &c.Forges[i]
		f.ID = strings.TrimSpace(f.ID)
		// forgejo stays forgejo (the page names it so); it is read as gitea.
		f.Type = strings.ToLower(strings.TrimSpace(f.Type))
		f.URL = strings.TrimRight(strings.TrimSpace(f.URL), "/")
		if f.Type == "github" && f.URL == "" {
			f.URL = githubAPI
		}
		f.Token = strings.TrimSpace(f.Token)
	}
	return c
}

func envForges(getenv func(string) string) []Forge {
	var out []Forge
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	if url, tok := get("GITEA_URL"), get("GITEA_TOKEN"); url != "" || tok != "" {
		out = append(out, Forge{ID: "gitea", Type: "gitea", URL: url, Token: tok, Owners: splitList(get("GITEA_OWNERS")), Heatmap: true})
	}
	if tok, owners := get("GITHUB_TOKEN"), get("GITHUB_OWNERS"); tok != "" || owners != "" {
		out = append(out, Forge{ID: "github", Type: "github", Token: tok, Owners: splitList(owners), Heatmap: true})
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// SourceNames are the app's source statuses besides one per forge id, in the
// order the site lists them (the first forge before them, the other forges
// after). No forge id may take one. Each call returns a fresh copy.
func SourceNames() []string {
	return []string{"registry", "heatmap", "ideas", "docker", "probes", "notes", "fdroid"}
}

// Validate checks the config. Errors name the field and never carry a token.
func (c Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	for _, e := range c.badEnv {
		bad("%s", e)
	}
	if _, err := time.LoadLocation(c.Site.Timezone); err != nil {
		bad("site.timezone: %q is not a known time zone (like UTC or America/New_York)", c.Site.Timezone)
	}
	if len(c.Forges) == 0 {
		bad("forges: at least one forge is required (set forges in the file, or GITEA_URL+GITEA_TOKEN or GITHUB_TOKEN)")
	}
	ids := map[string]bool{}
	for i, f := range c.Forges {
		at := fmt.Sprintf("forges[%d]", i)
		urlKey := at + ".url"
		if c.envOnly && f.ID == "gitea" {
			urlKey = "GITEA_URL"
		}
		switch {
		case f.ID == "":
			bad("%s.id: required", at)
		case slices.Contains(SourceNames(), f.ID):
			bad("%s.id: %q is the name of another source; choose another id", at, f.ID)
		case ids[f.ID]:
			bad("%s.id: duplicate forge id %q", at, f.ID)
		default:
			ids[f.ID] = true
		}
		switch f.Type {
		case "gitea", "forgejo":
			if f.URL == "" {
				bad("%s: required for type %s", urlKey, f.Type)
			} else if p := forgeURLProblem(f.URL, true); p != "" {
				bad("%s: %s", urlKey, p)
			}
		case "github":
			if p := forgeURLProblem(f.URL, false); f.URL != "" && p != "" {
				bad("%s: %s", urlKey, p)
			}
		default:
			bad("%s.type: %q is not one of gitea, forgejo, github", at, f.Type)
		}
	}

	checkRef := func(name string, r FileRef) {
		if r.File != "" && r.inRepo() {
			bad("%s: set either file or forge/repo/path, not both", name)
			return
		}
		if r.inRepo() {
			if !r.complete() {
				bad("%s: forge, repo and path must all be set together", name)
			} else if !ids[r.Forge] {
				bad("%s.forge: %q is not a configured forge id", name, r.Forge)
			}
		}
	}
	checkRef("projects", c.Projects)
	checkRef("ideas", c.Ideas.File)
	if !strings.ContainsFunc(c.Ideas.Marker, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		bad("ideas.marker: %q needs a letter or digit (filed ideas are searched for by it)", c.Ideas.Marker)
	}

	switch c.Notes.Type {
	case "none":
	case "markdown", "claude-memory":
		if c.Notes.Dir == "" {
			bad("notes.dir: required for type %s", c.Notes.Type)
		}
	default:
		bad("notes.type: %q is not one of none, markdown, claude-memory", c.Notes.Type)
	}
	if c.Docker.Feed != "" && c.Docker.Host != "" {
		bad("docker: set either feed or host, not both")
	}
	if c.Docker.Host != "" && !dockerHostOK(c.Docker.Host) {
		bad("docker.host: must be unix:///path/to/socket or tcp://host:port, with no credentials, path or query")
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

var backquoted = regexp.MustCompile("`[^`]*`")

// redactYAMLError drops the quoted values yaml.v3 puts in type errors.
func redactYAMLError(err error) string {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err.Error()
	}
	msgs := make([]string, len(te.Errors))
	for i, m := range te.Errors {
		msgs[i] = backquoted.ReplaceAllString(m, "`...`")
	}
	return "yaml: unmarshal errors:\n  " + strings.Join(msgs, "\n  ")
}

// expandStrings applies os.Expand to every string and []string reachable from v.
func expandStrings(v reflect.Value, mapping func(string) string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(os.Expand(v.String(), mapping))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			expandStrings(v.Field(i), mapping)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			expandStrings(v.Index(i), mapping)
		}
	}
}

// forgeURLProblem says what is wrong with a forge's url, or "". It never
// repeats the URL: it may hold a password. gitea: the site address, to which
// Waymark adds /api/v1 itself.
func forgeURLProblem(raw string, gitea bool) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "":
		return "must be an http:// or https:// address, such as https://git.example.com"
	case u.User != nil:
		return "must not hold a user name or password (the token goes in token)"
	case gitea && strings.HasSuffix(strings.ToLower(strings.TrimRight(u.Path, "/")), "/api/v1"):
		return "drop /api/v1: give the site address, and Waymark adds it"
	}
	return ""
}

// dockerHostOK reports whether host is unix:///path or tcp://host:port with
// nothing else. Errors about a bad host must not repeat it: it may carry a
// password.
func dockerHostOK(host string) bool {
	u, err := url.Parse(host)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	switch u.Scheme {
	case "unix":
		return u.Host == "" && u.Path != ""
	case "tcp":
		_, port, err := net.SplitHostPort(u.Host)
		return err == nil && port != "" && (u.Path == "" || u.Path == "/")
	}
	return false
}
