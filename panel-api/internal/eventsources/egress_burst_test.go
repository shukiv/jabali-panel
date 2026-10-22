package eventsources

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

type fakeEgressPolicies struct {
	repository.UserEgressPolicyRepository
	rows []models.UserEgressPolicy
}

func (f fakeEgressPolicies) List(context.Context) ([]models.UserEgressPolicy, error) {
	return f.rows, nil
}

type fakeBurstSettings struct {
	repository.ServerSettingsRepository
	threshold uint32
}

func (f fakeBurstSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{EgressBurstThreshold: f.threshold}, nil
}

type fakeUserFinder struct {
	repository.UserRepository
	users map[string]string // id → username
}

func (f fakeUserFinder) FindByID(_ context.Context, id string) (*models.User, error) {
	name, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &models.User{ID: id, Username: strPtr(name)}, nil
}

// historyPublisher records each envelope in the history the way the
// dispatcher does (same kind, title and body), so a second pass sees the
// first one through shouldFire.
type historyPublisher struct {
	capturingPublisher
	hist *fakeHistory
	now  time.Time
}

func (p *historyPublisher) Publish(ctx context.Context, env notifications.Envelope) (string, error) {
	p.hist.mu.Lock()
	p.hist.rows = append(p.hist.rows, models.NotificationHistory{
		EventKind: env.EventKind,
		Title:     env.Title,
		Body:      env.Body,
		CreatedAt: p.now,
	})
	p.hist.mu.Unlock()
	return p.capturingPublisher.Publish(ctx, env)
}

func egressBurstDeps(now time.Time, rows ...models.UserEgressPolicy) (Deps, *historyPublisher) {
	hist := &fakeHistory{}
	pub := &historyPublisher{hist: hist, now: now}
	return Deps{
		Queue:              pub,
		History:            hist,
		Users:              fakeUserFinder{users: map[string]string{"u1": "alice"}},
		UserEgressPolicies: fakeEgressPolicies{rows: rows},
		ServerSettings:     fakeBurstSettings{threshold: 50},
		Log:                slog.New(slog.DiscardHandler),
		Now:                func() time.Time { return now },
	}, pub
}

// --- tests ---

func TestEgressBurst_UnderThreshold_Silent(t *testing.T) {
	d, pub := egressBurstDeps(fixedNow(), models.UserEgressPolicy{UserID: "u1", State: "enforced", DropCount24h: 49})
	egressBurstPass(context.Background(), d)
	require.Equal(t, 0, pub.Count())
}

// The 15-minute cool-off holds: a user who stays over the threshold gets
// one alert, not one per minute.
func TestEgressBurst_CoolOff_OneAlertPerWindow(t *testing.T) {
	d, pub := egressBurstDeps(fixedNow(), models.UserEgressPolicy{UserID: "u1", State: "enforced", DropCount24h: 120})
	egressBurstPass(context.Background(), d)
	egressBurstPass(context.Background(), d)
	require.Equal(t, 1, pub.Count(), "second pass inside the cool-off must not alert again")
}

func TestEgressBurst_Enforced_SaysBlocked(t *testing.T) {
	d, pub := egressBurstDeps(fixedNow(), models.UserEgressPolicy{UserID: "u1", State: "enforced", DropCount24h: 120})
	egressBurstPass(context.Background(), d)
	require.Equal(t, 1, pub.Count())
	env := pub.Last()
	require.Equal(t, "egress.drop.burst", env.EventKind)
	require.Contains(t, env.Title, "alice")
	require.Contains(t, env.Title, "blocked")
	require.Contains(t, env.Body, "User alice")
	require.Contains(t, env.Body, "dropped them")
}

// Learning mode allows the traffic: the alert says so, and never says it
// was dropped or blocked.
func TestEgressBurst_Learning_SaysAllowed(t *testing.T) {
	d, pub := egressBurstDeps(fixedNow(), models.UserEgressPolicy{UserID: "u1", State: "learning", DropCount24h: 120})
	egressBurstPass(context.Background(), d)
	require.Equal(t, 1, pub.Count())
	env := pub.Last()
	require.Contains(t, env.Title, "learning")
	require.Contains(t, env.Title, "alice")
	require.Contains(t, env.Body, "learning mode")
	require.Contains(t, env.Body, "were allowed")
	require.NotContains(t, strings.ToLower(env.Title), "blocked")
	require.NotContains(t, strings.ToLower(env.Body), "dropped")
}

// A user the repo cannot resolve is still named, by id.
func TestEgressBurst_UnknownUser_NamedByID(t *testing.T) {
	d, pub := egressBurstDeps(fixedNow(), models.UserEgressPolicy{UserID: "u9", State: "enforced", DropCount24h: 120})
	egressBurstPass(context.Background(), d)
	require.Equal(t, 1, pub.Count())
	require.Contains(t, pub.Last().Body, "User u9")
}

type fakeAuditAgent struct{ events []auditEventDTO }

func (a fakeAuditAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.Marshal(auditEventsDTO{Events: a.events})
}

// exec.audit.burst honours its 30-minute cool-off the same way.
func TestExecAuditBurst_CoolOff_OneAlertPerWindow(t *testing.T) {
	now := fixedNow()
	var events []auditEventDTO
	for i := 0; i < execAuditBurstThreshold; i++ {
		events = append(events, auditEventDTO{
			Timestamp: now.Add(-time.Minute).Format(time.RFC3339),
			Username:  "alice",
			Exe:       "/usr/bin/curl",
			Comm:      "curl",
		})
	}
	d, pub := egressBurstDeps(now)
	d.Agent = fakeAuditAgent{events: events}
	execAuditPass(context.Background(), d)
	execAuditPass(context.Background(), d)
	require.Equal(t, 1, pub.Count(), "second pass inside the cool-off must not alert again")
}
