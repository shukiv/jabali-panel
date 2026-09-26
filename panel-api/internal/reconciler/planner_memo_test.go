package reconciler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// countingSettingsRepo counts Get calls that reach the repository.
type countingSettingsRepo struct {
	repository.ServerSettingsRepository
	gets atomic.Int64
}

func (c *countingSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	c.gets.Add(1)
	return c.ServerSettingsRepository.Get(ctx)
}

// countingTemplateRepo is a page-template repository with one row per key,
// counting Get calls.
type countingTemplateRepo struct {
	mu   sync.Mutex
	rows map[string]string
	gets atomic.Int64
}

func (c *countingTemplateRepo) List(context.Context) ([]models.PageTemplate, error) { return nil, nil }
func (c *countingTemplateRepo) Get(_ context.Context, key string) (*models.PageTemplate, error) {
	c.gets.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	content, ok := c.rows[key]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &models.PageTemplate{Key: key, Content: content}, nil
}
func (c *countingTemplateRepo) Upsert(context.Context, string, string) error { return nil }
func (c *countingTemplateRepo) EnsureDefaults(context.Context) (int, error)  { return 0, nil }

// memoFixture is the webmail-on steady-state fixture grown to n domains,
// with the server settings and page templates behind counters.
func memoFixture(t *testing.T, n int) (*Reconciler, *fakeAgent, *countingSettingsRepo, *countingTemplateRepo) {
	t.Helper()
	r, ag := steadyStateFixture(t, true)
	domains := r.domains.(*fakeDomainRepo)
	certs := r.sslCerts.(*fakeSSLCertRepo)
	base := *domainByID(t, r, "domain-1")
	now := time.Now().UTC()
	expires := now.Add(60 * 24 * time.Hour)
	for i := 3; i <= n; i++ {
		d := base
		d.ID = fmt.Sprintf("domain-%d", i)
		d.Name = fmt.Sprintf("site%d.example.org", i)
		d.DocRoot = "/home/alice/domains/" + d.Name + "/public_html"
		domains.domains[d.ID] = &d
		cp, kp := "/etc/jabali/ssl/"+d.Name+"/fullchain.pem", "/etc/jabali/ssl/"+d.Name+"/privkey.pem"
		certs.byDomain[d.ID] = &models.SSLCertificate{
			ID: "cert-" + d.ID, DomainID: d.ID, Status: models.SSLStatusIssued,
			IssuedAt: &now, ExpiresAt: &expires, CertPath: &cp, KeyPath: &kp,
		}
	}
	settings := &countingSettingsRepo{ServerSettingsRepository: r.serverSettings}
	r.serverSettings = settings
	templates := &countingTemplateRepo{rows: map[string]string{
		models.PageTemplateDomainDefaultIndex: "<h1>welcome</h1>",
	}}
	r.WithPageTemplates(templates)
	return r, ag, settings, templates
}

// JAB-369 AC7: a tick reads each global row a bounded number of times,
// however many domains it converges. Server settings and the default index
// template are one row each; before the per-tick memo, vhost assembly, DNS,
// SSL and webmail read them once per domain.
func TestTickMemo_GlobalReadsDoNotGrowWithDomainCount(t *testing.T) {
	type counts struct{ settings, templates int64 }
	measure := func(n int) (first, steady counts) {
		r, _, settings, templates := memoFixture(t, n)
		if _, err := r.Run(context.Background(), RunNormal); err != nil {
			t.Fatal(err)
		}
		first = counts{settings.gets.Load(), templates.gets.Load()}
		settings.gets.Store(0)
		templates.gets.Store(0)
		if _, err := r.Run(context.Background(), RunNormal); err != nil {
			t.Fatal(err)
		}
		steady = counts{settings.gets.Load(), templates.gets.Load()}
		return first, steady
	}
	firstSmall, steadySmall := measure(2)
	firstLarge, steadyLarge := measure(8)
	if firstSmall != firstLarge {
		t.Errorf("first tick: 2 domains read settings %d and templates %d times; 8 domains read them %d and %d times",
			firstSmall.settings, firstSmall.templates, firstLarge.settings, firstLarge.templates)
	}
	if steadySmall != steadyLarge {
		t.Errorf("steady tick: 2 domains read settings %d and templates %d times; 8 domains read them %d and %d times",
			steadySmall.settings, steadySmall.templates, steadyLarge.settings, steadyLarge.templates)
	}
	if steadyLarge.settings != 1 {
		t.Errorf("a steady tick should read server settings once, read them %d times", steadyLarge.settings)
	}
}

// The memo must not change what the Agent receives. Every domain's vhost
// payload built inside a run (memoized reads) hashes the same as the payload
// ReconcileOne builds outside one (direct reads).
func TestTickMemo_VhostPayloadsMatchUnmemoizedBuild(t *testing.T) {
	inRun, _, _, _ := memoFixture(t, 4)
	if _, err := inRun.Run(context.Background(), RunNormal); err != nil {
		t.Fatal(err)
	}
	direct, _, _, _ := memoFixture(t, 4)
	for id := range direct.domains.(*fakeDomainRepo).domains {
		if err := direct.ReconcileOne(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		a, okA := inRun.ledger.lookup(PhaseDomainVhost, id)
		b, okB := direct.ledger.lookup(PhaseDomainVhost, id)
		if !okA || !okB {
			t.Fatalf("%s: vhost not applied (run=%v direct=%v)", id, okA, okB)
		}
		if a.Hash != b.Hash {
			t.Errorf("%s: vhost payload differs between a memoized run and a direct build", id)
		}
	}
}

// A settings change between two ticks is seen by the second tick: the memo
// lives for one run only.
func TestTickMemo_SettingsChangeIsSeenNextTick(t *testing.T) {
	r, ag, settings, _ := memoFixture(t, 2)
	inner := settings.ServerSettingsRepository.(*fakeServerSettingsRepo)
	tickCalls(t, r, ag)

	inner.settings.WebmailEnabled = false
	calls, _ := tickCalls(t, r, ag)
	if countOf(calls, "service.stop") != 1 {
		t.Fatal("a settings change must reach the next tick")
	}
}

// Reads after a run has finished go to the repository, so a goroutine that
// outlives its tick never works from that tick's snapshot.
func TestTickMemo_ClosedRunReadsThrough(t *testing.T) {
	r, _, settings, _ := memoFixture(t, 2)
	ctx, rr := withRun(context.Background(), RunNormal)
	if _, err := r.settingsGet(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.settingsGet(ctx); err != nil {
		t.Fatal(err)
	}
	if n := settings.gets.Load(); n != 1 {
		t.Fatalf("within a run, settings reads = %d, want 1", n)
	}
	rr.finish()
	for i := 0; i < 2; i++ {
		if _, err := r.settingsGet(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := settings.gets.Load(); n != 3 {
		t.Fatalf("after the run finished, every read must reach the repository (reads = %d, want 3)", n)
	}
}

// Each caller gets its own copy of the memoized settings, as it would from
// the repository, so one pass cannot change what another reads.
func TestTickMemo_EachReadIsACopy(t *testing.T) {
	r, _, _, _ := memoFixture(t, 2)
	ctx, rr := withRun(context.Background(), RunNormal)
	defer rr.finish()
	first, err := r.settingsGet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first.Hostname = "changed.by.one.pass"
	second, err := r.settingsGet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Hostname != "panel.example.com" {
		t.Fatalf("a pass's change leaked into the memo: hostname = %q", second.Hostname)
	}
}

// ReconcileAll and ReconcileAllForce end the run they execute, whoever
// started it, so no read after they return is served from their memo.
func TestTickMemo_EntryPointsFinishTheirRun(t *testing.T) {
	for name, entry := range map[string]func(*Reconciler, context.Context) error{
		"ReconcileAll":      (*Reconciler).ReconcileAll,
		"ReconcileAllForce": (*Reconciler).ReconcileAllForce,
	} {
		t.Run(name, func(t *testing.T) {
			r, _, settings, _ := memoFixture(t, 2)
			ctx, _ := withRun(context.Background(), RunNormal)
			if err := entry(r, ctx); err != nil {
				t.Fatal(err)
			}
			before := settings.gets.Load()
			for i := 0; i < 2; i++ {
				if _, err := r.settingsGet(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if settings.gets.Load() != before+2 {
				t.Fatalf("%s returned with its run's memo still serving reads", name)
			}
		})
	}
}
