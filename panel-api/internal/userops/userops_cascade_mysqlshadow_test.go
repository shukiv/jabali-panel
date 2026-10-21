package userops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// shadowDrops lists the db_user.drop names the cascade asked for.
func shadowDrops(a *recCascadeAgent) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for i, c := range a.calls {
		if c == "db_user.drop" && i < len(a.params) {
			if n, ok := a.params[i]["db_user_name"].(string); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// A panel username may hold "-", and so may its shadow-admin: the cascade
// drops "<name>_mysqladmin" like any other.
func TestDeleteCascade_HyphenUsername_DropsShadow(t *testing.T) {
	ag := &recCascadeAgent{}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("claude-uitest")}

	if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if got := shadowDrops(ag); len(got) != 1 || got[0] != "claude-uitest_mysqladmin" {
		t.Errorf("drops = %v, want [claude-uitest_mysqladmin]", got)
	}
	if users.deleted != "u1" {
		t.Errorf("user row should be deleted, got %q", users.deleted)
	}
}

// A panel username may start with "_", but the agent never makes a
// shadow-admin for one and refuses to drop "<name>_mysqladmin" (db_user.drop
// name rule). Asking it anyway failed the drop, and the cascade kept the
// account forever.
func TestDeleteCascade_UnderscoreUsername_NoShadowDropAsked(t *testing.T) {
	// failOn mirrors the real agent, which rejects the name.
	ag := &recCascadeAgent{failOn: map[string]bool{"db_user.drop": true}}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("_svc")}

	if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if got := shadowDrops(ag); len(got) != 0 {
		t.Errorf("no shadow-admin can exist for this username, but drops were asked: %v", got)
	}
	if users.deleted != "u1" {
		t.Errorf("user row should be deleted, got %q", users.deleted)
	}
}

// A rename onto a username starting with "_" renames the shadow with it, and
// the agent cannot drop that name. The account is kept rather than orphan the
// login.
func TestDeleteCascade_RenamedUnderscoreShadow_KeepsAccount(t *testing.T) {
	ag := &recCascadeAgent{}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("_svc"), MysqladminUsername: strptr("_svc_mysqladmin")}

	err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test")
	var ce *DBCleanupError
	if !errors.As(err, &ce) {
		t.Fatalf("expected DBCleanupError, got %v", err)
	}
	if users.deleted != "" {
		t.Errorf("user row must NOT be deleted while its shadow login can't be dropped, got %q", users.deleted)
	}
}

// The shadow is dropped by its derived name, never by the one stored on the
// row: users.mysqladmin_username can arrive from an uploaded backup, and a
// forged value must not drop another account's login.
func TestDeleteCascade_MysqlShadowDroppedByDerivedNameOnly(t *testing.T) {
	ag := &recCascadeAgent{}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("alice"), MysqladminUsername: strptr("bob_mysqladmin")}

	if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if got := shadowDrops(ag); len(got) != 1 || got[0] != "alice_mysqladmin" {
		t.Errorf("drops = %v, want [alice_mysqladmin]", got)
	}
}
