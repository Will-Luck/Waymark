// Package registry parses the projects file (0.1.0's registry.yaml format)
// and the ideas file (ideas.json): identity only (which
// repos form which project, names, icons, containers, URLs, relations and a
// deliberate hold). State is never read from here.
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Will-Luck/waymark/internal/forge"
)

// UnsortedKey is the category of every repo the projects file does not mention.
const UnsortedKey = "unsorted"

var unsorted = Category{Key: UnsortedKey, Label: "Unsorted", Icon: "folder-git-2", Hue: 265}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Registry struct {
	Owner      string     `yaml:"owner"`
	Categories []Category `yaml:"categories"`
	HideRepos  []string   `yaml:"hide_repos"`
	Projects   []Project  `yaml:"projects"`
	// Warnings are problems that did not stop the parse (unknown category).
	Warnings []string `yaml:"-"`
}

type Category struct {
	Key   string `yaml:"key"`
	Label string `yaml:"label"`
	Icon  string `yaml:"icon"`
	Hue   int    `yaml:"hue"`
}

type Project struct {
	Slug       string     `yaml:"slug"`
	Name       string     `yaml:"name"`
	Category   string     `yaml:"category"`
	Icon       string     `yaml:"icon"`
	Summary    string     `yaml:"summary"`
	What       string     `yaml:"what"`
	Stack      []string   `yaml:"stack"`
	RunsOn     string     `yaml:"runs_on"`
	Repos      []RepoRef  `yaml:"repos"`
	Containers []string   `yaml:"containers"`
	FDroid     *FDroid    `yaml:"fdroid"`
	URLs       []URL      `yaml:"urls"`
	Notes      []string   `yaml:"notes"`
	Relations  []Relation `yaml:"relations"`
	Hold       *Hold      `yaml:"hold"`
	Retired    bool       `yaml:"retired"`
	// PlanDirs overrides the configured plan directories for this project.
	PlanDirs []string `yaml:"plan_dirs"`
	// Auto marks a project made from a repo the projects file does not name.
	Auto bool `yaml:"-"`
}

// RepoRef is one repos: entry: a one-key map from a forge id to owner/name
// ("- gitea: acme/tool"). FullName is always owner/name after Parse.
type RepoRef struct{ Forge, FullName string }

func (r *RepoRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 || n.Content[1].Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a repos entry is one forge id and one repo, like \"- gitea: owner/name\"", n.Line)
	}
	r.Forge, r.FullName = n.Content[0].Value, strings.TrimSpace(n.Content[1].Value)
	if r.Forge == "" || r.FullName == "" {
		return fmt.Errorf("line %d: a repos entry needs a forge id and a repo name", n.Line)
	}
	return nil
}

type FDroid struct {
	Index   string `yaml:"index"`
	Package string `yaml:"package"`
}

type URL struct {
	Label string `yaml:"label"`
	URL   string `yaml:"url"`
	Probe string `yaml:"probe"`
}

type Relation struct {
	To   string `yaml:"to"`
	Kind string `yaml:"kind"`
}

type Hold struct {
	Kind     string    `yaml:"kind"`
	SinceRaw string    `yaml:"since"`
	Reason   string    `yaml:"reason"`
	Since    time.Time `yaml:"-"`
}

// Empty is the registry when no projects file is configured or readable: no
// projects, only the unsorted category.
func Empty() *Registry { return &Registry{Categories: []Category{unsorted}} }

// Parse reads the projects file, applies defaults and validates it. A repos
// key must be one of forgeIDs. The unsorted category is always appended last.
func Parse(b []byte, forgeIDs []string) (*Registry, error) {
	var r Registry
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("projects file: %w", err)
	}
	cats := map[string]Category{}
	var kept []Category
	for _, c := range r.Categories {
		if c.Key == "" || c.Key == UnsortedKey {
			continue
		}
		if _, dup := cats[c.Key]; dup {
			return nil, fmt.Errorf("projects file: category %q listed twice", c.Key)
		}
		if c.Label == "" {
			c.Label = c.Key
		}
		cats[c.Key] = c
		kept = append(kept, c)
	}
	r.Categories = append(kept, unsorted)
	cats[UnsortedKey] = unsorted

	seen := map[string]bool{}
	for i := range r.Projects {
		p := &r.Projects[i]
		if p.Slug == "" {
			return nil, fmt.Errorf("projects file: project %d has no slug", i+1)
		}
		if !slugRe.MatchString(p.Slug) {
			return nil, fmt.Errorf("projects file: slug %q must be lowercase letters, digits and dashes", p.Slug)
		}
		if seen[p.Slug] {
			return nil, fmt.Errorf("projects file: slug %q listed twice", p.Slug)
		}
		seen[p.Slug] = true
		for j := range p.Repos {
			ref := &p.Repos[j]
			if !slices.Contains(forgeIDs, ref.Forge) {
				return nil, fmt.Errorf("projects file: project %s: repo key %q is not a configured forge (have: %s)", p.Slug, ref.Forge, strings.Join(forgeIDs, ", "))
			}
			if !strings.Contains(ref.FullName, "/") {
				if r.Owner == "" {
					return nil, fmt.Errorf("projects file: project %s: repo %q needs owner/name, or an owner: at the top of the file", p.Slug, ref.FullName)
				}
				ref.FullName = r.Owner + "/" + ref.FullName
			}
		}
		if p.Name == "" {
			p.Name = p.Slug
		}
		c, ok := cats[p.Category]
		if !ok {
			if p.Category != "" {
				r.Warnings = append(r.Warnings, fmt.Sprintf("project %s: unknown category %q, filed as unsorted", p.Slug, p.Category))
			}
			p.Category = UnsortedKey
			c = unsorted
		}
		if p.Icon == "" {
			p.Icon = c.Icon
		}
		if p.Hold != nil {
			switch p.Hold.Kind {
			case "parked", "frozen":
			default:
				return nil, fmt.Errorf("projects file: project %s: hold kind %q must be parked or frozen", p.Slug, p.Hold.Kind)
			}
			t, err := time.Parse("2006-01-02", p.Hold.SinceRaw)
			if err != nil {
				return nil, fmt.Errorf("projects file: project %s: hold since %q is not YYYY-MM-DD", p.Slug, p.Hold.SinceRaw)
			}
			p.Hold.Since = t
		}
	}
	return &r, nil
}

// Hidden reports whether a repo name is in hide_repos.
func (r *Registry) Hidden(name string) bool {
	for _, h := range r.HideRepos {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// Project returns the registry entry with that slug, or nil.
func (r *Registry) Project(slug string) *Project {
	for i := range r.Projects {
		if r.Projects[i].Slug == slug {
			return &r.Projects[i]
		}
	}
	return nil
}

// Category returns the category with that key, falling back to unsorted.
func (r *Registry) Category(key string) Category {
	for _, c := range r.Categories {
		if c.Key == key {
			return c
		}
	}
	return unsorted
}

// Primary is the project's first repo (tags, releases, CI, changelog, plans),
// or the zero value when it has none.
func (p *Project) Primary() RepoRef {
	if len(p.Repos) == 0 {
		return RepoRef{}
	}
	return p.Repos[0]
}

// Mirrors are the repos on forges other than the primary's.
func (p *Project) Mirrors() []RepoRef {
	prim := p.Primary()
	var out []RepoRef
	for _, ref := range p.Repos {
		if ref.Forge != prim.Forge {
			out = append(out, ref)
		}
	}
	return out
}

// FullRepos are every repo on the primary's forge, primary first; these get
// full detail (a project can span several repos on one forge).
func (p *Project) FullRepos() []RepoRef {
	prim := p.Primary()
	var out []RepoRef
	for _, ref := range p.Repos {
		if ref.Forge == prim.Forge {
			out = append(out, ref)
		}
	}
	return out
}

// FirstOn is the project's first repo on one forge, or the zero value.
func (p *Project) FirstOn(forgeID string) RepoRef {
	for _, ref := range p.Repos {
		if ref.Forge == forgeID {
			return ref
		}
	}
	return RepoRef{}
}

func refKey(forgeID, full string) string { return forgeID + "|" + strings.ToLower(full) }

func repoName(full string) string {
	_, name, _ := strings.Cut(full, "/")
	return name
}

func repoOwner(full string) string {
	owner, _, _ := strings.Cut(full, "/")
	return owner
}

// claim works out which listed repos the file already accounts for. A repo it
// names is claimed. An unclaimed repo whose name matches (case-insensitively)
// a repo of a file project on another forge, where that project has no repo on
// the unclaimed repo's forge, is attached to that project and claimed too.
// The returned projects are copies of the file's, attachments appended; the
// stored registry is never changed.
func (r *Registry) claim(repos []forge.Repo) (projects []Project, claimed map[string]bool) {
	claimed = map[string]bool{}
	projects = make([]Project, len(r.Projects))
	for i, p := range r.Projects {
		p.Repos = slices.Clone(p.Repos)
		projects[i] = p
		for _, ref := range p.Repos {
			claimed[refKey(ref.Forge, ref.FullName)] = true
		}
	}
	for _, repo := range repos {
		if r.Hidden(repo.Name) || claimed[refKey(repo.Forge, repo.FullName)] {
			continue
		}
		for i := range projects {
			p := &projects[i]
			if p.FirstOn(repo.Forge) != (RepoRef{}) {
				continue
			}
			if slices.ContainsFunc(p.Repos, func(o RepoRef) bool { return strings.EqualFold(repoName(o.FullName), repo.Name) }) {
				p.Repos = append(p.Repos, RepoRef{Forge: repo.Forge, FullName: repo.FullName})
				claimed[refKey(repo.Forge, repo.FullName)] = true
				break
			}
		}
	}
	return projects, claimed
}

// AllProjects is the file's projects (unclaimed twins attached, as copies)
// followed by the auto projects.
func (r *Registry) AllProjects(repos []forge.Repo, forgeOrder []string) []Project {
	ps, _ := r.claim(repos)
	return append(ps, r.AutoProjects(repos, forgeOrder)...)
}

// AutoProjects returns a project for every repo that is neither hidden, named
// by a file project nor attached to one, in the order given. Repos with the
// same name (case-insensitive) on different forges pair into one project: the
// primary is the forge earliest in forgeOrder, except that a repo flagged as a
// pull mirror never becomes primary while its twin is not one. Of several
// same-named repos on one forge, the one with the twin's owner pairs.
func (r *Registry) AutoProjects(repos []forge.Repo, forgeOrder []string) []Project {
	_, claimed := r.claim(repos)
	slugs := map[string]bool{}
	for i := range r.Projects {
		slugs[r.Projects[i].Slug] = true
	}
	rank := func(id string) int {
		if i := slices.Index(forgeOrder, id); i >= 0 {
			return i
		}
		return len(forgeOrder)
	}
	// One group per name; a forge holds one repo of a group, so two repos of
	// the same name on one forge stay two projects.
	var groups [][]forge.Repo
	byName := map[string][]int{}
	for _, repo := range repos {
		if r.Hidden(repo.Name) || claimed[refKey(repo.Forge, repo.FullName)] {
			continue
		}
		key := strings.ToLower(repo.Name)
		target := -1
		for _, gi := range byName[key] {
			if slices.ContainsFunc(groups[gi], func(o forge.Repo) bool { return o.Forge == repo.Forge }) {
				continue
			}
			if target < 0 {
				target = gi
			}
			if slices.ContainsFunc(groups[gi], func(o forge.Repo) bool { return strings.EqualFold(repoOwner(o.FullName), repoOwner(repo.FullName)) }) {
				target = gi
				break
			}
		}
		if target >= 0 {
			groups[target] = append(groups[target], repo)
		} else {
			byName[key] = append(byName[key], len(groups))
			groups = append(groups, []forge.Repo{repo})
		}
	}
	var out []Project
	for _, g := range groups {
		slices.SortStableFunc(g, func(a, b forge.Repo) int {
			if a.Mirror != b.Mirror {
				if a.Mirror {
					return 1
				}
				return -1
			}
			return rank(a.Forge) - rank(b.Forge)
		})
		primary := g[0]
		slug := Slugify(primary.Name)
		for slugs[slug] {
			slug += "-repo"
		}
		slugs[slug] = true
		summary := strings.TrimSpace(primary.Description)
		if summary == "" {
			summary = "No description yet."
		}
		refs := make([]RepoRef, len(g))
		for i, repo := range g {
			refs[i] = RepoRef{Forge: repo.Forge, FullName: repo.FullName}
		}
		out = append(out, Project{
			Slug:     slug,
			Name:     primary.Name,
			Category: UnsortedKey,
			Icon:     unsorted.Icon,
			Summary:  summary,
			Repos:    refs,
			Auto:     true,
		})
	}
	return out
}

// Slugify lowercases a repo name and keeps it a valid slug.
func Slugify(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "repo"
	}
	return s
}

// Ideas is ideas.json: a dated snapshot from a roundup.
type Ideas struct {
	RoundupDate string `json:"roundup_date"`
	Ideas       []Idea `json:"ideas"`
}

type Idea struct {
	ID            string `json:"id"`
	Project       string `json:"project"`
	Title         string `json:"title"`
	Pitch         string `json:"pitch"`
	Why           string `json:"why"`
	Size          string `json:"size"`
	Kind          string `json:"kind"`
	ExistingIssue int    `json:"existing_issue"`
	// Source is where the idea came from: "file" (ideas.json; derive marks
	// them) or "issue" (an open issue with an ideas label: ExistingIssue is
	// its number, URL the issue). The file cannot set either.
	Source string `json:"-"`
	URL    string `json:"-"`
}

// ParseIdeas reads ideas.json. Every idea needs a unique id.
func ParseIdeas(b []byte) (*Ideas, error) {
	var ideas Ideas
	if err := json.Unmarshal(b, &ideas); err != nil {
		return nil, fmt.Errorf("ideas.json: %w", err)
	}
	seen := map[string]bool{}
	for i, idea := range ideas.Ideas {
		if idea.ID == "" {
			return nil, fmt.Errorf("ideas.json: idea %d has no id", i+1)
		}
		if seen[idea.ID] {
			return nil, fmt.Errorf("ideas.json: id %q listed twice", idea.ID)
		}
		seen[idea.ID] = true
	}
	return &ideas, nil
}
