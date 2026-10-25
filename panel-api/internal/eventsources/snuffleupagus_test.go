package eventsources

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeSnufRepo struct {
	repository.SnuffleupagusRepository // methods the ingest doesn't call
	latest                             *time.Time
	inserted                           []models.SnuffleupagusIncident
}

func (f *fakeSnufRepo) LatestIncidentTs(context.Context) (*time.Time, error) { return f.latest, nil }

func (f *fakeSnufRepo) InsertIncident(_ context.Context, in *models.SnuffleupagusIncident) error {
	f.inserted = append(f.inserted, *in)
	return nil
}

type fakeSnufAgent struct {
	params map[string]any
	reply  string
}

func (a *fakeSnufAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "snuffleupagus.fetch_incidents" {
		return nil, nil
	}
	a.params = params.(map[string]any)
	return json.RawMessage(a.reply), nil
}

func snufDeps(repo *fakeSnufRepo, ag *fakeSnufAgent) Deps {
	return Deps{Snuffleupagus: repo, Agent: ag, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// GH #2262: a panel restart read the last minute again and stored what it
// already had. The first pass now starts just after the newest stored
// incident when that is inside the minute.
func TestSnuffleupagusIngestStart(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	recent := now.Add(-10 * time.Second)
	old := now.Add(-time.Hour)

	cases := []struct {
		name   string
		latest *time.Time
		want   time.Time
	}{
		{"no incidents yet", nil, now.Add(-snuffleupagusTick)},
		{"newest is inside the minute", &recent, recent.Add(time.Microsecond)},
		{"newest is older than the minute", &old, now.Add(-snuffleupagusTick)},
	}
	for _, c := range cases {
		got := snuffleupagusIngestStart(context.Background(), snufDeps(&fakeSnufRepo{latest: c.latest}, nil), now)
		if !got.Equal(c.want) {
			t.Errorf("%s: start = %v, want %v", c.name, got, c.want)
		}
	}
}

// GH #2262: each pass asks the agent for entries from since_us on, to the
// microsecond (journalctl alone reads whole seconds), and stores the client
// address packed so an IPv6 address fits the column.
func TestSnuffleupagusIngestPass_SendsSinceUSAndStoresThePackedAddress(t *testing.T) {
	since := time.Date(2026, 10, 25, 1, 43, 31, 2000, time.UTC)
	repo := &fakeSnufRepo{}
	ag := &fakeSnufAgent{reply: `{"incidents":[{"ts":"2026-10-25T01:43:31.000003Z","rule_name":"sp.disabled_function:exec",
		"action":"simulated_block","source_ip":"2001:db8::1","php_version":"8.4","raw":"x"}],
		"next_since":"2026-10-25T01:43:31.000004Z"}`}

	next, n := snuffleupagusIngestPass(context.Background(), snufDeps(repo, ag), since)

	if got, ok := ag.params["since_us"].(int64); !ok || got != since.UnixMicro() {
		t.Errorf("since_us = %v, want %d", ag.params["since_us"], since.UnixMicro())
	}
	if ag.params["since"] != "2026-10-25T01:43:31Z" {
		t.Errorf("since = %v, want the whole second for journalctl", ag.params["since"])
	}
	if n != 1 || len(repo.inserted) != 1 {
		t.Fatalf("stored %d incidents (n=%d), want 1", len(repo.inserted), n)
	}
	in := repo.inserted[0]
	if len(in.SourceIP) != 16 || in.SourceIPString() != "2001:db8::1" {
		t.Errorf("source_ip stored as %v (%q), want 16 bytes of 2001:db8::1", in.SourceIP, in.SourceIPString())
	}
	if in.PhpVersion == nil || *in.PhpVersion != "8.4" {
		t.Errorf("php_version = %v, want 8.4", in.PhpVersion)
	}
	if want := time.Date(2026, 10, 25, 1, 43, 31, 4000, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}
