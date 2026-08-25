package autoresponderops

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeAR struct {
	repository.EmailAutoresponderRepository
	saved     *models.EmailAutoresponder
	deleteErr error
	deleted   string
}

func (f *fakeAR) Update(_ context.Context, ar *models.EmailAutoresponder) error {
	f.saved = ar
	return nil
}
func (f *fakeAR) Delete(_ context.Context, id string) error {
	f.deleted = id
	return f.deleteErr
}

func sptr(s string) *string { return &s }

// The content policy must reject an ENABLED responder with no subject or no
// body — the drift the REST handler had (the CLI already rejected it).
func TestValidate_ContentPolicy(t *testing.T) {
	cases := []struct {
		name string
		in   SetInput
		want error
	}{
		{"enabled no subject", SetInput{Enabled: true, TextBody: sptr("hi")}, ErrContentRequired},
		{"enabled subject no body", SetInput{Enabled: true, Subject: sptr("Away")}, ErrContentRequired},
		{"enabled blank subject", SetInput{Enabled: true, Subject: sptr("   "), TextBody: sptr("hi")}, ErrContentRequired},
		{"enabled subject + text ok", SetInput{Enabled: true, Subject: sptr("Away"), TextBody: sptr("hi")}, nil},
		{"enabled subject + html ok", SetInput{Enabled: true, Subject: sptr("Away"), HTMLBody: sptr("<p>hi</p>")}, nil},
		{"disabled empty ok", SetInput{Enabled: false}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Validate(c.in); !errors.Is(err, c.want) {
				t.Errorf("Validate() = %v, want %v", err, c.want)
			}
		})
	}
}

func TestValidate_DateRange(t *testing.T) {
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// from after to → rejected (checked even when disabled).
	if err := Validate(SetInput{FromDate: &from, ToDate: &before}); !errors.Is(err, ErrInvalidDateRange) {
		t.Errorf("inverted range must be rejected, got %v", err)
	}
	// from == to and from < to are both fine.
	if err := Validate(SetInput{FromDate: &before, ToDate: &from}); err != nil {
		t.Errorf("valid range rejected: %v", err)
	}
}

// Set persists the desired state and pushes the canonical params; a failed push
// is a warning, not an error (DB stays the truth). A validation failure never
// touches the repo.
func TestSet(t *testing.T) {
	t.Run("valid persists and pushes canonical params", func(t *testing.T) {
		repo := &fakeAR{}
		var pushed map[string]any
		push := func(_ context.Context, cmd string, p map[string]any) error {
			if cmd != "autoresponder.set" {
				t.Errorf("cmd = %q", cmd)
			}
			pushed = p
			return nil
		}
		_, warning, err := Set(context.Background(), Deps{Autoresponders: repo}, SetInput{
			MailboxID: "m1", MailboxEmail: "a@ex.com", Enabled: true,
			Subject: sptr("Away"), TextBody: sptr("back soon"),
		}, push)
		if err != nil {
			t.Fatalf("Set: %v", err)
		}
		if warning != "" {
			t.Errorf("clean push must not warn, got %q", warning)
		}
		if repo.saved == nil || !repo.saved.Enabled || repo.saved.ManagedBy != managedBy {
			t.Fatalf("row not persisted correctly: %+v", repo.saved)
		}
		if pushed["mailbox_email"] != "a@ex.com" || pushed["enabled"] != true || pushed["subject"] != "Away" {
			t.Errorf("canonical params wrong: %+v", pushed)
		}
	})

	t.Run("push failure returns a warning, DB truth kept", func(t *testing.T) {
		repo := &fakeAR{}
		push := func(_ context.Context, _ string, _ map[string]any) error {
			return errors.New("agent down")
		}
		_, warning, err := Set(context.Background(), Deps{Autoresponders: repo}, SetInput{
			MailboxID: "m1", MailboxEmail: "a@ex.com", Enabled: true,
			Subject: sptr("Away"), TextBody: sptr("back soon"),
		}, push)
		if err != nil {
			t.Fatalf("push failure must not be a hard error: %v", err)
		}
		if warning == "" {
			t.Error("a failed push must surface a warning")
		}
		if repo.saved == nil {
			t.Error("desired state must be persisted even when the push fails")
		}
	})

	t.Run("validation failure never persists", func(t *testing.T) {
		repo := &fakeAR{}
		if _, _, err := Set(context.Background(), Deps{Autoresponders: repo}, SetInput{
			MailboxID: "m1", Enabled: true, // no subject/body
		}, nil); !errors.Is(err, ErrContentRequired) {
			t.Errorf("want ErrContentRequired, got %v", err)
		}
		if repo.saved != nil {
			t.Error("an invalid intake must not touch the repo")
		}
	})
}

// Clear is idempotent: a missing row is not an error, and the agent is told to
// disable.
func TestClear_Idempotent(t *testing.T) {
	repo := &fakeAR{deleteErr: repository.ErrNotFound}
	var pushed map[string]any
	push := func(_ context.Context, _ string, p map[string]any) error { pushed = p; return nil }
	if err := Clear(context.Background(), Deps{Autoresponders: repo}, "m1", "a@ex.com", push); err != nil {
		t.Fatalf("clearing a missing responder must succeed, got %v", err)
	}
	if repo.deleted != "m1" {
		t.Errorf("delete target = %q", repo.deleted)
	}
	if pushed["enabled"] != false {
		t.Errorf("clear must push enabled=false, got %+v", pushed)
	}
}

// AgentParams includes only the fields that are set — the exact projection all
// three callers share.
func TestAgentParams_OnlySetFields(t *testing.T) {
	from := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	p := AgentParams("a@ex.com", &models.EmailAutoresponder{
		Enabled: true, Subject: sptr("Away"), FromDate: &from,
	})
	if p["mailbox_email"] != "a@ex.com" || p["enabled"] != true || p["subject"] != "Away" {
		t.Errorf("params = %+v", p)
	}
	if p["from_date"] != "2026-06-01T12:00:00Z" {
		t.Errorf("from_date = %v", p["from_date"])
	}
	if _, ok := p["to_date"]; ok {
		t.Error("unset to_date must be absent from the payload")
	}
	if _, ok := p["text_body"]; ok {
		t.Error("unset text_body must be absent")
	}
}
