package reconciler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailbranding"
)

type fakeWebmailBrandingRepo struct {
	repository.DomainWebmailBrandingRepository
	rows []repository.WebmailBrandingRow
}

func (f *fakeWebmailBrandingRepo) ListAll(context.Context) ([]repository.WebmailBrandingRow, error) {
	return f.rows, nil
}

func countAgentCalls(ag *fakeAgent, method string) int {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	n := 0
	for _, c := range ag.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// The agent gets the brand text and the branding of every domain whose
// branding applies; an owner's branding whose package lost the switch is
// left out. An unchanged branding is not sent again; a change is, on the
// next pass at least webmailBrandingMinInterval after the last send. The
// pictures of a domain whose row is gone are removed.
func TestReconcileWebmailBranding_SendsWhatApplies(t *testing.T) {
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	s, _ := r.serverSettings.Get(context.Background())
	s.PanelBrandText = "Acme Hosting"

	const kept, off, deleted = "01HZZZZZZZZZZZZZZZZZZZZZZZ", "01HXXXXXXXXXXXXXXXXXXXXXXX", "01HYYYYYYYYYYYYYYYYYYYYYYY"
	dir := t.TempDir()
	r.webmailBrandingDir = dir
	for _, id := range []string{kept, off, deleted} {
		if err := webmailbranding.Write(dir, id, webmailbranding.LogoLight, []byte("png")); err != nil {
			t.Fatal(err)
		}
	}
	repo := &fakeWebmailBrandingRepo{rows: []repository.WebmailBrandingRow{
		{DomainWebmailBranding: models.DomainWebmailBranding{DomainID: kept, AppName: "A Mail", LogoLight: "aaaaaaaaaaaa"}, DomainName: "a.com", Applies: true},
		{DomainWebmailBranding: models.DomainWebmailBranding{DomainID: off, AppName: "B Mail"}, DomainName: "b.com", Applies: false},
	}}
	r.WithWebmailBranding(repo)

	run := func() {
		ctx, rr := ensureRun(context.Background(), RunNormal)
		r.reconcileWebmailBranding(ctx)
		rr.finish()
	}
	run()
	call, ok := findAgentCall(ag, "webmail.branding.apply")
	if !ok {
		t.Fatal("webmail.branding.apply was not called")
	}
	p := call.params.(map[string]any)
	if p["brand_text"] != "Acme Hosting" {
		t.Fatalf("brand_text = %v", p["brand_text"])
	}
	domains := p["domains"].([]webmailbranding.AgentDomain)
	if len(domains) != 1 || domains[0].Host != "mail.a.com" || domains[0].AppName != "A Mail" {
		t.Fatalf("domains = %+v, want only mail.a.com", domains)
	}
	if _, err := os.Stat(filepath.Join(dir, deleted)); !os.IsNotExist(err) {
		t.Fatal("the pictures of a domain with no branding row stayed")
	}
	for _, id := range []string{kept, off} {
		if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
			t.Fatalf("pictures of %s were removed: its row is there", id)
		}
	}

	run()
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 1 {
		t.Fatalf("an unchanged branding was sent again (%d calls)", n)
	}

	repo.rows[1].Applies = true // the owner's package got the switch
	run()
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 1 {
		t.Fatalf("a change was sent %d times within webmailBrandingMinInterval of the last send; each restarts webmail", n)
	}
	r.webmailBrandingSent = r.webmailBrandingSent.Add(-webmailBrandingMinInterval)
	run()
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 2 {
		t.Fatalf("a change was not sent once the interval passed (%d calls)", n)
	}
}
