package reconciler

import (
	"context"
	"time"
	"unicode/utf8"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// maxPushErrorBytes caps the agent error kept on a zone row (GH #2377). The
// TEXT column holds far more; the cap keeps a runaway agent message out of
// every DNS Zones page load.
const maxPushErrorBytes = 1000

// recordDNSZonePush stores the outcome of one push to PowerDNS on the zone row
// (GH #2377), so the DNS Zones page can tell a zone the DNS server holds from
// one that exists only in the panel database. Success: pushedAt is set and
// pushErr nil. Failure: pushedAt is nil and pushErr says why.
//
// A failure is written only when its text differs from the one already on the
// row: a push that keeps failing is retried every pass, and rewriting the row
// each time would add one write per zone per tick for nothing. A failed write
// is logged and otherwise ignored; it never changes what the push did.
func (r *Reconciler) recordDNSZonePush(ctx context.Context, zone *models.DNSZone, pushedAt *time.Time, pushErr error) {
	msg := ""
	if pushErr != nil {
		msg = cutPushError(pushErr.Error())
		if msg == "" {
			msg = "the DNS server did not accept the zone"
		}
		if zone.LastPushError != nil && *zone.LastPushError == msg {
			return
		}
	}
	if err := r.dnsZones.SetPushResult(ctx, zone.ID, pushedAt, msg); err != nil {
		r.log.Warn("record dns.zone.upsert result failed", "zone", zone.Name, "err", err)
	}
}

// cutPushError shortens s to at most maxPushErrorBytes, ending in an ellipsis,
// without splitting a UTF-8 sequence.
func cutPushError(s string) string {
	if len(s) <= maxPushErrorBytes {
		return s
	}
	const ellipsis = "…"
	cut := maxPushErrorBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
