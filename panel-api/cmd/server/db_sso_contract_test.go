package main

import (
	"os"
	"strings"
	"testing"
)

// JAB-348 AC4 — CLI adapter half of the DB-Console SSO contract.
//
// The operator CLI (`jabali db sso`) mints and redirects inline in its cobra
// RunE against services built from process globals — there is no injection seam,
// so its issuance path is pinned by source delegation, the same precedent as
// domain_advanced_cmd_ssl_mode_test.go. The behavioural half (the privileged
// doors driven through a fake minter, and the tenant/CLI single-authority
// invariant) lives in internal/api/dbconsole_adapter_contract_test.go.
//
// The contract: the CLI must route engine normalization, shadow dispatch, the
// mint<->redirect pairing, and the issuance audit taxonomy through dbconsoleops —
// so a CLI-minted handoff encodes database+engine scope identically to the REST
// doors and audits in the same taxonomy (AC3/AC4/AC5). If the CLI grows its own
// inline engine switch or URL builder, it drops one of these references and the
// pin reddens.
func TestDBSSOCLI_DelegatesIssuanceToLeaf(t *testing.T) {
	src, err := os.ReadFile("db_sso_cmd.go")
	if err != nil {
		t.Fatalf("read db_sso_cmd.go: %v", err)
	}
	s := string(src)

	needs := []struct {
		token string
		why   string
	}{
		{"dbconsoleops.NormalizeEngine", "engine scope must be normalized through the shared leaf so the CLI and REST adapters default and validate engines identically (AC3)"},
		{"dbconsoleops.EnsureShadowForEngine", "shadow provisioning must dispatch through the shared engine leaf, not a CLI-local switch"},
		{"dbconsoleops.IssuePhpMyAdminLogin", "the mariadb branch must mint+redirect through the phpMyAdmin issuance leaf (mint<->redirect pairing)"},
		{"dbconsoleops.IssueAdminerLogin", "the postgres branch must mint+redirect through the Adminer issuance leaf"},
		{"dbconsoleops.OutcomeIssued", "a successful CLI issuance must audit the canonical issued outcome, matching the tenant doors (AC5)"},
		{"dbconsoleops.OutcomeMintFail", "a CLI mint failure must audit the canonical mint_fail outcome"},
		{"dbconsoleops.OutcomeEnsureShadowFail", "a CLI shadow-provision failure must audit the canonical ensure_shadow_fail outcome"},
	}
	for _, n := range needs {
		if !strings.Contains(s, n.token) {
			t.Errorf("db_sso_cmd.go must reference %s — %s", n.token, n.why)
		}
	}
}
