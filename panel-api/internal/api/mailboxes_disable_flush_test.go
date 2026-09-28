package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// disableFlushLog records, in order, the repository writes and agent calls
// the PATCH handler makes.
type disableFlushLog struct{ events []string }

type disableFlushMailboxRepo struct {
	saMailboxRepo
	log *disableFlushLog
}

func (r *disableFlushMailboxRepo) SetDisabled(_ context.Context, _ string, disabled bool) error {
	if disabled {
		r.log.events = append(r.log.events, "db:disable")
	} else {
		r.log.events = append(r.log.events, "db:enable")
	}
	return nil
}

type disableFlushAgent struct{ log *disableFlushLog }

func (a *disableFlushAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.log.events = append(a.log.events, "agent:"+cmd)
	return json.RawMessage(`{}`), nil
}

// Stalwart answers JMAP (webmail) from a login cache the SQL directory's
// is_disabled filter never reaches, so a disabled mailbox kept its webmail
// access until something flushed that cache. The disable must flush it,
// after the row is written.
func TestMailboxUpdate_DisableFlushesTheMailLoginCache(t *testing.T) {
	log := &disableFlushLog{}
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "alice", EmailCached: "alice@x.test"}
	cfg := MailboxHandlerConfig{
		Mailboxes: &disableFlushMailboxRepo{
			saMailboxRepo: saMailboxRepo{byID: map[string]*models.Mailbox{"mb1": mb}},
			log:           log,
		},
		Domains: &saDomainRepo{byID: map[string]*models.Domain{"dom1": {ID: "dom1", Name: "x.test", UserID: "u1"}}},
		Agent:   &disableFlushAgent{log: log},
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMailboxRoutes(r.Group(""), cfg)

	req := httptest.NewRequest(http.MethodPatch, "/mailboxes/mb1", bytes.NewReader([]byte(`{"is_disabled":true}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	want := []string{"db:disable", "agent:mail.auth_cache.flush"}
	if len(log.events) != len(want) || log.events[0] != want[0] || log.events[1] != want[1] {
		t.Fatalf("events %v, want %v — the disabled mailbox keeps its webmail login", log.events, want)
	}
}
