package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/trustedsenders"
)

// GH #2017: the senders a mailbox trusts.

type tsRepo struct {
	repository.MailboxTrustedSenderRepository
	rows  []models.MailboxTrustedSender
	count int64 // added to len(rows) by CountByMailbox
}

func (r *tsRepo) ListByMailbox(_ context.Context, mbID string) ([]models.MailboxTrustedSender, error) {
	var out []models.MailboxTrustedSender
	for _, row := range r.rows {
		if row.MailboxID == mbID {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func (r *tsRepo) CountByMailbox(ctx context.Context, mbID string) (int64, error) {
	rows, _ := r.ListByMailbox(ctx, mbID)
	return r.count + int64(len(rows)), nil
}

func (r *tsRepo) Create(_ context.Context, row *models.MailboxTrustedSender) error {
	for _, have := range r.rows {
		if have.MailboxID == row.MailboxID && have.Address == row.Address {
			return repository.ErrConflict
		}
	}
	r.rows = append(r.rows, *row)
	return nil
}

func (r *tsRepo) Delete(_ context.Context, mbID, id string) error {
	for i, row := range r.rows {
		if row.MailboxID == mbID && row.ID == id {
			r.rows = append(r.rows[:i], r.rows[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

type tsAgentCall struct {
	cmd  string
	spec trustedsenders.Spec
	// rows is how many rows the repo held when the agent was called.
	rows int
}

type tsAgent struct {
	calls []tsAgentCall
	err   error
	repo  *tsRepo
}

func (a *tsAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	var spec trustedsenders.Spec
	_ = json.Unmarshal(raw, &spec)
	a.calls = append(a.calls, tsAgentCall{cmd, spec, len(a.repo.rows)})
	return json.RawMessage(`{"ok":true}`), a.err
}

func tsFixture(claims *auth.AccessClaims) (*gin.Engine, *tsRepo, *tsAgent) {
	gin.SetMode(gin.TestMode)
	dom := &models.Domain{ID: "dom1", Name: "x.test", UserID: "u1"}
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "me", EmailCached: "me@x.test"}
	repo := &tsRepo{}
	ag := &tsAgent{repo: repo}
	r := gin.New()
	r.Use(func(c *gin.Context) { ginctx.SetClaims(c, claims); c.Next() })
	RegisterMailboxTrustedSenderRoutes(r.Group(""), MailboxTrustedSenderHandlerConfig{
		Mailboxes:      &saMailboxRepo{byID: map[string]*models.Mailbox{"mb1": mb}},
		Domains:        &saDomainRepo{byID: map[string]*models.Domain{"dom1": dom}},
		TrustedSenders: repo,
		Agent:          ag,
	})
	return r, repo, ag
}

var tsOwner = &auth.AccessClaims{UserID: "u1"}

func tsDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTrustedSenders_AddStoresTheCanonicalAddressAndPushesTheList(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	repo.rows = []models.MailboxTrustedSender{{ID: "old", MailboxID: "mb1", Address: "alice@y.test"}}

	w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", `{"address":"  Bob+News@Example.COM "}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID, Address, Warning string
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Address != "bob+news@example.com" || resp.ID == "" || resp.Warning != "" {
		t.Fatalf("response = %s", w.Body.String())
	}
	if len(repo.rows) != 2 || repo.rows[1].Address != "bob+news@example.com" || repo.rows[1].MailboxID != "mb1" {
		t.Fatalf("rows = %+v", repo.rows)
	}
	if len(ag.calls) != 1 || ag.calls[0].cmd != "mailbox.trusted_senders.apply" {
		t.Fatalf("agent calls = %+v", ag.calls)
	}
	got := ag.calls[0].spec
	if got.Email != "me@x.test" || len(got.Addresses) != 2 || got.Addresses[0] != "alice@y.test" || got.Addresses[1] != "bob+news@example.com" {
		t.Fatalf("pushed %+v, want the whole list", got)
	}
}

func TestTrustedSenders_AddRefusesAnInvalidAddress(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	for _, body := range []string{`{"address":"not-an-address"}`, `{"address":"a b@x.com"}`, `{"address":""}`, `{"address":"bob@localhost"}`} {
		w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", body)
		if w.Code != http.StatusUnprocessableEntity || !bytes.Contains(w.Body.Bytes(), []byte(`"invalid_address"`)) {
			t.Fatalf("%s: status %d: %s", body, w.Code, w.Body.String())
		}
	}
	if len(repo.rows) != 0 || len(ag.calls) != 0 {
		t.Fatalf("rows %+v calls %+v after refusals", repo.rows, ag.calls)
	}
}

func TestTrustedSenders_AddRefusesBadJSON(t *testing.T) {
	r, _, _ := tsFixture(tsOwner)
	if w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

func TestTrustedSenders_AddDuplicateIsConflict(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	repo.rows = []models.MailboxTrustedSender{{ID: "a", MailboxID: "mb1", Address: "bob@example.com"}}
	w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", `{"address":"BOB@example.com"}`)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte(`"already_trusted"`)) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 0 {
		t.Fatalf("agent called on a duplicate: %+v", ag.calls)
	}
}

func TestTrustedSenders_AddRefusedAtTheCap(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	repo.count = trustedsenders.MaxPerMailbox
	w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", `{"address":"bob@example.com"}`)
	if w.Code != http.StatusUnprocessableEntity || !bytes.Contains(w.Body.Bytes(), []byte(`"too_many_trusted_senders"`)) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(repo.rows) != 0 || len(ag.calls) != 0 {
		t.Fatalf("rows %+v calls %+v at the cap", repo.rows, ag.calls)
	}
	// One under the cap is accepted.
	repo.count = trustedsenders.MaxPerMailbox - 1
	if w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", `{"address":"bob@example.com"}`); w.Code != http.StatusCreated {
		t.Fatalf("one under the cap: status %d", w.Code)
	}
}

// The mail server being down does not lose the sender: the row is kept and
// the reconciler applies it later. The answer says so.
func TestTrustedSenders_AddKeepsTheRowWhenTheMailServerFails(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	ag.err = errors.New("agent down")
	w := tsDo(r, "POST", "/mailboxes/mb1/trusted-senders", `{"address":"bob@example.com"}`)
	if w.Code != http.StatusCreated || !bytes.Contains(w.Body.Bytes(), []byte(`"warning"`)) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(repo.rows) != 1 {
		t.Fatalf("rows = %+v", repo.rows)
	}
}

func TestTrustedSenders_List(t *testing.T) {
	r, repo, _ := tsFixture(tsOwner)
	repo.rows = []models.MailboxTrustedSender{
		{ID: "b", MailboxID: "mb1", Address: "bob@example.com"},
		{ID: "a", MailboxID: "mb1", Address: "alice@example.com"},
		{ID: "z", MailboxID: "other", Address: "zed@example.com"},
	}
	w := tsDo(r, "GET", "/mailboxes/mb1/trusted-senders", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp struct {
		Data []struct {
			ID, Address string
		}
		Total, Max int
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 || resp.Data[0].Address != "alice@example.com" || resp.Total != 2 || resp.Max != trustedsenders.MaxPerMailbox {
		t.Fatalf("list = %s", w.Body.String())
	}
	// An empty list is [], not null.
	repo.rows = nil
	if w := tsDo(r, "GET", "/mailboxes/mb1/trusted-senders", ""); !bytes.Contains(w.Body.Bytes(), []byte(`"data":[]`)) {
		t.Fatalf("empty list = %s", w.Body.String())
	}
}

// Removing a sender takes it off the mail server first, then deletes the
// row, so the last removal can't leave a card behind that no pass sweeps.
func TestTrustedSenders_DeletePushesTheListWithoutTheRowFirst(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	repo.rows = []models.MailboxTrustedSender{
		{ID: "a", MailboxID: "mb1", Address: "alice@example.com"},
		{ID: "b", MailboxID: "mb1", Address: "bob@example.com"},
	}
	w := tsDo(r, "DELETE", "/mailboxes/mb1/trusted-senders/a", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 1 || ag.calls[0].rows != 2 {
		t.Fatalf("calls = %+v; want one push while both rows were still stored", ag.calls)
	}
	if got := ag.calls[0].spec.Addresses; len(got) != 1 || got[0] != "bob@example.com" {
		t.Fatalf("pushed %v, want the list without alice", got)
	}
	if len(repo.rows) != 1 || repo.rows[0].ID != "b" {
		t.Fatalf("rows = %+v", repo.rows)
	}

	// The last one: an empty list.
	if w := tsDo(r, "DELETE", "/mailboxes/mb1/trusted-senders/b", ""); w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if got := ag.calls[1].spec.Addresses; got == nil || len(got) != 0 {
		t.Fatalf("last removal pushed %#v, want []", got)
	}
}

// When the mail server can't take the sender off, the row stays and the
// answer is an error: the sender is still trusted there.
func TestTrustedSenders_DeleteKeepsTheRowWhenTheMailServerFails(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	repo.rows = []models.MailboxTrustedSender{{ID: "a", MailboxID: "mb1", Address: "alice@example.com"}}
	ag.err = errors.New("agent down")
	w := tsDo(r, "DELETE", "/mailboxes/mb1/trusted-senders/a", "")
	if w.Code != http.StatusBadGateway || !bytes.Contains(w.Body.Bytes(), []byte(`"mail_server_unavailable"`)) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(repo.rows) != 1 {
		t.Fatalf("row deleted although the mail server still trusts it: %+v", repo.rows)
	}
}

// An id that is not one of the mailbox's rows is not found, and the mail
// server is not asked to change anything.
func TestTrustedSenders_DeleteUnknown(t *testing.T) {
	r, repo, ag := tsFixture(tsOwner)
	repo.rows = []models.MailboxTrustedSender{
		{ID: "a", MailboxID: "mb1", Address: "alice@example.com"},
		{ID: "z", MailboxID: "other", Address: "zed@example.com"},
	}
	for _, id := range []string{"z", "nope"} {
		if w := tsDo(r, "DELETE", "/mailboxes/mb1/trusted-senders/"+id, ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d", id, w.Code)
		}
	}
	if len(ag.calls) != 0 || len(repo.rows) != 2 {
		t.Fatalf("calls %+v rows %+v", ag.calls, repo.rows)
	}
}

func TestTrustedSenders_OnlyTheOwnerOrAnAdmin(t *testing.T) {
	r, repo, ag := tsFixture(&auth.AccessClaims{UserID: "intruder"})
	repo.rows = []models.MailboxTrustedSender{{ID: "a", MailboxID: "mb1", Address: "alice@example.com"}}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/mailboxes/mb1/trusted-senders", ""},
		{"POST", "/mailboxes/mb1/trusted-senders", `{"address":"bob@example.com"}`},
		{"DELETE", "/mailboxes/mb1/trusted-senders/a", ""},
	} {
		if w := tsDo(r, c.method, c.path, c.body); w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status %d", c.method, c.path, w.Code)
		}
	}
	if len(ag.calls) != 0 || len(repo.rows) != 1 {
		t.Fatalf("calls %+v rows %+v", ag.calls, repo.rows)
	}

	admin, _, _ := tsFixture(&auth.AccessClaims{UserID: "root", IsAdmin: true})
	if w := tsDo(admin, "GET", "/mailboxes/mb1/trusted-senders", ""); w.Code != http.StatusOK {
		t.Fatalf("admin: status %d", w.Code)
	}
	if w := tsDo(admin, "GET", "/mailboxes/nope/trusted-senders", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown mailbox: status %d", w.Code)
	}
}
