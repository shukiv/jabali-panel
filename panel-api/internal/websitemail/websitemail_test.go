package websitemail

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type stubDomains struct {
	repository.DomainRepository
	rows []models.Domain
}

func (s stubDomains) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return s.rows, int64(len(s.rows)), nil
}

type stubUsers struct {
	repository.UserRepository
	rows []models.User
}

func (s stubUsers) FindByIDs(_ context.Context, ids []string) ([]models.User, error) {
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

func str(s string) *string { return &s }

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func domain(id, user, name string, age int, verified, enabled bool) models.Domain {
	d := models.Domain{ID: id, UserID: user, Name: name, IsEnabled: enabled, CreatedAt: t0.Add(time.Duration(age) * time.Hour)}
	if verified {
		d.OwnershipStatus = models.OwnershipVerified
	} else {
		d.OwnershipStatus = models.OwnershipPending
	}
	return d
}

func fixture() Deps {
	return Deps{
		Users: stubUsers{rows: []models.User{
			{ID: "u-alice", Username: str("alice"), PackageID: str("p1")},
			{ID: "u-bob", Username: str("bob"), PackageID: str("p1")},
			{ID: "u-nopkg", Username: str("nopkg")},
			{ID: "u-susp", Username: str("susp"), PackageID: str("p1"), Suspended: true},
			{ID: "u-admin", Username: str("admin"), PackageID: str("p1"), IsAdmin: true},
			{ID: "u-nolinux", PackageID: str("p1")},
		}},
		Domains: stubDomains{rows: []models.Domain{
			domain("d1", "u-alice", "Shop.Example", 2, true, true),
			domain("d2", "u-alice", "alice.example", 1, true, true),
			domain("d3", "u-alice", "pending.example", 0, false, true),
			domain("d4", "u-alice", "off.example", 0, true, false),
			domain("d5", "u-bob", "bob.example", 0, true, true),
			domain("d6", "u-nopkg", "nopkg.example", 0, true, true),
			domain("d7", "u-susp", "susp.example", 0, true, true),
			domain("d8", "u-admin", "panel.example", 0, true, true),
			domain("d9", "u-nolinux", "nolinux.example", 0, true, true),
			domain("d10", "u-gone", "orphan.example", 0, true, true),
		}},
	}
}

func TestSenders_OnlyPackagedSiteUsersWithTheirVerifiedEnabledDomains(t *testing.T) {
	got, err := Senders(context.Background(), fixture())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `[{"username":"alice","domains":["alice.example","shop.example"],"default":"alice.example"},{"username":"bob","domains":["bob.example"],"default":"bob.example"}]`
	if string(b) != want {
		t.Errorf("senders =\n%s\nwant\n%s", b, want)
	}
}

func testKey(t *testing.T) *ssokey.Key {
	t.Helper()
	var k ssokey.Key
	for i := range k {
		k[i] = byte(i + 1)
	}
	return &k
}

func TestRequest(t *testing.T) {
	ctx := context.Background()
	key := testKey(t)
	sealed, err := key.Seal([]byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	smart := &models.ServerSettings{
		Hostname: "panel.example.net", WebsiteMailMode: models.WebsiteMailSmarthost,
		SmarthostHost: "smtp.example.net", SmarthostPort: 587, SmarthostTLS: "starttls",
		SmarthostUsername: "relay", SmarthostPasswordEnc: sealed,
	}

	req, err := Request(ctx, fixture(), smart, key)
	if err != nil {
		t.Fatal(err)
	}
	if req.Mode != "smarthost" || req.Smarthost == nil || req.Smarthost.Password != "s3cret" || req.Smarthost.Helo != "panel.example.net" || len(req.Senders) != 2 {
		t.Errorf("request = %+v / %+v", req, req.Smarthost)
	}

	if _, err := Request(ctx, fixture(), smart, nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: err = %v, want ErrNoKey", err)
	}

	local := *smart
	local.WebsiteMailMode = models.WebsiteMailLocal
	req, err = Request(ctx, fixture(), &local, key)
	if err != nil || req.Mode != "local" || req.Smarthost != nil || req.Senders != nil {
		t.Errorf("local: %+v, %v (the password must not leave the panel in local mode)", req, err)
	}

	noLogin := *smart
	noLogin.SmarthostUsername, noLogin.SmarthostPasswordEnc, noLogin.SmarthostTLS, noLogin.SmarthostPort = "", nil, "none", 25
	req, err = Request(ctx, fixture(), &noLogin, nil)
	if err != nil || req.Smarthost.Password != "" {
		t.Errorf("no login: %+v, %v", req.Smarthost, err)
	}
}

func TestFingerprintFollowsThePassword(t *testing.T) {
	a := ApplyRequest{Mode: "smarthost", Smarthost: &Smarthost{Host: "h", Port: 587, TLS: "starttls", Username: "u", Password: "one"}}
	b := a
	sh := *a.Smarthost
	sh.Password = "two"
	b.Smarthost = &sh
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("a password change must re-apply")
	}
	if Fingerprint(a) != Fingerprint(a) {
		t.Error("fingerprint not stable")
	}
}

func TestApply(t *testing.T) {
	m := agent.NewMockClient().On(Command, ApplyResponse{Ok: true, Mode: "local", Changed: true})
	resp, err := Apply(context.Background(), m, ApplyRequest{Mode: "local"})
	if err != nil || !resp.Ok || resp.Mode != "local" {
		t.Fatalf("Apply: %+v, %v", resp, err)
	}
	calls := m.Calls()
	if len(calls) != 1 || calls[0].Command != "mail.relay.apply" || string(calls[0].Params) != `{"mode":"local"}` {
		t.Errorf("calls = %+v", calls)
	}
}
