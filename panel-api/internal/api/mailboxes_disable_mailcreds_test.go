package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailcreds"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type disableSweepRecorder struct {
	log     *disableFlushLog
	removed []mailcreds.Removal
	err     error
}

func (s *disableSweepRecorder) SweepMailCredentials(context.Context) ([]mailcreds.Removal, error) {
	s.log.events = append(s.log.events, "sweep")
	return s.removed, s.err
}

func patchMailboxDisabled(t *testing.T, body string, sweepErr error) []string {
	t.Helper()
	log := &disableFlushLog{}
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "alice", EmailCached: "alice@x.test"}
	cfg := MailboxHandlerConfig{
		Mailboxes: &disableFlushMailboxRepo{
			saMailboxRepo: saMailboxRepo{byID: map[string]*models.Mailbox{"mb1": mb}},
			log:           log,
		},
		Domains:         &saDomainRepo{byID: map[string]*models.Domain{"dom1": {ID: "dom1", Name: "x.test", UserID: "u1"}}},
		Agent:           &disableFlushAgent{log: log},
		MailCredentials: &disableSweepRecorder{log: log, err: sweepErr},
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMailboxRoutes(r.Group(""), cfg)

	req := httptest.NewRequest(http.MethodPatch, "/mailboxes/mb1", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return log.events
}

// The mail server checks the app passwords and API keys a mailbox made
// itself, so they kept working after the panel disabled the mailbox. The
// disable removes them after the row is written (the sweep reads who may
// sign in from it) and before the login cache flush.
func TestMailboxUpdate_DisableSweepsMailCredentials(t *testing.T) {
	events := patchMailboxDisabled(t, `{"is_disabled":true}`, nil)
	want := []string{"db:disable", "sweep", "agent:mail.auth_cache.flush"}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] || events[2] != want[2] {
		t.Fatalf("events %v, want %v", events, want)
	}
}

// A failed sweep does not fail the disable: the row is written, the cache is
// flushed, and the reconciler removes them on its next tick.
func TestMailboxUpdate_DisableWithAFailedSweepStillFlushes(t *testing.T) {
	events := patchMailboxDisabled(t, `{"is_disabled":true}`, errors.New("mail server down"))
	want := []string{"db:disable", "sweep", "agent:mail.auth_cache.flush"}
	if len(events) != len(want) || events[2] != want[2] {
		t.Fatalf("events %v, want %v", events, want)
	}
}

// Enabling a mailbox removes nothing.
func TestMailboxUpdate_EnableDoesNotSweep(t *testing.T) {
	events := patchMailboxDisabled(t, `{"is_disabled":false}`, nil)
	if len(events) != 1 || events[0] != "db:enable" {
		t.Fatalf("events %v, want [db:enable]", events)
	}
}

type rotateSweepMailboxRepo struct {
	saMailboxRepo
	log *disableFlushLog
	err error
}

func (r *rotateSweepMailboxRepo) UpdatePasswordHashAndEnc(context.Context, string, string, []byte) error {
	r.log.events = append(r.log.events, "db:password")
	return r.err
}

func rotateMailboxPassword(t *testing.T, writeErr error, removed int) ([]string, int) {
	t.Helper()
	log := &disableFlushLog{}
	sweeper := &disableSweepRecorder{log: log}
	for i := 0; i < removed; i++ {
		sweeper.removed = append(sweeper.removed, mailcreds.Removal{AccountID: "n2", Account: "alice@x.test", Type: "AppPassword"})
	}
	mb := &models.Mailbox{ID: "mb1", DomainID: "dom1", LocalPart: "alice", EmailCached: "alice@x.test"}
	cfg := MailboxHandlerConfig{
		Mailboxes: &rotateSweepMailboxRepo{
			saMailboxRepo: saMailboxRepo{
				byID:    map[string]*models.Mailbox{"mb1": mb},
				byEmail: map[string]*models.Mailbox{"alice@x.test": mb},
			},
			log: log,
			err: writeErr,
		},
		Domains:         &saDomainRepo{byID: map[string]*models.Domain{"dom1": {ID: "dom1", Name: "x.test", UserID: "u1"}}},
		Agent:           &disableFlushAgent{log: log},
		MailCredentials: sweeper,
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMailboxRoutes(r.Group(""), cfg)

	req := httptest.NewRequest(http.MethodPost, "/mailboxes/mb1/rotate-password", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return log.events, w.Code
}

// A password change removes the app passwords made before it at once, not
// on the reconciler's next tick: after the new password is written (the
// sweep compares against its time) and the mail server told of it, then the
// login cache is flushed so a removed app password is not still answered
// from it.
func TestMailboxRotate_SweepsMailCredentialsAfterTheWrite(t *testing.T) {
	events, code := rotateMailboxPassword(t, nil, 1)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	want := []string{"db:password", "agent:mailbox.set_password", "sweep", "agent:mail.auth_cache.flush"}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] || events[2] != want[2] || events[3] != want[3] {
		t.Fatalf("events %v, want %v", events, want)
	}
}

// Nothing removed: no extra flush.
func TestMailboxRotate_NothingRemovedNoFlush(t *testing.T) {
	events, code := rotateMailboxPassword(t, nil, 0)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	want := []string{"db:password", "agent:mailbox.set_password", "sweep"}
	if len(events) != len(want) || events[2] != want[2] {
		t.Fatalf("events %v, want %v", events, want)
	}
}

// A failed rotate removes nothing.
func TestMailboxRotate_FailedWriteDoesNotSweep(t *testing.T) {
	events, code := rotateMailboxPassword(t, errors.New("db down"), 1)
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d", code)
	}
	for _, e := range events {
		if e == "sweep" {
			t.Fatalf("events %v: swept after a failed rotate", events)
		}
	}
}
