package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// appsec_exclusion_list_test.go — `jabali appsec exclusion list` says which rows
// an app install carries, as the panel's API does. It read the table directly,
// so a row a Flarum or Nextcloud install manages looked like one an operator
// added: removing it by hand only lasts until the install's next sync.

func listFixture(t *testing.T) *fsExcl {
	t.Helper()
	managed, err := appsecops.FlarumExclusion("i1", "a.example.com", false, "forum")
	if err != nil {
		t.Fatal(err)
	}
	return &fsExcl{rows: []models.CRSRuleExclusion{
		{ID: "r1", Host: managed.Host, URIPrefix: managed.URIPrefix, RuleID: managed.RuleID, Note: managed.Note},
		{ID: "r2", Host: "b.example.com", URIPrefix: "/hook/", RuleID: "942100", Note: "webhook body"},
	}}
}

func TestListAppSecExclusions_JSONNamesTheManagingInstall(t *testing.T) {
	var out bytes.Buffer
	if err := listAppSecExclusions(context.Background(), appsecops.Deps{Exclusions: listFixture(t)}, &out, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Exclusions []map[string]any `json:"exclusions"`
		Total      int              `json:"total"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if got.Total != 2 || len(got.Exclusions) != 2 {
		t.Fatalf("got %d rows (total %d), want 2:\n%s", len(got.Exclusions), got.Total, out.String())
	}
	byID := map[string]map[string]any{}
	for _, r := range got.Exclusions {
		byID[r["id"].(string)] = r
	}
	if m := byID["r1"]; m["managed_app"] != "flarum" || m["managed_install_id"] != "i1" {
		t.Fatalf("the Flarum row does not name its install: %v", m)
	}
	if o := byID["r2"]; o["managed_app"] != nil || o["managed_install_id"] != nil {
		t.Fatalf("the operator's row is marked managed: %v", o)
	}
}

func TestListAppSecExclusions_TableNamesTheManagingInstall(t *testing.T) {
	var out bytes.Buffer
	if err := listAppSecExclusions(context.Background(), appsecops.Deps{Exclusions: listFixture(t)}, &out, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "MANAGED BY") {
		t.Fatalf("want a header with MANAGED BY and two rows:\n%s", out.String())
	}
	for _, l := range lines[1:] {
		fields := strings.Fields(l)
		switch fields[0] {
		case "r1":
			if !strings.Contains(l, "flarum install i1") {
				t.Errorf("the Flarum row does not name its install: %q", l)
			}
		case "r2":
			if strings.Contains(l, "install") || len(fields) < 5 || fields[4] != "-" {
				t.Errorf("the operator's row should show - under MANAGED BY: %q", l)
			}
		}
	}
}

// The command has to print through it; it needs the database, so check the
// call in the source.
func TestAppSecExclusionListCmd_UsesTheSharedListing(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "appsec_exclusion_cmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newAppSecExclusionListCmd" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "listAppSecExclusions" {
					calls++
				}
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("newAppSecExclusionListCmd calls listAppSecExclusions %d times, want 1", calls)
	}
}
