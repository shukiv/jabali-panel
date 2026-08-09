package reconciler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
)

// dnsZoneDispatchState is what each zone's dnsZoneDispatchCache entry holds.
// Hash covers the compiled record set plus the AXFR/NOTIFY lists — i.e.
// everything the agent actually acts on. At is when we last pushed.
type dnsZoneDispatchState struct {
	Hash string
	At   time.Time
}

// dnsZoneReDispatchInterval forces a push even when the hash matches, so
// out-of-band drift in PowerDNS (operator editing the SQL backend directly,
// a restored/rebuilt pdns database, a push that partially applied) is still
// corrected on a bounded schedule. Same self-healing contract as
// sshKeysReDispatchInterval — DB stays the source of truth; we just stop
// asserting it sixty times an hour per zone.
const dnsZoneReDispatchInterval = 10 * time.Minute

// desiredDNSZoneHash computes a stable hash of what a zone push would do.
//
// The SOA serial is deliberately EXCLUDED. The serial was previously stamped
// with time.Now() on every pass, which made the payload differ every tick by
// construction — so no content compare could ever match and the zone was
// rewritten forever. Content identity is what should drive a push; the serial
// is an artefact of pushing, not an input to the decision.
func desiredDNSZoneHash(records []dnscompile.Record, allowAXFR, alsoNotify []string) string {
	lines := make([]string, 0, len(records))
	for _, rec := range records {
		if strings.EqualFold(rec.Type, "SOA") {
			// The SOA rdata embeds the serial; comparing it would
			// reintroduce the never-converges bug.
			continue
		}
		// Every field the agent writes must be in the hash, or a change
		// that only touches one of them (an MX priority edit, disabling a
		// row) would be skipped as "unchanged".
		lines = append(lines, fmt.Sprintf("%s|%s|%d|%d|%t|%s",
			rec.Name, rec.Type, rec.TTL, rec.Priority, rec.Disabled, rec.Content))
	}
	// Order in the DB must not affect the decision.
	sort.Strings(lines)

	axfr := append([]string(nil), allowAXFR...)
	notify := append([]string(nil), alsoNotify...)
	sort.Strings(axfr)
	sort.Strings(notify)

	h := sha256.New()
	// Counts are encoded separately so an empty set can't collide with a
	// differently-shaped one.
	fmt.Fprintf(h, "records:%d\n", len(lines))
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	fmt.Fprintf(h, "axfr:%d\n%s\n", len(axfr), strings.Join(axfr, ","))
	fmt.Fprintf(h, "notify:%d\n%s\n", len(notify), strings.Join(notify, ","))
	return hex.EncodeToString(h.Sum(nil))
}

// dnsZonePushNeeded reports whether the zone should be pushed this tick, and
// is the single place that decides it. Returns true when the compiled content
// changed, when this process has not pushed the zone yet (e.g. after a panel
// restart), or when the self-heal interval has elapsed.
func (r *Reconciler) dnsZonePushNeeded(zoneID, hash string, now time.Time) bool {
	v, ok := r.dnsZoneDispatchCache.Load(zoneID)
	if !ok {
		return true
	}
	st, okT := v.(dnsZoneDispatchState)
	if !okT {
		return true
	}
	if st.Hash != hash {
		return true
	}
	return now.Sub(st.At) >= dnsZoneReDispatchInterval
}

// dnsZonePushed records a successful push so the next tick can short-circuit.
func (r *Reconciler) dnsZonePushed(zoneID, hash string, now time.Time) {
	r.dnsZoneDispatchCache.Store(zoneID, dnsZoneDispatchState{Hash: hash, At: now})
}
