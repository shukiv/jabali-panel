package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fwRefusingForwarders is the database refusing the row (migration 000307).
type fwRefusingForwarders struct{ fwFakeForwarders }

func (f *fwRefusingForwarders) Create(context.Context, *models.EmailForwarder) error {
	return mailaddr.ErrPostmasterReserved
}

// ADR-0110: postmaster@ on a tenant domain belongs to the server admin.
// Stalwart keeps the address on the admin's postmaster account, so a tenant
// alias there would never get its mail. The handler refuses it before any row
// is written.
func TestForwarderCreate_RefusesAPostmasterAliasOnATenantDomain(t *testing.T) {
	for _, local := range []string{"postmaster", "Postmaster"} {
		h := newForwarderHandlerFake(nil)
		w := postForwarder(h, `{"type":"alias","local_part":"`+local+`"}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved_local_part") {
			t.Fatalf("alias %q: want 400 reserved_local_part, got %d %s", local, w.Code, w.Body.String())
		}
		if rows := h.cfg.Forwarders.(*fwFakeForwarders).rows; len(rows) != 0 {
			t.Fatalf("alias %q: no row may be written, got %+v", local, rows)
		}
	}
}

// On the panel hostname's domain postmaster@ is the admin's own address.
func TestForwarderCreate_AllowsAPostmasterAliasOnThePanelDomain(t *testing.T) {
	h := newForwarderHandlerFake(nil)
	h.cfg.Domains = fwFakeDomains{dom: &models.Domain{ID: "dom1", Name: "panel.example.com", UserID: "u1", IsPanelPrimary: true}}
	w := postForwarder(h, `{"type":"alias","local_part":"postmaster"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d %s", w.Code, w.Body.String())
	}
}

// A refusal from the database is reported as the reserved address too, not as
// an internal error.
func TestForwarderCreate_DatabaseRefusalIsAReservedAddress(t *testing.T) {
	h := newForwarderHandlerFake(nil)
	h.cfg.Forwarders = &fwRefusingForwarders{}
	h.cfg.Domains = fwFakeDomains{dom: &models.Domain{ID: "dom1", Name: "panel.example.com", UserID: "u1", IsPanelPrimary: true}}
	w := postForwarder(h, `{"type":"alias","local_part":"postmaster"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved_local_part") {
		t.Fatalf("want 400 reserved_local_part, got %d %s", w.Code, w.Body.String())
	}
}
