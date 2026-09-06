package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// captureAgent records the last restore_from_tar params and returns a canned reply.
type captureAgent struct {
	reply       string
	lastCommand string
	lastParams  map[string]any
}

func (a *captureAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.lastCommand = cmd
	if m, ok := params.(map[string]any); ok {
		a.lastParams = m
	}
	return json.RawMessage(a.reply), nil
}

var _ agent.AgentInterface = (*captureAgent)(nil)

func tenantRestoreHandler(ag agent.AgentInterface) *meBackupHandler {
	return &meBackupHandler{cfg: MeBackupsHandlerConfig{Agent: ag}}
}

func TestRunTenantUploadRestore_FailsClosedWithoutAck(t *testing.T) {
	dir := t.TempDir()
	// Agent applied the restore but did NOT report enforcement (e.g. an old
	// agent) → must be sealed as failed, never trusted.
	ag := &captureAgent{reply: `{"applied":["home → /home/alice"],"warnings":[]}`}
	oc := filepath.Join(dir, "outcome.json")
	tenantRestoreHandler(ag).runTenantUploadRestore(tenantUploadRestoreArgs{
		path: filepath.Join(dir, "archive.tar.zst"), outcomePath: oc, username: "alice",
		allowedDBs: []string{}, allowedDomains: []string{},
	})
	o, err := readRestoreUploadOutcome(oc)
	if err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if o.Status != "failed" {
		t.Fatalf("no enforcement ack must fail closed, got status %q", o.Status)
	}
}

func TestRunTenantUploadRestore_DoneWithAck(t *testing.T) {
	dir := t.TempDir()
	ag := &captureAgent{reply: `{"applied":["home → /home/alice"],"warnings":["db \"x\": not one of your databases — skipped"],"db_allowlist_enforced":true,"mail_allowlist_enforced":true}`}
	oc := filepath.Join(dir, "outcome.json")
	tenantRestoreHandler(ag).runTenantUploadRestore(tenantUploadRestoreArgs{
		path: filepath.Join(dir, "archive.tar.zst"), outcomePath: oc, username: "alice",
		allowedDBs: []string{"alice_blog"}, allowedDomains: []string{"alice.com"},
	})
	o, err := readRestoreUploadOutcome(oc)
	if err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if o.Status != "done" {
		t.Fatalf("enforced restore should be done, got %q (%s)", o.Status, o.Error)
	}
}

func TestRunTenantUploadRestore_SendsModeAndNonNilAllowlists(t *testing.T) {
	dir := t.TempDir()
	ag := &captureAgent{reply: `{"db_allowlist_enforced":true,"mail_allowlist_enforced":true}`}
	tenantRestoreHandler(ag).runTenantUploadRestore(tenantUploadRestoreArgs{
		path: filepath.Join(dir, "a.tar.zst"), outcomePath: filepath.Join(dir, "o.json"),
		username: "alice", allowedDBs: nil, allowedDomains: nil, // even nil in → non-nil on the wire
	})
	if ag.lastCommand != "backup.restore_from_tar" {
		t.Fatalf("command = %q", ag.lastCommand)
	}
	if ag.lastParams["mode"] != "tenant" {
		t.Errorf("mode = %v, want tenant", ag.lastParams["mode"])
	}
	for _, k := range []string{"allowed_db_names", "allowed_mail_domains"} {
		v, ok := ag.lastParams[k]
		if !ok || v == nil {
			t.Errorf("%s must be present and non-nil on the wire (empty=enforce, null=unrestricted)", k)
		}
	}
}

func TestIntersectRestoreComponents(t *testing.T) {
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	allowed := []string{"home", "db", "mail"}
	if got := intersectRestoreComponents(nil, allowed); !eq(got, allowed) {
		t.Errorf("empty → allowed set, got %v", got)
	}
	if got := intersectRestoreComponents([]string{"home", "docker"}, allowed); !eq(got, []string{"home"}) {
		t.Errorf("subset, got %v", got)
	}
	// Only-disallowed must NOT fall through to "all" (empty) — it applies nothing.
	if got := intersectRestoreComponents([]string{"docker", "dns"}, allowed); !eq(got, []string{"__none__"}) {
		t.Errorf("only-disallowed must map to the deny-all sentinel, got %v", got)
	}
}
