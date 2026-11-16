package reconciler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	r.webmailBrandingSent.Add(-int64(webmailBrandingMinInterval))
	run()
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 2 {
		t.Fatalf("a change was not sent once the interval passed (%d calls)", n)
	}
}

// brandingFixture is a reconciler with one domain whose branding applies,
// and a recorder for the retries it schedules.
func brandingFixture(t *testing.T) (*Reconciler, *fakeAgent, *fakeWebmailBrandingRepo, *[]time.Duration, *[]func()) {
	t.Helper()
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	r.webmailBrandingDir = t.TempDir()
	repo := &fakeWebmailBrandingRepo{rows: []repository.WebmailBrandingRow{
		{DomainWebmailBranding: models.DomainWebmailBranding{DomainID: "01HZZZZZZZZZZZZZZZZZZZZZZZ", AppName: "A Mail"}, DomainName: "a.com", Applies: true},
	}}
	r.WithWebmailBranding(repo)
	var waits []time.Duration
	var fires []func()
	r.webmailBrandingAfterFunc = func(d time.Duration, f func()) {
		waits = append(waits, d)
		fires = append(fires, f)
	}
	return r, ag, repo, &waits, &fires
}

func brandingQueued(r *Reconciler) bool {
	r.dirtyMu.Lock()
	defer r.dirtyMu.Unlock()
	_, ok := r.dirty[ResourceKey{Kind: KindWebmailBranding}]
	return ok
}

// GH #2502: a save sends the branding at once, out of band, instead of on the
// next pass, and says webmail shows it now.
func TestWebmailBrandingSoon_SendsAtOnce(t *testing.T) {
	r, ag, _, _, _ := brandingFixture(t)

	at := r.WebmailBrandingSoon()
	if d := time.Until(at); d > time.Second || d < -time.Second {
		t.Errorf("webmail shows it at %v, want now", at)
	}
	if !brandingQueued(r) {
		t.Fatal("the branding was not queued for the reconcile loop")
	}
	if !r.drainOne(context.Background()) {
		t.Fatal("the loop found nothing to do")
	}
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 1 {
		t.Fatalf("webmail.branding.apply called %d times, want 1", n)
	}
}

// A save within webmailBrandingMinInterval of the last send is held back, says
// when it shows, and goes out then: one retry, at the end of the interval.
func TestWebmailBrandingSoon_HeldBackChangeGoesOutWhenTheIntervalEnds(t *testing.T) {
	r, ag, repo, waits, fires := brandingFixture(t)
	r.WebmailBrandingSoon()
	r.drainOne(context.Background())

	repo.rows[0].AppName = "A Mail 2"
	at := r.WebmailBrandingSoon()
	if d := time.Until(at); d < webmailBrandingMinInterval-time.Minute || d > webmailBrandingMinInterval {
		t.Errorf("webmail shows it in %v, want about %v", d, webmailBrandingMinInterval)
	}
	r.drainOne(context.Background())
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 1 {
		t.Fatalf("a change was sent within the interval (%d calls); each restarts webmail", n)
	}
	if len(*waits) != 1 {
		t.Fatalf("%d retries scheduled, want 1", len(*waits))
	}
	if d := (*waits)[0]; d < time.Until(at) || d > time.Until(at)+2*time.Second {
		t.Errorf("the retry waits %v, want just past %v", d, time.Until(at))
	}

	// Another save in the same interval: still one retry.
	repo.rows[0].AppName = "A Mail 3"
	r.WebmailBrandingSoon()
	r.drainOne(context.Background())
	if len(*waits) != 1 {
		t.Fatalf("%d retries scheduled for one interval, want 1", len(*waits))
	}

	// The interval passes and the retry fires: the change goes out.
	r.webmailBrandingSent.Add(-int64(webmailBrandingMinInterval))
	(*fires)[0]()
	if !brandingQueued(r) {
		t.Fatal("the retry did not queue the branding")
	}
	r.drainOne(context.Background())
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 2 {
		t.Fatalf("the held-back change was not sent when the interval ended (%d calls)", n)
	}
}

// A pass inside the interval with nothing new schedules no retry.
func TestWebmailBranding_NoChangeNoRetry(t *testing.T) {
	r, _, _, waits, _ := brandingFixture(t)
	r.WebmailBrandingSoon()
	r.drainOne(context.Background())
	r.WebmailBrandingSoon()
	r.drainOne(context.Background())
	if len(*waits) != 0 {
		t.Fatalf("a retry was scheduled with nothing to send (%v)", *waits)
	}
}

// A DR standby serves nothing: a queued branding sends nothing either.
func TestWebmailBranding_StandbySendsNothing(t *testing.T) {
	r, ag := buildGateReconciler(t, models.ServerRoleStandby)
	r.WithWebmailBranding(&fakeWebmailBrandingRepo{rows: []repository.WebmailBrandingRow{
		{DomainWebmailBranding: models.DomainWebmailBranding{DomainID: "01HZZZZZZZZZZZZZZZZZZZZZZZ", AppName: "A Mail"}, DomainName: "a.com", Applies: true},
	}})
	r.webmailBrandingDir = t.TempDir()
	r.WebmailBrandingSoon()
	r.drainOne(context.Background())
	if n := countAgentCalls(ag, "webmail.branding.apply"); n != 0 {
		t.Fatalf("a DR standby sent webmail its branding (%d calls)", n)
	}
}
