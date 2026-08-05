package reconciler

// GH #840: Zimbra/Logwatch-style daily digest email. The ticker checks
// every 15 minutes whether today's (UTC) digest is due — due means the
// clock has passed 06:00 UTC and server_settings.digest_last_sent_date is
// an earlier day — aggregates the previous 24h via DigestStatsRepository,
// and publishes ONE digest.daily envelope. Delivery is entirely governed
// by the notification pipeline: the kind defaults OFF, so nothing is sent
// until an admin flips "Daily digest" on in notification settings, and it
// then flows through whichever channels they configured.
//
// The last-sent day is persisted (targeted single-column UPDATE) BEFORE
// publishing, so a crash between the two costs one digest instead of
// sending twice — the digest is a convenience summary, dupes annoy more
// than a gap hurts. JAB-203 note: TestServeWiresDigestTicker pins the
// serve.go call site so this can't silently never run.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// digestSendHourUTC is when the digest becomes due each day. 06:00 UTC
// lands in the morning across Europe/Africa/Americas-East where the
// current fleet lives; configurable later if anyone asks.
const digestSendHourUTC = 6

// digestPublisher is the Publish subset of *notifications.Queue.
type digestPublisher interface {
	Publish(ctx context.Context, env notifications.Envelope) (string, error)
}

type DigestTickerDeps struct {
	Stats    repository.DigestStatsRepository
	Settings repository.ServerSettingsRepository
	Queue    digestPublisher
	Log      bwTickerLogger
	// now is injectable for tests; nil means time.Now.
	now func() time.Time
}

// StartDigestTicker runs until ctx is cancelled. Call in its own goroutine.
func StartDigestTicker(ctx context.Context, deps DigestTickerDeps) {
	if deps.Stats == nil || deps.Settings == nil || deps.Queue == nil || deps.Log == nil {
		return
	}
	const interval = 15 * time.Minute
	deps.Log.Info("digest_ticker starting", "interval", interval.String(), "send_hour_utc", digestSendHourUTC)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			digestTick(ctx, deps)
		}
	}
}

// digestTick sends today's digest if it is due. Split out for tests.
func digestTick(ctx context.Context, deps DigestTickerDeps) {
	nowFn := deps.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()
	if now.Hour() < digestSendHourUTC {
		return
	}
	today := now.Format("2006-01-02")

	srv, err := deps.Settings.Get(ctx)
	if err != nil || srv == nil {
		return
	}
	if srv.DigestLastSentDate == today {
		return
	}

	stats, err := deps.Stats.Collect(ctx, now.Add(-24*time.Hour))
	if err != nil {
		deps.Log.Warn("digest: stats collect failed", "err", err)
		return
	}

	// Persist-then-publish: a crash in between skips one digest rather
	// than double-sending (see package comment).
	if err := deps.Settings.SetDigestLastSent(ctx, today); err != nil {
		deps.Log.Warn("digest: persist last-sent failed", "err", err)
		return
	}

	title, body := renderDigest(now, stats)
	if _, err := deps.Queue.Publish(ctx, notifications.Envelope{
		EventKind: "digest.daily",
		Severity:  "info",
		Title:     title,
		Body:      body,
		Deeplink:  "/jabali-admin/notifications",
	}); err != nil {
		deps.Log.Warn("digest: publish failed", "err", err)
		return
	}
	deps.Log.Info("digest: published", "day", today)
}

// renderDigest builds the plain-text digest. Deliberately plain — every
// channel sender (mail, telegram, webhook...) can carry it verbatim.
func renderDigest(now time.Time, s *repository.DigestStats) (title, body string) {
	sev := func(k string) int64 { return s.AlertsBySeverity[k] }
	alerts := sev("critical") + sev("error") + sev("warning") + sev("info")
	backups := int64(0)
	for _, n := range s.BackupsByStatus {
		backups += n
	}
	title = fmt.Sprintf("Daily digest — %d alert%s, %d backup%s, %d cert%s expiring soon",
		alerts, plural(alerts), backups, plural(backups), s.CertsExpiring14d, plural(s.CertsExpiring14d))

	var b strings.Builder
	fmt.Fprintf(&b, "Summary for the 24h ending %s UTC.\n\n", now.Format("2006-01-02 15:04"))

	fmt.Fprintf(&b, "Alerts: %d critical, %d error, %d warning, %d info\n",
		sev("critical"), sev("error"), sev("warning"), sev("info"))
	if len(s.TopKinds) > 0 {
		b.WriteString("Busiest events:\n")
		for _, k := range s.TopKinds {
			fmt.Fprintf(&b, "  - %s: %d\n", k.EventKind, k.Count)
		}
	}

	if backups == 0 {
		b.WriteString("\nBackups: none ran\n")
	} else {
		keys := make([]string, 0, len(s.BackupsByStatus))
		for k := range s.BackupsByStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%d %s", s.BackupsByStatus[k], k))
		}
		fmt.Fprintf(&b, "\nBackups: %s\n", strings.Join(parts, ", "))
	}

	fmt.Fprintf(&b, "Certificates expiring within 14 days: %d\n", s.CertsExpiring14d)

	// Mail volume (GH #840): omit entirely on a mail-less install rather
	// than report a misleading zero.
	if s.MailStatsAvailable {
		fmt.Fprintf(&b, "\nMail (24h): %d received, %d sent; %d queued now\n",
			s.MailReceived, s.MailSent, s.MailQueueNow)
	}

	fmt.Fprintf(&b, "\nFleet: %d domain%s (%d enabled), %d user%s\n",
		s.DomainsTotal, plural(s.DomainsTotal), s.DomainsEnabled, s.UsersTotal, plural(s.UsersTotal))
	return title, b.String()
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}
