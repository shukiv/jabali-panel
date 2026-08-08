package reconciler

import (
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
)

func recs() []dnscompile.Record {
	return []dnscompile.Record{
		{Name: "example.com", Type: "A", Content: "203.0.113.10", TTL: 300},
		{Name: "www.example.com", Type: "CNAME", Content: "example.com", TTL: 300},
		{Name: "example.com", Type: "MX", Content: "mail.example.com", TTL: 300, Priority: 10},
	}
}

// The SOA serial must NOT feed the hash. It used to be stamped with
// time.Now() on every pass, so including it would make the payload differ
// every tick by construction — which is exactly why the zone could never
// converge and was rewritten in PowerDNS sixty times an hour.
func TestDesiredDNSZoneHashIgnoresSOASerial(t *testing.T) {
	base := recs()
	withSOA1 := append(base, dnscompile.Record{
		Name: "example.com", Type: "SOA", TTL: 300,
		Content: "ns1.example.com. hostmaster.example.com. 1000 10800 3600 604800 3600",
	})
	withSOA2 := append(base, dnscompile.Record{
		Name: "example.com", Type: "SOA", TTL: 300,
		Content: "ns1.example.com. hostmaster.example.com. 2000 10800 3600 604800 3600",
	})
	if desiredDNSZoneHash(withSOA1, nil, nil) != desiredDNSZoneHash(withSOA2, nil, nil) {
		t.Fatal("a changed SOA serial must not change the hash — including it " +
			"reintroduces the never-converges bug")
	}
}

// Record order in the DB must not cause a spurious push.
func TestDesiredDNSZoneHashOrderIndependent(t *testing.T) {
	a := recs()
	b := []dnscompile.Record{a[2], a[0], a[1]}
	if desiredDNSZoneHash(a, nil, nil) != desiredDNSZoneHash(b, nil, nil) {
		t.Fatal("hash must not depend on record order")
	}
}

// Every field the agent writes must be covered, or a change touching only
// that field would be skipped as "unchanged" and never reach PowerDNS.
func TestDesiredDNSZoneHashCoversEveryWrittenField(t *testing.T) {
	baseline := desiredDNSZoneHash(recs(), nil, nil)

	for name, mutate := range map[string]func([]dnscompile.Record){
		"content":  func(r []dnscompile.Record) { r[0].Content = "203.0.113.99" },
		"ttl":      func(r []dnscompile.Record) { r[0].TTL = 60 },
		"priority": func(r []dnscompile.Record) { r[2].Priority = 20 },
		"disabled": func(r []dnscompile.Record) { r[0].Disabled = true },
		"name":     func(r []dnscompile.Record) { r[0].Name = "other.example.com" },
		"type":     func(r []dnscompile.Record) { r[0].Type = "AAAA" },
	} {
		t.Run(name, func(t *testing.T) {
			m := recs()
			mutate(m)
			if desiredDNSZoneHash(m, nil, nil) == baseline {
				t.Errorf("changing %s did not change the hash — that change would "+
					"never be pushed to PowerDNS", name)
			}
		})
	}

	// AXFR / NOTIFY lists are part of the agent payload too.
	if desiredDNSZoneHash(recs(), []string{"198.51.100.2"}, nil) == baseline {
		t.Error("allow_axfr_from must affect the hash")
	}
	if desiredDNSZoneHash(recs(), nil, []string{"198.51.100.2"}) == baseline {
		t.Error("also_notify must affect the hash")
	}
}

func TestDNSZonePushGate(t *testing.T) {
	r := &Reconciler{}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	hash := desiredDNSZoneHash(recs(), nil, nil)

	// Never pushed in this process → must push (covers panel restart).
	if !r.dnsZonePushNeeded("zone-1", hash, now) {
		t.Fatal("first sight of a zone must push")
	}
	r.dnsZonePushed("zone-1", hash, now)

	// Unchanged content inside the self-heal window → skip.
	if r.dnsZonePushNeeded("zone-1", hash, now.Add(time.Minute)) {
		t.Error("unchanged content must not be re-pushed every tick")
	}

	// Changed content → push immediately, regardless of the window.
	changed := recs()
	changed[0].Content = "203.0.113.99"
	if !r.dnsZonePushNeeded("zone-1", desiredDNSZoneHash(changed, nil, nil), now.Add(time.Minute)) {
		t.Error("changed content must push immediately")
	}

	// Past the self-heal interval → push even though nothing changed, so
	// out-of-band PowerDNS drift is still corrected.
	if !r.dnsZonePushNeeded("zone-1", hash, now.Add(dnsZoneReDispatchInterval+time.Second)) {
		t.Error("self-heal interval must force a periodic re-push")
	}

	// A different zone is tracked independently.
	if !r.dnsZonePushNeeded("zone-2", hash, now.Add(time.Minute)) {
		t.Error("a different zone must not inherit another zone's cache entry")
	}
}
