package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// JAB-222: a settings-backed knob must APPLY TO THE AGENT before it persists to
// server_settings. The reverse order lets a failed apply leave the database
// advertising a protection that is not in force — `crowdsec captcha get`
// reported enabled=true provider=turnstile on a host whose bouncer conf still
// had an empty SITE_KEY and FALLBACK_REMEDIATION=ban.
//
// The REST handler got this right in GH #685; the CLI kept the inverted order
// for captcha (geoblock was already correct). Same CLI-vs-HTTP parity gap as the
// delete-cascade divergence in #754, which is why this is a structural guard
// rather than a one-line fix: the next settings-backed verb should not be able
// to reintroduce it quietly.
//
// Rule enforced: within a single function, if both repo.Upsert(...) and an
// agent Call(...) appear, the Call must come first.
func TestCrowdSecCLI_AgentApplyPrecedesSettingsPersist(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "crowdsec_cmd.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		var upsertPos, callPos token.Pos

		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Upsert":
				if upsertPos == token.NoPos {
					upsertPos = call.Pos()
				}
			case "Call":
				// Only the agent dispatch counts.
				if x, ok := sel.X.(*ast.Ident); ok && strings.Contains(x.Name, "Agent") {
					if callPos == token.NoPos {
						callPos = call.Pos()
					}
				}
			}
			return true
		})

		if upsertPos != token.NoPos && callPos != token.NoPos && upsertPos < callPos {
			t.Errorf("%s persists to server_settings at %s BEFORE dispatching to the agent at %s — "+
				"a failed apply would leave the database advertising a protection that is not in "+
				"force (JAB-222). Dispatch first, persist on success.",
				fn.Name.Name, fset.Position(upsertPos), fset.Position(callPos))
		}
		return true
	})
}
