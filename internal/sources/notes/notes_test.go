package notes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/registry"
)

const dir = "../../../testdata/notes"

func TestParseIndex(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	idx := ParseIndex(string(b))
	cases := map[string]string{
		"beacon.md":                  "**v2.3.1 prod; v2.4.0 tagged + smoke PASS 2026-10-29, deploy awaits Mark** (release #24 incl. timeline #18); load test 3 findings open; open #17, #19, #21",
		"office_network.md":          "**two access point models shortlisted 2026-10-30** at the office (rack B), guest VLAN done; which model to order awaits Mark",
		"feedback_snapshot_first.md": "snapshot the database first; restore it on staging before the real run",
		"harbour.md":                 "**v3.1.0 released, prod on 3.1.0**; **arm64 build (#7) merged on main `9f8e7d6`, mirrored to GitHub; v3.2.0 release + deploy await Mark**; **GitHub #9 reply awaits Mark**",
		"user_release_notes.md":      "one changelog line per change, newest first, the version in bold (all repos)",
		// Two links on one line: each note's text is the line without its own link.
		"feedback_two_approvals.md": "Reviews: (main only); [no Friday deploys](feedback_no_friday_deploys.md)",
		"harbour_gc.md":             "harbour: ; known: untagged layers are skipped",
	}
	for file, want := range cases {
		if got := idx[file]; got != want {
			t.Errorf("%s:\n got %q\nwant %q", file, got, want)
		}
	}
}

func TestLoadFixtures(t *testing.T) {
	got, err := NewClaudeMemory(dir, nil).Load([]registry.Project{
		{Slug: "fixtures", Notes: []string{"beacon.md", "harbour.md", "ledger.md", "office_network.md", "nope.md", "../beacon.md"}},
	})
	if err == nil || !strings.HasSuffix(err.Error(), ": nope.md") {
		t.Errorf("err = %v, want only nope.md missing", err)
	}
	notes := map[string]Note{}
	for _, n := range got["fixtures"] {
		notes[n.File] = n
	}
	if len(got["fixtures"]) != 4 {
		t.Errorf("notes = %q, want 4 (../beacon.md is beacon.md, once)", files(got["fixtures"]))
	}
	b := notes["beacon.md"]
	if b.Name != "beacon" || !b.Modified.Equal(time.Date(2026, 10, 31, 19, 30, 25, 607000000, time.UTC)) {
		t.Errorf("beacon = %q %v (modified sits under metadata:)", b.Name, b.Modified)
	}
	if !strings.HasPrefix(b.IndexLine, "**v2.3.1 prod;") {
		t.Errorf("index line = %q", b.IndexLine)
	}
	if b.HeadingDate.Format("2006-01-02") != "2026-10-31" || b.Heading != "v2.4.0 TAGGED + smoke PASS, NOT DEPLOYED (waits for Mark). Production still v2.3.1." {
		t.Errorf("heading = %v %q", b.HeadingDate, b.Heading)
	}

	h := notes["harbour.md"]
	// harbour.md has modified at the top level and an unquoted description
	// containing ": ", which strict YAML refuses.
	if !h.Modified.Equal(time.Date(2026, 10, 26, 19, 16, 45, 933000000, time.UTC)) {
		t.Errorf("harbour modified = %v", h.Modified)
	}
	if !strings.HasPrefix(h.Description, "Harbour, the team's container registry") || !strings.HasSuffix(h.Description, "…") {
		t.Errorf("harbour description = %q", h.Description)
	}
	if n := utf8.RuneCountInString(h.Description); n > DescriptionRunes+1 {
		t.Errorf("description runes = %d", n)
	}
	if strings.Count(h.Description, "**")%2 != 0 {
		t.Errorf("unbalanced bold left in %q", h.Description)
	}
	if h.HeadingDate.Format("2006-01-02") != "2026-10-26" || !strings.HasPrefix(h.Heading, "arm64 build MERGED") {
		t.Errorf("harbour heading = %v %q", h.HeadingDate, h.Heading)
	}

	on := notes["office_network.md"]
	if on.Name != "office-network" || on.Description != "Office network kit (switches, access points): new access points shortlisted 2026-10-30, guest VLAN done; open items for Mark" {
		t.Errorf("office network = %+v", on)
	}
	if on.Heading != "" {
		t.Errorf("office network has no dated heading, got %q", on.Heading)
	}
	// "metadata: " with a trailing space still opens the nested block.
	if l := notes["ledger.md"]; !l.Modified.Equal(time.Date(2026, 10, 30, 17, 26, 0, 713000000, time.UTC)) || l.Description != "Ledger, the billing API: invoices, credit notes and VAT" {
		t.Errorf("ledger = %v %q", l.Modified, l.Description)
	}
	if _, ok := notes["../beacon.md"]; ok {
		t.Error("paths must be reduced to their base name")
	}
}

func TestFrontmatterVariants(t *testing.T) {
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name, in, desc string
		mod            time.Time
		dateOnly       bool
	}{
		{"no frontmatter", "# Title\n", "", mtime, false},
		{"unclosed", "---\nname: x\n", "", mtime, false},
		{"single quoted, date only", "---\ndescription: 'it''s fine'\nmodified: 2026-10-01\n---\n", "it's fine", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), true},
		{"full timestamp", "---\nmodified: 2026-10-01T09:30:00Z\n---\n", "", time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), false},
		{"folded", "---\ndescription: >\n  first line\n  second line\n---\n", "first line second line", mtime, false},
		{"bad modified", "---\nmodified: yesterday\n---\n", "", mtime, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := ParseNote("x.md", []byte(tc.in), mtime, nil)
			if n.Description != tc.desc || !n.Modified.Equal(tc.mod) || n.Name != "x" && tc.name != "unclosed" {
				t.Errorf("got %+v", n)
			}
			if n.ModifiedDateOnly != tc.dateOnly {
				t.Errorf("date only = %v, want %v", n.ModifiedDateOnly, tc.dateOnly)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"one two three four", 9, "one two…"},
		{"**bold run** then more words", 8, "bold…"},
		{"a `code span here` x", 10, "a code…"},
		{"x  y\n z", 10, "x y z"},
	}
	for _, tc := range cases {
		if got := Truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestSignatureChanges(t *testing.T) {
	d := t.TempDir()
	ad := NewClaudeMemory(d, []string{"Mark"})
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(d, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("MEMORY.md", "- [a](a.md): one\n")
	write("a.md", "x")
	s1 := ad.Signature()
	write("a.md", "longer")
	s2 := ad.Signature()
	if s1 == s2 {
		t.Error("signature did not change")
	}
	write("MEMORY.md", "- [a](a.md): one, awaits Mark\n")
	if s3 := ad.Signature(); s3 == s2 {
		t.Error("an edited index did not change the signature")
	}
}

// memoryDir is a synthetic Claude memory folder: an index and three notes.
func memoryDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	files := map[string]string{
		"MEMORY.md": "# Memory index\n\n" +
			"- [Acme web](acme_web.md): **v1.4.0 live**; rename awaits Mark\n" +
			"- [Acme API](api_notes.md): key rotation waits for Mark\n" +
			"- [Acme app](acme_app.md): store build 12 (v2.1.0, crash fix) since 2026-10-25 (`build-10`, `build-11` still listed, removal awaits Mark)\n" +
			"- [Not claimed](acme_cli.md): no project reads this, awaits Mark\n",
		"acme_web.md": "---\nname: acme-web\ndescription: The Acme shop front\nmetadata:\n  modified: 2026-10-30T10:00:00Z\n---\n" +
			"## 2026-10-29 older entry, awaits Mark\n\n## 2026-10-30 (pm) v1.5.0 tagged, deploy awaits Mark\n",
		"api_notes.md": "plain note, no front matter\n",
		"acme_app.md":  "---\nmodified: 2026-10-25\n---\n",
		"acme_cli.md":  "unclaimed\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

var memoryProjects = []registry.Project{
	{Slug: "acme-web"},
	{Slug: "acme-api", Notes: []string{"api_notes.md", "gone.md"}},
	{Slug: "acme-app"},
	{Slug: "acme-shop", Notes: []string{"acme_web.md"}},
	{Slug: "acme-docs"}, // its candidate acme_docs.md is not there: not an error
}

// The v0.1 matching: a project's notes are its projects-file list, else the
// slug with - as _; only a listed note that is missing is reported.
func TestClaudeMemoryMatchesByNameOrList(t *testing.T) {
	ad := NewClaudeMemory(memoryDir(t), []string{"Mark"})
	if ad.Kind() != "claude-memory" || ad.Label() != "Claude's notes" {
		t.Errorf("kind %q label %q", ad.Kind(), ad.Label())
	}
	got, err := ad.Load(memoryProjects)
	if err == nil || !strings.HasSuffix(err.Error(), ": gone.md") {
		t.Errorf("err = %v, want gone.md reported", err)
	}
	want := map[string][]string{
		"acme-web":  {"acme_web.md"},
		"acme-api":  {"api_notes.md"},
		"acme-app":  {"acme_app.md"},
		"acme-shop": {"acme_web.md"}, // one note, two projects
	}
	for slug, w := range want {
		if f := files(got[slug]); !reflect.DeepEqual(f, w) {
			t.Errorf("%s: files %q, want %q", slug, f, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("projects with notes = %d, want %d", len(got), len(want))
	}
	web := got["acme-web"][0]
	if web.Project != "acme-web" || got["acme-shop"][0].Project != "acme-shop" {
		t.Errorf("projects: %q and %q", web.Project, got["acme-shop"][0].Project)
	}
	if web.IndexLine != "**v1.4.0 live**; rename awaits Mark" || web.Heading != "v1.5.0 tagged, deploy awaits Mark" ||
		!web.Modified.Equal(time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)) || web.Body != "" {
		t.Errorf("acme_web.md = %+v", web)
	}
	clauses := map[string][]string{
		"acme-web": {"rename awaits Mark", "v1.5.0 tagged, deploy awaits Mark"},
		"acme-api": {"key rotation waits for Mark"},
		// The wait is only in the trailing aside, which says what waits.
		"acme-app": {"build-10, build-11 still listed, removal awaits Mark"},
	}
	for slug, w := range clauses {
		if c := got[slug][0].Clauses; !reflect.DeepEqual(c, w) {
			t.Errorf("%s: clauses %q, want %q", slug, c, w)
		}
	}

	// No index: nothing read.
	if ns, err := NewClaudeMemory(t.TempDir(), []string{"Mark"}).Load(memoryProjects); err == nil || ns != nil {
		t.Errorf("no MEMORY.md: %v %v", ns, err)
	}
}

func TestClaudeMemoryNoNamesNoClauses(t *testing.T) {
	d := memoryDir(t)
	for _, you := range [][]string{nil, {}, {"", "  "}} {
		got, _ := NewClaudeMemory(d, you).Load(memoryProjects)
		if len(got["acme-web"]) != 1 || got["acme-web"][0].IndexLine == "" {
			t.Fatalf("you %q: the notes must still be read: %+v", you, got)
		}
		for slug, ns := range got {
			for _, n := range ns {
				if len(n.Clauses) != 0 {
					t.Errorf("you %q: %s %s has clauses %q", you, slug, n.File, n.Clauses)
				}
			}
		}
	}
}

func TestNeedsYouReOtherName(t *testing.T) {
	sam, mark := NeedsYouRe([]string{"Sam"}), NeedsYouRe([]string{"Mark"})
	if !sam.MatchString("awaits Sam") || sam.MatchString("awaits Mark") {
		t.Error("you: [Sam] must match awaits Sam and only Sam")
	}
	if mark.MatchString("awaits Sam") {
		t.Error("you: [Mark] matched awaits Sam")
	}
	both := NeedsYouRe([]string{"Mark", "Sam"})
	if !both.MatchString("awaits Mark") || !both.MatchString("needs SAM") {
		t.Error("two names: each must match")
	}
	// A name is matched as written, never as a pattern.
	if aj := NeedsYouRe([]string{"A.J"}); !aj.MatchString("awaits A.J") || aj.MatchString("awaits AxJ") {
		t.Error("A.J must be matched literally")
	}
	if NeedsYouRe(nil) != nil || NeedsYouRe([]string{"", "  "}) != nil {
		t.Error("no names must give no pattern")
	}

	// A name that does not start and end with an ASCII letter or digit
	// still matches in every v0.1 phrasing, and only as a whole word.
	cases := []struct {
		names []string
		text  string
		want  bool
	}{
		{[]string{"José"}, "removal awaits José", true},
		{[]string{"José"}, "needs JOSÉ to pick a name", true},
		{[]string{"José"}, "José's call on the beta", true},
		{[]string{"José"}, "a test for José", true},
		{[]string{"José"}, "José, awaiting the merge", true},
		{[]string{"José"}, "removal awaits josé", false},
		{[]string{"José"}, "removal awaits Josélito", false},
		{[]string{"Zoë"}, "deploy awaits Zoë.", true},
		{[]string{"Zoë"}, "deploy awaits ZOË", true},
		{[]string{"Zoë"}, "deploy awaits Zoëy", false},
		{[]string{"A.J."}, "deploy awaits A.J.", true},
		{[]string{"A.J."}, "waits for A.J. (after lunch)", true},
		{[]string{"A.J."}, "deploy awaits AxJx", false},
		{[]string{"Mark", "José"}, "awaits Mark", true},
		{[]string{"Mark", "José"}, "awaits José", true},
		{[]string{"Mark", "José"}, "we will mark it, awaiting reply", false},
	}
	for _, tc := range cases {
		if got := NeedsYouRe(tc.names).MatchString(tc.text); got != tc.want {
			t.Errorf("you %q, %q: %v, want %v", tc.names, tc.text, got, tc.want)
		}
	}
	if got := noteClauses(NeedsYouRe([]string{"Zoë"}), "v2 live; removal awaits Zoë (after the sweep)"); len(got) != 1 || got[0] != "removal awaits Zoë" {
		t.Errorf("clauses for Zoë = %q", got)
	}
}

// The v0.1 pattern, parameterised: the name as written or in capitals.
func TestNeedsYouReKeepsTheV01Shape(t *testing.T) {
	want := `(?i:\bawait(s|ing|ed)?\b).*\b(Mark|MARK)\b|\b(Mark|MARK)\b.*(?i:\bawait)|(?i:\bneeds?) (Mark|MARK)\b|\b(Mark|MARK)'?s\b[^;.]{0,30}?\b(ruling|decision|call|tests?|approval|hands-on|sign-off|go-ahead)\b|(?i:\b(waits? )?for) (Mark|MARK)\b`
	if got := NeedsYouRe([]string{"Mark"}).String(); got != want {
		t.Errorf("pattern:\n got %s\nwant %s", got, want)
	}
}

// The v0.1 clause rules, with the name Mark: also an English verb, so the
// case rule ("we will mark it") is still tested.
func TestNoteClauses(t *testing.T) {
	re := NeedsYouRe([]string{"Mark"})
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "index line and heading",
			in: []string{
				"**v1.4.0 prod; v1.5.0 tagged + smoke PASS 2026-10-26, deploy awaits Mark** (release #24 incl. timeline #18); load test 3 findings open; open #17, #19, #21",
				"v1.5.0 TAGGED + prod-copy smoke PASS, NOT DEPLOYED (waits for Mark). Prod still v1.4.0.",
			},
			want: []string{
				"v1.5.0 tagged + smoke PASS 2026-10-26, deploy awaits Mark",
				"v1.5.0 TAGGED + prod-copy smoke PASS, NOT DEPLOYED (waits for Mark).",
			},
		},
		{
			name: "two clauses, wiki and code markup stripped",
			in:   []string{"**v3.1.0 released, prod on 3.1.0**; **arm64 build (#7) merged on main `9f8e7d6`, mirrored to GitHub; v3.2.0 release + deploy await Mark**; **GitHub #9 reply awaits Mark**"},
			want: []string{"v3.2.0 release + deploy await Mark", "GitHub #9 reply awaits Mark"},
		},
		{
			name: "phrasings",
			in:   []string{"Needs Mark to pick a name. Mark's ruling pending; a test for Mark; Mark's hands-on check due! Mark said yes; awaiting reply"},
			want: []string{"Needs Mark to pick a name.", "Mark's ruling pending", "a test for Mark", "Mark's hands-on check due!"},
		},
		{
			name: "a test named in between",
			in:   []string{"open: Mark's hardware key test (`k-310`); Mark's laptop is fine"},
			want: []string{"open: Mark's hardware key test"},
		},
		{
			name: "the wait sits only in a trailing aside: keep the aside",
			in:   []string{"store build 12 (v2.1.0, crash fix) since 2026-10-25 (`build-10`, `build-11` still listed, removal awaits Mark)"},
			want: []string{"build-10, build-11 still listed, removal awaits Mark"},
		},
		{
			name: "an await counts only when it names Mark",
			in:   []string{"posted forums.example.com/t/1234, awaiting reply; removal awaits Mark; awaiting Mark's decisions; it will await the merge; AWAITS MARK"},
			want: []string{"removal awaits Mark", "awaiting Mark's decisions", "AWAITS MARK"},
		},
		{
			name: "a trailing aside is dropped only when the rest still matches",
			in:   []string{"x waits for Mark (because y); z (for Mark)"},
			want: []string{"x waits for Mark", "z (for Mark)"},
		},
		{
			name: "duplicates dropped, case-insensitively",
			in:   []string{"a awaits Mark; b awaits Mark; A AWAITS Mark; c awaits Mark"},
			want: []string{"a awaits Mark", "b awaits Mark", "c awaits Mark"},
		},
		{
			name: "no match: the verb mark, await-free",
			in:   []string{"Mark is marking; awaitable things; we will mark it; it awaits a mark; needs marking"},
			want: nil,
		},
	}
	for _, tc := range cases {
		got := noteClauses(re, tc.in...)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %q", tc.name, got)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: [%d] = %q, want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
	if got := noteClauses(nil, "removal awaits Mark"); got != nil {
		t.Errorf("no names: %q", got)
	}
}
