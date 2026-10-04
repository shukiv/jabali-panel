package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// runServe wires the outbound-throttle reconciler and the throttle inline-
// delete client by type-asserting deps.StalwartAdmin. Both checks ran before
// deps.StalwartAdmin was assigned, so both saw nil: since M47 Wave 3 no
// mail_outbound_policy row ever reached Stalwart, and deleting a row never
// removed anything. This pins the order: runServe must assign
// deps.StalwartAdmin before it reads it.
func TestRunServe_AssignsStalwartAdminBeforeUse(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "serve.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "runServe" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("runServe not found in serve.go")
	}
	isStalwartAdmin := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "StalwartAdmin" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == "deps"
	}
	writes := map[token.Pos]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, l := range as.Lhs {
				if isStalwartAdmin(l) {
					writes[l.Pos()] = true
				}
			}
		}
		return true
	})
	firstWrite, firstRead := token.NoPos, token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok || !isStalwartAdmin(e) {
			return true
		}
		if writes[e.Pos()] {
			if firstWrite == token.NoPos || e.Pos() < firstWrite {
				firstWrite = e.Pos()
			}
		} else if firstRead == token.NoPos || e.Pos() < firstRead {
			firstRead = e.Pos()
		}
		return true
	})
	if firstWrite == token.NoPos {
		t.Fatal("runServe never assigns deps.StalwartAdmin")
	}
	if firstRead != token.NoPos && firstRead < firstWrite {
		t.Errorf("runServe reads deps.StalwartAdmin at %s before it assigns it at %s; the reader sees nil",
			fset.Position(firstRead), fset.Position(firstWrite))
	}
}
