package config

import (
	"bytes"
	"encoding"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Will-Luck/waymark/internal/registry"
)

const configDoc = "../../docs/configuration.md"

// Types that decode themselves from one value: each is a leaf key.
var selfDecoding = []reflect.Type{
	reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem(),
	reflect.TypeOf((*json.Unmarshaler)(nil)).Elem(),
	reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem(),
}

// docKeys lists the dotted path of every leaf key a struct decodes from, by
// its tag (yaml or json): "site.title", "forges[].token". An inline field
// adds no segment, a slice of structs adds "[]", and a type that is not a
// record (see isRecord) is a leaf. A record that yields no key fails the
// test: its key would otherwise go unchecked.
func docKeys(t *testing.T, typ reflect.Type, tag, prefix string) []string {
	t.Helper()
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get(tag), ",")
		if name == "-" {
			continue
		}
		if slices.Contains(strings.Split(opts, ","), "inline") {
			out = append(out, docKeys(t, f.Type, tag, prefix)...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
			if tag == "yaml" {
				name = strings.ToLower(name)
			}
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := deref(f.Type)
		if ft.Kind() == reflect.Slice {
			if el := deref(ft.Elem()); isRecord(el) {
				out = append(out, docKeys(t, el, tag, path+"[]")...)
				continue
			}
		}
		if isRecord(ft) {
			out = append(out, docKeys(t, ft, tag, path)...)
			continue
		}
		out = append(out, path)
	}
	if len(out) == 0 {
		t.Errorf("%s (%s) yields no keys: make it a leaf in isRecord, or give it a key", typ, prefix)
	}
	return out
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// isRecord: a struct whose fields are keys. time.Time, a struct with no
// exported field and a type that decodes itself (yaml, json or text
// unmarshaler) are leaves.
func isRecord(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) {
		return false
	}
	for _, u := range selfDecoding {
		if t.Implements(u) || reflect.PointerTo(t).Implements(u) {
			return false
		}
	}
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.IsExported() || f.Anonymous {
			return true
		}
	}
	return false
}

// Every key of the config file, the projects file and the ideas file, and
// every environment variable the config reads, is written in
// docs/configuration.md as `its.full.path`. A key added without docs fails.
func TestDocsListEveryKey(t *testing.T) {
	doc, err := os.ReadFile(configDoc)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		file string
		typ  reflect.Type
		tag  string
		// some is a sample the walk must find, so a broken walk cannot pass.
		some []string
	}{
		{"config file", reflect.TypeOf(fileConfig{}), "yaml",
			[]string{"listen", "site.you", "include_private", "forges[].token", "forges[].heatmap", "projects.file", "ideas.repo", "ideas.marker", "needs_you.review_requests", "docker.host", "probes.homepages"}},
		{"projects file", reflect.TypeOf(registry.Registry{}), "yaml",
			[]string{"owner", "categories[].hue", "hide_repos", "projects[].repos", "projects[].fdroid.package", "projects[].urls[].probe", "projects[].hold.since", "projects[].relations[].kind", "projects[].plan_dirs"}},
		{"ideas file", reflect.TypeOf(registry.Ideas{}), "json",
			[]string{"roundup_date", "ideas[].id", "ideas[].existing_issue"}},
	} {
		keys := docKeys(t, c.typ, c.tag, "")
		for _, k := range c.some {
			if !slices.Contains(keys, k) {
				t.Errorf("%s: the walk missed %s (found %v)", c.file, k, keys)
			}
		}
		for _, k := range keys {
			if strings.Contains(k, "warnings") || strings.HasSuffix(k, ".auto") || k == "ideas[].source" || k == "ideas[].url" {
				t.Errorf("%s: %s is not a key the file can set", c.file, k)
			}
			if !bytes.Contains(doc, []byte("`"+k+"`")) {
				t.Errorf("%s key `%s` is not in %s", c.file, k, configDoc)
			}
		}
	}

	// Every variable set, so every branch that reads one runs.
	asked := map[string]bool{}
	getenv := func(k string) string { asked[k] = true; return "x" }
	ResolvePath(getenv, func(string) bool { return false })
	if _, err := Load("", getenv); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"WAYMARK_CONFIG", "GITEA_OWNERS", "GITHUB_TOKEN", "DATA_DIR"} {
		if !asked[k] {
			t.Errorf("the env walk missed %s (asked %v)", k, asked)
		}
	}
	for k := range asked {
		if !bytes.Contains(doc, []byte("`"+k+"`")) {
			t.Errorf("environment variable `%s` is not in %s", k, configDoc)
		}
	}
}

// The example shipped at the repo root is the one built into the binary.
func TestExampleCopiesMatch(t *testing.T) {
	root, err := os.ReadFile("../../waymark.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(root, ExampleYAML) {
		t.Error("waymark.example.yml and internal/config/example.yml differ; copy one over the other")
	}
}
