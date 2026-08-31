package eventsources

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeSettings implements just Get; the embedded nil interface supplies the rest
// (never called here).
type fakeSettings struct {
	repository.ServerSettingsRepository
	list string
	err  error
	gets int
}

func (f *fakeSettings) Get(context.Context) (*models.ServerSettings, error) {
	f.gets++
	if f.err != nil {
		return nil, f.err
	}
	return &models.ServerSettings{SSHLoginIgnoreAccounts: f.list}, nil
}

func TestSSHIgnoreCache_MatchesExplicitList(t *testing.T) {
	ss := &fakeSettings{list: "drfeed\nbackup-sync, other"}
	c := newSSHIgnoreCache(ss, time.Now)
	ctx := context.Background()

	if !c.ignored(ctx, "drfeed") {
		t.Error("drfeed should be ignored")
	}
	if !c.ignored(ctx, "backup-sync") {
		t.Error("backup-sync should be ignored")
	}
	if c.ignored(ctx, "alice") {
		t.Error("alice must NOT be ignored")
	}
}

func TestSSHIgnoreCache_NilNeverIgnores(t *testing.T) {
	var c *sshIgnoreCache
	if c.ignored(context.Background(), "drfeed") {
		t.Error("nil cache must not ignore")
	}
	c2 := newSSHIgnoreCache(nil, time.Now) // settings unwired
	if c2.ignored(context.Background(), "drfeed") {
		t.Error("nil settings must not ignore")
	}
}

func TestSSHIgnoreCache_TTLAndFailOpen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	ss := &fakeSettings{list: "drfeed"}
	c := newSSHIgnoreCache(ss, clock)
	ctx := context.Background()

	if !c.ignored(ctx, "drfeed") {
		t.Fatal("drfeed ignored on first load")
	}
	gets := ss.gets

	// Within TTL: list change is NOT observed and Get is not called again.
	ss.list = ""
	if !c.ignored(ctx, "drfeed") {
		t.Error("within TTL the cached set should still ignore drfeed")
	}
	if ss.gets != gets {
		t.Errorf("Get should not be called within TTL (was %d, now %d)", gets, ss.gets)
	}

	// Past TTL: reloads and picks up the empty list.
	now = now.Add(sshIgnoreTTL + time.Second)
	if c.ignored(ctx, "drfeed") {
		t.Error("past TTL the emptied list should no longer ignore drfeed")
	}

	// Fail-open: a settings error with no prior good set ignores no one.
	now = now.Add(sshIgnoreTTL + time.Second)
	ss.err = context.DeadlineExceeded
	if c.ignored(ctx, "drfeed") {
		t.Error("on settings error, must fail open (ignore no one)")
	}
}

func TestProcessSSHJournalEntry_DropsIgnoredAccount(t *testing.T) {
	pub := &capturingPublisher{}
	ss := &fakeSettings{list: "drfeed"}
	d := Deps{Queue: pub, ServerSettings: ss, Log: slog.New(slog.DiscardHandler), Now: time.Now}
	gr := newSSHLoginGrouper(time.Now)
	ig := newSSHIgnoreCache(ss, time.Now)
	ctx := context.Background()

	// Ignored account → nothing published.
	processSSHJournalEntry(ctx, d, gr, ig, journalEntry{
		Comm:    "sshd",
		Message: "Accepted publickey for drfeed from 10.0.0.5 port 49152 ssh2: ED25519 SHA256:abc",
	})
	if pub.Count() != 0 {
		t.Fatalf("ignored account must not notify, got %d envelopes", pub.Count())
	}

	// A different account → notifies (first login in a new group).
	processSSHJournalEntry(ctx, d, gr, ig, journalEntry{
		Comm:    "sshd",
		Message: "Accepted publickey for alice from 10.0.0.5 port 49152 ssh2: ED25519 SHA256:abc",
	})
	if pub.Count() != 1 {
		t.Fatalf("non-ignored account should notify once, got %d", pub.Count())
	}
}
