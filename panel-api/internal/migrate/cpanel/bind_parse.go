package cpanel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// openZone wraps os.Open with context for symmetry with the rest
// of the migrate package's IO. Caller closes.
func openZone(path string) (*os.File, error) { return os.Open(path) }

// ParseBINDZone walks a cPanel-emitted BIND zone file and returns
// a DNSZoneSpec ready for migration.AccountManifest. Handles the
// directives + record types cPanel actually emits in cpmove tarballs:
//
//	$ORIGIN <name>.
//	$TTL <secs>
//	<name> <ttl?> IN <type> <rdata...>
//	<ttl?> IN <type> <rdata...>           (inherits previous owner)
//
// Supported types: A, AAAA, CNAME, MX, TXT, NS, SOA. Anything else
// records as Skipped at the caller level — restore-stage upserts
// only the supported types and the operator manually re-adds
// SRV/CAA/SPF-via-RR after migration if they want them.
//
// Multi-line records (parens-continued TXT, SOA blocks) are
// folded by tracking parenthesis depth across lines. Comments
// (`;`) are stripped. Quoted TXT strings preserve embedded spaces.
//
// Returns (zone, true) on success; (zero, false) when the file
// can't be parsed at all (no $ORIGIN visible, malformed top
// line). Skipped record types surface in the returned skipped
// slice so the caller can fold into the manifest's Warnings.
func ParseBINDZone(r io.Reader, defaultOrigin string) (zone migrate.DNSZoneSpec, skipped []string, ok bool) {
	zone.Origin = strings.TrimSuffix(defaultOrigin, ".")
	defaultTTL := 3600

	// Pre-process: fold multi-line records by parenthesis depth.
	// BIND's SOA + multi-string TXT both wrap with `(` ... `)`.
	folded, err := foldParens(r)
	if err != nil {
		return zone, nil, false
	}

	var lastOwner string
	for _, line := range folded {
		line = stripCommentLite(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Directives.
		if strings.HasPrefix(line, "$ORIGIN") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				zone.Origin = strings.TrimSuffix(fields[1], ".")
			}
			continue
		}
		if strings.HasPrefix(line, "$TTL") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if v, err := strconv.Atoi(fields[1]); err == nil {
					defaultTTL = v
				}
			}
			continue
		}

		owner, ttl, rrType, rdata, ok2 := splitBindLine(line, lastOwner, defaultTTL)
		if !ok2 {
			continue
		}
		lastOwner = owner

		switch rrType {
		case "A", "AAAA", "CNAME", "NS":
			zone.Records = append(zone.Records, migrate.DNSRecord{
				Name:    expandOwner(owner, zone.Origin),
				Type:    rrType,
				Content: rdata,
				TTL:     ttl,
			})
		case "MX":
			// rdata = "10 mail.example.com."
			fields := strings.Fields(rdata)
			if len(fields) < 2 {
				skipped = append(skipped, "malformed_mx:"+line)
				continue
			}
			prio, _ := strconv.Atoi(fields[0])
			zone.Records = append(zone.Records, migrate.DNSRecord{
				Name:    expandOwner(owner, zone.Origin),
				Type:    "MX",
				Content: strings.TrimSuffix(fields[1], "."),
				TTL:     ttl,
				Prio:    prio,
			})
		case "TXT":
			zone.Records = append(zone.Records, migrate.DNSRecord{
				Name: expandOwner(owner, zone.Origin),
				Type: "TXT",
				// Strip surrounding quotes from each string token
				// then join with spaces — BIND multi-string TXT
				// renders as "abc" "def" → content "abc def".
				Content: unquoteTXT(rdata),
				TTL:     ttl,
			})
		case "SRV":
			// rdata = "priority weight port target."
			fields := strings.Fields(rdata)
			if len(fields) < 4 {
				skipped = append(skipped, "malformed_srv:"+line)
				continue
			}
			// JAB-28: PowerDNS stores SRV priority in the dedicated prio column;
			// content is "weight port target" ONLY. Stuffing the priority into
			// content (with prio=0) makes pdns serve a malformed SRV that doesn't
			// resolve. Split it out like MX above.
			prio, _ := strconv.Atoi(fields[0])
			target := strings.TrimSuffix(fields[3], ".")
			content := fields[1] + " " + fields[2] + " " + target
			zone.Records = append(zone.Records, migrate.DNSRecord{
				Name:    expandOwner(owner, zone.Origin),
				Type:    "SRV",
				Content: content,
				TTL:     ttl,
				Prio:    prio,
			})
		case "CAA":
			// GH #327: PowerDNS + the panel DNS API both support CAA — stop
			// dropping it on import. BIND + pdns share the same content form
			// (`<flags> <tag> "<value>"`, e.g. `0 issue "letsencrypt.org"`), so
			// the rdata carries over verbatim; no prio column.
			zone.Records = append(zone.Records, migrate.DNSRecord{
				Name:    expandOwner(owner, zone.Origin),
				Type:    "CAA",
				Content: rdata,
				TTL:     ttl,
			})
		case "SOA":
			// cPanel emits SOA with the cPanel server's NS as primary.
			// We skip — pdns generates its own SOA on zone create
			// (M15 DNSSEC pattern). Recording as Skipped is
			// intentional, not a parse failure.
			skipped = append(skipped, "soa_handled_by_pdns")
		default:
			skipped = append(skipped, "unsupported_rr_type:"+rrType+":"+line)
		}
	}

	if zone.Origin == "" {
		return zone, skipped, false
	}
	return zone, skipped, true
}

// foldParens reads `r` line-by-line + concatenates lines that fall
// inside an unclosed `(`. Comments inside parens are stripped per
// BIND conventions. Returns the folded line set.
func foldParens(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	var out []string
	var sb strings.Builder
	depth := 0
	for scanner.Scan() {
		line := scanner.Text()
		// Track paren depth ignoring quoted runs.
		inQuote := false
		for _, r := range line {
			switch r {
			case '"':
				inQuote = !inQuote
			case '(':
				if !inQuote {
					depth++
				}
			case ')':
				if !inQuote && depth > 0 {
					depth--
				}
			}
		}
		// Strip the literal parens since the consumer doesn't need them.
		clean := strings.NewReplacer("(", " ", ")", " ").Replace(line)
		if depth > 0 {
			sb.WriteString(clean)
			sb.WriteByte(' ')
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString(clean)
			out = append(out, sb.String())
			sb.Reset()
			continue
		}
		out = append(out, clean)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if sb.Len() > 0 {
		// Unclosed paren — treat the buffer as the last line so we
		// don't drop content silently.
		out = append(out, sb.String())
	}
	return out, nil
}

// stripCommentLite removes a trailing `;` comment if it isn't
// inside a quoted string. BIND comments start with ';' anywhere
// to end-of-line.
func stripCommentLite(s string) string {
	inQuote := false
	for i, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case ';':
			if !inQuote {
				return s[:i]
			}
		}
	}
	return s
}

// splitBindLine cracks one BIND record line into (owner, ttl,
// type, rdata). Owner inheritance: a leading whitespace line
// inherits the previous owner.
//
// Accepted shapes:
//
//	<owner> <ttl?> IN <type> <rdata...>
//	<ttl?> IN <type> <rdata...>             (owner = lastOwner)
//	<owner> <ttl?> <type> <rdata...>        (no class — IN implied)
//
// Returns ok=false when the line doesn't parse to those.
func splitBindLine(line, lastOwner string, defaultTTL int) (owner string, ttl int, rrType, rdata string, ok bool) {
	leadingWS := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", 0, "", "", false
	}
	idx := 0
	if leadingWS {
		owner = lastOwner
	} else {
		owner = fields[idx]
		idx++
	}
	ttl = defaultTTL
	if idx < len(fields) {
		if v, err := strconv.Atoi(fields[idx]); err == nil {
			ttl = v
			idx++
		}
	}
	// Class — skip "IN" / "in" if present.
	if idx < len(fields) && strings.EqualFold(fields[idx], "IN") {
		idx++
	}
	if idx >= len(fields) {
		return "", 0, "", "", false
	}
	rrType = strings.ToUpper(fields[idx])
	idx++
	if idx >= len(fields) {
		return "", 0, "", "", false
	}
	rdata = strings.TrimSpace(strings.Join(fields[idx:], " "))
	return owner, ttl, rrType, rdata, true
}

// expandOwner resolves BIND-style owner shorthand:
//
//	@            → origin
//	<name>       → <name>.<origin>
//	<name>.      → <name> (FQDN, strip trailing dot)
func expandOwner(owner, origin string) string {
	owner = strings.TrimSpace(owner)
	if owner == "" || owner == "@" {
		return origin
	}
	if strings.HasSuffix(owner, ".") {
		return strings.TrimSuffix(owner, ".")
	}
	if origin == "" {
		return owner
	}
	return owner + "." + origin
}

// unquoteTXT strips surrounding double-quotes off each token in
// the rdata + folds multi-string TXT (`"abc" "def"`) into
// "abc def" preserving the space separator between tokens but
// dropping the quote literals.
func unquoteTXT(rdata string) string {
	rdata = strings.TrimSpace(rdata)
	if !strings.Contains(rdata, "\"") {
		return rdata
	}
	var out strings.Builder
	inQuote := false
	for _, r := range rdata {
		switch r {
		case '"':
			inQuote = !inQuote
		default:
			if inQuote {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

// ImportDNSResult is returned to the restore-stage caller.
type ImportDNSResult struct {
	Zones   int
	Records int
	Skipped []string
}

// ImportDNS walks each ZoneFile recorded by the parser and upserts
// it into PowerDNS via the existing dns.zone.upsert agent command.
//
// Apex SOA + apex NS records are filtered out — pdns's own backend
// generates those at zone-create time per ADR-0003 + M15 DNSSEC
// pattern. cPanel embeds its own NS authority in the zone file
// which would point users back at the source server post-migration.
//
// agentCaller is required.
func ImportDNS(
	ctx context.Context,
	zonesRepo repository.DNSZoneRepository,
	recordsRepo repository.DNSRecordRepository,
	domainsRepo repository.DomainRepository,
	parsed *ParsedTarball,
	serverIPv4 string,
	defaultTTL int,
) (*ImportDNSResult, error) {
	if zonesRepo == nil || recordsRepo == nil || domainsRepo == nil {
		return nil, fmt.Errorf("ImportDNS: dns repos nil")
	}
	if parsed == nil {
		return nil, fmt.Errorf("ImportDNS: parsed nil")
	}
	res := &ImportDNSResult{}

	for _, zonePath := range parsed.ZoneFiles {
		f, err := openZone(zonePath)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("open %s: %v", zonePath, err))
			continue
		}
		// cPanel filenames are typically <domain>.db. Strip suffix
		// for the default origin if the zone file lacks $ORIGIN.
		base := zonePath
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		base = strings.TrimSuffix(base, ".db")

		zone, skipped, ok := ParseBINDZone(f, base)
		_ = f.Close()
		if !ok {
			res.Skipped = append(res.Skipped, fmt.Sprintf("parse failed: %s", zonePath))
			continue
		}
		res.Skipped = append(res.Skipped, skipped...)

		// Filter apex SOA + apex NS — pdns owns those. A normal zone
		// carries two apex NS records (ns1 + ns2); emit the skip note
		// ONCE per zone, not once per filtered record, so the manifest
		// doesn't duplicate apex_ns_handled_by_pdns:<origin>.
		// #327: records that point at the SOURCE server's IP must be repointed at
		// THIS box (the whole point of the migration) — otherwise mail A + custom
		// subdomains keep resolving to the old server. The source server IP is the
		// source apex A; only records matching it are rewritten, so a record that
		// deliberately points elsewhere (external service) is left alone.
		sourceIPv4 := ""
		for _, r := range zone.Records {
			if (r.Name == zone.Origin || r.Name == "@") && r.Type == "A" {
				sourceIPv4 = r.Content
				break
			}
		}

		filtered := zone.Records[:0]
		apexFiltered := false
		mailFiltered := false
		serviceFiltered := false
		ipRewritten := false
		for _, r := range zone.Records {
			isApex := r.Name == zone.Origin || r.Name == "@"
			// Apex NS is generated by pdns. Apex A/AAAA are owned by jabali:
			// domain.create points the apex at THIS box, so importing the
			// source's apex address would repoint the live domain back at the old
			// server (JAB-28 follow-up: DNS now runs AFTER domain create, so an
			// imported apex address would win). Custom / subdomain records still
			// import — the operator repoints those after cutover.
			if isApex && (r.Type == "NS" || r.Type == "A" || r.Type == "AAAA") {
				apexFiltered = true
				continue
			}
			// #327: drop the source's mail-infrastructure records. jabali owns
			// mail DNS (MX / SPF / DKIM / DMARC / client-service SRVs / autoconfig
			// CNAMEs) and regenerates them for THIS server whenever the domain uses
			// jabali mail, so importing the source's would duplicate jabali's set
			// and pin the OLD server's SPF ip4: / DKIM selector. A non-mail domain
			// gets none (ImportDomains sets MailProvider "none").
			//
			// JAB-226 reverses the previous "keep the `mail` record" decision.
			// Its stated reason was that dropping it would leave the MX target
			// unresolvable, and that does not hold: every imported MX is dropped
			// unconditionally by the MX case below, and dnscompile/bootstrap.go
			// generates BOTH `@ MX mail.<zone>` and `mail A <this server>` for a
			// domain that uses jabali mail. So the imported record is redundant
			// when jabali mail is on, and an orphan pointing at the OLD box when
			// it is off — and either way it lands in the certificate SAN, where
			// it 404s HTTP-01 and fails the whole cert. A source `mail` CNAME
			// alongside jabali's generated `mail` A is also a CNAME-plus-other-
			// data conflict.
			if isMailInfraRecord(toPanelRecordName(r.Name, zone.Origin), r.Type, r.Content) {
				mailFiltered = true
				continue
			}
			// JAB-226: cPanel's own control-panel hostnames. Nothing on jabali
			// serves cpanel./whm./webdisk./ftp./cpcalendars./cpcontacts., and
			// leaving them in DNS puts them in the certificate SAN where they
			// fail HTTP-01 and take the whole cert down with them.
			if isCPanelServiceRecord(toPanelRecordName(r.Name, zone.Origin)) {
				serviceFiltered = true
				continue
			}
			// Repoint any A record that pointed at the old server (incl. the
			// mail A the mail-infra filter deliberately keeps) at THIS box.
			if serverIPv4 != "" && sourceIPv4 != "" && r.Type == "A" && r.Content == sourceIPv4 {
				r.Content = serverIPv4
				ipRewritten = true
			}
			filtered = append(filtered, r)
		}
		if apexFiltered {
			res.Skipped = append(res.Skipped, "apex_ns_a_handled_by_jabali:"+zone.Origin)
		}
		if mailFiltered {
			res.Skipped = append(res.Skipped, "mail_infra_handled_by_jabali:"+zone.Origin)
		}
		if serviceFiltered {
			res.Skipped = append(res.Skipped, "cpanel_service_subdomains_dropped:"+zone.Origin)
		}
		if ipRewritten {
			res.Skipped = append(res.Skipped, "source_ip_repointed_to_this_server:"+zone.Origin)
		}
		zone.Records = filtered
		if len(zone.Records) == 0 {
			res.Skipped = append(res.Skipped, "empty_zone_after_filter:"+zone.Origin)
			continue
		}

		// JAB-28: write to the panel dns_records table (the reconciler's source
		// of truth), NOT straight to PDNS via dns.zone.upsert — the reconciler
		// re-compiles this table into PowerDNS on its next tick, so a PDNS-direct
		// write is clobbered. Resolve the zone domain.create provisioned during
		// ImportDomains; skip if it is not there yet.
		pz, _ := zonesRepo.FindByName(ctx, zone.Origin)
		if pz == nil {
			// JAB-28: the reconciler creates the dns_zone asynchronously on its
			// next tick, AFTER the restore stage finishes — so during restore the
			// zone does not exist yet (manifest showed zone_not_provisioned, 0
			// records). Create it here from the domain ImportDomains just made so
			// the records land + the reconciler compiles them into PowerDNS.
			dom, derr := domainsRepo.FindByName(ctx, zone.Origin)
			if derr != nil || dom == nil {
				res.Skipped = append(res.Skipped, "domain_not_found:"+zone.Origin)
				continue
			}
			now := time.Now().UTC()
			pz = &models.DNSZone{
				ID:             ids.NewULID(),
				DomainID:       dom.ID,
				Name:           zone.Origin,
				RefreshSeconds: 3600,
				RetrySeconds:   600,
				ExpireSeconds:  604800,
				MinimumTTL:     3600,
				IsEnabled:      true,
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			if cerr := zonesRepo.Create(ctx, pz); cerr != nil {
				res.Skipped = append(res.Skipped, "zone_create:"+zone.Origin+":"+cerr.Error())
				continue
			}
		}
		// Skip an imported record whose (name,type) already exists in the zone so
		// jabali's bootstrap MX/SPF/DMARC/apex records win over the source's.
		existing, _ := recordsRepo.ListByZoneID(ctx, pz.ID)
		have := make(map[string]bool, len(existing))
		for _, er := range existing {
			have[er.Name+"|"+er.Type] = true
		}
		inserted := 0
		for _, r := range zone.Records {
			pname := toPanelRecordName(r.Name, zone.Origin)
			if have[pname+"|"+r.Type] {
				res.Skipped = append(res.Skipped, "dup_skip:"+pname+"/"+r.Type)
				continue
			}
			rec := &models.DNSRecord{
				ID:      ids.NewULID(),
				ZoneID:  pz.ID,
				Name:    pname,
				Type:    r.Type,
				Content: r.Content,
				// GH #527: normalize migrated records to the panel's default
				// TTL (fallback to the parsed source TTL only when no default
				// is supplied) so a migrated zone is consistent with what the
				// panel creates, instead of carrying the source's mixed TTLs.
				TTL:       ttlOrDefault(r.TTL, defaultTTL),
				Priority:  r.Prio,
				IsEnabled: true,
			}
			if cerr := recordsRepo.Create(ctx, rec); cerr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("insert %s/%s: %v", pname, r.Type, cerr))
				continue
			}
			inserted++
		}
		res.Zones++
		res.Records += inserted
	}
	return res, nil
}

// ttlOrDefault returns the panel default TTL when one is supplied (>0),
// otherwise falls back to the source record's TTL. GH #527: migrated records
// adopt the panel's default_dns_ttl so the zone isn't a mix of source TTLs.
func ttlOrDefault(sourceTTL, defaultTTL int) int {
	if defaultTTL > 0 {
		return defaultTTL
	}
	return sourceTTL
}

// toPanelRecordName converts a parsed FQDN record name into the panel's
// zone-relative convention: "@" for the apex, otherwise the leftmost labels
// with the origin suffix stripped (digibandit.itflow.app -> digibandit).
func toPanelRecordName(name, origin string) string {
	name = strings.TrimSuffix(name, ".")
	origin = strings.TrimSuffix(origin, ".")
	if name == "" || name == "@" || name == origin {
		return "@"
	}
	if strings.HasSuffix(name, "."+origin) {
		return strings.TrimSuffix(name, "."+origin)
	}
	return name
}

// isMailInfraRecord reports whether a zone-relative record (name via
// toPanelRecordName) is jabali-owned mail infrastructure that must NOT be
// imported from the source zone (#327): jabali regenerates MX / SPF / DKIM /
// DMARC / client-service SRVs / autoconfig CNAMEs for THIS server. The `mail` A
// is NOT dropped here (it is the MX target and jabali does not re-create it for
// imported zones); repointing its IP to this server is a separate follow-up.
// cpanelServiceHostnames are subdomains cPanel creates for its own control
// panel and client-service endpoints. They are meaningless on jabali — nothing
// serves them here — and importing them is actively harmful: the per-domain
// Let's Encrypt SAN is built from DNS, so each one becomes a hostname that must
// pass HTTP-01. Pre-cutover they still resolve to the source box and 404, which
// fails the entire certificate (JAB-226).
var cpanelServiceHostnames = map[string]bool{
	"cpanel":      true,
	"whm":         true,
	"webdisk":     true,
	"cpcalendars": true,
	"cpcontacts":  true,
	"ftp":         true,
}

// isCPanelServiceRecord reports whether a record is one of cPanel's own service
// hostnames. Matched on the leaf label for every record type: cPanel writes some
// as A and some as CNAME depending on the account's setup, and the reason to
// drop them does not depend on the type.
func isCPanelServiceRecord(name string) bool {
	return cpanelServiceHostnames[strings.ToLower(strings.TrimSpace(name))]
}

func isMailInfraRecord(name, typ, content string) bool {
	lc := strings.ToLower(strings.TrimSpace(content))
	switch typ {
	case "MX":
		return true
	case "CNAME", "A", "AAAA":
		// JAB-226: cPanel writes these as A records at least as often as CNAMEs.
		// Matching only CNAME let the A variants through, and because the
		// per-domain LE certificate builds its SAN from what is in DNS, every
		// migrated domain then tried to validate autoconfig./autodiscover./
		// webmail. — which still resolve to the OLD box until the source NS
		// stops being authoritative, 404 the HTTP-01 challenge, and fail the
		// WHOLE certificate. The domain is then stuck on a self-signed origin,
		// which behind Cloudflare Full (strict) is a hard 526 outage.
		return name == "webmail" || name == "autoconfig" ||
			name == "autodiscover" || name == "mail"
	case "TXT":
		lc = strings.Trim(lc, `"`)
		if strings.HasPrefix(lc, "v=spf1") || strings.HasPrefix(lc, "v=dmarc1") || strings.HasPrefix(lc, "v=tlsrptv1") {
			return true
		}
		return name == "_dmarc" || name == "_smtp._tls" || strings.HasSuffix(name, "._domainkey")
	case "SRV":
		switch name {
		case "_imap._tcp", "_imaps._tcp", "_pop3._tcp", "_pop3s._tcp",
			"_submission._tcp", "_submissions._tcp", "_autodiscover._tcp",
			"_caldav._tcp", "_caldavs._tcp", "_carddav._tcp", "_carddavs._tcp":
			return true
		}
	}
	return false
}
