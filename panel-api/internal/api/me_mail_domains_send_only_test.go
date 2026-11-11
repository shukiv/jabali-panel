package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A domain whose mail is hosted elsewhere can have send-only accounts (#2491),
// and Mail Domains is where a tenant manages accounts. It lists such a domain
// once it has an account, marked mail_hosted_elsewhere; one without accounts
// stays off the page (GH #1387). The system relay is not an account.
func TestMailDomains_ListsMailElsewhereDomainsWithAccounts(t *testing.T) {
	cfg := MeMailDomainsConfig{
		Domains: mdDomainRepo{ds: []models.Domain{
			{ID: "da", UserID: "u1", Name: "a.test", EmailEnabled: true},
			{ID: "dd", UserID: "u1", Name: "off.test", MailProvider: models.MailProviderNone},
			{ID: "de", UserID: "u1", Name: "sender.test", MailProvider: models.MailProviderNone},
			{ID: "dg", UserID: "u1", Name: "google.test", MailProvider: models.MailProviderGoogle},
		}},
		Mailboxes: mdMailboxRepo{mbs: []repository.MailboxWithDomain{
			mb("da", "u1", false, 10),
			mb("de", "u1", false, 0), // a send-only account
			mb("dg", "u1", true, 0),  // only the system relay
		}},
		MailStats: mdStatsRepo{},
	}

	w := setupMailDomainsRouter(t, "u1", cfg)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []mailDomainRow `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]mailDomainRow{}
	for _, r := range resp.Data {
		byName[r.Name] = r
	}
	if len(byName) != 2 {
		t.Fatalf("want a.test and sender.test, got %+v", resp.Data)
	}
	if a, ok := byName["a.test"]; !ok || a.MailHostedElsewhere {
		t.Errorf("a.test: want listed, mail hosted here: %+v", a)
	}
	s, ok := byName["sender.test"]
	if !ok || !s.MailHostedElsewhere || s.MailboxCount != 1 {
		t.Errorf("sender.test: want listed, mail hosted elsewhere, 1 account: %+v", s)
	}
}
