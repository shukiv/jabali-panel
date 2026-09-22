package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- minimal fakes: only the methods create()/loadMailbox/Converge call ---

type fwFakeMailboxes struct {
	repository.MailboxRepository
	mb *models.Mailbox
}

func (f fwFakeMailboxes) FindByID(context.Context, string) (*models.Mailbox, error) {
	return f.mb, nil
}

type fwFakeDomains struct {
	repository.DomainRepository
	dom *models.Domain
}

func (f fwFakeDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return f.dom, nil
}

type fwFakeForwarders struct {
	repository.EmailForwarderRepository
	rows []models.EmailForwarder
}

func (f *fwFakeForwarders) Create(_ context.Context, row *models.EmailForwarder) error {
	f.rows = append(f.rows, *row)
	return nil
}

func (f *fwFakeForwarders) ListByMailboxID(context.Context, string, repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

// fwStubAgent returns a fixed result/error for forwarder.apply.
type fwStubAgent struct{ err error }

func (a fwStubAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), a.err
}

func postForwarder(h *forwarderHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "mbid", Value: "mb1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/mailboxes/mb1/forwarders", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
	h.create(c)
	return w
}

func newForwarderHandlerFake(agentErr error) *forwarderHandler {
	return &forwarderHandler{cfg: MailboxForwarderHandlerConfig{
		Mailboxes:  fwFakeMailboxes{mb: &models.Mailbox{ID: "mb1", LocalPart: "joe", DomainID: "dom1"}},
		Domains:    fwFakeDomains{dom: &models.Domain{ID: "dom1", Name: "example.com", UserID: "u1"}},
		Forwarders: &fwFakeForwarders{},
		Agent:      fwStubAgent{err: agentErr},
	}}
}

// GH #1795: a forwarder create whose follow-up forwarder.apply FAILS must not
// report an unqualified 201 — the row persists but is not forwarding until it
// converges. The response carries warning.code=convergence_failed so the caller
// (and a future UI) can see the divergence. Falsify by reverting create() to
// swallow the applyForwarders error: the warning field disappears → RED.
func TestForwarderCreate_SurfacesConvergenceFailure(t *testing.T) {
	h := newForwarderHandlerFake(errors.New("mailbox not registered with mail server"))
	w := postForwarder(h, `{"type":"external","target":"out@elsewhere.com"}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (DB is truth; the row still persists)", w.Code)
	}
	var resp struct {
		Warning *struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"warning"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Warning == nil {
		t.Fatalf("expected a convergence_failed warning, got none: %s", w.Body.String())
	}
	if resp.Warning.Code != "convergence_failed" {
		t.Errorf("warning.code = %q, want convergence_failed", resp.Warning.Code)
	}
	if resp.Warning.Detail == "" {
		t.Errorf("warning.detail is empty; the agent error must be surfaced")
	}
}

// The happy path stays clean: a successful apply carries no warning, so the
// normal 201 is unchanged and the SPA sees exactly what it did before.
func TestForwarderCreate_NoWarningOnSuccess(t *testing.T) {
	h := newForwarderHandlerFake(nil)
	w := postForwarder(h, `{"type":"external","target":"out@elsewhere.com"}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if strings.Contains(w.Body.String(), "warning") {
		t.Fatalf("no warning expected on a successful apply, got: %s", w.Body.String())
	}
}
