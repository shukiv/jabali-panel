package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const restoreReplyWithMeta = `{"user":{"id":"01U","username":"alice","is_admin":false},"metadata":{"schema_version":2},"applied":[]}`
const restoreReplyUserOnly = `{"user":{"id":"01U","username":"alice","email":"a@example.test","is_admin":false},"applied":[]}`

func runRestoreReply(t *testing.T, raw string, apply bool, ensureErr error) (metaCalls, userCalls int, out string) {
	t.Helper()
	var buf bytes.Buffer
	handleRestoreReply(&buf, json.RawMessage(raw), apply,
		func(json.RawMessage) { metaCalls++ },
		func(accountRestoreUserBlock) error { userCalls++; return ensureErr })
	return metaCalls, userCalls, buf.String()
}

// --apply=false is the staging-only smoke test: the agent still returns the
// metadata bundle, and the CLI must not reinstate it onto the live panel.
func TestHandleRestoreReply_ReconModeWritesNothing(t *testing.T) {
	for _, raw := range []string{restoreReplyWithMeta, restoreReplyUserOnly} {
		meta, user, out := runRestoreReply(t, raw, false, nil)
		if meta != 0 || user != 0 {
			t.Fatalf("recon mode reinstated panel state: metadata=%d user=%d", meta, user)
		}
		if !strings.Contains(out, "NOT reconstructed") {
			t.Fatalf("recon mode output does not say nothing was applied: %q", out)
		}
	}
}

func TestHandleRestoreReply_AppliedRestoreReinstates(t *testing.T) {
	if meta, user, _ := runRestoreReply(t, restoreReplyWithMeta, true, nil); meta != 1 || user != 0 {
		t.Fatalf("metadata bundle: metadata=%d user=%d, want 1/0", meta, user)
	}
	if meta, user, _ := runRestoreReply(t, restoreReplyUserOnly, true, nil); meta != 0 || user != 1 {
		t.Fatalf("no bundle: metadata=%d user=%d, want 0/1", meta, user)
	}
	_, _, out := runRestoreReply(t, restoreReplyUserOnly, true, errors.New("boom"))
	if !strings.Contains(out, "jabali user create --user-id 01U --username alice") {
		t.Fatalf("a failed user-row rebuild must print the manual command: %q", out)
	}
}

// GH #1993: after the CLI restores an account, the PostgreSQL databases the
// agent loaded get their users granted again, and the CLI prints what didn't
// work.
func TestRegrantRestoredPostgresCLI_GrantsTheLoadedDatabasesForTheAccount(t *testing.T) {
	prev := regrantRestoredPostgres
	t.Cleanup(func() { regrantRestoredPostgres = prev })
	var gotAccount string
	var gotNames []string
	regrantRestoredPostgres = func(_ context.Context, _ dbops.AgentCaller, _ repository.DatabaseRepository, _ repository.DatabaseUserGrantRepository, _ repository.DatabaseUserRepository, accountID string, names []string) ([]string, []string) {
		gotAccount, gotNames = accountID, names
		return []string{"db alice_pg (postgres): granting alice_u on it again failed: boom"}, []string{"db alice_bare (postgres): no database user is granted on it"}
	}
	var buf bytes.Buffer
	regrantRestoredPostgresCLI(context.Background(), &buf, nil,
		json.RawMessage(`{"restored_postgres_databases":["alice_pg","alice_bare"]}`), "01U")

	if gotAccount != "01U" || strings.Join(gotNames, ",") != "alice_pg,alice_bare" {
		t.Fatalf("regranted %v for %q, want both databases for 01U", gotNames, gotAccount)
	}
	if out := buf.String(); !strings.Contains(out, "WARNING: db alice_pg (postgres): granting alice_u") || !strings.Contains(out, "note: db alice_bare (postgres)") {
		t.Errorf("output %q should print the failure and the note", out)
	}

	gotNames = nil
	regrantRestoredPostgresCLI(context.Background(), &buf, nil, json.RawMessage(`{"applied":[]}`), "01U")
	if gotNames != nil {
		t.Errorf("a reply naming no PostgreSQL database regranted %v", gotNames)
	}
}
