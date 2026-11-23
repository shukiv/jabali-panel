package reconciler

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// convergeServerAddrRecords keeps a zone's records that name the server
// itself on the server's current public addresses: the `mail` A/AAAA (the MX
// target — the mail server listens on the server primary) and the apex SPF
// that lists the server's ip4/ip6. Both are written from
// server_settings.public_ipv4/public_ipv6 when the zone is made
// (BootstrapRecords, restoreBootstrapApex), and before this nothing moved
// them when those addresses changed, so after an IP swap every zone kept
// pointing its mail and its SPF at the old address. convergeApexAddrRecords
// covers only the apex A/AAAA. The in-zone nameserver A rows (`ns1`, `ns2`
// when server_settings.ns1_name/ns2_name live inside the zone) follow
// ns1_ipv4/ns2_ipv4 the same way: the reconciler only ever backfilled them
// when missing, so an IP swap left the glue on the old address.
//
// Guard rails, the same as migrateBootstrapShape's:
//
//   - Only panel-owned bootstrap rows move (Managed=true, ManagedBy=nil). An
//     operator's edit (Managed=false), a template seed (Managed=false) and a
//     feature's rows (ManagedBy set) stay as they are.
//   - The SPF moves only while it is still exactly the bootstrap shape,
//     `"v=spf1 mx[ ip4:<v4>][ ip6:<v6>] ~all"`. Anything else is an edit.
//   - An address family the settings leave empty is never blanked or dropped:
//     an unset setting is not evidence the address went away. The SPF keeps
//     its own token for that family; a mail A/AAAA keeps its content.
//   - Existing rows only. A missing row is restoreBootstrapApex's to create.
//
// Idempotent: once the rows match the settings it writes nothing.
func (r *Reconciler) convergeServerAddrRecords(ctx context.Context, zone *models.DNSZone, srv *models.ServerSettings) {
	if r.dnsRecords == nil || zone == nil || srv == nil {
		return
	}
	nsWant := serverNSAddrs(zone.Name, srv)
	if srv.PublicIPv4 == "" && srv.PublicIPv6 == "" && len(nsWant) == 0 {
		return
	}
	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Error("converge server addresses: list records failed", "zone", zone.Name, "err", err)
		return
	}
	for i := range existing {
		rec := existing[i]
		if !rec.Managed || rec.ManagedBy != nil {
			continue
		}
		want := rec.Content
		switch {
		case rec.Name == "mail" && rec.Type == "A" && srv.PublicIPv4 != "":
			want = srv.PublicIPv4
		case rec.Name == "mail" && rec.Type == "AAAA" && srv.PublicIPv6 != "":
			want = srv.PublicIPv6
		case rec.Type == "A" && nsWant[rec.Name] != "":
			want = nsWant[rec.Name]
		case rec.Name == "@" && rec.Type == "TXT":
			v4, v6, ok := parseBootstrapSPF(rec.Content)
			if !ok {
				continue
			}
			if srv.PublicIPv4 != "" {
				v4 = srv.PublicIPv4
			}
			if srv.PublicIPv6 != "" {
				v6 = srv.PublicIPv6
			}
			want = dnscompile.BuildSPFString(&models.ServerSettings{PublicIPv4: v4, PublicIPv6: v6})
		default:
			continue
		}
		if want == rec.Content {
			continue
		}
		old := rec.Content
		rec.Content = want
		rec.UpdatedAt = time.Now().UTC()
		if err := r.dnsRecords.Update(ctx, &rec); err != nil {
			r.log.Error("converge server addresses: update failed",
				"zone", zone.Name, "name", rec.Name, "type", rec.Type, "err", err)
			continue
		}
		r.log.Info("record follows the server address",
			"zone", zone.Name, "name", rec.Name, "type", rec.Type, "from", old, "to", want)
	}
}

// serverNSAddrs maps the in-zone label of each configured nameserver to its
// IPv4 — the rows BootstrapRecords seeds (appendNSARecord): ns1_name
// "ns1.example.com" in zone "example.com" is label "ns1". A nameserver outside
// the zone, or one without an address, is left out.
func serverNSAddrs(zoneName string, srv *models.ServerSettings) map[string]string {
	out := map[string]string{}
	if zoneName == "" {
		return out
	}
	for _, ns := range [][2]string{{srv.NS1Name, srv.NS1IPv4}, {srv.NS2Name, srv.NS2IPv4}} {
		label := strings.TrimSuffix(ns[0], "."+zoneName)
		if ns[1] == "" || label == "" || label == ns[0] {
			continue
		}
		out[label] = ns[1]
	}
	return out
}

// parseBootstrapSPF reports whether content is exactly the SPF shape
// BuildSPFString writes — `"v=spf1 mx[ ip4:<v4>][ ip6:<v6>] ~all"`, ip4
// before ip6, each at most once — and returns the two addresses (empty when
// absent).
func parseBootstrapSPF(content string) (v4, v6 string, ok bool) {
	const prefix, suffix = `"v=spf1 mx`, ` ~all"`
	if !strings.HasPrefix(content, prefix) || !strings.HasSuffix(content, suffix) || len(content) < len(prefix)+len(suffix) {
		return "", "", false
	}
	mid := content[len(prefix) : len(content)-len(suffix)]
	if mid == "" {
		return "", "", true
	}
	if !strings.HasPrefix(mid, " ") {
		return "", "", false
	}
	tokens := strings.Split(mid[1:], " ")
	if len(tokens) > 2 {
		return "", "", false
	}
	for _, tok := range tokens {
		switch {
		case strings.HasPrefix(tok, "ip4:") && v4 == "" && v6 == "":
			a, err := netip.ParseAddr(tok[len("ip4:"):])
			if err != nil || !a.Is4() || a.String() != tok[len("ip4:"):] {
				return "", "", false
			}
			v4 = a.String()
		case strings.HasPrefix(tok, "ip6:") && v6 == "":
			a, err := netip.ParseAddr(tok[len("ip6:"):])
			if err != nil || !a.Is6() || a.Is4In6() {
				return "", "", false
			}
			v6 = tok[len("ip6:"):]
		default:
			return "", "", false
		}
	}
	return v4, v6, true
}
