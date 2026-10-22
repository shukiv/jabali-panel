package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ssl_ownership_cmd_test.go — GH #2200. A domain whose owner has not proven
// the name yet (GH #1816) gets a self-signed placeholder and never a
// certificate from Let's Encrypt. `jabali ssl retry` answered "retry only
// applies to 'failed' or 'pending_acme_retry'" and `jabali ssl renew` "wait
// for the reconciler to finish issuing", which sent the operator looking for
// an ACME failure that never happened. Both doors now name the cause and the
// command that resolves it.

func TestSSLOwnershipPendingRefusal(t *testing.T) {
	pending := &models.Domain{Name: "site.example.com", SSLMode: models.SSLModeLE}
	err := sslOwnershipPendingRefusal(pending)
	if err == nil {
		t.Fatal("a pending domain must be refused")
	}
	for _, want := range []string{
		"proof of ownership",
		"jabali domain ownership status site.example.com",
		"jabali domain ownership approve site.example.com",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should contain %q: %v", want, err)
		}
	}

	verified := &models.Domain{Name: "site.example.com", SSLMode: models.SSLModeLE}
	verified.OwnershipStatus = models.OwnershipVerified
	if err := sslOwnershipPendingRefusal(verified); err != nil {
		t.Errorf("a verified domain must not be refused: %v", err)
	}
	// A custom certificate is the owner's own; the gate leaves it in place.
	custom := &models.Domain{Name: "site.example.com", SSLMode: models.SSLModeCustom}
	if err := sslOwnershipPendingRefusal(custom); err != nil {
		t.Errorf("a custom-certificate domain must not be refused: %v", err)
	}
}

// The commands need the database and the agent, so pin the wiring in the
// source: renew and retry refuse a pending domain before they read the
// certificate's status (whose message would point at the wrong cause), and
// before their first write or agent call.
func TestSSLDoors_RefuseOwnershipPendingFirst(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "ssl_cmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	later := map[string]bool{
		"FindByDomainID": true, "UpdateStatus": true, "ResetForRetry": true, "Call": true,
	}
	for _, name := range []string{"newSSLRenewCmd", "newSSLRetryCmd"} {
		var fn *ast.FuncDecl
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
				fn = fd
			}
		}
		if fn == nil {
			t.Fatalf("%s not found", name)
		}
		var refusal, first token.Pos
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := c.Fun.(type) {
			case *ast.Ident:
				if f.Name == "sslOwnershipPendingRefusal" && refusal == 0 {
					refusal = c.Pos()
				}
			case *ast.SelectorExpr:
				if later[f.Sel.Name] && first == 0 {
					first = c.Pos()
				}
			}
			return true
		})
		if refusal == 0 {
			t.Errorf("%s does not call sslOwnershipPendingRefusal", name)
			continue
		}
		if first == 0 || refusal > first {
			t.Errorf("%s must refuse a pending domain before it reads the certificate or calls the agent", name)
		}
	}
}
