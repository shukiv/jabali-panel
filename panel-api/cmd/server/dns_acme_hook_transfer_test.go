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
// so pin the source: the lists come from dnscompile.TransferTargets.
func TestDNSACMEHook_UsesTransferTargets(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "dns_acme_hook_cmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls, ns2Reads := 0, 0
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "TransferTargets" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "dnscompile" {
					calls++
				}
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "NS2IPv4" {
				ns2Reads++
			}
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("dns_acme_hook_cmd.go calls dnscompile.TransferTargets %d times, want 1", calls)
	}
	if ns2Reads != 0 {
		t.Fatalf("dns_acme_hook_cmd.go reads NS2IPv4 itself (%d times); take the lists from dnscompile.TransferTargets", ns2Reads)
	}
}
