package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/websitemail"
)

type wmUserRepo struct {
	repository.UserRepository
	rows []models.User
}

func (s *wmUserRepo) FindByIDs(_ context.Context, ids []string) ([]models.User, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []models.User
	for _, u := range s.rows {
		if want[u.ID] {
			out = append(out, u)
		}
	}
	return out, nil
}

type wmPackageRepo struct {
	repository.PackageRepository
	rows []models.HostingPackage
}

func (s *wmPackageRepo) List(context.Context, repository.ListOptions) ([]models.HostingPackage, int64, error) {
	return s.rows, int64(len(s.rows)), nil
}

type wmDomainRepo struct {
	repository.DomainRepository
	rows []models.Domain
}

func (s *wmDomainRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return s.rows, int64(len(s.rows)), nil
}

func wmVerified(id, user, name string) models.Domain {
	return models.Domain{ID: id, UserID: user, Name: name, IsEnabled: true,
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}}
}

func websiteMailReconciler(t *testing.T, srv *models.ServerSettings, a agent.AgentInterface) (*Reconciler, *wmDomainRepo) {
	t.Helper()
	key := ssokey.Key{}
	for i := range key {
		key[i] = byte(i)
	}
	if srv.SmarthostUsername != "" {
		sealed, err := key.Seal([]byte("s3cret"))
		if err != nil {
			t.Fatal(err)
		}
		srv.SmarthostPasswordEnc = sealed
	}
	pkg := "p1"
	domains := &wmDomainRepo{rows: []models.Domain{wmVerified("d1", "u1", "alice.example")}}
	return &Reconciler{
		domains:        domains,
		users:          &wmUserRepo{rows: []models.User{{ID: "u1", Username: strPtr("alice"), PackageID: &pkg}}},
		packages:       &wmPackageRepo{rows: []models.HostingPackage{{ID: "p1", WebsiteSendsEmail: true}}},
		serverSettings: &fakeSettingsRepo{srv: srv},
		agent:          a,
		sendmailSSOKey: &key,
		log:            slog.New(slog.DiscardHandler),
	}, domains
}

func smarthostSettings() *models.ServerSettings {
	return &models.ServerSettings{Hostname: "panel.example.tld", WebsiteMailMode: "smarthost",
		SmarthostHost: "smtp.example.net", SmarthostPort: 587, SmarthostTLS: "starttls", SmarthostUsername: "relay"}
}

func applied(t *testing.T, m *agent.MockClient) []websitemail.ApplyRequest {
	t.Helper()
	var out []websitemail.ApplyRequest
	for _, c := range m.Calls() {
		var req websitemail.ApplyRequest
		if err := json.Unmarshal(c.Params, &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req)
	}
	return out
}

func TestReconcileWebsiteMail_SenderListFollowsTheSites(t *testing.T) {
	m := agent.NewMockClient().On("mail.relay.apply", websitemail.ApplyResponse{Ok: true, Mode: "smarthost"})
	r, domains := websiteMailReconciler(t, smarthostSettings(), m)
	ctx := context.Background()

	r.reconcileWebsiteMail(ctx)
	r.reconcileWebsiteMail(ctx)
	calls := applied(t, m)
	if len(calls) != 1 {
		t.Fatalf("applies = %d, want 1 (the second tick has nothing new)", len(calls))
	}
	if calls[0].Smarthost.Password != "s3cret" || len(calls[0].Senders) != 1 || calls[0].Senders[0].Domains[0] != "alice.example" {
		t.Errorf("applied %+v", calls[0])
	}

	// A newly verified domain reaches the relay on the next tick.
	domains.rows = append(domains.rows, wmVerified("d2", "u1", "shop.example"))
	r.reconcileWebsiteMail(ctx)
	calls = applied(t, m)
	if len(calls) != 2 || len(calls[1].Senders[0].Domains) != 2 {
		t.Errorf("after a new domain: %+v", calls)
	}
}

func TestReconcileWebsiteMail_LocalKeepsTheRelayOff(t *testing.T) {
	m := agent.NewMockClient().On("mail.relay.apply", websitemail.ApplyResponse{Ok: true, Mode: "local"})
	r, _ := websiteMailReconciler(t, &models.ServerSettings{Hostname: "panel.example.tld"}, m)
	r.reconcileWebsiteMail(context.Background())
	calls := m.Calls()
	if len(calls) != 1 || string(calls[0].Params) != `{"mode":"local"}` {
		t.Errorf("calls = %+v", calls)
	}
}

func TestReconcileWebsiteMail_OldAgentIsNotRetriedEveryTick(t *testing.T) {
	m := agent.NewMockClient() // no handler: unknown_command, like an agent from before the relay
	r, _ := websiteMailReconciler(t, smarthostSettings(), m)
	r.reconcileWebsiteMail(context.Background())
	r.reconcileWebsiteMail(context.Background())
	if n := len(m.Calls()); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestReconcileWebsiteMail_FailureIsRetried(t *testing.T) {
	m := agent.NewMockClient().OnError("mail.relay.apply", errors.New("agent socket down"))
	r, _ := websiteMailReconciler(t, smarthostSettings(), m)
	r.reconcileWebsiteMail(context.Background())
	r.reconcileWebsiteMail(context.Background())
	if n := len(m.Calls()); n != 2 {
		t.Errorf("calls = %d, want a retry every tick until it applies", n)
	}
}
