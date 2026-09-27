package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1898: CheckName is Create's name-guard sequence, exported so a backup
// restore runs the same guards. The cross-tenant guard binds only a non-admin
// actor; the rest bind everyone.
func TestCheckName_Guards(t *testing.T) {
	store := newCreateStore(&models.Domain{ID: "d-other", Name: "example.com", UserID: "other"})
	deps := NameCheckDeps{Domains: store}
	ctx := context.Background()

	if err := CheckName(ctx, deps, "sub.example.com", "me", false); !errors.Is(err, ErrDomainConflictsTenant) {
		t.Fatalf("tenant nesting under another owner: want ErrDomainConflictsTenant, got %v", err)
	}
	if err := CheckName(ctx, deps, "sub.example.com", "me", true); err != nil {
		t.Fatalf("admin actor is trusted to delegate: got %v", err)
	}
	if err := CheckName(ctx, deps, "sub.example.com", "other", false); err != nil {
		t.Fatalf("same owner may nest: got %v", err)
	}
	if err := CheckName(ctx, deps, "fresh.org", "", false); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("no owner: want ErrOwnerRequired, got %v", err)
	}
	if err := CheckName(ctx, deps, "not a domain", "me", true); err == nil {
		t.Fatal("an invalid name passed")
	}
	if err := CheckName(ctx, deps, "fresh.org", "me", false); err != nil {
		t.Fatalf("a free name was refused: %v", err)
	}
}
