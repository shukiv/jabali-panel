package appseccfg

// GH #2328: paths of a site that skip the bot-detection challenge.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const wantPathFilter = `req.Host == "api.example.com" && req.URL.Host == "" && req.URL.Path startsWith "/api/endpoint" && !(req.URL.Path contains "..")`

func TestRenderBotExempt_PathExclusion(t *testing.T) {
	out := RenderBotExempt(Opts{BotPathExclusions: []BotPathExclusion{
		{Host: "api.example.com", PathPrefix: "/api/endpoint"},
	}})
	mustContain(t, out, "    - filter: '"+wantPathFilter+"'\n", "host + path filter, every method")
	mustContain(t, out, `ExemptFromChallenge("jabali-path-optout")`, "distinct path opt-out label")
	mustNotContain(t, out, "req.Method", "no method clause when no methods are set")
	// The built-in exemptions stay.
	mustContain(t, out, `ExemptFromChallenge("jabali-acme-http01")`, "acme still exempt")
}

func TestRenderBotExempt_PathExclusionMethods(t *testing.T) {
	out := RenderBotExempt(Opts{BotPathExclusions: []BotPathExclusion{
		{Host: "api.example.com", PathPrefix: "/api/endpoint", Methods: []string{"post", "GET"}},
	}})
	mustContain(t, out, wantPathFilter+` && req.Method in ["GET", "POST"]'`, "method clause, upper-cased and sorted")
}

// An entry that fails validation is never rendered as a live filter: the
// state file is written by the panel, and a quote in a path would end the
// expr string literal and widen the exemption.
func TestRenderBotExempt_PathExclusionInvalidSkipped(t *testing.T) {
	bad := []BotPathExclusion{
		{Host: "api.example.com", PathPrefix: `/a" || true || "`},
		{Host: "api.example.com", PathPrefix: "/"},
		{Host: "api.example.com", PathPrefix: "/x", Methods: []string{"TRACE"}},
		{Host: "api example.com", PathPrefix: "/x"},
		{Host: "api.example.com", PathPrefix: "/a\n    - filter: true"},
	}
	out := RenderBotExempt(Opts{BotPathExclusions: bad})
	mustNotContain(t, out, "jabali-path-optout", "no live exemption for invalid entries")
	if got := strings.Count(out, "# SKIPPED"); got != len(bad) {
		t.Fatalf("want %d SKIPPED comments, got %d:\n%s", len(bad), got, out)
	}
	mustNotContain(t, out, "\n    - filter: true", "a line break in a path never starts a live entry")
}

// A path may hold ' and ": ", which would end a plain or single-quoted YAML
// scalar early. The filter must parse back to the exact expr.
func TestRenderBotExempt_PathExclusionYAMLRoundTrip(t *testing.T) {
	out := RenderBotExempt(Opts{BotPathExclusions: []BotPathExclusion{
		{Host: "api.example.com", PathPrefix: "/it's: here"},
	}})
	var cfg struct {
		Inband struct {
			PreEval []struct {
				Filter string   `yaml:"filter"`
				Apply  []string `yaml:"apply"`
			} `yaml:"pre_eval"`
		} `yaml:"inband"`
	}
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("rendered config does not parse: %v\n%s", err, out)
	}
	want := `req.Host == "api.example.com" && req.URL.Host == "" && req.URL.Path startsWith "/it's: here" && !(req.URL.Path contains "..")`
	for _, e := range cfg.Inband.PreEval {
		if len(e.Apply) == 1 && e.Apply[0] == `ExemptFromChallenge("jabali-path-optout")` {
			if e.Filter != want {
				t.Fatalf("filter = %q, want %q", e.Filter, want)
			}
			return
		}
	}
	t.Fatalf("no path opt-out entry in:\n%s", out)
}

func TestRenderBotExempt_PathExclusionInSelectedScope(t *testing.T) {
	out := RenderBotExempt(Opts{
		BotScope:          "selected",
		BotIncludeHosts:   []string{"api.example.com"},
		BotPathExclusions: []BotPathExclusion{{Host: "api.example.com", PathPrefix: "/api/endpoint"}},
	})
	mustContain(t, out, wantPathFilter, "a path of an included site is still exempted")
}

func TestValidateBotPathPrefix(t *testing.T) {
	for _, p := range []string{"/api/", "/webhook", "/download/file name.zip", "/it's"} {
		if err := ValidateBotPathPrefix(p); err != nil {
			t.Errorf("ValidateBotPathPrefix(%q) = %v, want ok", p, err)
		}
	}
	for _, p := range []string{
		"", "api", "/", "//evil.example/x", "/a?b=1", "/a#x", `/a"b`, `/a\b`, "/a\nb",
		"/a/../b", "/a%20b", "/" + strings.Repeat("a", MaxBotPathPrefixLen),
	} {
		if err := ValidateBotPathPrefix(p); err == nil {
			t.Errorf("ValidateBotPathPrefix(%q) = nil, want an error", p)
		}
	}
}

func TestNormalizeBotMethods(t *testing.T) {
	got, err := NormalizeBotMethods([]string{"post", " GET", "get"})
	if err != nil || !reflect.DeepEqual(got, []string{"GET", "POST"}) {
		t.Fatalf("NormalizeBotMethods = %v, %v; want [GET POST]", got, err)
	}
	if got, err := NormalizeBotMethods(nil); err != nil || got != nil {
		t.Fatalf("NormalizeBotMethods(nil) = %v, %v; want nil, nil", got, err)
	}
	if _, err := NormalizeBotMethods([]string{"TRACE"}); err == nil {
		t.Fatal("NormalizeBotMethods(TRACE) = nil error, want an error")
	}
}

func TestBotExemptPathsStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot-exempt-paths.json")
	if got, err := LoadBotExemptPaths(path); err != nil || got != nil {
		t.Fatalf("missing file: got %v, %v; want nil, nil", got, err)
	}
	list := []BotPathExclusion{
		{Host: "www.b.example", PathPrefix: "/hook"},
		{Host: "a.example", PathPrefix: "/api/", Methods: []string{"POST"}},
	}
	if changed, err := WriteBotExemptPaths(path, list); err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	// Same entries in another order: nothing to write.
	if changed, err := WriteBotExemptPaths(path, []BotPathExclusion{list[1], list[0]}); err != nil || changed {
		t.Fatalf("second write: changed=%v err=%v, want unchanged", changed, err)
	}
	got, err := LoadBotExemptPaths(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []BotPathExclusion{list[1], list[0]} // sorted by host
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}
}
