package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A domain row a Docker app created takes its mail with it when it goes for
// good, as domainops.Delete's teardown does for any other domain: the mail
// server's accounts under the name (the sendmail relay's noreply@), its DKIM
// signatures and the mail server's domain. Before, the row and the vhost went
// and those stayed on the mail server.

type mailPurgeCall struct {
	Domain       string `json:"domain"`
	RemoveDomain bool   `json:"remove_domain"`
}

func mailPurges(t *testing.T, mock *agent.MockClient) []mailPurgeCall {
	t.Helper()
	var out []mailPurgeCall
	for _, c := range mock.Calls() {
		if c.Command != "mail.domain.purge_accounts" {
			continue
		}
		var v mailPurgeCall
		if err := json.Unmarshal(c.Params, &v); err != nil {
			t.Fatalf("decode purge params: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func wantMailPurged(t *testing.T, mock *agent.MockClient, names ...string) {
	t.Helper()
	got := mailPurges(t, mock)
	if len(got) != len(names) {
		t.Fatalf("mail purges %+v, want %v", got, names)
	}
	for i, n := range names {
		if got[i].Domain != n || !got[i].RemoveDomain {
			t.Fatalf("mail purge %d = %+v, want %s with remove_domain", i, got[i], n)
		}
	}
}

func purgeMock() *agent.MockClient {
	mock := agent.NewMockClient()
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	mock.On("mail.domain.purge_accounts", map[string]any{"destroyed": 1})
	return mock
}

func TestRemoveManagedDockerDomain_PurgesTheDomainsMail(t *testing.T) {
	mock := purgeMock()
	RemoveManagedDockerDomain(context.Background(), &fakeDomainRepo{}, mock, managedDomain("d1", "app.example.com", "a1"))
	wantMailPurged(t, mock, "app.example.com")
}

// deleteFailsRepo can't delete the row.
type deleteFailsRepo struct {
	*fakeDomainRepo
	err error
}

func (d deleteFailsRepo) Delete(context.Context, string) error { return d.err }

// The host-side cleanup is for a row that is gone. A row the delete could
// not remove still names its vhost, certificate and mail.
func TestRemoveManagedDockerDomain_ARowThatStaysKeepsItsVhostAndMail(t *testing.T) {
	mock := purgeMock()
	repo := deleteFailsRepo{&fakeDomainRepo{}, errors.New("database is down")}
	RemoveManagedDockerDomain(context.Background(), repo, mock, managedDomain("d1", "app.example.com", "a1"))
	if calls := mock.Calls(); len(calls) != 0 {
		t.Fatalf("the agent was called %+v for a row the delete did not remove", calls)
	}
}

// A row someone else removed first still gets its host-side cleanup.
func TestRemoveManagedDockerDomain_ARowAlreadyGoneStillCleansUp(t *testing.T) {
	mock := purgeMock()
	repo := deleteFailsRepo{&fakeDomainRepo{}, repository.ErrNotFound}
	RemoveManagedDockerDomain(context.Background(), repo, mock, managedDomain("d1", "app.example.com", "a1"))
	wantPurged(t, mock, "app.example.com")
	wantMailPurged(t, mock, "app.example.com")
}

// The name an edit drops loses its mail; the name it keeps does not.
func TestEditDomainPorts_ANameTheAppDropsGoesWithItsMail(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.3")
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	mock.On("mail.domain.purge_accounts", map[string]any{"destroyed": 1})
	dom := &fakeDomainRepo{owned: []models.Domain{managedDomain("old", "old.example.com", repo.app.ID)}}
	h.cfg.Domains = listingDomainRepo{dom}

	newDomain := "new.example.com"
	if err := h.editDomainPorts(context.Background(), repo.app, &newDomain, nil, "admin", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	wantMailPurged(t, mock, "old.example.com")
}

func TestEditDomainPorts_TheSameNameKeepsItsMail(t *testing.T) {
	h, repo, mock := trackHandler(t, "nextcloud", "34.0.3")
	mock.On("docker_app.vhost_remove", map[string]any{"ok": true})
	mock.On("mail.domain.purge_accounts", map[string]any{"destroyed": 1})
	dom := &fakeDomainRepo{owned: []models.Domain{managedDomain("same", "same.example.com", repo.app.ID)}}
	h.cfg.Domains = listingDomainRepo{dom}

	sameDomain := "Same.example.com "
	if err := h.editDomainPorts(context.Background(), repo.app, &sameDomain, nil, "admin", true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := mailPurges(t, mock); len(got) != 0 {
		t.Fatalf("mail purge %+v for a name the app keeps", got)
	}
}

func TestClearDockerAppCorpse_PurgesTheMailOfNamesNotReused(t *testing.T) {
	mock := purgeMock()
	dom := &fakeDomainRepo{}
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Repo: &fakeDockerRepo{}, Domains: dom, Agent: mock}}
	scope := []models.Domain{
		managedDomain("reuse", "reuse.example.com", "a1"),
		managedDomain("gone", "gone.example.com", "a1"),
	}

	if err := h.clearDockerAppCorpse(context.Background(), &models.DockerApp{ID: "a1"}, scope, "reuse.example.com"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	wantMailPurged(t, mock, "gone.example.com")
}
