package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
)

// The CLI issuance audit line must carry the token hash-prefix and NEVER the
// raw token — the AC5 "audited without logging token material" property, tested
// at the layer the bug would live (no DB/cobra harness needed).
func TestAuditCLIIssuance_NoTokenMaterial(t *testing.T) {
	const token = "Zm9vYmFyLWJhei0xMjM0NTY3ODkw_AB-cd"
	prefix := dbconsoleops.TokenAuditPrefix(token)

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	auditCLIIssuance(log, "user_01ABC", "db_01XYZ", "postgres", prefix, "issued")

	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("audit line leaked the raw token: %s", out)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("audit line is not JSON: %v (%s)", err, out)
	}
	if rec["msg"] != "sso_cli" {
		t.Errorf("msg = %v, want sso_cli", rec["msg"])
	}
	if rec["outcome"] != "issued" {
		t.Errorf("outcome = %v, want issued", rec["outcome"])
	}
	if rec["token_hash_prefix"] != prefix {
		t.Errorf("token_hash_prefix = %v, want %q", rec["token_hash_prefix"], prefix)
	}
	if rec["user_id"] != "user_01ABC" || rec["database_id"] != "db_01XYZ" || rec["engine"] != "postgres" {
		t.Errorf("audit fields wrong: %s", out)
	}
}

// Failure paths audit with an empty hash-prefix (no token minted yet) — still a
// record, still no token material.
func TestAuditCLIIssuance_FailureHasNoToken(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	auditCLIIssuance(log, "user_01ABC", "db_01XYZ", "mariadb", "", "mint_fail")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("audit line is not JSON: %v", err)
	}
	if rec["outcome"] != "mint_fail" {
		t.Errorf("outcome = %v, want mint_fail", rec["outcome"])
	}
	if rec["token_hash_prefix"] != "" {
		t.Errorf("token_hash_prefix = %v, want empty on a pre-token failure", rec["token_hash_prefix"])
	}
}

// A nil logger must be a safe no-op (defensive — the CLI wiring may not set it).
func TestAuditCLIIssuance_NilLoggerNoPanic(t *testing.T) {
	auditCLIIssuance(nil, "u", "d", "mariadb", "", "issued")
}
