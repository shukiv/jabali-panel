package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRestoreEnforcement_Helpers(t *testing.T) {
	// nil list = unrestricted; non-nil (even empty) = enforce.
	if (restoreEnforcement{}).enforceDB() {
		t.Error("nil AllowedDBNames must be unrestricted")
	}
	if !(restoreEnforcement{AllowedDBNames: []string{}}).enforceDB() {
		t.Error("empty AllowedDBNames must enforce (own nothing)")
	}
	if !(restoreEnforcement{AllowedMailDomains: []string{}}).enforceMail() {
		t.Error("empty AllowedMailDomains must enforce")
	}

	e := restoreEnforcement{AllowedDBNames: []string{"alice_blog"}, AllowedMailDomains: []string{"Alice.com"}}
	if !e.dbAllowed("alice_blog") || e.dbAllowed("alice_Blog") || e.dbAllowed("jabali_panel") {
		t.Error("dbAllowed must be an exact, case-sensitive match")
	}
	if !e.mailDomainAllowed("alice.com") || !e.mailDomainAllowed("ALICE.COM") {
		t.Error("mailDomainAllowed must be case-insensitive")
	}
	if e.mailDomainAllowed("victim.com") {
		t.Error("mailDomainAllowed must reject a non-owned domain")
	}
}

func TestBackupRestoreFromTar_TenantRequiresAllowlists(t *testing.T) {
	call := func(p map[string]any) error {
		raw, _ := json.Marshal(p)
		_, err := backupRestoreFromTarHandler(context.Background(), raw)
		return err
	}
	// mode=tenant with a nil (missing) allowlist → refused BEFORE any restore work.
	err := call(map[string]any{"mode": "tenant", "job_id": "x", "tar_path": "/x", "target_username": "alice"})
	if err == nil || !strings.Contains(err.Error(), "mode=tenant requires") {
		t.Fatalf("nil allowlist under tenant must be refused, got %v", err)
	}
	// mode=tenant WITH both lists (empty allowed) → passes the guard, fails later
	// on the (bogus) job_id/tar_path, NOT on the allowlist guard.
	err = call(map[string]any{
		"mode": "tenant", "job_id": "x", "tar_path": "/x", "target_username": "alice",
		"allowed_db_names": []string{}, "allowed_mail_domains": []string{},
	})
	if err != nil && strings.Contains(err.Error(), "mode=tenant requires") {
		t.Fatalf("empty (present) allowlists must pass the guard, got %v", err)
	}
}

func TestIntersectComponents(t *testing.T) {
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
	// Empty request = "all" → the allowed set (NOT empty, so it can't fall open).
	if got := intersectComponents(nil, allowed); !eq(got, allowed) {
		t.Errorf("empty request: got %v, want %v", got, allowed)
	}
	if got := intersectComponents([]string{"home", "docker", "db"}, allowed); !eq(got, []string{"home", "db"}) {
		t.Errorf("subset: got %v, want [home db]", got)
	}
	if got := intersectComponents([]string{"docker", "dns"}, allowed); len(got) != 0 {
		t.Errorf("only-disallowed: got %v, want empty", got)
	}
}
