package reconciler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeDigestStats struct {
	stats *repository.DigestStats
}

func (f *fakeDigestStats) Collect(context.Context, time.Time) (*repository.DigestStats, error) {
	return f.stats, nil
}

type fakeDigestSettings struct {
	repository.ServerSettingsRepository
	srv      *models.ServerSettings
	setDays  []string
	setFails bool
}

func (f *fakeDigestSettings) Get(context.Context) (*models.ServerSettings, error) {
	return f.srv, nil
}
func (f *fakeDigestSettings) SetDigestLastSent(_ context.Context, day string) error {
	if f.setFails {
		return context.DeadlineExceeded
	}
	f.setDays = append(f.setDays, day)
	f.srv.DigestLastSentDate = day
	return nil
}

type fakeDigestQueue struct {
	envs []notifications.Envelope
}

func (f *fakeDigestQueue) Publish(_ context.Context, env notifications.Envelope) (string, error) {
	f.envs = append(f.envs, env)
	return "1-1", nil
}

type nopLogger struct{}

func (nopLogger) Info(string, ...interface{})  {}
func (nopLogger) Warn(string, ...interface{})  {}
func (nopLogger) Error(string, ...interface{}) {}

func digestDeps(hourUTC int, lastSent string) (DigestTickerDeps, *fakeDigestQueue, *fakeDigestSettings) {
	q := &fakeDigestQueue{}
	settings := &fakeDigestSettings{srv: &models.ServerSettings{DigestLastSentDate: lastSent}}
	deps := DigestTickerDeps{
		Stats: &fakeDigestStats{stats: &repository.DigestStats{
			AlertsBySeverity: map[string]int64{"error": 2, "warning": 5},
			TopKinds:         []repository.DigestKindCount{{EventKind: "cert.renew.fail", Count: 2}},
			BackupsByStatus:  map[string]int64{"succeeded": 3, "failed": 1},
			CertsExpiring14d: 1,
			DomainsTotal:     12, DomainsEnabled: 11, UsersTotal: 4,
		}},
		Settings: settings,
		Queue:    q,
		Log:      nopLogger{},
		now: func() time.Time {
			return time.Date(2026, 8, 2, hourUTC, 10, 0, 0, time.UTC)
		},
	}
	return deps, q, settings
}

// GH #840: due once per UTC day, only after the send hour; the same day
// never sends twice; the next day sends again.
func TestDigestTick_DayGate(t *testing.T) {
	// Before the send hour: nothing.
	deps, q, _ := digestDeps(digestSendHourUTC-1, "")
	digestTick(context.Background(), deps)
	if len(q.envs) != 0 {
		t.Fatal("digest sent before the send hour")
	}

	// After the send hour: exactly one send, day persisted.
	deps, q, settings := digestDeps(digestSendHourUTC+1, "")
	digestTick(context.Background(), deps)
	digestTick(context.Background(), deps)
	if len(q.envs) != 1 {
		t.Fatalf("sends = %d, want exactly 1 for the day", len(q.envs))
	}
	if len(settings.setDays) != 1 || settings.setDays[0] != "2026-08-02" {
		t.Fatalf("persisted days = %v", settings.setDays)
	}

	// Next day: due again.
	deps.now = func() time.Time { return time.Date(2026, 8, 3, digestSendHourUTC+1, 0, 0, 0, time.UTC) }
	digestTick(context.Background(), deps)
	if len(q.envs) != 2 {
		t.Fatalf("next-day sends = %d, want 2 total", len(q.envs))
	}

	env := q.envs[0]
	if env.EventKind != "digest.daily" {
		t.Fatalf("kind = %q", env.EventKind)
	}
	for _, want := range []string{"2 error", "5 warning", "cert.renew.fail: 2", "3 succeeded", "1 failed", "12 domains (11 enabled), 4 users", "Certificates expiring within 14 days: 1"} {
		if !strings.Contains(env.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, env.Body)
		}
	}
}

// Persist-then-publish: a failed last-sent write must NOT send (dupes
// annoy more than a gap hurts).
func TestDigestTick_PersistFailureSkipsSend(t *testing.T) {
	deps, q, settings := digestDeps(digestSendHourUTC+1, "")
	settings.setFails = true
	digestTick(context.Background(), deps)
	if len(q.envs) != 0 {
		t.Fatal("digest sent although the last-sent persist failed")
	}
}

// JAB-203 regression shape: StartSSLTicker existed but was never wired to a
// call site, so it silently never ran. Pin that serve.go actually starts
// the digest ticker.
func TestServeWiresDigestTicker(t *testing.T) {
	assertServeCalls(t, "reconciler.StartDigestTicker(")
}

// assertServeCalls fails unless serve.go contains the given call — the
// JAB-203 regression shape (ticker written, never wired, silently dead).
func assertServeCalls(t *testing.T, call string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "server", "serve.go"))
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	if !strings.Contains(string(src), call) {
		t.Fatalf("serve.go does not call %s — the ticker would silently never run (JAB-203 shape)", call)
	}
}

// The digest kind must exist in the canonical event-kind list (the seed +
// settings UI derive from it) and default OFF.
func TestDigestKindRegisteredDefaultOff(t *testing.T) {
	for _, k := range models.AllNotificationEventKinds {
		if k.Kind == "digest.daily" {
			if k.DefaultOn {
				t.Fatal("digest.daily must default OFF — opt-in")
			}
			return
		}
	}
	t.Fatal("digest.daily missing from AllNotificationEventKinds")
}
