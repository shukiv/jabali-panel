// Package pdns additions for DNSSEC operations via the pdnsutil CLI.
//
// `pdnsutil` is PowerDNS's first-party CLI. It reads the same MySQL backend
// the pdns-server does and offers the only supported surface for generating
// DNSSEC key material + rectifying signed zones (ADR-0057).
//
// This file wraps `pdnsutil` calls + parses their stdout. Tests substitute
// the pdnsutilBinary variable to point at a fake script, keeping unit tests
// hermetic.

package pdns

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// pdnsutilBinary is the path to the pdnsutil CLI. Overridable in tests; defaults
// to a no-op stub under `go test` so mutating dnssec ops can't reach real
// pdnsutil (GH #994/#1160 — see exec_seam.go).
var pdnsutilBinary = defaultPdnsutilBinary()

// DNSSECKey mirrors one row from `pdnsutil show-zone` output.
type DNSSECKey struct {
	KeyTag    int
	KeyType   string // "KSK", "ZSK", or "CSK"
	Algorithm uint8
	PublicKey string
	Active    bool
}

// DSRecord mirrors one line from `pdnsutil export-zone-ds`.
type DSRecord struct {
	KeyTag     int
	Algorithm  uint8
	DigestType uint8
	Digest     string
}

// domainNamePattern is a conservative hostname validator — lowercase letters,
// digits, hyphens, dots. Refuses anything that could be interpreted as a
// flag or path component by pdnsutil. The agent's command handler validates
// its input before this function runs; this is defence in depth.
var domainNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?)+$`)

func validateDomainName(name string) error {
	if !domainNamePattern.MatchString(name) {
		return fmt.Errorf("invalid domain name %q", name)
	}
	return nil
}

// runPdnsutil runs pdnsutil with the given args and returns stdout on success,
// or stderr wrapped with the argv on failure.
func runPdnsutil(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, pdnsutilBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdnsutil %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// SecureZone runs `pdnsutil secure-zone`, then sets NSEC3 defaults, then
// rectifies. Returns the list of keys on success.
func SecureZone(ctx context.Context, zone string) ([]DNSSECKey, error) {
	if err := validateDomainName(zone); err != nil {
		return nil, err
	}
	if _, err := runPdnsutil(ctx, "secure-zone", zone); err != nil {
		return nil, err
	}
	if _, err := runPdnsutil(ctx, "set-nsec3", zone, "1 0 10 ab"); err != nil {
		return nil, err
	}
	if _, err := runPdnsutil(ctx, "rectify-zone", zone); err != nil {
		return nil, err
	}
	return ListKeys(ctx, zone)
}

// DisableDNSSEC removes all signing keys + rectifies.
func DisableDNSSEC(ctx context.Context, zone string) error {
	if err := validateDomainName(zone); err != nil {
		return err
	}
	if _, err := runPdnsutil(ctx, "disable-dnssec", zone); err != nil {
		// idempotent: if the zone was already unsigned, pdnsutil prints an
		// "Zone is not secured" message and exits non-zero. Swallow.
		if strings.Contains(err.Error(), "not secured") || strings.Contains(err.Error(), "not signed") {
			return nil
		}
		return err
	}
	_, _ = runPdnsutil(ctx, "rectify-zone", zone)
	return nil
}

// ListKeys parses `pdnsutil show-zone <zone>`.
func ListKeys(ctx context.Context, zone string) ([]DNSSECKey, error) {
	if err := validateDomainName(zone); err != nil {
		return nil, err
	}
	out, err := runPdnsutil(ctx, "show-zone", zone)
	if err != nil {
		return nil, err
	}
	return parseShowZone(out), nil
}

// ExportZoneDS parses `pdnsutil export-zone-ds <zone>`.
func ExportZoneDS(ctx context.Context, zone string) ([]DSRecord, error) {
	if err := validateDomainName(zone); err != nil {
		return nil, err
	}
	out, err := runPdnsutil(ctx, "export-zone-ds", zone)
	if err != nil {
		return nil, err
	}
	return parseExportZoneDS(out), nil
}

// showZoneIDRE captures "ID = 12" within an "ID = 12 (KSK), flags = 257,
// tag = 34567, algo = 13, bits = 256 Active: 1 ..." line.
var (
	showZoneIDLine   = regexp.MustCompile(`ID\s*=\s*\d+\s*\(([A-Z]{3})\)`)
	showZoneTag      = regexp.MustCompile(`tag\s*=\s*(\d+)`)
	showZoneAlgo     = regexp.MustCompile(`algo\s*=\s*(\d+)`)
	showZoneActive   = regexp.MustCompile(`Active:\s*(\d+)`)
	dsLinePublicKey  = regexp.MustCompile(`DNSKEY\s*=\s*(.+?)\s*;`)
	// PowerDNS 4.9 writes each line as:
	//   zone. IN DS <tag> <algo> <digesttype> <hex> ; ( SHA… digest )
	// The trailing "; (…)" is a human-readable comment we must tolerate.
	exportZoneDSLine = regexp.MustCompile(`^\S+\s+IN\s+DS\s+(\d+)\s+(\d+)\s+(\d+)\s+([0-9A-Fa-f]+)(?:\s*;.*)?\s*$`)
)

// parseShowZone extracts one DNSSECKey per ID line. The format across PDNS
// versions varies; we match on anchors (ID = N (KSK), tag = N, algo = N,
// Active: 0/1). Public key (DNSKEY record) is on a follow-up line; we
// capture the bytes between `DNSKEY = ` and a trailing `;`.
func parseShowZone(out string) []DNSSECKey {
	var keys []DNSSECKey
	scanner := bufio.NewScanner(strings.NewReader(out))
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)
	var current *DNSSECKey
	for scanner.Scan() {
		line := scanner.Text()
		if m := showZoneIDLine.FindStringSubmatch(line); len(m) == 2 {
			if current != nil {
				keys = append(keys, *current)
			}
			current = &DNSSECKey{KeyType: m[1], Active: true}
			if t := showZoneTag.FindStringSubmatch(line); len(t) == 2 {
				current.KeyTag, _ = strconv.Atoi(t[1])
			}
			if a := showZoneAlgo.FindStringSubmatch(line); len(a) == 2 {
				v, _ := strconv.Atoi(a[1])
				current.Algorithm = uint8(v) //nolint:gosec
			}
			if act := showZoneActive.FindStringSubmatch(line); len(act) == 2 {
				current.Active = act[1] != "0"
			}
			continue
		}
		if current != nil {
			if pk := dsLinePublicKey.FindStringSubmatch(line); len(pk) == 2 && current.PublicKey == "" {
				current.PublicKey = strings.TrimSpace(pk[1])
			}
		}
	}
	if current != nil {
		keys = append(keys, *current)
	}
	return keys
}

// parseExportZoneDS extracts one DSRecord per line formatted as
//
//	example.com. IN DS 34567 13 2 ABCDEF...
func parseExportZoneDS(out string) []DSRecord {
	var records []DSRecord
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		m := exportZoneDSLine.FindStringSubmatch(line)
		if len(m) != 5 {
			continue
		}
		rec := DSRecord{Digest: strings.ToLower(m[4])}
		if v, err := strconv.Atoi(m[1]); err == nil {
			rec.KeyTag = v
		}
		if v, err := strconv.Atoi(m[2]); err == nil {
			rec.Algorithm = uint8(v) //nolint:gosec
		}
		if v, err := strconv.Atoi(m[3]); err == nil {
			rec.DigestType = uint8(v) //nolint:gosec
		}
		records = append(records, rec)
	}
	return records
}
