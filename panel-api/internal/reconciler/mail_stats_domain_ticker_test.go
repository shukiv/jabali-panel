package reconciler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeDomainStatsAgent struct {
	domainCalls int
	lastArgs    map[string]any
}

func (f *fakeDomainStatsAgent) Call(_ context.Context, method string, args interface{}) (json.RawMessage, error) {
	if method != "mail.stats_domain_sample" {
		return nil, nil
	}
	f.domainCalls++
	if m, ok := args.(map[string]any); ok {
		f.lastArgs = m
	}
	return json.RawMessage(`{"sampled_at":"2026-08-10T00:00:00Z","counts":[` +
		`{"domain":"a.test","metric":"sent","count":3},` +
		`{"domain":"a.test","metric":"received","count":1}]}`), nil
}

type fakeDomainStatsRepo struct {
	repository.MailStatsRepository
	inserted   []repository.DomainCount
	insertedAt time.Time
}

func (f *fakeDomainStatsRepo) LastDomainSampleAt(context.Context) (time.Time, error) {
	return time.Time{}, nil // first run
}
func (f *fakeDomainStatsRepo) InsertDomainSamples(_ context.Context, at time.Time, c []repository.DomainCount) error {
	f.insertedAt = at
	f.inserted = c
	return nil
}

type fakeDomainsRepo struct {
	repository.DomainRepository
	names []string
}

func (f *fakeDomainsRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	out := make([]models.Domain, len(f.names))
	for i, n := range f.names {
		out[i] = models.Domain{Name: n}
	}
	return out, int64(len(out)), nil
}

// GH #873 round 3: the domain tick passes the local-domain set, stores the
// agent's per-domain counts at the returned sampled_at, and gates on the mail
// module.
func TestDomainStatsTick_StoresPerDomainCounts(t *testing.T) {
	agent := &fakeDomainStatsAgent{}
	repo := &fakeDomainStatsRepo{}
	deps := MailStatsTickerDeps{
		Agent:    agent,
		Stats:    repo,
		Domains:  &fakeDomainsRepo{names: []string{"a.test", "b.test"}},
		Settings: &fakeSettingsRepo{srv: &models.ServerSettings{MailEnabled: true}},
		Log:      nopLogger{},
	}
	domainStatsTick(context.Background(), deps)

	if agent.domainCalls != 1 {
		t.Fatalf("domain agent calls = %d, want 1", agent.domainCalls)
	}
	// local_domains forwarded to the agent.
	ld, _ := agent.lastArgs["local_domains"].([]string)
	if len(ld) != 2 || ld[0] != "a.test" {
		t.Fatalf("local_domains = %v", agent.lastArgs["local_domains"])
	}
	// first run: no watermark passed.
	if _, ok := agent.lastArgs["since"]; ok {
		t.Errorf("first run must not send a since watermark: %v", agent.lastArgs["since"])
	}
	// counts stored at the agent's sampled_at.
	if len(repo.inserted) != 2 {
		t.Fatalf("inserted = %v", repo.inserted)
	}
	if !repo.insertedAt.Equal(time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("insertedAt = %v, want the agent's sampled_at", repo.insertedAt)
	}

	// Mail module off: no agent call, no insert.
	agent2 := &fakeDomainStatsAgent{}
	repo2 := &fakeDomainStatsRepo{}
	domainStatsTick(context.Background(), MailStatsTickerDeps{
		Agent:    agent2,
		Stats:    repo2,
		Domains:  &fakeDomainsRepo{names: []string{"a.test"}},
		Settings: &fakeSettingsRepo{srv: &models.ServerSettings{MailEnabled: false}},
		Log:      nopLogger{},
	})
	if agent2.domainCalls != 0 || repo2.inserted != nil {
		t.Fatalf("mail-off host must not sample (calls=%d inserted=%v)", agent2.domainCalls, repo2.inserted)
	}
}
