package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// captureSQL swaps the exec seam for one that records every `-c <sql>` argument
// and runs a harmless `true`, so a handler's psql calls succeed while the test
// inspects the SQL text. Non-parallel; restored on cleanup.
func captureSQL(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-c" {
				seen = append(seen, args[i+1])
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &seen
}

func TestShadowadminGrantMembers_InvalidPanelUsername(t *testing.T) {
	_ = captureSQL(t)
	_, err := dbPostgresShadowadminGrantMembersHandler(context.Background(),
		json.RawMessage(`{"panel_username":"Bad Name","member_roles":["x"]}`))
	if err == nil {
		t.Fatal("expected invalid_argument for a bad panel username")
	}
}

func TestShadowadminGrantMembers_EmptyIsNoop(t *testing.T) {
	seen := captureSQL(t)
	_, err := dbPostgresShadowadminGrantMembersHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","member_roles":[]}`))
	if err != nil {
		t.Fatalf("empty member list should be a no-op success, got %v", err)
	}
	if len(*seen) != 0 {
		t.Fatalf("no SQL should run for an empty member list, got %v", *seen)
	}
}

func TestShadowadminGrantMembers_GrantsExplicitListWithGuards(t *testing.T) {
	seen := captureSQL(t)
	// alice_pgadmin (self) and "bad;name" (invalid ident) must be filtered out;
	// alice_app and alice_web must be granted.
	_, err := dbPostgresShadowadminGrantMembersHandler(context.Background(),
		json.RawMessage(`{"panel_username":"alice","member_roles":["alice_app","alice_pgadmin","bad;name","alice_web"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("expected exactly one GRANT statement, got %d: %v", len(*seen), *seen)
	}
	sql := (*seen)[0]

	// Explicit allow-list, not a pattern.
	if !strings.Contains(sql, `'alice_app'`) || !strings.Contains(sql, `'alice_web'`) {
		t.Fatalf("SQL must reference the passed roles explicitly:\n%s", sql)
	}
	if strings.Contains(sql, "LIKE") {
		t.Fatalf("membership must NOT use a LIKE pattern (cross-tenant risk):\n%s", sql)
	}
	// Self and invalid ident filtered out.
	if strings.Contains(sql, "'bad;name'") {
		t.Fatalf("invalid identifier leaked into SQL:\n%s", sql)
	}
	if strings.Count(sql, "'alice_pgadmin'") != 2 {
		// appears exactly twice: the `<> 'alice_pgadmin'` guard and the grantee —
		// never as a membership TARGET in the ARRAY.
		t.Fatalf("alice_pgadmin should appear only as the guard + grantee, not as a target:\n%s", sql)
	}
	// Defence-in-depth guards.
	for _, want := range []string{"NOT rolsuper", "rolcanlogin", "<> 'alice_pgadmin'", "GRANT %I TO %I"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing guard %q:\n%s", want, sql)
		}
	}
}
