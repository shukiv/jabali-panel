package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- self-contained fakes ---

type srDomFake struct {
	repository.DomainRepository
	dom *models.Domain
}

func (f *srDomFake) FindByID(context.Context, string) (*models.Domain, error) { return f.dom, nil }

type srAgentFake struct{ calls []string }

func (a *srAgentFake) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	return json.RawMessage(`{"ok":true}`), nil
}

type srResFake struct {
	repository.SharedResourceRepository
	created    []*models.SharedResource
	grantsSet  map[string][]models.SharedResourceGrant
	tombstones []string
	tombErr    error
	deleted    []string
	byID       map[string]*models.SharedResource
}

func (f *srResFake) ExistsByEmail(context.Context, string) (bool, error) { return false, nil }
func (f *srResFake) Create(_ context.Context, r *models.SharedResource) error {
	f.created = append(f.created, r)
	return nil
}
func (f *srResFake) ListByDomainID(context.Context, string) ([]models.SharedResource, error) {
	out := make([]models.SharedResource, 0, len(f.byID))
	for _, v := range f.byID {
		out = append(out, *v)
	}
	return out, nil
}
func (f *srResFake) FindByID(_ context.Context, id string) (*models.SharedResource, error) {
	if v, ok := f.byID[id]; ok {
		return v, nil
	}
	return nil, repository.ErrNotFound
}
func (f *srResFake) ReplaceGrants(_ context.Context, id string, g []models.SharedResourceGrant) error {
	if f.grantsSet == nil {
		f.grantsSet = map[string][]models.SharedResourceGrant{}
	}
	f.grantsSet[id] = g
	return nil
}
func (f *srResFake) AddTombstone(_ context.Context, e string) error {
	f.tombstones = append(f.tombstones, e)
	return f.tombErr
}
func (f *srResFake) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// srMbFake / srMgFake are the grantee-existence lookups the grant handler now
// consults (JAB-339 AC4). They embed the full repository interface but only
// implement FindByID: a seeded id resolves, anything else is ErrNotFound, and a
// non-nil err drives the fail-closed path.
type srMbFake struct {
	repository.MailboxRepository
	ids map[string]bool
	err error
}

func (f *srMbFake) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.ids[id] {
		return &models.Mailbox{ID: id}, nil
	}
	return nil, repository.ErrNotFound
}

type srMgFake struct {
	repository.MailGroupRepository
	ids map[string]bool
	err error
}

func (f *srMgFake) FindByID(_ context.Context, id string) (*models.MailGroup, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.ids[id] {
		return &models.MailGroup{ID: id}, nil
	}
	return nil, repository.ErrNotFound
}

// srRouter wires the default grantee lookups: mailbox "mb1" and group "grp1"
// exist. Tests that need a missing or erroring grantee use srRouterFull.
func srRouter(t *testing.T, res *srResFake, ag *srAgentFake) *gin.Engine {
	return srRouterFull(t, res, ag,
		&srMbFake{ids: map[string]bool{"mb1": true}},
		&srMgFake{ids: map[string]bool{"grp1": true}})
}

func srRouterFull(t *testing.T, res *srResFake, ag *srAgentFake, mb *srMbFake, mg *srMgFake) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1", IsAdmin: false})
		c.Next()
	})
	dom := &srDomFake{dom: &models.Domain{ID: "dom1", UserID: "user1", Name: "example.org", EmailEnabled: true}}
	RegisterSharedResourceRoutes(v1, SharedResourceHandlerConfig{
		Resources: res, Domains: dom, Mailboxes: mb, MailGroups: mg, Agent: ag,
	})
	return r
}

func do(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSharedResource_Create(t *testing.T) {
	res := &srResFake{}
	ag := &srAgentFake{}
	r := srRouter(t, res, ag)

	w := do(t, r, "POST", "/api/v1/domains/dom1/shared-resources",
		map[string]any{"name": "teamcal", "kind": "calendar", "display_name": "Team Cal"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, res.created, 1)
	require.Equal(t, "calendar", res.created[0].Kind)
	require.Equal(t, "teamcal@example.org", *res.created[0].EmailCached)
	require.Contains(t, ag.calls, "sharedresource.apply")
}

func TestSharedResource_Create_InvalidKind(t *testing.T) {
	r := srRouter(t, &srResFake{}, &srAgentFake{})
	w := do(t, r, "POST", "/api/v1/domains/dom1/shared-resources",
		map[string]any{"name": "x", "kind": "bogus"})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharedResource_SetGrants(t *testing.T) {
	res := &srResFake{byID: map[string]*models.SharedResource{
		"res1": {ID: "res1", DomainID: "dom1", Kind: "calendar"},
	}}
	r := srRouter(t, res, &srAgentFake{})
	// bad rights -> 400
	w := do(t, r, "PUT", "/api/v1/shared-resources/res1/grants",
		map[string]any{"grants": []map[string]any{{"grantee_kind": "mailbox", "grantee_id": "mb1", "rights": "boss"}}})
	require.Equal(t, http.StatusBadRequest, w.Code)
	// valid -> 200 + grants stored
	w = do(t, r, "PUT", "/api/v1/shared-resources/res1/grants",
		map[string]any{"grants": []map[string]any{{"grantee_kind": "mailbox", "grantee_id": "mb1", "rights": "readwrite"}}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, res.grantsSet["res1"], 1)
	require.Equal(t, "readwrite", res.grantsSet["res1"][0].Rights)
}

// JAB-339 AC4: a grant to a non-existent mailbox grantee is rejected 400
// grantee_not_found BEFORE the write — the dangling reference never persists.
func TestSharedResource_SetGrants_GranteeNotFound(t *testing.T) {
	res := &srResFake{byID: map[string]*models.SharedResource{
		"res1": {ID: "res1", DomainID: "dom1", Kind: "calendar"},
	}}
	r := srRouter(t, res, &srAgentFake{}) // seeds mb1 / grp1 only
	w := do(t, r, "PUT", "/api/v1/shared-resources/res1/grants",
		map[string]any{"grants": []map[string]any{{"grantee_kind": "mailbox", "grantee_id": "ghost", "rights": "read"}}})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "grantee_not_found")
	require.Nil(t, res.grantsSet, "ReplaceGrants must not run when a grantee is missing")
}

// A group grantee is checked against the mail-group lookup too.
func TestSharedResource_SetGrants_GroupGranteeNotFound(t *testing.T) {
	res := &srResFake{byID: map[string]*models.SharedResource{
		"res1": {ID: "res1", DomainID: "dom1", Kind: "calendar"},
	}}
	r := srRouter(t, res, &srAgentFake{})
	w := do(t, r, "PUT", "/api/v1/shared-resources/res1/grants",
		map[string]any{"grants": []map[string]any{{"grantee_kind": "group", "grantee_id": "ghost", "rights": "read"}}})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "grantee_not_found")
	require.Nil(t, res.grantsSet)
}

// Fail-closed: a lookup that errors (not a clean not-found) is a 500, and the
// grant is NOT written. A transient DB blip must never let a grant slip through.
func TestSharedResource_SetGrants_LookupFailsClosed(t *testing.T) {
	res := &srResFake{byID: map[string]*models.SharedResource{
		"res1": {ID: "res1", DomainID: "dom1", Kind: "calendar"},
	}}
	r := srRouterFull(t, res, &srAgentFake{},
		&srMbFake{err: errors.New("db down")},
		&srMgFake{ids: map[string]bool{"grp1": true}})
	w := do(t, r, "PUT", "/api/v1/shared-resources/res1/grants",
		map[string]any{"grants": []map[string]any{{"grantee_kind": "mailbox", "grantee_id": "mb1", "rights": "read"}}})
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.Nil(t, res.grantsSet, "ReplaceGrants must not run on a lookup error")
}

func TestSharedResource_Delete_Tombstones(t *testing.T) {
	res := &srResFake{byID: map[string]*models.SharedResource{
		"res1": {ID: "res1", DomainID: "dom1", Kind: "calendar", EmailCached: strptr("teamcal@example.org")},
	}}
	ag := &srAgentFake{}
	r := srRouter(t, res, ag)
	w := do(t, r, "DELETE", "/api/v1/shared-resources/res1", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"teamcal@example.org"}, res.tombstones)
	require.Contains(t, ag.calls, "sharedresource.destroy")
	require.Equal(t, []string{"res1"}, res.deleted)
}

// AC5: a tombstone that cannot be persisted must NOT delete the row — deleting
// it would strand the Stalwart host principal with no handle for the reconciler
// GC. Was 200 + row deleted (the swallowed-tombstone bug); now 500 + row kept.
func TestSharedResource_Delete_TombstoneFailureKeepsRow(t *testing.T) {
	res := &srResFake{
		tombErr: errors.New("db down"),
		byID: map[string]*models.SharedResource{
			"res1": {ID: "res1", DomainID: "dom1", Kind: "calendar", EmailCached: strptr("teamcal@example.org")},
		},
	}
	ag := &srAgentFake{}
	r := srRouter(t, res, ag)
	w := do(t, r, "DELETE", "/api/v1/shared-resources/res1", nil)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.Empty(t, res.deleted, "row must not be deleted when the tombstone fails")
	require.NotContains(t, ag.calls, "sharedresource.destroy", "destroy must not fire when the tombstone fails")
}

func strptr(s string) *string { return &s }
