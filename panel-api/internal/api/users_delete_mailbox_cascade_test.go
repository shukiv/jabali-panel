package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// udDomainRepo: interface-embedding mock; only the two methods the
// user-delete cascade calls are real (ListByUserID + Delete).
type udDomainRepo struct {
	repository.DomainRepository
	domains []models.Domain
	deleted []string
}

func (m *udDomainRepo) ListByUserID(_ context.Context, _ string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	var live []models.Domain
	for _, d := range m.domains {
		gone := false
		for _, id := range m.deleted {
			if id == d.ID {
				gone = true
				break
			}
		}
		if !gone {
			live = append(live, d)
		}
	}
	return live, int64(len(live)), nil
}

func (m *udDomainRepo) Delete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

// udMailboxRepo: only ListByDomainID is real.
type udMailboxRepo struct {
	repository.MailboxRepository
	byDomain map[string][]models.Mailbox
}

func (m *udMailboxRepo) ListByDomainID(_ context.Context, domainID string, _ repository.ListOptions) ([]models.Mailbox, int64, error) {
	mbs := m.byDomain[domainID]
	return mbs, int64(len(mbs)), nil
}

// udAgent records every Call. Thread-safe: the user-delete handler fires
// some agent calls from background goroutines (fire-and-forget teardown)
// that can outlive the HTTP response, so reads/writes of calls must be
// guarded.
type udAgent struct {
	mu    sync.Mutex
	calls []struct {
		cmd    string
		params any
	}
}

func (a *udAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	a.calls = append(a.calls, struct {
		cmd    string
		params any
	}{cmd, params})
	a.mu.Unlock()
	return json.RawMessage(`{"ok":true}`), nil
}

// purgedDomain reports whether mail.domain.purge_accounts was called for
// the given domain.
func (a *udAgent) purgedDomain(domain string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c.cmd == "mail.domain.purge_accounts" {
			if m, ok := c.params.(map[string]any); ok {
				if d, ok := m["domain"].(string); ok && d == domain {
					return true
				}
			}
		}
	}
	return false
}

// TestUserDelete_DestroysStalwartAccounts is the regression guard for the
// orphaned-Stalwart-account bug: jabali user delete must dispatch
// mailbox.delete (which Account/set-destroys the Stalwart registry
// account) for every mailbox under the user's domains BEFORE the domain
// delete FK-cascades the rows away. Without it, re-creating/re-migrating
// the same address collides with primaryKeyViolation on email.
func TestUserDelete_DestroysStalwartAccounts(t *testing.T) {
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	victim := makeUser(t, "victim@example.com", false, "victimpassword")
	uname := "victim"
	victim.Username = &uname
	repo.seed(victim)

	domID := "01DOMAIN0000000000000000AA"
	dom := &udDomainRepo{domains: []models.Domain{{ID: domID, UserID: victim.ID, Name: "victim.example.com"}}}
	mbx := &udMailboxRepo{byDomain: map[string][]models.Mailbox{
		domID: {
			{ID: "01MBX00000000000000000001", DomainID: domID, LocalPart: "info", EmailCached: "info@victim.example.com"},
			{ID: "01MBX00000000000000000002", DomainID: domID, LocalPart: "sales", EmailCached: "sales@victim.example.com"},
		},
	}}
	ag := &udAgent{}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	claims := &auth.AccessClaims{UserID: admin.ID, IsAdmin: true}
	g := r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, claims)
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:      repo,
		Agent:     ag,
		Domains:   dom,
		Mailboxes: mbx,
	})

	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+victim.ID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// The whole domain's Stalwart accounts must be purged (by domain, not
	// per panel row — so orphans without a row are caught too). JAB-236
	// moved the purge into the durable async teardown executor (it must
	// never run before a delete the repo layer could still refuse), so
	// the assertion awaits the detached goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ag.purgedDomain("victim.example.com") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected mail.domain.purge_accounts for victim.example.com; calls=%v", ag.calls)
}
