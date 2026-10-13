package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// GH #2131: the DNS-01 hook pushes a whole zone (dns.zone.upsert), and that
// push replaces the zone's ALLOW-AXFR-FROM / ALSO-NOTIFY metadata. It must
// send the same lists as the reconciler, or every DNS-01 issuance would drop
// the secondary DNS servers until the next reconcile. It needs the database,
// so pin the source: the lists come from dnscompile.TransferTargets, called
// with the server settings the function loaded.
func TestDNSACMEHook_UsesTransferTargets(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "dns_acme_hook_cmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls, ns2Reads := 0, 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Variables this function assigns from
		// repository.NewServerSettingsRepository(...).Get(ctx).
		loaded := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
				return true
			}
			get, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := get.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Get" {
				return true
			}
			repo, ok := sel.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			if rs, ok := repo.Fun.(*ast.SelectorExpr); ok && rs.Sel.Name == "NewServerSettingsRepository" {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					loaded[id.Name] = true
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "TransferTargets" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "dnscompile" {
					return true
				}
				calls++
				var arg *ast.Ident
				if len(x.Args) == 1 {
					arg, _ = x.Args[0].(*ast.Ident)
				}
				if arg == nil || !loaded[arg.Name] {
					t.Errorf("%s: dnscompile.TransferTargets must be called with the server settings loaded in the function", fn.Name.Name)
				}
			case *ast.SelectorExpr:
				if x.Sel.Name == "NS2IPv4" {
					ns2Reads++
				}
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("dns_acme_hook_cmd.go calls dnscompile.TransferTargets %d times, want 1", calls)
	}
	if ns2Reads != 0 {
		t.Fatalf("dns_acme_hook_cmd.go reads NS2IPv4 itself (%d times); take the lists from dnscompile.TransferTargets", ns2Reads)
	}
}
