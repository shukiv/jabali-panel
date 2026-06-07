package cpanel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/agent"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/migrate"
)

// openZone wraps os.Open with context for symmetry with the rest
// of the migrate package's IO. Caller closes.
func openZone(path string) (*os.File, error) { return os.Open(path) }

// ParseBINDZone walks a cPanel-emitted BIND zone file and returns
// a DNSZoneSpec ready for migration.AccountManifest. Handles the
// directives + record types cPanel actually emits in cpmove tarballs:
//
//   $ORIGIN <name>.
//   $TTL <secs>
//   <name> <ttl?> IN <type> <rdata...>
//   <ttl?> IN <type> <rdata...>           (inherits previous owner)
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
				Name:    expandOwner(owner, zone.Origin),
				Type:    "TXT",
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
			target := strings.TrimSuffix(fields[3], ".")
			content := fields[0] + " " + fields[1] + " " + fields[2] + " " + target
			zone.Records = append(zone.Records, migrate.DNSRecord{
				Name:    expandOwner(owner, zone.Origin),
				Type:    "SRV",
				Content: content,
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
//   <owner> <ttl?> IN <type> <rdata...>
//   <ttl?> IN <type> <rdata...>             (owner = lastOwner)
//   <owner> <ttl?> <type> <rdata...>        (no class — IN implied)
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
//   @            → origin
//   <name>       → <name>.<origin>
//   <name>.      → <name> (FQDN, strip trailing dot)
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
	Zones    int
	Records  int
	Skipped  []string
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
	agentCaller agent.AgentInterface,
	parsed *ParsedTarball,
) (*ImportDNSResult, error) {
	if agentCaller == nil {
		return nil, fmt.Errorf("ImportDNS: agent caller nil")
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
		filtered := zone.Records[:0]
		apexNSFiltered := false
		for _, r := range zone.Records {
			if r.Type == "NS" && (r.Name == zone.Origin || r.Name == "@") {
				apexNSFiltered = true
				continue
			}
			filtered = append(filtered, r)
		}
		if apexNSFiltered {
			res.Skipped = append(res.Skipped, "apex_ns_handled_by_pdns:"+zone.Origin)
		}
		zone.Records = filtered
		if len(zone.Records) == 0 {
			res.Skipped = append(res.Skipped, "empty_zone_after_filter:"+zone.Origin)
			continue
		}

		// dns.zone.upsert takes records as []map[string]any so we
		// can avoid importing the agent's typed param struct.
		wireRecords := make([]map[string]any, 0, len(zone.Records))
		for _, r := range zone.Records {
			row := map[string]any{
				"name":    r.Name,
				"type":    r.Type,
				"content": r.Content,
				"ttl":     r.TTL,
			}
			if r.Prio != 0 {
				row["priority"] = r.Prio
			}
			wireRecords = append(wireRecords, row)
		}

		if _, err := agentCaller.Call(ctx, "dns.zone.upsert", map[string]any{
			"zone":    zone.Origin,
			"records": wireRecords,
		}); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("upsert %s: %v", zone.Origin, err))
			continue
		}
		res.Zones++
		res.Records += len(zone.Records)
	}
	return res, nil
}
