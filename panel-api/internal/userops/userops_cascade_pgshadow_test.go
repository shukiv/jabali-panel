package userops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// recCascadeAgent records agent calls and can fail selected verbs.
type recCascadeAgent struct {
	calls  []string
	params []map[string]any
	failOn map[string]bool
}

func (a *recCascadeAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, method)
	m, _ := params.(map[string]any)
	a.params = append(a.params, m)
	if a.failOn[method] {
		return nil, errors.New(method + " boom")
	}
	return json.RawMessage(`{}`), nil
}

// fakeCascadeUsers records the row delete; all other UserRepository methods are
// unused (embedded interface panics on unexpected use).
type fakeCascadeUsers struct {
	repository.UserRepository
	deleted string
}

func (f *fakeCascadeUsers) Delete(_ context.Context, id string) error {
	f.deleted = id
	return nil
}

func cascadeCalledWith(a *recCascadeAgent, method, key, val string) bool {
	for i, c := range a.calls {
		if c == method && i < len(a.params) && a.params[i][key] == val {
			return true
		}
	}
	return false
}

// JAB-289: deleting a tenant who used the Adminer SSO bridge against Postgres
// must drop their <osuser>_pgadmin shadow LOGIN role, or a live credential is
// orphaned on the engine.
func TestDeleteCascade_DropsPgShadowRole(t *testing.T) {
	ag := &recCascadeAgent{}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("alice"), PgadminUsername: strptr("alice_pgadmin")}

	if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if !cascadeCalledWith(ag, "db.postgres.drop_role", "role", "alice_pgadmin") {
		t.Fatalf("PG shadow role not dropped; calls=%v params=%v", ag.calls, ag.params)
	}
	// The MariaDB shadow is still reaped too.
	if !cascadeCalledWith(ag, "db_user.drop", "db_user_name", "alice_mysqladmin") {
		t.Errorf("MariaDB shadow reap regressed; calls=%v", ag.calls)
	}
	if users.deleted != "u1" {
		t.Errorf("user row should be deleted after a clean cascade, got %q", users.deleted)
	}
}

// No PG shadow provisioned → no drop_role attempted (also avoids hitting the
// optional Postgres engine on MariaDB-only boxes).
func TestDeleteCascade_NoPgShadow_NoRoleDrop(t *testing.T) {
	ag := &recCascadeAgent{}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("alice")} // PgadminUsername nil

	if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	for _, c := range ag.calls {
		if c == "db.postgres.drop_role" {
			t.Fatalf("must not attempt a PG role drop with no shadow provisioned; calls=%v", ag.calls)
		}
	}
}

// A failed shadow-role drop aborts the cascade with DBCleanupError and keeps the
// user row as the only handle on the orphaned role — never a silent delete.
func TestDeleteCascade_PgShadowDropFails_AbortsBeforeRowDelete(t *testing.T) {
	ag := &recCascadeAgent{failOn: map[string]bool{"db.postgres.drop_role": true}}
	users := &fakeCascadeUsers{}
	target := &models.User{ID: "u1", Username: strptr("alice"), PgadminUsername: strptr("alice_pgadmin")}

	err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{}, target, "test")
	var ce *DBCleanupError
	if !errors.As(err, &ce) {
		t.Fatalf("expected DBCleanupError, got %v", err)
	}
	if users.deleted != "" {
		t.Errorf("user row must NOT be deleted when a shadow drop failed, got %q", users.deleted)
	}
}
