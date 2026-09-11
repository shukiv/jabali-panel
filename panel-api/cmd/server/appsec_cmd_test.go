package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

func TestReadOperatorHeader(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantMode      string
		wantCountries []string
	}{
		{"empty file → defaults", "", "", nil},
		{
			"present: mode=allow, countries=US,IL,GB",
			"# jabali-mode: allow\n# jabali-countries: US, IL, GB\nname: x\n",
			"allow", []string{"US", "IL", "GB"},
		},
		{
			"present: mode only",
			"# jabali-mode: deny\n# jabali-countries:\nname: x\n",
			"deny", nil,
		},
		{
			"header stops at first non-comment line",
			"name: x\n# jabali-mode: allow\n",
			"", nil, // mode line is below the first non-comment → ignored
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jabali-appsec.yaml")
			if c.body != "" {
				if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			mode, countries, _, _ := readOperatorHeader(path)
			if mode != c.wantMode {
				t.Errorf("mode = %q, want %q", mode, c.wantMode)
			}
			if !reflect.DeepEqual(countries, c.wantCountries) {
				t.Errorf("countries = %v, want %v", countries, c.wantCountries)
			}
		})
	}
}

func TestReadOperatorHeader_BotDetection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jabali-appsec.yaml")
	body := "# jabali-mode: off\n# jabali-countries: \n# jabali-bot-detection: balanced\n# jabali-bot-detection-scope: selected\nname: crowdsecurity/jabali-appsec\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mode, _, botMode, botScope := readOperatorHeader(path)
	if mode != "off" {
		t.Errorf("mode = %q, want off", mode)
	}
	if botMode != "balanced" {
		t.Errorf("botMode = %q, want balanced", botMode)
	}
	// The scope header is a prefix of the mode header — must not be misparsed.
	if botScope != "selected" {
		t.Errorf("botScope = %q, want selected", botScope)
	}
	// Missing marker → "" (caller defaults to off).
	p2 := filepath.Join(t.TempDir(), "no-bot.yaml")
	if err := os.WriteFile(p2, []byte("# jabali-mode: off\nname: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, bm, _ := readOperatorHeader(p2); bm != "" {
		t.Errorf("missing bot marker → %q, want empty", bm)
	}
}

func TestDetectInbandRules(t *testing.T) {
	dir := t.TempDir()
	// Empty dir → just the always-on patterns.
	got := detectInbandRules(dir)
	if len(got) != 2 || got[0] != "crowdsecurity/vpatch-*" || got[1] != "crowdsecurity/generic-*" {
		t.Errorf("empty dir got %v", got)
	}
	// Touch crs.yaml + base-config.yaml → include them.
	for _, f := range []string{"crs.yaml", "base-config.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got = detectInbandRules(dir)
	want := []string{
		"crowdsecurity/vpatch-*",
		"crowdsecurity/generic-*",
		"crowdsecurity/base-config",
		"crowdsecurity/crs",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after touch got %v, want %v", got, want)
	}
}

// reconcileCRSBeforeFiles is the core of the GH #1655 fix: operator exclusions
// go to their OWN file so the agent's boot re-render of the built-in file (which
// has no DB access) can no longer clobber them. These cases pin the invariants a
// future edit could quietly break.
func TestReconcileCRSBeforeFiles(t *testing.T) {
	sample := []appseccfg.Exclusion{{
		Host: "forum.example.com", URIPrefix: "/api/", RuleID: "931120", Note: "sample",
	}}

	// Case 1: rows present. Built-in file is byte-identical to what the agent
	// boot writer emits (CRSPluginBefore verbatim, NO operator content), operator
	// file carries exactly RenderOperatorBeforeFile.
	t.Run("rows present — files disjoint and byte-exact", func(t *testing.T) {
		dir := t.TempDir()
		builtin := filepath.Join(dir, "jabali-before.conf")
		operator := filepath.Join(dir, "jabali-operator-before.conf")

		changed, err := reconcileCRSBeforeFiles(&bytes.Buffer{}, builtin, operator, sample, true)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Error("first write should report changed=true")
		}
		gotBuiltin, _ := os.ReadFile(builtin)
		if string(gotBuiltin) != appseccfg.CRSPluginBefore() {
			t.Error("built-in file is NOT byte-identical to CRSPluginBefore() — agent boot would clobber it (GH #1655)")
		}
		if strings.Contains(string(gotBuiltin), "ctl:ruleRemoveById=931120") {
			t.Error("operator content leaked into the built-in file")
		}
		gotOperator, _ := os.ReadFile(operator)
		if string(gotOperator) != appseccfg.RenderOperatorBeforeFile(sample) {
			t.Error("operator file does not match RenderOperatorBeforeFile")
		}

		// Idempotent: a second pass writes nothing.
		changed, err = reconcileCRSBeforeFiles(&bytes.Buffer{}, builtin, operator, sample, true)
		if err != nil || changed {
			t.Errorf("second pass should be a no-op, got changed=%v err=%v", changed, err)
		}
	})

	// Case 2: last exclusion removed. A stale operator file must be deleted, not
	// left with content that keeps a since-removed exclusion live.
	t.Run("empty list removes a stale operator file", func(t *testing.T) {
		dir := t.TempDir()
		builtin := filepath.Join(dir, "jabali-before.conf")
		operator := filepath.Join(dir, "jabali-operator-before.conf")
		if err := os.WriteFile(builtin, []byte(appseccfg.CRSPluginBefore()), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(operator, []byte(appseccfg.RenderOperatorBeforeFile(sample)), 0o644); err != nil {
			t.Fatal(err)
		}

		changed, err := reconcileCRSBeforeFiles(&bytes.Buffer{}, builtin, operator, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Error("removing the operator file should report changed=true")
		}
		if _, err := os.Stat(operator); !os.IsNotExist(err) {
			t.Errorf("operator file should be gone, stat err=%v", err)
		}
	})

	// Case 3: DB unreachable (operatorKnown=false). The operator file must be
	// left EXACTLY as-is — a transient outage cannot drop live exclusions.
	t.Run("db down leaves the operator file untouched", func(t *testing.T) {
		dir := t.TempDir()
		builtin := filepath.Join(dir, "jabali-before.conf")
		operator := filepath.Join(dir, "jabali-operator-before.conf")
		if err := os.WriteFile(builtin, []byte(appseccfg.CRSPluginBefore()), 0o644); err != nil {
			t.Fatal(err)
		}
		want := appseccfg.RenderOperatorBeforeFile(sample)
		if err := os.WriteFile(operator, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}

		changed, err := reconcileCRSBeforeFiles(&bytes.Buffer{}, builtin, operator, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Error("nothing changed (built-in already current, operator left as-is) — want changed=false")
		}
		got, _ := os.ReadFile(operator)
		if string(got) != want {
			t.Error("operator file was modified while the DB was unreachable — live exclusions at risk")
		}
	})

	// Case 4: one-time migration off the old combined file. A box whose
	// jabali-before.conf still holds built-ins + operator (the pre-#1655 format)
	// must end with built-ins-only there and the operator file split out.
	t.Run("migrates the combined pre-split file", func(t *testing.T) {
		dir := t.TempDir()
		builtin := filepath.Join(dir, "jabali-before.conf")
		operator := filepath.Join(dir, "jabali-operator-before.conf")
		combined := appseccfg.CRSPluginBefore() + appseccfg.RenderExclusions(sample)
		if err := os.WriteFile(builtin, []byte(combined), 0o644); err != nil {
			t.Fatal(err)
		}

		changed, err := reconcileCRSBeforeFiles(&bytes.Buffer{}, builtin, operator, sample, true)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Error("migration should report changed=true")
		}
		gotBuiltin, _ := os.ReadFile(builtin)
		if string(gotBuiltin) != appseccfg.CRSPluginBefore() {
			t.Error("combined file was not reduced to built-ins-only")
		}
		gotOperator, _ := os.ReadFile(operator)
		if string(gotOperator) != appseccfg.RenderOperatorBeforeFile(sample) {
			t.Error("operator exclusions were not split into their own file")
		}
	})
}
