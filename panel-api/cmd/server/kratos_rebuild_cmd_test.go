package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeRebuildUserRepo is a minimal UserRepository: only LinkKratosIdentity
// is exercised by rebuildOne. Everything else is a stub that's never hit by
// this command path. `linkErr` lets a test force a relink failure to drive
// the rollback branch.
type fakeRebuildUserRepo struct {
	linkErr     error
	linkedNew   map[string]string // userID → new kratos UUID, for assertions
	linkAttempt int
}

func (f *fakeRebuildUserRepo) LinkKratosIdentity(_ context.Context, userID, kratosID string) error {
	f.linkAttempt++
	if f.linkErr != nil {
		return f.linkErr
	}
	if f.linkedNew == nil {
		f.linkedNew = map[string]string{}
	}
	f.linkedNew[userID] = kratosID
	return nil
}

// --- unused stubs (the compiler insists we satisfy the full interface) ---
func (*fakeRebuildUserRepo) Create(context.Context, *models.User) error { return nil }
func (*fakeRebuildUserRepo) FindByID(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (*fakeRebuildUserRepo) FindByEmail(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (*fakeRebuildUserRepo) FindByUsername(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (*fakeRebuildUserRepo) FindByKratosIdentityID(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (*fakeRebuildUserRepo) FindByIDs(context.Context, []string) ([]models.User, error) {
	return nil, nil
}
func (*fakeRebuildUserRepo) List(context.Context, repository.ListOptions) ([]models.User, int64, error) {
	return nil, 0, nil
}
func (*fakeRebuildUserRepo) Update(context.Context, *models.User) error { return nil }
func (*fakeRebuildUserRepo) UpdateCLIPHPVersion(context.Context, string, *string) error {
	return nil
}
func (*fakeRebuildUserRepo) SetAdmin(context.Context, string, bool) error { return nil }
func (*fakeRebuildUserRepo) CountAdmins(context.Context) (int64, error)   { return 0, nil }
func (*fakeRebuildUserRepo) FindAdminsByEmail(context.Context) ([]*models.User, error) {
	return nil, nil
}
func (*fakeRebuildUserRepo) SetSuspended(context.Context, string, bool, string) error { return nil }
func (*fakeRebuildUserRepo) SetSSHForwardingEnabled(context.Context, string, bool) error {
	return nil
}
func (*fakeRebuildUserRepo) Delete(context.Context, string) error { return nil }

// fakeKratos routes POST /admin/identities → returns a new UUID; POST
// /admin/recovery/code → returns a recovery link. Both paths are optional
// and can be overridden per test via the `identityHandler` / `codeHandler`
// fields. `getHandler` intercepts GET /admin/identities/{id} (the
// idempotency probe); default 404 lets existing tests proceed as if the
// old identity were gone.
type fakeKratos struct {
	identityHandler http.HandlerFunc
	codeHandler     http.HandlerFunc
	deleteHandler   http.HandlerFunc
	getHandler      http.HandlerFunc
}

func newFakeKratosServer(fk *fakeKratos) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/admin/identities":
			if fk.identityHandler != nil {
				fk.identityHandler(w, r)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"new-kratos-uuid","traits":{"email":"u@example.com","is_admin":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/admin/recovery/code":
			if fk.codeHandler != nil {
				fk.codeHandler(w, r)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"recovery_link":"https://panel.example/recover?token=xyz","recovery_code":"xyz"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/identities/"):
			if fk.deleteHandler != nil {
				fk.deleteHandler(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/identities/"):
			if fk.getHandler != nil {
				fk.getHandler(w, r)
				return
			}
			// Default 404 keeps existing tests (which exercise the
			// rebuild path) behaving as if the current identity is
			// gone — matching the DB-loss scenario.
			http.Error(w, "identity not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
}

func testUser(id, email string, admin bool, existingKratosID string) *models.User {
	u := &models.User{ID: id, Email: email, IsAdmin: admin}
	if existingKratosID != "" {
		u.KratosIdentityID = &existingKratosID
	}
	return u
}

func TestRebuildOne_HappyPath(t *testing.T) {
	t.Parallel()
	srv := newFakeKratosServer(&fakeKratos{})
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	u := testUser("user-id-1", "alice@example.com", false, "old-dangling")
	status, newID, link := rebuildOne(context.Background(), kc, users, u, "24h")

	if status != statusOK {
		t.Errorf("status = %q, want ok", status)
	}
	if newID != "new-kratos-uuid" {
		t.Errorf("newID = %q, want new-kratos-uuid", newID)
	}
	if link == "" {
		t.Error("link (temp password) should be non-empty under M54 (no recovery link)")
	}
	if users.linkedNew["user-id-1"] != "new-kratos-uuid" {
		t.Errorf("user row not relinked to new UUID: %v", users.linkedNew)
	}
}

func TestRebuildOne_CreateIdentityFailureBailsBeforeRelink(t *testing.T) {
	t.Parallel()
	srv := newFakeKratosServer(&fakeKratos{
		identityHandler: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "kratos explosion", http.StatusInternalServerError)
		},
	})
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	u := testUser("user-id-2", "bob@example.com", false, "old")
	status, newID, _ := rebuildOne(context.Background(), kc, users, u, "1h")

	if status != statusCreateFailed {
		t.Errorf("status = %q, want create_failed", status)
	}
	if newID != "" {
		t.Errorf("newID should be empty on CreateIdentityWithPassword failure, got %q", newID)
	}
	if users.linkAttempt != 0 {
		t.Errorf("LinkKratosIdentity must NOT be called when create failed (got %d attempts)", users.linkAttempt)
	}
}

func TestRebuildOne_LinkFailureRollsBackKratosCreate(t *testing.T) {
	t.Parallel()
	var deleteCalled bool
	var deletedID string
	fk := &fakeKratos{
		deleteHandler: func(w http.ResponseWriter, r *http.Request) {
			deleteCalled = true
			deletedID = strings.TrimPrefix(r.URL.Path, "/admin/identities/")
			w.WriteHeader(http.StatusNoContent)
		},
	}
	srv := newFakeKratosServer(fk)
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)

	users := &fakeRebuildUserRepo{linkErr: repository.ErrNotFound}
	u := testUser("missing-id", "ghost@example.com", false, "old")

	status, newID, _ := rebuildOne(context.Background(), kc, users, u, "1h")

	if status != statusLinkFailed {
		t.Errorf("status = %q, want link_failed", status)
	}
	if newID != "" {
		t.Errorf("newID must be empty when link failed (user row isn't relinked), got %q", newID)
	}
	if !deleteCalled {
		t.Error("rollback DeleteIdentity must run when LinkKratosIdentity fails — otherwise Kratos accumulates an orphan per retry")
	}
	if deletedID != "new-kratos-uuid" {
		t.Errorf("delete hit wrong identity: %q (want new-kratos-uuid)", deletedID)
	}
}

func TestRebuildOne_RecoveryCodeFailureKeepsRelink(t *testing.T) {
	t.Parallel()
	fk := &fakeKratos{
		codeHandler: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "recovery disabled", http.StatusBadRequest)
		},
	}
	srv := newFakeKratosServer(fk)
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	u := testUser("user-id-3", "carol@example.com", true, "old")
	status, newID, link := rebuildOne(context.Background(), kc, users, u, "1h")

	// M54: rebuild no longer calls /admin/recovery/code, so a failing recovery
	// endpoint is irrelevant — it succeeds and emits a temp password instead.
	if status != statusOK {
		t.Errorf("status = %q, want ok (M54 doesn't depend on the recovery endpoint)", status)
	}
	if newID != "new-kratos-uuid" {
		t.Errorf("newID = %q, want new-kratos-uuid (user is relinked)", newID)
	}
	if link == "" {
		t.Error("link (temp password) should be non-empty under M54")
	}
	if users.linkedNew["user-id-3"] != "new-kratos-uuid" {
		t.Error("user row must stay relinked")
	}
}

func TestRebuildOne_TraitsCarryUsernameAndAdmin(t *testing.T) {
	t.Parallel()
	var seenPayload map[string]any
	fk := &fakeKratos{
		identityHandler: func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&seenPayload)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"new-uuid","traits":{}}`))
		},
	}
	srv := newFakeKratosServer(fk)
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	uname := "linux_carol"
	u := &models.User{
		ID:               "user-id-4",
		Email:            "carol@example.com",
		IsAdmin:          true,
		Username:         &uname,
		KratosIdentityID: ptr("old"),
	}
	_, _, _ = rebuildOne(context.Background(), kc, users, u, "1h")

	traits, _ := seenPayload["traits"].(map[string]any)
	if traits["email"] != "carol@example.com" {
		t.Errorf("traits.email wrong: %v", traits)
	}
	if traits["username"] != "linux_carol" {
		t.Errorf("traits.username wrong: %v", traits)
	}
	if traits["is_admin"] != true {
		t.Errorf("traits.is_admin wrong: %v", traits)
	}
}

func ptr[T any](v T) *T { return &v }

func TestRebuildOne_SkipsWhenCurrentIdentityIsLive(t *testing.T) {
	t.Parallel()
	var createCalled bool
	fk := &fakeKratos{
		getHandler: func(w http.ResponseWriter, r *http.Request) {
			// Current ID still resolves — operator is re-running after a
			// partial rebuild and this user was already handled.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"live-existing","traits":{"email":"already@ok.com","is_admin":false}}`))
		},
		identityHandler: func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"wrong"}`))
		},
	}
	srv := newFakeKratosServer(fk)
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	u := testUser("user-id-5", "already@ok.com", false, "live-existing")
	status, kratosID, link := rebuildOne(context.Background(), kc, users, u, "1h")

	if status != statusSkippedLive {
		t.Errorf("status = %q, want skipped_live", status)
	}
	if kratosID != "live-existing" {
		t.Errorf("kratosID = %q, want live-existing (unchanged)", kratosID)
	}
	if link != "" {
		t.Errorf("recovery link should be empty for skipped user, got %q", link)
	}
	if createCalled {
		t.Error("CreateIdentityWithPassword must NOT be called when current identity is already live — that's the idempotency guarantee")
	}
	if users.linkAttempt != 0 {
		t.Errorf("LinkKratosIdentity must NOT be called for skipped users (got %d)", users.linkAttempt)
	}
}

func TestRebuildOne_ProbeTransportErrorAborts(t *testing.T) {
	t.Parallel()
	var createCalled bool
	fk := &fakeKratos{
		getHandler: func(w http.ResponseWriter, r *http.Request) {
			// Simulate Kratos being down or misconfigured — NOT a 404.
			// We must NOT proceed to mutate state in this case, or we'd
			// create duplicate identities the next time it recovers.
			http.Error(w, "internal error", http.StatusInternalServerError)
		},
		identityHandler: func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
		},
	}
	srv := newFakeKratosServer(fk)
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	u := testUser("user-id-6", "down@example.com", false, "existing-id")
	status, _, _ := rebuildOne(context.Background(), kc, users, u, "1h")

	if status != statusProbeFailed {
		t.Errorf("status = %q, want probe_failed", status)
	}
	if createCalled {
		t.Error("must not create new identity when the probe itself failed — Kratos state is ambiguous")
	}
}

func TestRebuildOne_ProbeNotFoundTriggersRebuild(t *testing.T) {
	t.Parallel()
	// This is the actual DB-loss case: the old identity_id points at
	// nothing because the Kratos DB is gone. Probe returns 404, we
	// proceed with the normal mint+relink+recovery dance.
	srv := newFakeKratosServer(&fakeKratos{})
	defer srv.Close()
	kc := kratosclient.NewClient(srv.URL, srv.URL)
	users := &fakeRebuildUserRepo{}

	u := testUser("user-id-7", "dbloss@example.com", false, "dangling-id")
	status, newID, _ := rebuildOne(context.Background(), kc, users, u, "1h")

	if status != statusOK {
		t.Errorf("status = %q, want ok (probe 404 must fall through to rebuild)", status)
	}
	if newID != "new-kratos-uuid" {
		t.Errorf("newID = %q, want new-kratos-uuid", newID)
	}
}

func (r *fakeRebuildUserRepo) UpdateUsername(context.Context, string, string) error { return nil }
