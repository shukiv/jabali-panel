package reconciler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeMailStatsAgent struct {
	calls int
}

func (f *fakeMailStatsAgent) Call(_ context.Context, method string, _ interface{}) (json.RawMessage, error) {
	if method != "mail.stats_sample" {
		return nil, nil
	}
	f.calls++
	return json.RawMessage(`{"metrics":{"message_ingest":42,"smtp_active_connections":2},"queue_size":5,"enabled_now":false}`), nil
}

type fakeMailStatsRepo struct {
	repository.MailStatsRepository
	inserted map[string]float64
}

func (f *fakeMailStatsRepo) InsertSamples(_ context.Context, _ time.Time, metrics map[string]float64) error {
	f.inserted = metrics
	return nil
}

// GH #873: a tick stores every scraped metric plus queue_size as a
// pseudo-gauge; hosts with the mail module off skip sampling entirely.
func TestMailStatsTick_StoresMetricsAndQueue(t *testing.T) {
	agent := &fakeMailStatsAgent{}
	repo := &fakeMailStatsRepo{}
	deps := MailStatsTickerDeps{
		Agent:    agent,
		Stats:    repo,
		Settings: &fakeSettingsRepo{srv: &models.ServerSettings{MailEnabled: true}},
		Log:      nopLogger{},
	}
	mailStatsTick(context.Background(), deps)
	if agent.calls != 1 {
		t.Fatalf("agent calls = %d", agent.calls)
	}
	if repo.inserted["message_ingest"] != 42 || repo.inserted["queue_size"] != 5 {
		t.Fatalf("inserted = %v", repo.inserted)
	}

	// Mail module off: no agent call, no insert.
	agent2 := &fakeMailStatsAgent{}
	repo2 := &fakeMailStatsRepo{}
	deps2 := MailStatsTickerDeps{
		Agent:    agent2,
		Stats:    repo2,
		Settings: &fakeSettingsRepo{srv: &models.ServerSettings{MailEnabled: false}},
		Log:      nopLogger{},
	}
	mailStatsTick(context.Background(), deps2)
	if agent2.calls != 0 || repo2.inserted != nil {
		t.Fatalf("mail-off host must not sample (calls=%d inserted=%v)", agent2.calls, repo2.inserted)
	}
}

// JAB-203 shape guard for this ticker too.
func TestServeWiresMailStatsTicker(t *testing.T) {
	assertServeCalls(t, "reconciler.StartMailStatsTicker(")
}
