package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestCreateDomainOp_AliasLookupFailsClosed proves the REST create path (the GUI
// and automation entrypoint) ACTS on an AliasCollision lookup error: when the
// alias table cannot be read, createDomainOp aborts with 500 db_alias_lookup and
// persists no domain. TestAliasCollision_FailsClosedOnLookupError proves the
// shared leaf RETURNS the error; this proves the caller does not discard it. An
// edit that dropped the error (hit, clash, _ := AliasCollision(...)) would make
// the create fail OPEN again — reddening this test, not the leaf's own test.
func TestCreateDomainOp_AliasLookupFailsClosed(t *testing.T) {
	uname := "alice"
	users := newAbUsers(&models.User{ID: "u1", Username: &uname})
	dom := newDCDomains()
	h := &domainHandler{cfg: DomainHandlerConfig{
		Users:            users,
		Domains:          dom,
		WebDomainAliases: aliasErrRepo{}, // every hostname lookup fails non-ErrNotFound
	}}

	_, oerr := createDomainOp(context.Background(), h, createDomainInput{
		OwnerID:       "u1",
		Name:          "shop.example.com",
		MailProvider:  models.MailProviderNone,
		SSLMode:       models.SSLModeNone,
		SkipInlineSSL: true,
	})

	if oerr == nil || oerr.Status != 500 || oerr.Code != "db_alias_lookup" {
		t.Fatalf("want 500 db_alias_lookup on a failed alias lookup, got %+v", oerr)
	}
	if len(dom.created) != 0 {
		t.Fatalf("no domain must be persisted when the alias lookup fails, got %d", len(dom.created))
	}
}
