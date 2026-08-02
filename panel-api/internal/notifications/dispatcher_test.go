package notifications

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifsecret"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// --- helpers ---

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

// --- fake channel sender ---

type fakeSender struct {
	kind    string
	sends   atomic.Int32
	err     error
	wait    chan struct{} // closed to release Send
	hookErr func(models.NotificationChannel, Envelope) error
}

func (f *fakeSender) Kind() string { return f.kind }
func (f *fakeSender) Send(ctx context.Context, ch models.NotificationChannel, env Envelope) error {
	f.sends.Add(1)
	if f.wait != nil {
		<-f.wait
	}
	if f.hookErr != nil {
		return f.hookErr(ch, env)
	}
	return f.err
}

// --- fake repos ---

type fakeChannels struct {
	mu     sync.Mutex
	byID   map[string]*models.NotificationChannel
	enabled []models.NotificationChannel
}

func (f *fakeChannels) Create(ctx context.Context, ch *models.NotificationChannel) error { return nil }
func (f *fakeChannels) Update(ctx context.Context, ch *models.NotificationChannel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[ch.ID] = ch
	// Rebuild enabled slice.
	f.enabled = f.enabled[:0]
	for _, v := range f.byID {
		if v.Enabled {
			f.enabled = append(f.enabled, *v)
		}
	}
	return nil
}
func (f *fakeChannels) Delete(ctx context.Context, id string) error { return nil }
func (f *fakeChannels) FindByID(ctx context.Context, id string) (*models.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.byID[id]; ok {
		out := *ch
		return &out, nil
	}
	return nil, errNotFound
}
func (f *fakeChannels) ListAll(ctx context.Context, opts repository.ListOptions) ([]models.NotificationChannel, int64, error) {
	return nil, 0, nil
}
func (f *fakeChannels) FindEnabledByKind(ctx context.Context, kind string) ([]models.NotificationChannel, error) {
	return nil, nil
}
func (f *fakeChannels) FindEnabledAll(ctx context.Context) ([]models.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.NotificationChannel, len(f.enabled))
	copy(out, f.enabled)
	return out, nil
}

// FindEnabledServerWide returns only the global (user_id IS NULL) enabled
// channels — mirrors the SQL filter so dispatcher tests can assert tenant-owned
// channels are excluded from broadcast/server fan-out (JAB-171).
func (f *fakeChannels) SealAllPlaintext(ctx context.Context) (int, error) { return 0, nil }

func (f *fakeChannels) ListByUser(_ context.Context, userID string) ([]models.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.NotificationChannel, 0)
	for _, ch := range f.enabled {
		if ch.UserID != nil && *ch.UserID == userID {
			out = append(out, ch)
		}
	}
	return out, nil
}

func (f *fakeChannels) FindEnabledServerWide(ctx context.Context) ([]models.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.NotificationChannel, 0, len(f.enabled))
	for _, ch := range f.enabled {
		if ch.UserID == nil {
			out = append(out, ch)
		}
	}
	return out, nil
}

var errNotFound = errors.New("not found")

// Upstream repo interface uses repository.ListOptions — we bridge via
// an embed so the dispatcher's compile-time check against
// NotificationChannelRepository keeps working even though the fake
// doesn't need the full list semantics for these tests. See the
// ListAll wrapper below.

type fakeHistory struct {
	mu     sync.Mutex
	rows   map[string]*models.NotificationHistory
	outcomes []string
}

func (f *fakeHistory) Create(ctx context.Context, h *models.NotificationHistory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]*models.NotificationHistory{}
	}
	dup := *h
	f.rows[h.ID] = &dup
	return nil
}
func (f *fakeHistory) UpdateOutcome(ctx context.Context, id, outcome, errMsg string, retryCount int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h, ok := f.rows[id]; ok {
		h.Outcome = outcome
		h.ErrorMessage = errMsg
		h.RetryCount = retryCount
	}
	f.outcomes = append(f.outcomes, outcome)
	return nil
}
func (f *fakeHistory) MarkRead(ctx context.Context, id string) error              { return nil }
func (f *fakeHistory) MarkAllReadForUser(ctx context.Context, u string) (int64, error) { return 0, nil }
func (f *fakeHistory) DeleteAllForUser(ctx context.Context, u string, b bool) (int64, error) {
	return 0, nil
}
func (f *fakeHistory) FindByID(ctx context.Context, id string) (*models.NotificationHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h, ok := f.rows[id]; ok {
		out := *h
		return &out, nil
	}
	return nil, errNotFound
}
func (f *fakeHistory) ListForUser(ctx context.Context, u string, opts repository.ListOptions) ([]models.NotificationHistory, int64, error) {
	return nil, 0, nil
}
func (f *fakeHistory) ListForAdminInbox(ctx context.Context, u string, opts repository.ListOptions) ([]models.NotificationHistory, int64, error) {
	return nil, 0, nil
}
func (f *fakeHistory) CountUnreadForAdminInbox(ctx context.Context, u string) (int64, error) {
	return 0, nil
}
func (f *fakeHistory) CountUnreadForUser(ctx context.Context, u string) (int64, error) {
	return 0, nil
}
func (f *fakeHistory) ListRecentByEvent(ctx context.Context, kind string, since time.Time) ([]models.NotificationHistory, error) {
	return nil, nil
}

type fakeWebhook struct {
	mu         sync.Mutex
	failures   map[string]int
	lastError  map[string]string
	successes  map[string]int
}

func (f *fakeWebhook) ensure() {
	if f.failures == nil {
		f.failures = map[string]int{}
		f.lastError = map[string]string{}
		f.successes = map[string]int{}
	}
}
func (f *fakeWebhook) FindByChannelID(ctx context.Context, id string) (*models.WebhookEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	return &models.WebhookEndpoint{
		ChannelID:           id,
		ConsecutiveFailures: f.failures[id],
		LastError:           f.lastError[id],
	}, nil
}
func (f *fakeWebhook) RecordSuccess(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	f.successes[id]++
	f.failures[id] = 0
	f.lastError[id] = ""
	return nil
}
func (f *fakeWebhook) RecordFailure(ctx context.Context, id, errMsg string, backoff *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	f.failures[id]++
	f.lastError[id] = errMsg
	return nil
}
func (f *fakeWebhook) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	delete(f.failures, id)
	delete(f.lastError, id)
	delete(f.successes, id)
	return nil
}

func newFixture(t *testing.T, senders ...ChannelSender) (*Dispatcher, *fakeChannels, *fakeHistory, *fakeWebhook, *Queue) {
	t.Helper()
	rdb, _ := newRedis(t)
	queue := NewQueue(rdb)
	// Create the consumer group up front so publishes in the test body
	// are guaranteed to be delivered — Start() also does this but runs
	// in a goroutine, so the test would otherwise race.
	require.NoError(t, queue.EnsureGroup(context.Background()))
	reg := NewRegistry()
	for _, s := range senders {
		reg.Register(s)
	}
	channels := &fakeChannels{byID: map[string]*models.NotificationChannel{}}
	history := &fakeHistory{}
	webhooks := &fakeWebhook{}
	d, err := NewDispatcher(queue, reg, channels, history, webhooks, slog.New(slog.DiscardHandler), Config{
		BatchSize:           4,
		ReadBlock:           20 * time.Millisecond,
		ReclaimInterval:     50 * time.Millisecond,
		ReclaimMinIdle:      30 * time.Millisecond,
		MaxRetries:          3,
		CircuitBreakerLimit: 3,
		ShutdownGrace:       250 * time.Millisecond,
	})
	require.NoError(t, err)
	return d, channels, history, webhooks, queue
}

func addChannel(c *fakeChannels, id, name, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := &models.NotificationChannel{ID: id, Name: name, Kind: kind, Enabled: true}
	c.byID[id] = ch
	c.enabled = append(c.enabled, *ch)
}

// --- tests ---

// TestDispatcher_DeliversEnvelope — happy path. Publish one envelope
// against a registered sender; assert it was sent + history row moved
// to `sent`.
func TestDispatcher_DeliversEnvelope(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{kind: "slack"}
	d, channels, history, _, queue := newFixture(t, sender)
	addChannel(channels, "01HF00CHSLK0000000000001", "Ops Slack", "slack")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { _ = d.Start(ctx); close(done) }()

	_, err := queue.Publish(ctx, Envelope{
		EventKind: "domain.expiry.7d",
		Severity:  models.NotificationSeverityWarning,
		Title:     "example.com expires in 7 days",
		Body:      "Renew it.",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sender.sends.Load() == 1 }, time.Second, 5*time.Millisecond)

	cancel()
	<-done

	// At least one history row should have moved to `sent`.
	history.mu.Lock()
	defer history.mu.Unlock()
	require.Contains(t, history.outcomes, models.NotificationOutcomeSent)
}

// TestDispatcher_CircuitBreakerAutoDisables — three consecutive
// failures trip the breaker: channel gets Enabled=false + an
// auto_disabled alarm row is created.
func TestDispatcher_CircuitBreakerAutoDisables(t *testing.T) {
	t.Parallel()
	boom := errors.New("slack 502")
	sender := &fakeSender{kind: "slack", err: boom}
	d, channels, history, webhooks, queue := newFixture(t, sender)
	addChannel(channels, "01HF00CHSLK0000000000002", "Ops Slack", "slack")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.Start(ctx); close(done) }()

	// Publish three envelopes — breaker limit is 3.
	for i := 0; i < 3; i++ {
		_, err := queue.Publish(ctx, Envelope{
			EventKind: "cert.renew.failure",
			Severity:  models.NotificationSeverityError,
			Title:     "renewal failure",
			Body:      "details",
		})
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		channels.mu.Lock()
		defer channels.mu.Unlock()
		ch := channels.byID["01HF00CHSLK0000000000002"]
		return ch != nil && !ch.Enabled
	}, 2*time.Second, 10*time.Millisecond, "channel should auto-disable")

	webhooks.mu.Lock()
	require.GreaterOrEqual(t, webhooks.failures["01HF00CHSLK0000000000002"], 3)
	webhooks.mu.Unlock()

	cancel()
	<-done

	// An auto_disabled alarm history row should exist.
	history.mu.Lock()
	defer history.mu.Unlock()
	found := false
	for _, row := range history.rows {
		if row.EventKind == "notifications.channel.auto_disabled" {
			found = true
			break
		}
	}
	require.True(t, found, "auto-disabled alarm history row must exist")
}

// TestDispatcher_MalformedEnvelopeGoesToDLQ — missing required fields
// (no title) → DLQ entry + queue entry acked.
func TestDispatcher_MalformedEnvelopeGoesToDLQ(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{kind: "slack"}
	d, channels, _, _, queue := newFixture(t, sender)
	addChannel(channels, "01HF00CHSLK0000000000003", "Ops", "slack")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.Start(ctx); close(done) }()

	// Direct XADD bypassing Envelope.StreamMap so we can build a
	// malformed entry (missing title).
	rdb := queue.rdb
	_, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamQueue,
		Values: map[string]any{"event_kind": "x", "severity": "info"},
	}).Result()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		n, _ := rdb.XLen(ctx, StreamDLQ).Result()
		return n >= 1
	}, time.Second, 10*time.Millisecond, "malformed entry should land in DLQ")

	cancel()
	<-done
}

// TestDispatcher_NoTargetChannelsAcksAndMovesOn — event with no
// matching enabled channel is acked without error so the stream
// doesn't grow.
func TestDispatcher_NoTargetChannelsAcksAndMovesOn(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{kind: "slack"}
	d, _, _, _, queue := newFixture(t, sender)
	// No channels added to the fakeChannels repo — FindEnabledAll
	// returns empty.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.Start(ctx); close(done) }()

	_, err := queue.Publish(ctx, Envelope{
		EventKind: "disk.full.85",
		Severity:  models.NotificationSeverityWarning,
		Title:     "85% on /",
		Body:      "heads up",
	})
	require.NoError(t, err)

	rdb := queue.rdb
	require.Eventually(t, func() bool {
		n, _ := rdb.XLen(ctx, StreamQueue).Result()
		return n == 0
	}, time.Second, 10*time.Millisecond, "queue should be drained after no-target ack")

	require.Equal(t, int32(0), sender.sends.Load())

	cancel()
	<-done
}

// TestQueue_EnsureGroupIsIdempotent — a second EnsureGroup does not
// return BUSYGROUP to the caller.
func TestQueue_EnsureGroupIsIdempotent(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	q := NewQueue(rdb)
	ctx := context.Background()
	require.NoError(t, q.EnsureGroup(ctx))
	require.NoError(t, q.EnsureGroup(ctx))
}

// TestEnvelope_StreamRoundTrip — StreamMap → envelopeFromStream returns
// the same logical envelope.
func TestEnvelope_StreamRoundTrip(t *testing.T) {
	t.Parallel()
	in := Envelope{
		EventKind:  "cert.renew.failure",
		Severity:   "error",
		Title:      "Renewal failed",
		Body:       "Let's Encrypt returned 429",
		Deeplink:   "/admin/ssl",
		ChannelIDs: []string{"a", "b", "c"},
		UserID:     "01HF000USER0000000000000",
	}
	m := in.StreamMap()
	// XADD values map has any-typed values; simulate what
	// XREADGROUP hands back (string values).
	sm := map[string]any{}
	for k, v := range m {
		sm[k] = v
	}
	out, err := envelopeFromStream(sm)
	require.NoError(t, err)
	require.Equal(t, in, out)
}

// --- JAB-171 phase 2: recipient-aware routing ---

type fakeUserRoutes struct {
	// keyed by userID -> eventKind -> channelIDs
	routes map[string]map[string][]string
}

func (f *fakeUserRoutes) Create(ctx context.Context, r *models.UserNotificationRoute) error { return nil }
func (f *fakeUserRoutes) ListByUser(ctx context.Context, userID string) ([]models.UserNotificationRoute, error) {
	return nil, nil
}
func (f *fakeUserRoutes) ListByUserEvent(ctx context.Context, userID, eventKind string) ([]models.UserNotificationRoute, error) {
	var out []models.UserNotificationRoute
	for _, chID := range f.routes[userID][eventKind] {
		out = append(out, models.UserNotificationRoute{UserID: userID, EventKind: eventKind, ChannelID: chID})
	}
	return out, nil
}
func (f *fakeUserRoutes) Delete(ctx context.Context, id string) error            { return nil }
func (f *fakeUserRoutes) DeleteByChannel(ctx context.Context, channelID string) error { return nil }

func ptr(s string) *string { return &s }

func ids(chs []models.NotificationChannel) map[string]bool {
	m := map[string]bool{}
	for _, c := range chs {
		m[c.ID] = true
	}
	return m
}

func TestResolveTargets_PerUserRouting(t *testing.T) {
	g1 := models.NotificationChannel{ID: "G1", Enabled: true, UserID: nil}                 // server-wide
	a1 := models.NotificationChannel{ID: "A1", Enabled: true, UserID: ptr("userA")}        // tenant A owns
	b1 := models.NotificationChannel{ID: "B1", Enabled: true, UserID: ptr("userB")}        // tenant B owns
	fc := &fakeChannels{
		byID:    map[string]*models.NotificationChannel{"G1": &g1, "A1": &a1, "B1": &b1},
		enabled: []models.NotificationChannel{g1, a1, b1},
	}
	fr := &fakeUserRoutes{routes: map[string]map[string][]string{
		"userA": {"backup.done": {"A1", "B1"}}, // A1 owned; B1 is a forged/stale route to another user's channel
	}}
	d := &Dispatcher{channels: fc, userRoutes: fr}
	ctx := context.Background()

	// Server event (no UserID): server-wide channels only; tenant channels excluded.
	got, err := d.resolveTargets(ctx, Envelope{EventKind: "backup.done"})
	if err != nil {
		t.Fatal(err)
	}
	m := ids(got)
	if !m["G1"] || m["A1"] || m["B1"] {
		t.Errorf("server event: want {G1}, got %v", m)
	}

	// User A event: server-wide G1 + A1 (owned+routed); B1 excluded by ownership guard.
	got, err = d.resolveTargets(ctx, Envelope{EventKind: "backup.done", UserID: "userA"})
	if err != nil {
		t.Fatal(err)
	}
	m = ids(got)
	if !m["G1"] || !m["A1"] {
		t.Errorf("userA event: want G1+A1, got %v", m)
	}
	if m["B1"] {
		t.Error("userA event MUST NOT include B1 (another tenant's channel) — ownership guard failed")
	}

	// User A, event with no route: server-wide only.
	got, err = d.resolveTargets(ctx, Envelope{EventKind: "cert.renewed", UserID: "userA"})
	if err != nil {
		t.Fatal(err)
	}
	m = ids(got)
	if !m["G1"] || m["A1"] || m["B1"] {
		t.Errorf("userA unrouted event: want {G1}, got %v", m)
	}
}

// fakeSettings is a minimal ServerSettingsRepository for gate tests.
type fakeSettings struct {
	st  *models.ServerSettings
	err error
}

func (f *fakeSettings) Get(context.Context) (*models.ServerSettings, error) { return f.st, f.err }
func (f *fakeSettings) Upsert(context.Context, *models.ServerSettings) error { return nil }
func (f *fakeSettings) EnsureVAPID(context.Context, string) (bool, error)    { return false, nil }
func (f *fakeSettings) SetDigestLastSent(context.Context, string) error      { return nil }

// JAB-171 phase 4e: the master gate + kind allowlist are a LIVE delivery kill
// switch, not just a create-time gate.
func TestResolveTargets_TenantGate(t *testing.T) {
	g1 := models.NotificationChannel{ID: "G1", Enabled: true, UserID: nil}
	a1 := models.NotificationChannel{ID: "A1", Kind: "ntfy", Enabled: true, UserID: ptr("userA")}
	newFC := func() *fakeChannels {
		return &fakeChannels{
			byID:    map[string]*models.NotificationChannel{"G1": &g1, "A1": &a1},
			enabled: []models.NotificationChannel{g1, a1},
		}
	}
	fr := &fakeUserRoutes{routes: map[string]map[string][]string{"userA": {"backup.done": {"A1"}}}}
	env := Envelope{EventKind: "backup.done", UserID: "userA"}
	ctx := context.Background()

	// Gate OFF → tenant channel A1 excluded (only server-wide G1 delivered).
	dOff := &Dispatcher{channels: newFC(), userRoutes: fr,
		serverSettings: &fakeSettings{st: &models.ServerSettings{TenantNotificationsEnabled: false}}}
	got, err := dOff.resolveTargets(ctx, env)
	require.NoError(t, err)
	m := ids(got)
	require.True(t, m["G1"])
	require.False(t, m["A1"], "gate OFF must exclude tenant channels from delivery")

	// Settings read error → fail CLOSED (tenant excluded).
	dErr := &Dispatcher{channels: newFC(), userRoutes: fr,
		serverSettings: &fakeSettings{err: context.DeadlineExceeded}}
	got, _ = dErr.resolveTargets(ctx, env)
	require.False(t, ids(got)["A1"], "settings read error must fail closed")

	// Gate ON, allowlist excludes ntfy → A1 excluded (live kind revocation).
	dRevoked := &Dispatcher{channels: newFC(), userRoutes: fr,
		serverSettings: &fakeSettings{st: &models.ServerSettings{
			TenantNotificationsEnabled: true,
			TenantNotificationKinds:    models.TenantNotificationKinds{"telegram"},
		}}}
	got, _ = dRevoked.resolveTargets(ctx, env)
	require.False(t, ids(got)["A1"], "a kind removed from the allowlist must stop delivering")

	// Gate ON, allowlist includes ntfy → A1 delivered.
	dOn := &Dispatcher{channels: newFC(), userRoutes: fr,
		serverSettings: &fakeSettings{st: &models.ServerSettings{
			TenantNotificationsEnabled: true,
			TenantNotificationKinds:    models.TenantNotificationKinds{"ntfy"},
		}}}
	got, err = dOn.resolveTargets(ctx, env)
	require.NoError(t, err)
	require.True(t, ids(got)["A1"], "gate ON + kind allowed must deliver")
}

// JAB-171 phase 3: the dispatcher opens a sealed secret just before the sender
// reads it — the sender must see plaintext, never the enc:1: envelope.
func TestSendOne_OpensSealedSecret(t *testing.T) {
	var key ssokey.Key
	for i := range key {
		key[i] = byte(i + 7)
	}
	var gotToken string
	fs := &fakeSender{kind: "telegram", hookErr: func(ch models.NotificationChannel, env Envelope) error {
		gotToken = ch.Config.BotToken
		return nil
	}}
	d, _, _, _, _ := newFixture(t, fs)
	d.WithSecretKey(&key)

	cfg := models.NotificationChannelConfig{BotToken: "123:PLAINTEXT-TOKEN"}
	if err := notifsecret.Seal(&cfg, key); err != nil {
		t.Fatal(err)
	}
	if gotToken := cfg.BotToken; gotToken == "123:PLAINTEXT-TOKEN" {
		t.Fatal("precondition: config should be sealed")
	}
	ch := models.NotificationChannel{ID: "c1", Kind: "telegram", Enabled: true, Config: cfg}

	if err := d.sendOne(context.Background(), ch, Envelope{EventKind: "x", StreamID: "s1"}); err != nil {
		t.Fatalf("sendOne: %v", err)
	}
	if gotToken != "123:PLAINTEXT-TOKEN" {
		t.Errorf("sender saw %q, want the opened plaintext token", gotToken)
	}
}
