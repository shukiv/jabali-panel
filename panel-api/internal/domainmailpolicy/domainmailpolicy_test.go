package domainmailpolicy

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeDomRepo struct {
	repository.DomainRepository
	catchall    *string
	catchallSet bool
	discEnabled bool
	discText    *string
	discSet     bool
}

func (f *fakeDomRepo) UpdateCatchallTarget(_ context.Context, _ string, target *string) error {
	f.catchall = target
	f.catchallSet = true
	return nil
}
func (f *fakeDomRepo) UpdateDisclaimer(_ context.Context, _ string, enabled bool, text *string) error {
	f.discEnabled = enabled
	f.discText = text
	f.discSet = true
	return nil
}

func mailDomain(enabled bool) *models.Domain {
	return &models.Domain{ID: "d1", Name: "example.com", EmailEnabled: enabled}
}

func okPush(captured *map[string]any) PushFunc {
	return func(_ context.Context, _ string, p map[string]any) error {
		if captured != nil {
			*captured = p
		}
		return nil
	}
}

func TestSetCatchall(t *testing.T) {
	t.Run("gates on email-enabled", func(t *testing.T) {
		repo := &fakeDomRepo{}
		if _, _, err := SetCatchall(context.Background(), Deps{Domains: repo}, mailDomain(false), "a@b.com", nil); !errors.Is(err, ErrEmailNotEnabled) {
			t.Errorf("want ErrEmailNotEnabled, got %v", err)
		}
		if repo.catchallSet {
			t.Error("must not persist when the gate rejects")
		}
	})

	t.Run("rejects a non-canonical target", func(t *testing.T) {
		repo := &fakeDomRepo{}
		if _, _, err := SetCatchall(context.Background(), Deps{Domains: repo}, mailDomain(true), "not-an-email", nil); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("want ErrInvalidTarget, got %v", err)
		}
		if repo.catchallSet {
			t.Error("must not persist a garbage target")
		}
	})

	t.Run("validates + lowercases the domain half, preserving +tag and local case", func(t *testing.T) {
		repo := &fakeDomRepo{}
		var pushed map[string]any
		// External target with plus-addressing + mixed case — the local part
		// and its +tag must survive; only the domain half is lowercased.
		canon, warning, err := SetCatchall(context.Background(), Deps{Domains: repo}, mailDomain(true), "  Me+catchall@Gmail.COM ", okPush(&pushed))
		if err != nil {
			t.Fatalf("SetCatchall: %v", err)
		}
		if canon != "Me+catchall@gmail.com" {
			t.Errorf("target normalization wrong: %q (must keep +tag and local case, lowercase domain)", canon)
		}
		if repo.catchall == nil || *repo.catchall != "Me+catchall@gmail.com" {
			t.Errorf("persisted target = %v", repo.catchall)
		}
		if warning != "" {
			t.Errorf("clean push should not warn, got %q", warning)
		}
		if pushed["target"] != "Me+catchall@gmail.com" || pushed["domain_name"] != "example.com" {
			t.Errorf("push params wrong: %+v", pushed)
		}
	})

	t.Run("push failure is a warning, DB truth kept", func(t *testing.T) {
		repo := &fakeDomRepo{}
		failPush := func(_ context.Context, _ string, _ map[string]any) error { return errors.New("down") }
		_, warning, err := SetCatchall(context.Background(), Deps{Domains: repo}, mailDomain(true), "a@b.com", failPush)
		if err != nil {
			t.Fatalf("push failure must not be a hard error: %v", err)
		}
		if warning == "" {
			t.Error("failed push must surface a warning")
		}
		if !repo.catchallSet {
			t.Error("target must be persisted even when the push fails")
		}
	})
}

func TestClearCatchall_Idempotent(t *testing.T) {
	repo := &fakeDomRepo{}
	var pushed map[string]any
	if _, err := ClearCatchall(context.Background(), Deps{Domains: repo}, mailDomain(false), okPush(&pushed)); err != nil {
		t.Fatalf("clear (even on a non-email domain) must succeed: %v", err)
	}
	if !repo.catchallSet || repo.catchall != nil {
		t.Errorf("clear must NULL the target, got %v", repo.catchall)
	}
}

func TestSetDisclaimer(t *testing.T) {
	t.Run("enabled requires text", func(t *testing.T) {
		repo := &fakeDomRepo{}
		if _, _, err := SetDisclaimer(context.Background(), Deps{Domains: repo}, mailDomain(true), true, "   ", nil); !errors.Is(err, ErrDisclaimerTextRequired) {
			t.Errorf("want ErrDisclaimerTextRequired, got %v", err)
		}
	})

	t.Run("enabled gates on email", func(t *testing.T) {
		repo := &fakeDomRepo{}
		if _, _, err := SetDisclaimer(context.Background(), Deps{Domains: repo}, mailDomain(false), true, "hi", nil); !errors.Is(err, ErrEmailNotEnabled) {
			t.Errorf("want ErrEmailNotEnabled, got %v", err)
		}
	})

	t.Run("disabling keeps text without gate/requirement", func(t *testing.T) {
		repo := &fakeDomRepo{}
		// A disabled disclaimer on a non-email domain, with text, must persist
		// {false, text} — the UI toggle-off-but-keep-draft flow.
		norm, _, err := SetDisclaimer(context.Background(), Deps{Domains: repo}, mailDomain(false), false, "  draft  ", nil)
		if err != nil {
			t.Fatalf("disabling must not gate or require text: %v", err)
		}
		if repo.discEnabled {
			t.Error("must persist enabled=false")
		}
		if norm != "draft" || repo.discText == nil || *repo.discText != "draft" {
			t.Errorf("disabled disclaimer must keep the trimmed text, got %v", repo.discText)
		}
	})

	t.Run("enabled persists + pushes", func(t *testing.T) {
		repo := &fakeDomRepo{}
		var pushed map[string]any
		norm, _, err := SetDisclaimer(context.Background(), Deps{Domains: repo}, mailDomain(true), true, "  Out of office  ", okPush(&pushed))
		if err != nil {
			t.Fatalf("SetDisclaimer: %v", err)
		}
		if norm != "Out of office" || !repo.discEnabled {
			t.Errorf("persisted wrong: enabled=%v text=%v", repo.discEnabled, repo.discText)
		}
		if pushed["enabled"] != true || pushed["text"] != "Out of office" {
			t.Errorf("push params wrong: %+v", pushed)
		}
	})
}

func TestClearDisclaimer_WipesAndDisables(t *testing.T) {
	repo := &fakeDomRepo{}
	var pushed map[string]any
	if _, err := ClearDisclaimer(context.Background(), Deps{Domains: repo}, mailDomain(true), okPush(&pushed)); err != nil {
		t.Fatalf("ClearDisclaimer: %v", err)
	}
	if repo.discEnabled || repo.discText == nil || *repo.discText != "" {
		t.Errorf("clear must persist {false, \"\"}, got enabled=%v text=%v", repo.discEnabled, repo.discText)
	}
	if pushed["enabled"] != false {
		t.Errorf("clear must push enabled=false, got %+v", pushed)
	}
}
