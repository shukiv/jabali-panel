package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- in-memory store for the share handlers ---

type shStore struct {
	mailboxes map[string]models.Mailbox
	domains   map[string]models.Domain
	shares    map[string]models.MailboxShare
}

// newShStore: tenant u1 owns one.test (alice, bob); tenant u2 owns
// two.test (mallory).
func newShStore() *shStore {
	s := &shStore{
		mailboxes: map[string]models.Mailbox{},
		domains: map[string]models.Domain{
			"d1": {ID: "d1", UserID: "u1", Name: "one.test"},
			"d2": {ID: "d2", UserID: "u2", Name: "two.test"},
		},
		shares: map[string]models.MailboxShare{},
	}
	for _, mb := range []models.Mailbox{
		{ID: "alice", DomainID: "d1", LocalPart: "alice", EmailCached: "alice@one.test"},
		{ID: "bob", DomainID: "d1", LocalPart: "bob", EmailCached: "bob@one.test"},
		{ID: "mallory", DomainID: "d2", LocalPart: "mallory", EmailCached: "mallory@two.test"},
	} {
		s.mailboxes[mb.ID] = mb
	}
	return s
}

type shMailboxes struct {
	repository.MailboxRepository
	s *shStore
}

func (r shMailboxes) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	mb, ok := r.s.mailboxes[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &mb, nil
}

func (r shMailboxes) FindByIDs(_ context.Context, ids []string) ([]models.Mailbox, error) {
	var out []models.Mailbox
	for _, id := range ids {
		if mb, ok := r.s.mailboxes[id]; ok {
			out = append(out, mb)
		}
	}
	return out, nil
}

type shDomains struct {
	repository.DomainRepository
	s *shStore
}

func (r shDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	d, ok := r.s.domains[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &d, nil
}

func (r shDomains) FindByIDs(_ context.Context, ids []string) ([]models.Domain, error) {
	var out []models.Domain
	for _, id := range ids {
		if d, ok := r.s.domains[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

type shShares struct {
	repository.MailboxShareRepository
	s *shStore
}

func (r shShares) FindByID(_ context.Context, id string) (*models.MailboxShare, error) {
	sh, ok := r.s.shares[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &sh, nil
}

func (r shShares) FindByOwnerID(_ context.Context, owner string, _ repository.ListOptions) ([]models.MailboxShare, int64, error) {
	var out []models.MailboxShare
	for _, sh := range r.s.shares {
		if sh.OwnerMailboxID == owner {
			out = append(out, sh)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, int64(len(out)), nil
}

func (r shShares) Create(_ context.Context, sh *models.MailboxShare) error {
	r.s.shares[sh.ID] = *sh
	return nil
}

func (r shShares) DeleteByOwner(_ context.Context, id, owner string) error {
	sh, ok := r.s.shares[id]
	if !ok || sh.OwnerMailboxID != owner {
		return repository.ErrNotFound
	}
	delete(r.s.shares, id)
	return nil
}

// shAgent records every command and its params as JSON.
type shAgent struct {
	calls []string
	sent  []map[string]any
	err   error
}

func (a *shAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	b, _ := json.Marshal(params)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	a.sent = append(a.sent, m)
	return json.RawMessage(`{"ok":true}`), a.err
}

func newShareHandlerFake(s *shStore, ag *shAgent) *shareHandler {
	return &shareHandler{cfg: MailboxShareHandlerConfig{
		Mailboxes:     shMailboxes{s: s},
		Domains:       shDomains{s: s},
		MailboxShares: shShares{s: s},
		Agent:         ag,
	}}
}

func shareRequest(h *shareHandler, method, mbid, shareID, body string, claims *auth.AccessClaims) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "mbid", Value: mbid}}
	path := "/mailboxes/" + mbid + "/shares"
	if shareID != "" {
		c.Params = append(c.Params, gin.Param{Key: "shareId", Value: shareID})
		path += "/" + shareID
	}
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, claims)
	if method == http.MethodPost {
		h.create(c)
	} else {
		h.del(c)
	}
	return w
}

var tenantU1 = &auth.AccessClaims{UserID: "u1"}

// A share hands the target read access to the owner's mail, so a tenant must
// not be able to grant it to another tenant's mailbox. The old handler only
// checked that the target existed and answered 201.
func TestShareCreate_RefusesATargetInAnotherAccount(t *testing.T) {
	for _, claims := range []*auth.AccessClaims{tenantU1, {UserID: "admin", IsAdmin: true}} {
		s := newShStore()
		ag := &shAgent{}
		w := shareRequest(newShareHandlerFake(s, ag), http.MethodPost, "alice", "",
			`{"shared_with_mailbox_id":"mallory","rights":{"mayRead":true}}`, claims)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "target_not_found") {
			t.Fatalf("admin=%v: status %d body %s, want 400 target_not_found", claims.IsAdmin, w.Code, w.Body.String())
		}
		if len(s.shares) != 0 || len(ag.calls) != 0 {
			t.Errorf("admin=%v: rows=%d agent calls=%v, want none", claims.IsAdmin, len(s.shares), ag.calls)
		}
	}
}

// Creating a share must reach Stalwart. The old handler saved the row and
// relied on a reconciler phase that was never registered.
func TestShareCreate_AppliesTheShareToStalwart(t *testing.T) {
	s := newShStore()
	ag := &shAgent{}
	w := shareRequest(newShareHandlerFake(s, ag), http.MethodPost, "alice", "",
		`{"shared_with_mailbox_id":"bob","rights":{"mayRead":true}}`, tenantU1)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d body %s, want 201", w.Code, w.Body.String())
	}
	if len(ag.calls) != 1 || ag.calls[0] != "mailbox.share_set" {
		t.Fatalf("agent calls = %v, want one mailbox.share_set", ag.calls)
	}
	shares, _ := ag.sent[0]["shares"].(map[string]any)
	if ag.sent[0]["owner_email"] != "alice@one.test" || shares["bob@one.test"] == nil {
		t.Errorf("payload = %v, want alice's list with bob", ag.sent[0])
	}
	if strings.Contains(w.Body.String(), "warning") {
		t.Errorf("unexpected warning on a clean apply: %s", w.Body.String())
	}
}

func TestShareCreate_ApplyFailureReturnsAWarning(t *testing.T) {
	s := newShStore()
	ag := &shAgent{err: errors.New("stalwart down")}
	w := shareRequest(newShareHandlerFake(s, ag), http.MethodPost, "alice", "",
		`{"shared_with_mailbox_id":"bob","rights":{"mayRead":true}}`, tenantU1)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201 (the row is kept for the sweep)", w.Code)
	}
	var resp struct {
		Warning *struct {
			Code string `json:"code"`
		} `json:"warning"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Warning == nil || resp.Warning.Code != "convergence_failed" {
		t.Fatalf("body %s, want warning.code=convergence_failed", w.Body.String())
	}
	if len(s.shares) != 1 {
		t.Errorf("rows = %d, want the share kept", len(s.shares))
	}
}

func TestShareCreate_Rejections(t *testing.T) {
	cases := []struct {
		name, body string
		seed       bool
		status     int
		code       string
	}{
		{"self", `{"shared_with_mailbox_id":"alice","rights":{"mayRead":true}}`, false, http.StatusBadRequest, "cannot_share_with_self"},
		{"no rights", `{"shared_with_mailbox_id":"bob","rights":{}}`, false, http.StatusBadRequest, "rights_required"},
		{"duplicate", `{"shared_with_mailbox_id":"bob","rights":{"mayRead":true}}`, true, http.StatusConflict, "already_shared"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newShStore()
			if tc.seed {
				s.shares["s0"] = models.MailboxShare{ID: "s0", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: models.Rights{MayRead: true}}
			}
			w := shareRequest(newShareHandlerFake(s, &shAgent{}), http.MethodPost, "alice", "", tc.body, tenantU1)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("status %d body %s, want %d %s", w.Code, w.Body.String(), tc.status, tc.code)
			}
		})
	}
}

// A revoke is applied on Stalwart BEFORE the row goes. The old handler
// deleted the row, answered 204 and never told Stalwart.
func TestShareDelete_RevokesOnStalwartThenDeletesTheRow(t *testing.T) {
	s := newShStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: models.Rights{MayRead: true}}
	ag := &shAgent{}
	w := shareRequest(newShareHandlerFake(s, ag), http.MethodDelete, "alice", "s1", "", tenantU1)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s, want 204", w.Code, w.Body.String())
	}
	if len(ag.calls) != 1 || ag.calls[0] != "mailbox.share_set" {
		t.Fatalf("agent calls = %v, want one mailbox.share_set", ag.calls)
	}
	if shares, _ := ag.sent[0]["shares"].(map[string]any); len(shares) != 0 {
		t.Errorf("pushed shares = %v, want an empty list", shares)
	}
	if _, ok := s.shares["s1"]; ok {
		t.Error("row still present after a 204")
	}
}

// When Stalwart does not accept the revoke, the panel must not claim it
// happened: 502, and the row stays so the list still shows the live share.
func TestShareDelete_AgentFailureKeepsTheRow(t *testing.T) {
	s := newShStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: models.Rights{MayRead: true}}
	ag := &shAgent{err: errors.New("stalwart down")}
	w := shareRequest(newShareHandlerFake(s, ag), http.MethodDelete, "alice", "s1", "", tenantU1)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "share_apply_failed") {
		t.Fatalf("status %d body %s, want 502 share_apply_failed", w.Code, w.Body.String())
	}
	if _, ok := s.shares["s1"]; !ok {
		t.Fatal("row deleted although the revoke never reached Stalwart")
	}
}

// Deleting another owner's share through your own mailbox stays a 404 (the
// DeleteByOwner scoping the handler already had).
func TestShareDelete_AnotherOwnersShareIsNotFound(t *testing.T) {
	s := newShStore()
	s.shares["s1"] = models.MailboxShare{ID: "s1", OwnerMailboxID: "alice", SharedWithMailboxID: "bob", Rights: models.Rights{MayRead: true}}
	ag := &shAgent{}
	w := shareRequest(newShareHandlerFake(s, ag), http.MethodDelete, "bob", "s1", "", tenantU1)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
	if _, ok := s.shares["s1"]; !ok || len(ag.calls) != 0 {
		t.Errorf("row kept=%v agent calls=%v, want row kept and no calls", ok, ag.calls)
	}
}
