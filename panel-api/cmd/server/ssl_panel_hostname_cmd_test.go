package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ssl_panel_hostname_cmd_test.go — GH #2126. `jabali ssl enable|renew|retry`
// on the panel hostname made a per-domain certificate row the reconciler could
// only fail on (the row has no document root: "invalid webroot \"\""). The
// panel's own certificate is managed with `jabali panel-cert` / Server
// Settings → Panel SSL, so these doors refuse it.

func TestSSLPanelHostnameRefusal(t *testing.T) {
	err := sslPanelHostnameRefusal(&models.Domain{Name: "panel.example.com", IsPanelPrimary: true})
	if err == nil {
		t.Fatal("the panel hostname must be refused")
	}
	if !strings.Contains(err.Error(), "jabali panel-cert") {
		t.Errorf("the refusal should say where the panel's certificate is managed: %v", err)
	}
	if err := sslPanelHostnameRefusal(&models.Domain{Name: "site.example.com"}); err != nil {
		t.Errorf("an ordinary domain must not be refused: %v", err)
	}
}

// The commands need the database, so pin the wiring in the source: each door
// calls the refusal before its first write or agent call.
func TestSSLDoors_RefusePanelHostnameBeforeWriting(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "ssl_cmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]bool{
		"UpdateSSLMode": true, "Create": true, "UpdateStatus": true,
		"ResetForRetry": true, "Call": true,
	}
	for _, name := range []string{"newSSLEnableCmd", "newSSLRenewCmd", "newSSLRetryCmd"} {
		var fn *ast.FuncDecl
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
				fn = fd
			}
		}
		if fn == nil {
			t.Fatalf("%s not found", name)
		}
		var refusal, firstWrite token.Pos
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := c.Fun.(type) {
			case *ast.Ident:
				if f.Name == "sslPanelHostnameRefusal" && refusal == 0 {
					refusal = c.Pos()
				}
			case *ast.SelectorExpr:
				if writes[f.Sel.Name] && firstWrite == 0 {
					firstWrite = c.Pos()
				}
			}
			return true
		})
		if refusal == 0 {
			t.Errorf("%s does not call sslPanelHostnameRefusal", name)
			continue
		}
		if firstWrite == 0 || refusal > firstWrite {
			t.Errorf("%s must refuse the panel hostname before its first write or agent call", name)
		}
	}
}
