package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Cloudflare replaces an origin's 502 or 504 with its own error page: behind
// it only the status reaches the browser, never the body. So a 502 or 504
// here carries nothing but a literal error code (and "status"). A failure the
// user has to read answers 422 when a check of an outside service failed, or
// 500 when the panel's own change failed, with the same body (GH #2077).
//
// This walks the handlers' source: every c.JSON / return / error literal that
// pairs http.StatusBadGateway or http.StatusGatewayTimeout with a gin.H body
// may hold only "error" (a string literal) and "status". A positional error
// value ({status, code, detail}) may not carry a detail.

// badGatewayBodyAllowed lists the 502/504 answers that keep a body, as
// "file:function", with why.
var badGatewayBodyAllowed = map[string]string{
	// The 504 "inspect fewer" is shown from the status by the UI
	// (AppSecExclusionsCard), so it still works behind Cloudflare; the 502 is
	// an agent reply the panel can't decode.
	"security_appsec_exclusions.go:appsecEventsHandler": "UI keys on the 504 status",
	// An agent that runs out of time answers 504 (its message is lost behind
	// Cloudflare, the status isn't); the AppSec card reads it from the status.
	"health_agent.go:translateAgentError": "agent timeout, 504 by status",
	// Internal route: the agent posts over the local socket, never through
	// Cloudflare.
	"notifications_internal.go:RegisterNotificationsInternalRoutes": "internal route",
	// The panel's own agent failed: the body is only the code each caller
	// passes as a literal, and the agent's error is withheld on purpose.
	"error_response.go:respondAgentErr":       "agent failure, code only",
	"error_response.go:respondAgentErrStatus": "agent failure, code only",
}

func TestBadGatewayBodiesCarryOnlyACode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range af.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			site := f + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				// A 502 or 504 kept in a variable: this test can't see the body
				// it goes out with, so it counts as carrying one.
				if as, ok := n.(*ast.AssignStmt); ok && hasGatewayStatus(as.Rhs) {
					if _, ok := badGatewayBodyAllowed[site]; !ok {
						bad = append(bad, fmt.Sprintf("%s (line %d): a 502/504 kept in a variable", site, fset.Position(n.Pos()).Line))
					}
					return true
				}
				var items []ast.Expr
				positional := false
				switch x := n.(type) {
				case *ast.CallExpr:
					items = x.Args
				case *ast.ReturnStmt:
					items = x.Results
				case *ast.CompositeLit:
					items = x.Elts
					positional = !isGinH(x) && len(x.Elts) > 0 && !isKeyValue(x.Elts[0])
				default:
					return true
				}
				if !hasGatewayStatus(items) {
					return true
				}
				why := ""
				for _, it := range items {
					if lit, ok := it.(*ast.CompositeLit); ok && isGinH(lit) {
						if w := ginHCarriesMore(lit); w != "" {
							why = w
						}
					}
				}
				if positional && why == "" {
					why = positionalCarriesDetail(items)
				}
				if why == "" {
					return true
				}
				if _, ok := badGatewayBodyAllowed[site]; ok {
					return true
				}
				bad = append(bad, fmt.Sprintf("%s (line %d): %s", site, fset.Position(n.Pos()).Line, why))
				return true
			})
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Fatalf("%d 502/504 answers carry a body Cloudflare would replace; answer 422 or 500 instead (or add the site to badGatewayBodyAllowed with why):\n  %s",
			len(bad), strings.Join(bad, "\n  "))
	}
}

func hasGatewayStatus(items []ast.Expr) bool {
	for _, it := range items {
		if sel, ok := it.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "http" &&
				(sel.Sel.Name == "StatusBadGateway" || sel.Sel.Name == "StatusGatewayTimeout") {
				return true
			}
		}
	}
	return false
}

func isGinH(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "gin" && sel.Sel.Name == "H"
}

func isKeyValue(e ast.Expr) bool {
	_, ok := e.(*ast.KeyValueExpr)
	return ok
}

func stringLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

// ginHCarriesMore says what a 502/504 gin.H body carries beyond a literal
// error code and "status", or "" when it carries nothing more.
func ginHCarriesMore(lit *ast.CompositeLit) string {
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return "a body that isn't key: value"
		}
		key, ok := stringLit(kv.Key)
		if !ok {
			return "a computed key"
		}
		switch key {
		case "status":
		case "error":
			if _, ok := stringLit(kv.Value); !ok {
				return `an "error" built at runtime`
			}
		default:
			return fmt.Sprintf("%q", key)
		}
	}
	return ""
}

// positionalCarriesDetail checks an error value written {status, code,
// detail}: past the status and the code, any element but "" is a detail.
func positionalCarriesDetail(items []ast.Expr) string {
	var rest []ast.Expr
	for _, it := range items {
		if !hasGatewayStatus([]ast.Expr{it}) {
			rest = append(rest, it)
		}
	}
	for _, e := range rest[min(1, len(rest)):] {
		if s, ok := stringLit(e); ok && s == "" {
			continue
		}
		return "a detail in the error value"
	}
	return ""
}
