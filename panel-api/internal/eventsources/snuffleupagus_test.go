package eventsources

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// a.test belongs to alice, b.test to bob.
type fakeSnufDomains struct{ repository.DomainRepository }

func (fakeSnufDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	owner := map[string]string{"a.test": "01ALICE", "b.test": "01BOB"}[name]
	if owner == "" {
		return nil, repository.ErrNotFound
	}
	return &models.Domain{ID: "01DOM" + name, Name: name, UserID: owner}, nil
}

type fakeSnufUsers struct{ repository.UserRepository }

func (fakeSnufUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	name := map[string]string{"01ALICE": "alice", "01BOB": "bob"}[id]
	if name == "" {
		return nil, repository.ErrNotFound
	}
	return &models.User{Username: &name}, nil
}

func snufDeps(repo *fakeSnufRepo, ag *fakeSnufAgent) Deps {
	return Deps{
		Snuffleupagus: repo, Agent: ag, Domains: fakeSnufDomains{}, Users: fakeSnufUsers{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
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
		"action":"simulated_block","source_ip":"2001:db8::1","php_version":"8.4","domain":"a.test","owner":"alice","raw":"x"}],
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
	if in.Domain == nil || *in.Domain != "a.test" || in.DomainID == nil || *in.DomainID != "01DOMa.test" {
		t.Errorf("domain = %v (id %v), want a.test", in.Domain, in.DomainID)
	}
	if want := time.Date(2026, 10, 25, 1, 43, 31, 4000, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

// GH #2262: a directory under /home/<owner>/domains/ is the account's own
// choice, so its name counts only when the panel's domain of that name
// belongs to <owner>.
func TestSnuffleupagusSite_OnlyTheOwnersDomain(t *testing.T) {
	d := snufDeps(&fakeSnufRepo{}, nil)
	cases := []struct{ owner, name, want string }{
		{"alice", "a.test", "a.test"},
		{"alice", "b.test", ""},   // bob's site, from a directory in alice's home
		{"alice", "new.test", ""}, // no such domain on the panel
		{"", "a.test", ""},
	}
	for _, c := range cases {
		id, name := snuffleupagusSite(context.Background(), d, c.owner, c.name)
		got := ""
		if name != nil {
			got = *name
		}
		if got != c.want || (c.want == "") != (id == nil) {
			t.Errorf("owner %q domain %q: got %q (id %v), want %q", c.owner, c.name, got, id, c.want)
		}
	}
}

// GH #2262: a field longer than its column failed the insert in strict
// mode, so the incident went unrecorded. The pass cuts it to fit.
func TestSnuffleupagusIngestPass_CutsFieldsToTheirColumns(t *testing.T) {
	repo := &fakeSnufRepo{}
	ag := &fakeSnufAgent{reply: `{"incidents":[{"ts":"2026-10-25T01:43:31Z","action":"simulated_block",
		"rule_name":"sp.wrappers_whitelist:` + strings.Repeat("x", 300) + `","request_uri":"/` + strings.Repeat("y", 3000) + `","raw":"x"}],
		"next_since":"2026-10-25T01:43:32Z"}`}

	snuffleupagusIngestPass(context.Background(), snufDeps(repo, ag), time.Now())

	if len(repo.inserted) != 1 {
		t.Fatalf("stored %d incidents, want 1", len(repo.inserted))
	}
	in := repo.inserted[0]
	if n := utf8.RuneCountInString(in.RuleName); n != 128 {
		t.Errorf("rule_name is %d characters, want 128", n)
	}
	if in.RequestURI == nil || utf8.RuneCountInString(*in.RequestURI) != 2048 {
		t.Errorf("request_uri not cut to 2048 characters")
	}
}
