package userops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailcreds"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeCredSweeper records when it ran, counted in agent calls made before it.
type fakeCredSweeper struct {
	ag         *recordingAgent
	runs       int
	callsAtRun int
	err        error
}

func (f *fakeCredSweeper) SweepMailCredentials(context.Context) ([]mailcreds.Removal, error) {
	f.runs++
	f.callsAtRun = len(f.ag.calls)
	return nil, f.err
}

func flushIndex(ag *recordingAgent) int {
	for i, c := range ag.calls {
		if c.method == "mail.auth_cache.flush" {
			return i
		}
	}
	return -1
}

// The mail server keeps the app passwords a mailbox made apart from the
// panel's password and checks them itself, so a suspension takes them off
// the user's mailboxes at once, before the login cache is flushed.
func TestSuspend_SweepsMailCredentialsBeforeTheFlush(t *testing.T) {
	ag := &recordingAgent{}
	sw := &fakeCredSweeper{ag: ag}
	d := Deps{Users: &fakeSuspendUsers{}, Domains: &fakeSuspendDomains{}, Agent: ag, MailCredentials: sw}

	res, err := Suspend(context.Background(), d, &models.User{ID: "u1", Username: strptr("alice")}, "spam")
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if sw.runs != 1 {
		t.Fatalf("sweeps = %d, want 1", sw.runs)
	}
	if fi := flushIndex(ag); fi < 0 || sw.callsAtRun > fi {
		t.Errorf("the sweep ran after the flush (sweep at call %d, flush at %d)", sw.callsAtRun, fi)
	}
	if res.MailWarning != "" {
		t.Errorf("MailWarning = %q", res.MailWarning)
	}
}

// A failed sweep is reported: the user's app passwords would keep working.
func TestSuspend_ReportsAFailedMailCredentialsSweep(t *testing.T) {
	ag := &recordingAgent{}
	sw := &fakeCredSweeper{ag: ag, err: errors.New("mail server down")}
	d := Deps{Users: &fakeSuspendUsers{}, Domains: &fakeSuspendDomains{}, Agent: ag, MailCredentials: sw}

	res, err := Suspend(context.Background(), d, &models.User{ID: "u1"}, "spam")
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if !strings.Contains(res.MailWarning, "mail_credentials_sweep_failed") {
		t.Fatalf("MailWarning = %q, want the sweep failure", res.MailWarning)
	}
	if flushIndex(ag) < 0 {
		t.Error("the login cache was not flushed after a failed sweep")
	}
}

// Unsuspending gives nothing back and removes nothing.
func TestUnsuspend_DoesNotSweepMailCredentials(t *testing.T) {
	ag := &recordingAgent{}
	sw := &fakeCredSweeper{ag: ag}
	d := Deps{Users: &fakeSuspendUsers{}, Domains: &fakeSuspendDomains{}, Agent: ag, MailCredentials: sw}
	if _, err := Unsuspend(context.Background(), d, &models.User{ID: "u1", Suspended: true}); err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	if sw.runs != 0 {
		t.Errorf("sweeps = %d, want 0", sw.runs)
	}
}
