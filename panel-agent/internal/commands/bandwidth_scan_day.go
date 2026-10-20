// bandwidth.scan_day — runs goaccess against /var/log/nginx/<domain>-access.log.1
// (yesterday's rotated nginx log; logrotate's delaycompress keeps it
// uncompressed) and emits per-domain {bytes_total, requests_total}.
//
// Why .log.1 specifically: rotation runs daily ~00:00 UTC, so a
// scheduled run at ~00:30 UTC sees the prior-day file fully written.
// Scanning the live access.log instead would either double-count
// today (running totals + tomorrow's rerun) or miss the last hour of
// activity (if scanned before midnight).
//
// goaccess output: `-o json` yields a structured object
// whose `general.bandwidth` (total bytes) and `general.total_requests`
// (incl. failed) are what we need. We don't need the full hosts/urls
// breakdown for this M13.1 scope; agent returns just the totals so the
// panel can build per-user / per-month aggregations from cheap rows.
//
// When the GeoLite2 City database CrowdSec downloads is present, the scan
// also returns the day's requests and bytes per country, from goaccess's
// geolocation panel, for the domain Statistics tab.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type bandwidthScanDayParams struct {
	// LogDir overrides the default /var/log/nginx for tests. Empty =
	// production default.
	LogDir string `json:"log_dir,omitempty"`
}

// geoIPCityDB is the MaxMind GeoLite2 City database that CrowdSec's
// geoip-enrich parser keeps up to date. The agent runs as root, so it reads
// the root-only file in place. Without it the country list is empty.
var geoIPCityDB = "/var/lib/crowdsec/data/GeoLite2-City.mmdb"

// maxScanCountries caps the countries kept per domain and day. The rest are
// added to "ZZ" with the addresses that have no country, so a busy site does
// not write a row for every country there is.
const maxScanCountries = 30

// unknownCountry is the code for requests whose country is unknown (private
// and unlisted addresses) or that fell outside maxScanCountries.
const unknownCountry = "ZZ"

type bandwidthDomainStats struct {
	Domain        string `json:"domain"`
	BytesTotal    uint64 `json:"bytes_total"`
	RequestsTotal uint64 `json:"requests_total"`
	// Day is the date the traffic occurred (YYYY-MM-DD UTC). Computed
	// as `now-1d` since logrotate runs at 00:00 UTC and we scan .log.1.
	Day string `json:"day"`
	// Countries is the day's traffic per country, most requests first.
	// Empty without the GeoIP database. Its requests are the requests
	// goaccess counts as valid, so they can sum to less than RequestsTotal.
	Countries []bandwidthCountryStats `json:"countries,omitempty"`
}

type bandwidthCountryStats struct {
	// Code is the ISO 3166-1 alpha-2 country code, or unknownCountry.
	Code          string `json:"code"`
	RequestsTotal uint64 `json:"requests_total"`
	BytesTotal    uint64 `json:"bytes_total"`
}

type bandwidthScanDayResponse struct {
	Stats   []bandwidthDomainStats `json:"stats"`
	Skipped []string               `json:"skipped,omitempty"`
}

// goaccessGeneral mirrors the slice of goaccess `-o json`
// output we care about. goaccess ships a stable schema for this
// section across 1.5+; the rest of the JSON document is hosts/urls
// breakdowns we don't need.
type goaccessJSON struct {
	General struct {
		// Some goaccess versions emit numbers as strings inside the
		// JSON; tolerate both via json.Number.
		Bandwidth     json.Number `json:"bandwidth"`
		TotalRequests json.Number `json:"total_requests"`
	} `json:"general"`
	// Geolocation is present only when goaccess ran with a GeoIP
	// database: continents, each with its countries as items.
	Geolocation struct {
		Data []struct {
			Items []goaccessGeoItem `json:"items"`
		} `json:"data"`
	} `json:"geolocation"`
}

type goaccessGeoItem struct {
	// Data is "<code> <name>", e.g. "DE Germany", or "Unknown".
	Data string `json:"data"`
	Hits struct {
		Count json.Number `json:"count"`
	} `json:"hits"`
	Bytes struct {
		Count json.Number `json:"count"`
	} `json:"bytes"`
}

func bandwidthScanDayHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p bandwidthScanDayParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
		}
	}
	logDir := p.LogDir
	if logDir == "" {
		logDir = "/var/log/nginx"
	}

	// Yesterday in UTC. logrotate's `daily` rotates on calendar day
	// transition; a scan at 00:30 UTC sees yesterday's full file.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	// Rotated logs are <domain>-access.log.1 thanks to delaycompress.
	pattern := filepath.Join(logDir, "*-access.log.1")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("glob %s: %v", pattern, err),
		}
	}

	stats := make([]bandwidthDomainStats, 0, len(matches))
	skipped := []string{}

	geoDB := ""
	if fi, err := os.Stat(geoIPCityDB); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
		geoDB = geoIPCityDB
	}

	for _, file := range matches {
		base := filepath.Base(file)
		// "<domain>-access.log.1" → "<domain>"
		name := strings.TrimSuffix(base, "-access.log.1")
		if name == "" || name == base {
			skipped = append(skipped, base)
			continue
		}

		st, err := scanLogFile(ctx, file, geoDB)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (err: %v)", base, err))
			continue
		}
		st.Domain = name
		st.Day = yesterday
		stats = append(stats, st)
	}

	return bandwidthScanDayResponse{Stats: stats, Skipped: skipped}, nil
}

// goaccessScanArgs builds the goaccess CLI args for a single access log.
// Extracted so the flag set is unit-testable without the binary present.
//
// goaccess has NO --output-format flag; JSON-to-stdout is `-o json` (`-o`
// takes a format keyword or a filename, e.g. `-o csv`, `-o out.json`).
// `--output-format=json` fatals on goaccess 1.9.x ("unknown option") and
// silently broke the M13.1 bandwidth scan on hosts shipping that version.
//
// geoDB, when set, adds the GeoIP database so the output carries the
// geolocation panel.
func goaccessScanArgs(path, geoDB string) []string {
	args := []string{
		path,
		"--log-format=COMBINED",
		"-o", "json",
		"--no-progress",
		"--no-html-last-updated",
		"--ignore-crawlers",
	}
	if geoDB != "" {
		args = append(args, "--geoip-database="+geoDB)
	}
	return args
}

// scanLogFile runs goaccess on one log. With geoDB set and goaccess failing,
// it runs again without it: a goaccess built without GeoIP support refuses
// the flag, and the day's totals matter more than its countries.
func scanLogFile(ctx context.Context, path, geoDB string) (bandwidthDomainStats, error) {
	st, err := runGoaccessScan(ctx, path, geoDB)
	if err != nil && geoDB != "" {
		return runGoaccessScan(ctx, path, "")
	}
	return st, err
}

func runGoaccessScan(ctx context.Context, path, geoDB string) (bandwidthDomainStats, error) {
	// Bound the goaccess invocation to 90s to prevent a runaway scan
	// holding the agent socket open across timer ticks.
	scanCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := execCommandContext(scanCtx, "goaccess", goaccessScanArgs(path, geoDB)...)
	out, err := cmd.Output()
	if err != nil {
		// goaccess exits non-zero on parse errors; emit the file basename
		// + truncated stderr so the operator can spot which log is bad.
		var msg string
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			s := string(ee.Stderr)
			if len(s) > 200 {
				s = s[:200]
			}
			msg = fmt.Sprintf("goaccess exit: %v stderr=%s", err, s)
		} else {
			msg = fmt.Sprintf("goaccess: %v", err)
		}
		return bandwidthDomainStats{}, fmt.Errorf("%s", msg)
	}
	return parseGoaccessScan(out)
}

// parseGoaccessScan reads the totals, and the countries when present, from
// goaccess's JSON output.
func parseGoaccessScan(out []byte) (bandwidthDomainStats, error) {
	var doc goaccessJSON
	if err := json.Unmarshal(out, &doc); err != nil {
		return bandwidthDomainStats{}, fmt.Errorf("parse goaccess json: %w", err)
	}
	var items []goaccessGeoItem
	for _, c := range doc.Geolocation.Data {
		items = append(items, c.Items...)
	}
	return bandwidthDomainStats{
		BytesTotal:    nonNegative(doc.General.Bandwidth),
		RequestsTotal: nonNegative(doc.General.TotalRequests),
		Countries:     countryStats(items),
	}, nil
}

func nonNegative(n json.Number) uint64 {
	v, _ := n.Int64()
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// countryStats merges goaccess's country items by code, most requests first,
// keeping at most maxScanCountries entries; the rest count as unknownCountry.
func countryStats(items []goaccessGeoItem) []bandwidthCountryStats {
	if len(items) == 0 {
		return nil
	}
	byCode := map[string]*bandwidthCountryStats{}
	for _, it := range items {
		code := geoCountryCode(it.Data)
		c := byCode[code]
		if c == nil {
			c = &bandwidthCountryStats{Code: code}
			byCode[code] = c
		}
		c.RequestsTotal += nonNegative(it.Hits.Count)
		c.BytesTotal += nonNegative(it.Bytes.Count)
	}
	other := byCode[unknownCountry]
	delete(byCode, unknownCountry)
	if other == nil {
		other = &bandwidthCountryStats{Code: unknownCountry}
	}
	list := make([]bandwidthCountryStats, 0, len(byCode)+1)
	for _, c := range byCode {
		list = append(list, *c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].RequestsTotal != list[j].RequestsTotal {
			return list[i].RequestsTotal > list[j].RequestsTotal
		}
		return list[i].Code < list[j].Code
	})
	if len(list) > maxScanCountries-1 {
		for _, c := range list[maxScanCountries-1:] {
			other.RequestsTotal += c.RequestsTotal
			other.BytesTotal += c.BytesTotal
		}
		list = list[:maxScanCountries-1]
	}
	if other.RequestsTotal > 0 || other.BytesTotal > 0 {
		list = append(list, *other)
	}
	return list
}

// geoCountryCode returns the country code that starts a goaccess
// geolocation label ("DE Germany"), or unknownCountry ("Unknown").
func geoCountryCode(label string) string {
	if len(label) >= 3 && label[2] == ' ' && isUpperASCII(label[0]) && isUpperASCII(label[1]) {
		return label[:2]
	}
	return unknownCountry
}

func isUpperASCII(b byte) bool { return b >= 'A' && b <= 'Z' }

func init() {
	Default.Register("bandwidth.scan_day", bandwidthScanDayHandler)
}
