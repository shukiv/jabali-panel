package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Config tunes the dispatcher's loop behaviours. Zero values pick
// sensible production defaults; tests poke smaller values to finish
// quickly.
type Config struct {
	BatchSize           int           // XREADGROUP COUNT. Default 16.
	ReadBlock           time.Duration // XREADGROUP BLOCK. Default 5s. Tests poke smaller.
	ReclaimInterval     time.Duration // How often the reclaim loop runs. Default 30s.
	ReclaimMinIdle      time.Duration // Entry must be idle this long to reclaim. Default 60s.
	MaxRetries          int           // History rows past this go to DLQ. Default 5.
	CircuitBreakerLimit int           // Consecutive failures before auto-disable. Default 3.
	ShutdownGrace       time.Duration // Max wait for in-flight entry on stop. Default 10s.
	// MaxConcurrentSenders bounds parallel outbound HTTP calls per
	// envelope (Step 8). Caps the fanout so a broadcast to 10 channels
	// doesn't burst 10 goroutines against 10 third parties at once.
	// Default 4.
	MaxConcurrentSenders int
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 16
	}
	if c.ReadBlock <= 0 {
		c.ReadBlock = 5 * time.Second
	}
	if c.ReclaimInterval <= 0 {
		c.ReclaimInterval = 30 * time.Second
	}
	if c.ReclaimMinIdle <= 0 {
		c.ReclaimMinIdle = 60 * time.Second
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = defaultRetries
	}
	if c.CircuitBreakerLimit <= 0 {
		c.CircuitBreakerLimit = 3
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = 10 * time.Second
	}
	if c.MaxConcurrentSenders <= 0 {
		c.MaxConcurrentSenders = 4
	}
	return c
}

// Dispatcher drives the XREADGROUP loop + reclaim loop. Construct via
// NewDispatcher; Start runs the loops in background goroutines until
// the context passed to Start is cancelled.
type Dispatcher struct {
	cfg         Config
	queue       *Queue
	registry    *Registry
	channels    repository.NotificationChannelRepository
	history     repository.NotificationHistoryRepository
	webhookRepo repository.WebhookEndpointRepository
	// eventSettings gates per-kind enable. Optional — when nil, every
	// envelope passes (matches pre-M14.1 behaviour for tests + first
	// boot before the table is seeded).
	eventSettings repository.NotificationEventSettingRepository
	// users powers the broadcast → admin-bell fan-out. Optional —
	// when nil, broadcast envelopes (env.UserID == "") get no bell rows.
	// Non-broadcast envelopes always get a bell row regardless.
	users repository.UserRepository
	// userRoutes powers per-user event routing (JAB-171). Optional — when
	// nil, user-scoped envelopes fan to server-wide channels only (the
	// pre-JAB-171 behaviour). When set, a user-scoped envelope also reaches
	// the recipient's OWN channels that they routed this event kind to.
	userRoutes repository.UserNotificationRouteRepository
	log        *slog.Logger
	consumer   string

	wg      sync.WaitGroup
	stopped chan struct{}
}

// WithEventSettings injects the per-event-kind toggle repo so the
// consumer can ack+drop disabled kinds before fanout.
func (d *Dispatcher) WithEventSettings(r repository.NotificationEventSettingRepository) *Dispatcher {
	d.eventSettings = r
	return d
}

// WithUsers injects the user repo so the dispatcher can fan out
// broadcast envelopes (env.UserID == "") to every admin's in-app bell.
// Without it, broadcast envelopes still go to channels but won't
// surface in any admin's bell.
func (d *Dispatcher) WithUsers(r repository.UserRepository) *Dispatcher {
	d.users = r
	return d
}

// WithUserRoutes injects the per-user routing repo (JAB-171). With it, a
// user-scoped envelope (env.UserID set) additionally fans out to the
// recipient's own channels routed for that event kind. Without it, behaviour
// is unchanged (server-wide channels only).
func (d *Dispatcher) WithUserRoutes(r repository.UserNotificationRouteRepository) *Dispatcher {
	d.userRoutes = r
	return d
}

// NewDispatcher wires a dispatcher. All collaborators are required —
// nil passes turn into runtime panics on first event, so fail at
// construction time instead.
func NewDispatcher(
	q *Queue,
	reg *Registry,
	channels repository.NotificationChannelRepository,
	history repository.NotificationHistoryRepository,
	webhookRepo repository.WebhookEndpointRepository,
	log *slog.Logger,
	cfg Config,
) (*Dispatcher, error) {
	if q == nil || reg == nil || channels == nil || history == nil || webhookRepo == nil {
		return nil, errors.New("dispatcher: all collaborators required (queue/registry/channels/history/webhookRepo)")
	}
	if log == nil {
		log = slog.Default()
	}
	if len(reg.Kinds()) == 0 {
		return nil, errors.New("dispatcher: empty sender registry; refuse to start with no way to deliver")
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "panel"
	}
	// Stable across restarts — NO pid. A pid-suffixed name stranded this
	// process's in-flight PEL entries on every restart, so frequent restarts
	// (deploys, crash-loops) piled up hundreds of dead consumers whose pending
	// entries churned through reclaim straight into the DLQ. A stable name lets
	// a restarted dispatcher reclaim its own prior PEL instead of orphaning it.
	consumer := fmt.Sprintf("panel-api-%s", host)
	return &Dispatcher{
		cfg:         cfg.withDefaults(),
		queue:       q,
		registry:    reg,
		channels:    channels,
		history:     history,
		webhookRepo: webhookRepo,
		log:         log,
		consumer:    consumer,
		stopped:     make(chan struct{}),
	}, nil
}

// Start runs the dispatch + reclaim goroutines. Blocks until ctx is
// cancelled, then waits up to cfg.ShutdownGrace for in-flight work to
// drain. Callers typically do:
//
//	go func() { _ = dispatcher.Start(ctx) }()
//
// and cancel ctx on SIGTERM from the outer runServe.
func (d *Dispatcher) Start(ctx context.Context) error {
	if err := d.queue.EnsureGroup(ctx); err != nil {
		return fmt.Errorf("ensure consumer group: %w", err)
	}
	d.log.Info("notifications dispatcher starting",
		"consumer", d.consumer,
		"kinds", strings.Join(d.registry.Kinds(), ","))
	d.wg.Add(2)
	go func() { defer d.wg.Done(); d.runConsumer(ctx) }()
	go func() { defer d.wg.Done(); d.runReclaim(ctx) }()

	<-ctx.Done()
	d.log.Info("notifications dispatcher stopping; draining in-flight", "grace", d.cfg.ShutdownGrace)
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d.cfg.ShutdownGrace):
		d.log.Warn("notifications dispatcher drain timeout; some entries may redeliver on next start")
	}
	close(d.stopped)
	return nil
}

// Stopped returns a channel that closes after Start returns. Useful
// for tests that want to assert clean shutdown.
func (d *Dispatcher) Stopped() <-chan struct{} { return d.stopped }

// runConsumer reads new + pending entries and processes each.
func (d *Dispatcher) runConsumer(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		streams, err := d.queue.Read(ctx, d.consumer, d.cfg.BatchSize, d.cfg.ReadBlock)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			d.log.Error("xreadgroup failed", "err", err)
			// Short sleep so we don't spin on a Redis outage. The
			// systemd Requires= keeps us co-terminus with Redis
			// anyway, but defensive.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, s := range streams {
			for _, msg := range s.Messages {
				d.process(ctx, msg)
			}
		}
	}
}

func (d *Dispatcher) runReclaim(ctx context.Context) {
	t := time.NewTicker(d.cfg.ReclaimInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.reclaim(ctx)
		}
	}
}

func (d *Dispatcher) reclaim(ctx context.Context) {
	pending, err := d.queue.Pending(ctx, d.cfg.ReclaimMinIdle, 100)
	if err != nil {
		d.log.Error("xpending failed", "err", err)
		return
	}
	for _, p := range pending {
		if int(p.RetryCount) >= d.cfg.MaxRetries {
			// Retry budget exhausted. Move to DLQ — fetch the original
			// payload first so the DLQ inspector can replay/inspect it.
			// Without this the DLQ row is just {reason, orig_id} and
			// Replay has nothing to re-publish (silent corruption).
			var values map[string]any
			if msgs, mErr := d.queue.RangeOne(ctx, p.ID); mErr == nil && len(msgs) > 0 {
				values = msgs[0].Values
			}
			if err := d.queue.ToDLQ(ctx, p.ID, "max_retries_exceeded", values); err != nil {
				d.log.Error("move to dlq failed", "id", p.ID, "err", err)
			} else {
				d.log.Warn("dispatched to DLQ", "id", p.ID, "owner", p.Consumer, "retries", p.RetryCount)
			}
			continue
		}
		// Re-claim to ourselves. On success the entry lands in our
		// next XREADGROUP cycle.
		if _, err := d.queue.Claim(ctx, d.consumer, d.cfg.ReclaimMinIdle, p.ID); err != nil {
			d.log.Error("xclaim failed", "id", p.ID, "err", err)
		} else {
			d.log.Info("reclaimed stale entry", "id", p.ID, "owner", p.Consumer, "retries", p.RetryCount)
		}
	}
}

// process handles one stream message end-to-end: parse, resolve target
// channels, fan out to senders, write history rows, finalise.
func (d *Dispatcher) process(ctx context.Context, msg redis.XMessage) {
	env, err := envelopeFromStream(msg.Values)
	if err != nil {
		// Malformed — straight to DLQ. No history row to write (we
		// don't know which event it was).
		d.log.Error("envelope parse failed; routing to DLQ", "id", msg.ID, "err", err)
		if dlqErr := d.queue.ToDLQ(ctx, msg.ID, "parse_error: "+err.Error(), msg.Values); dlqErr != nil {
			d.log.Error("move to dlq failed", "id", msg.ID, "err", dlqErr)
		}
		return
	}
	// Stamp the stream entry id onto the in-memory envelope so every
	// downstream history row can be correlated back to the queue entry
	// (and, transitively, to a DLQ row keyed by orig_id).
	env.StreamID = msg.ID

	// Operator toggle: per-event-kind enable. Disabled kinds get
	// ack+delete with no fanout and no inbox/history row. Repo cache
	// has 30s TTL so flipping a switch propagates within that window.
	if d.eventSettings != nil {
		enabled, evtErr := d.eventSettings.IsEnabled(ctx, env.EventKind)
		if evtErr != nil {
			d.log.Warn("event settings lookup failed; allowing through", "event", env.EventKind, "err", evtErr)
		} else if !enabled {
			d.log.Debug("event kind disabled; dropping", "event", env.EventKind, "id", msg.ID)
			if ackErr := d.queue.AckAndDelete(ctx, msg.ID); ackErr != nil {
				d.log.Error("ack failed on disabled-event drop", "id", msg.ID, "err", ackErr)
			}
			return
		}
	}

	// In-app bell: write a channel_id=NULL history row before fanout
	// so events appear in the inbox even when zero channels are
	// configured. Targeted envelopes (env.UserID set) get one row;
	// broadcast envelopes (UserID empty) get one row per admin so
	// every operator's bell lights up. Errors are logged — the bell
	// shouldn't block delivery to real channels.
	d.writeBellRows(ctx, env)

	targets, err := d.resolveTargets(ctx, env)
	if err != nil {
		d.log.Error("resolve targets failed", "id", msg.ID, "err", err)
		// Leave in PEL — reclaim loop will retry (db error is
		// treated as transient).
		return
	}
	if len(targets) == 0 {
		// No enabled channel matched. The bell row above is the
		// only delivery; ACK + delete so the queue doesn't grow.
		// Bell delivery is the silent default. Demoted to Debug —
		// operator hasn't configured any email/Slack/ntfy channels.
		// Used to fire INFO per event = log noise on a fresh install
		// where every event hits this path.
		d.log.Debug("no target channels for event; bell only", "event", env.EventKind)
		if err := d.queue.AckAndDelete(ctx, msg.ID); err != nil {
			d.log.Error("ack failed on no-target path", "id", msg.ID, "err", err)
		}
		return
	}

	// Bounded-parallel fanout (Step 8). Cap at cfg.MaxConcurrentSenders
	// so a broadcast to 10 channels doesn't open 10 simultaneous
	// outbound TLS handshakes. Semaphore = buffered channel; each
	// goroutine claims a slot before calling sendOne.
	sem := make(chan struct{}, d.cfg.MaxConcurrentSenders)
	var mu sync.Mutex
	var wg sync.WaitGroup
	anyFailed := false
	for _, ch := range targets {
		ch := ch
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := d.sendOne(ctx, ch, env); err != nil {
				mu.Lock()
				anyFailed = true
				mu.Unlock()
				d.log.Warn("send failed", "channel_id", ch.ID, "kind", ch.Kind, "err", err)
			}
		}()
	}
	wg.Wait()
	// All-succeed → ACK. Any-fail: leave in PEL for reclaim. Permanent
	// failures already marked via history row; the reclaim path will
	// eventually push them to DLQ if they stay stuck.
	if !anyFailed {
		if err := d.queue.AckAndDelete(ctx, msg.ID); err != nil {
			d.log.Error("ack failed after successful fanout", "id", msg.ID, "err", err)
		}
	}
}

// resolveTargets returns the channels the envelope should fan out to.
// Explicit ChannelIDs → load each by id. Empty → every enabled channel.
func (d *Dispatcher) resolveTargets(ctx context.Context, env Envelope) ([]models.NotificationChannel, error) {
	if len(env.ChannelIDs) == 0 {
		// Base set = server-wide admin channels (user_id IS NULL). Using
		// FindEnabledServerWide (not FindEnabledAll) keeps tenant-owned
		// channels out of broadcast/server fan-out — JAB-171.
		targets, err := d.channels.FindEnabledServerWide(ctx)
		if err != nil {
			return nil, err
		}
		// Additively, a user-scoped envelope also reaches the recipient's
		// OWN channels routed for this event kind. Ownership is re-checked
		// here (channel.UserID == recipient) so a stray route can never
		// deliver another user's — or a global admin — channel.
		if env.UserID != "" && d.userRoutes != nil {
			userChans, err := d.recipientRoutedChannels(ctx, env.UserID, env.EventKind)
			if err != nil {
				return nil, err
			}
			targets = append(targets, userChans...)
		}
		return targets, nil
	}
	out := make([]models.NotificationChannel, 0, len(env.ChannelIDs))
	for _, id := range env.ChannelIDs {
		ch, err := d.channels.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				// Channel deleted between publish and consume —
				// skip, not an error.
				continue
			}
			return nil, err
		}
		if !ch.Enabled {
			continue
		}
		out = append(out, *ch)
	}
	return out, nil
}

// recipientRoutedChannels returns the recipient's OWN enabled channels that
// they routed the given event kind to. Every candidate is ownership-checked
// (channel.UserID == userID) so a route can only ever deliver a channel the
// same user owns — never a global admin channel, never another tenant's.
func (d *Dispatcher) recipientRoutedChannels(ctx context.Context, userID, eventKind string) ([]models.NotificationChannel, error) {
	routes, err := d.userRoutes.ListByUserEvent(ctx, userID, eventKind)
	if err != nil {
		return nil, err
	}
	out := make([]models.NotificationChannel, 0, len(routes))
	for _, rt := range routes {
		ch, err := d.channels.FindByID(ctx, rt.ChannelID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				continue // channel deleted since the route was written
			}
			return nil, err
		}
		if !ch.Enabled {
			continue
		}
		if ch.UserID == nil || *ch.UserID != userID {
			// Ownership guard: never deliver a global or other-user channel
			// via a user route, even if a stale/forged route points at one.
			continue
		}
		out = append(out, *ch)
	}
	return out, nil
}

// sendOne runs the sender for one channel, writes the history row,
// updates the webhook_endpoints retry state, and trips the circuit
// breaker when consecutive failures reach the limit.
func (d *Dispatcher) sendOne(ctx context.Context, ch models.NotificationChannel, env Envelope) error {
	hist := &models.NotificationHistory{
		ID:        ulid.Make().String(),
		ChannelID: strPtr(ch.ID),
		EventKind: env.EventKind,
		Severity:  env.Severity,
		Title:     env.Title,
		Body:      env.Body,
		Deeplink:  env.Deeplink,
		Outcome:   models.NotificationOutcomePending,
	}
	if env.UserID != "" {
		hist.UserID = strPtr(env.UserID)
	}
	if env.StreamID != "" {
		hist.EnvelopeID = strPtr(env.StreamID)
	}
	if err := d.history.Create(ctx, hist); err != nil {
		return fmt.Errorf("create history row: %w", err)
	}

	sender, err := d.registry.Lookup(ch.Kind)
	if err != nil {
		// Unknown kind → mark history skipped, record webhook error,
		// don't count toward circuit breaker (this is a config bug,
		// not a transport fault).
		_ = d.history.UpdateOutcome(ctx, hist.ID, models.NotificationOutcomeSkipped, err.Error(), 0)
		return err
	}

	sendErr := sender.Send(ctx, ch, env)
	if sendErr == nil {
		_ = d.history.UpdateOutcome(ctx, hist.ID, models.NotificationOutcomeSent, "", 0)
		_ = d.webhookRepo.RecordSuccess(ctx, ch.ID)
		return nil
	}

	// Failure path.
	permanent := errors.Is(sendErr, ErrPermanent)
	outcome := models.NotificationOutcomeFailed
	backoff := nextBackoff(ctx)
	_ = d.history.UpdateOutcome(ctx, hist.ID, outcome, sendErr.Error(), 1)
	_ = d.webhookRepo.RecordFailure(ctx, ch.ID, sendErr.Error(), backoff)

	// Circuit breaker — look at the updated failure count from the
	// repo and disable the channel if it crossed the threshold.
	if endpoint, lookupErr := d.webhookRepo.FindByChannelID(ctx, ch.ID); lookupErr == nil {
		if endpoint.ConsecutiveFailures >= d.cfg.CircuitBreakerLimit {
			d.tripBreaker(ctx, ch, endpoint.ConsecutiveFailures)
		}
	}
	if permanent {
		// Don't rethrow — permanent is a per-channel outcome. The
		// stream entry can still ACK when every target resolves to
		// sent-or-permanent; the caller handles that.
		return nil
	}
	return sendErr
}

// tripBreaker disables the channel + fires a critical in-app-bell
// event naming it so the admin can re-enable after fixing config.
// Errors here are logged, not returned — the calling sendOne already
// has its own error surface and the breaker is a best-effort alarm.
func (d *Dispatcher) tripBreaker(ctx context.Context, ch models.NotificationChannel, failures int) {
	ch.Enabled = false
	if err := d.channels.Update(ctx, &ch); err != nil {
		d.log.Error("circuit breaker: update channel failed", "id", ch.ID, "err", err)
		return
	}
	d.log.Warn("circuit breaker tripped: channel auto-disabled",
		"id", ch.ID, "name", ch.Name, "kind", ch.Kind, "failures", failures)
	alarm := &models.NotificationHistory{
		ID:        ulid.Make().String(),
		EventKind: "notifications.channel.auto_disabled",
		Severity:  models.NotificationSeverityCritical,
		Title:     "Notification channel auto-disabled",
		Body:      fmt.Sprintf("Channel %q (kind %s) exceeded %d consecutive failures and was disabled. Inspect config and re-enable from Admin → Channels.", ch.Name, ch.Kind, failures),
		Outcome:   models.NotificationOutcomeSent,
	}
	if err := d.history.Create(ctx, alarm); err != nil {
		d.log.Error("circuit breaker: alarm history write failed", "err", err)
	}
}

// nextBackoff returns a pointer to a "try again after N" timestamp
// for the webhook_endpoints row. 5 minutes is the production default;
// we don't yet tier backoff by retry count (consecutive_failures
// lives on the same row, so callers can compute their own cadence
// from it if needed).
func nextBackoff(_ context.Context) *time.Time {
	t := time.Now().UTC().Add(5 * time.Minute)
	return &t
}

func strPtr(s string) *string { return &s }

// writeBellRows materializes one notification_history row per target
// admin with channel_id=NULL — the in-app bell's source-of-truth row.
// Always non-fatal: bell delivery should never block channel fanout.
func (d *Dispatcher) writeBellRows(ctx context.Context, env Envelope) {
	targets := []string{}
	if env.UserID != "" {
		targets = append(targets, env.UserID)
	} else if d.users != nil {
		admins, err := d.users.FindAdminsByEmail(ctx)
		if err != nil {
			d.log.Warn("bell: list admins failed", "event", env.EventKind, "err", err)
			return
		}
		for _, a := range admins {
			targets = append(targets, a.ID)
		}
	}
	now := time.Now().UTC()
	for _, uid := range targets {
		uidCopy := uid
		row := &models.NotificationHistory{
			ID:        ulid.Make().String(),
			EventKind: env.EventKind,
			Severity:  env.Severity,
			Title:     env.Title,
			Body:      env.Body,
			Deeplink:  env.Deeplink,
			Outcome:   models.NotificationOutcomeSent,
			UserID:    &uidCopy,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if env.StreamID != "" {
			row.EnvelopeID = strPtr(env.StreamID)
		}
		if err := d.history.Create(ctx, row); err != nil {
			d.log.Warn("bell row insert failed", "event", env.EventKind, "user_id", uid, "err", err)
		}
	}
}
