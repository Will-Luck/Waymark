package mdlite

import "testing"

func TestRender(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"**v2.3.1 prod**; next", "<strong>v2.3.1 prod</strong>; next"},
		{"main `a1b2c3d` has fixes", "main <code>a1b2c3d</code> has fixes"},
		{"see [Gitea](https://git.example.com/x)", `see <a href="https://git.example.com/x" rel="noopener noreferrer">Gitea</a>`},
		{"[[beacon]] link", "beacon link"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"[x](javascript:alert(1))", "[x](javascript:alert(1))"},
		{`[x](https://a.b/"onmouseover=alert(1))`, `[x](https://a.b/&#34;onmouseover=alert(1))`},
		{`[x](https://a.b/"q)`, `<a href="https://a.b/&#34;q" rel="noopener noreferrer">x</a>`},
		{"`**not bold**`", "<code>**not bold**</code>"},
		{"`<b>`", "<code>&lt;b&gt;</code>"},
		{"unclosed `tick **b**", "unclosed `tick <strong>b</strong>"},
		{"a & b", "a &amp; b"},
		{"**key-only (`ssh web-01`)**; sudo", "<strong>key-only (<code>ssh web-01</code>)</strong>; sudo"},
		{"see ([maintenance window](feedback_maintenance_window.md))", "see (maintenance window)"},
		{"[x](javascript:alert)", "[x](javascript:alert)"},
		{"[x](https://a/`b`)", "[x](https://a/<code>b</code>)"},
		{"nul\x00 0\x00 byte", "nul 0 byte"},
	}
	for _, c := range cases {
		if got := string(Render(c.in)); got != c.want {
			t.Errorf("Render(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestPlain(t *testing.T) {
	if got := Plain("**v1** at [site](https://x.y) and `code` [[note]] ([more](note.md))"); got != "v1 at site and code note (more)" {
		t.Fatalf("Plain = %q", got)
	}
}
