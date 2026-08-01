package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// M26 Step 2 (ADR-0053). CrowdSec LAPI surface for the admin Security
// tab. Every handler shells `cscli`; cscli reads
// /etc/crowdsec/local_api_credentials.yaml which the M26 install_crowdsec
// pinned to the unix socket /run/crowdsec/api.sock (ADR-0050).

// ---- shared helpers --------------------------------------------------------

func csInvalidArg(msg string) error {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}

func csInternal(msg string, err error) error {
	return &agentwire.AgentError{
		Code:    agentwire.CodeInternal,
		Message: fmt.Sprintf("%s: %v", msg, err),
	}
}

// runCscliJSON runs `cscli <args...> -o json` and returns the raw JSON.
// Error is wrapped as CodeInternal with the captured stderr.
func runCscliJSON(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{}, args...)
	full = append(full, "-o", "json")
	cmd := exec.CommandContext(ctx, "cscli", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Surface cscli's own diagnostic (auth error, missing config,
		// hub-not-loaded, etc) instead of the bare "exit status 1"
		// that cmd.Output() returns. Trim to keep the agent-wire
		// payload small.
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 512 {
			msg = msg[:512] + "…(truncated)"
		}
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%v: %s", err, msg)
	}
	return stdout.Bytes(), nil
}

// ---- security.crowdsec.status ----------------------------------------------

type csStatusResponse struct {
	Running       bool   `json:"running"`
	LapiReachable bool   `json:"lapi_reachable"`
	Version       string `json:"version,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	OSPretty      string `json:"os_pretty,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	MachineID     string `json:"machine_id,omitempty"`
	LastHeartbeat string `json:"last_heartbeat,omitempty"`
	CAPIReachable bool   `json:"capi_reachable"`
	// GH #716: config validity (crowdsec -t) + active bouncer count for the
	// health-cards overview.
	ConfigValid       bool   `json:"config_valid"`
	ConfigValidDetail string `json:"config_valid_detail,omitempty"`
	BouncerCount      int    `json:"bouncer_count"`
}

func csStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := csStatusResponse{}
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "crowdsec").Run(); err == nil {
		resp.Running = true
	}
	// `cscli lapi status` prints success line on stdout when reachable.
	if out, err := exec.CommandContext(ctx, "cscli", "lapi", "status").CombinedOutput(); err == nil {
		resp.LapiReachable = strings.Contains(string(out), "successfully interact with Local API")
	}
	// CAPI status check — separate verb, may fail when offline. Best-effort.
	if err := exec.CommandContext(ctx, "cscli", "capi", "status").Run(); err == nil {
		resp.CAPIReachable = true
	}
	if out, err := exec.CommandContext(ctx, "cscli", "version").Output(); err == nil {
		// First non-empty line typically holds "version: vX.Y.Z" or similar.
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(line), "version:") {
				resp.Version = strings.TrimSpace(strings.TrimPrefix(line, "version:"))
				resp.Version = strings.TrimPrefix(resp.Version, "Version:")
				break
			}
		}
	}
	if name, err := os.Hostname(); err == nil {
		resp.Hostname = name
	}
	// /etc/os-release PRETTY_NAME — best-effort, debian/ubuntu format.
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				v := strings.TrimPrefix(line, "PRETTY_NAME=")
				resp.OSPretty = strings.Trim(v, `"`)
				break
			}
		}
	}
	// crowdsec.service ActiveEnterTimestamp → daemon start time
	if out, err := exec.CommandContext(ctx, "systemctl", "show",
		"crowdsec.service", "--property=ActiveEnterTimestamp",
		"--value").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			// Convert "Tue 2026-06-03 02:50:53 IDT" to RFC3339 if possible.
			// systemd timestamps are operator-friendly, not parseable;
			// pass through as-is and let the UI format.
			resp.StartedAt = s
		}
	}
	// GH #716: config validation via `crowdsec -t` (dry parse of the full config).
	if out, err := exec.CommandContext(ctx, "crowdsec", "-t").CombinedOutput(); err == nil {
		resp.ConfigValid = true
	} else {
		resp.ConfigValid = false
		resp.ConfigValidDetail = strings.TrimSpace(string(out))
		if len(resp.ConfigValidDetail) > 500 {
			resp.ConfigValidDetail = resp.ConfigValidDetail[:500]
		}
	}
	// GH #716: active bouncer count (cscli bouncers list -o json → array length).
	if out, err := exec.CommandContext(ctx, "cscli", "bouncers", "list", "-o", "json").Output(); err == nil {
		var arr []map[string]any
		if json.Unmarshal(out, &arr) == nil {
			resp.BouncerCount = len(arr)
		}
	}
	// First locally-registered machine = this engine's LAPI ID +
	// last_heartbeat. cscli machines list -o json returns an array.
	if out, err := runCscliJSON(ctx, "machines", "list"); err == nil {
		var machines []struct {
			MachineID     string `json:"machineId"`
			LastHeartbeat string `json:"last_heartbeat"`
		}
		if jerr := json.Unmarshal(out, &machines); jerr == nil && len(machines) > 0 {
			resp.MachineID = machines[0].MachineID
			resp.LastHeartbeat = machines[0].LastHeartbeat
		}
	}
	return resp, nil
}

// ---- security.crowdsec.decisions.list --------------------------------------

type csDecisionsListParams struct {
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type csDecision struct {
	ID       int    `json:"id"`
	IP       string `json:"ip"`
	Duration string `json:"duration"`
	Reason   string `json:"reason"`
	Scenario string `json:"scenario"`
	Until    string `json:"until"`
}

type csDecisionsListResponse struct {
	Decisions []csDecision `json:"decisions"`
}

// rawDecisionEntry mirrors the "ip" group cscli returns under
// `cscli decisions list -o json` — one outer entry per source with
// nested decisions[]. Field names match upstream JSON.
type rawDecisionEntry struct {
	Decisions []struct {
		ID       int    `json:"id"`
		Value    string `json:"value"`
		Duration string `json:"duration"`
		Scenario string `json:"scenario"`
		Origin   string `json:"origin"`
		Until    string `json:"until"`
		Type     string `json:"type"`
	} `json:"decisions"`
}

func csDecisionsListHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csDecisionsListParams
	_ = json.Unmarshal(params, &p)
	args := []string{"decisions", "list"}
	if p.Scope != "" {
		if p.Scope != "ip" && p.Scope != "range" && p.Scope != "country" && p.Scope != "as" {
			return nil, csInvalidArg("scope must be ip|range|country|as")
		}
		args = append(args, "--scope", p.Scope)
	}
	if p.Limit > 0 {
		if p.Limit > 1000 {
			return nil, csInvalidArg("limit max 1000")
		}
		args = append(args, "--limit", strconv.Itoa(p.Limit))
	}
	out, err := runCscliJSON(ctx, args...)
	if err != nil {
		return nil, csInternal("cscli decisions list", err)
	}
	resp := csDecisionsListResponse{Decisions: []csDecision{}}
	// cscli returns either a [] or `null` when there are no decisions.
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return resp, nil
	}
	var raw []rawDecisionEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, csInternal("parse decisions json", err)
	}
	for _, entry := range raw {
		for _, d := range entry.Decisions {
			resp.Decisions = append(resp.Decisions, csDecision{
				ID:       d.ID,
				IP:       d.Value,
				Duration: d.Duration,
				Reason:   d.Origin + "/" + d.Scenario,
				Scenario: d.Scenario,
				Until:    d.Until,
			})
		}
	}
	return resp, nil
}

// ---- security.crowdsec.decisions.add ---------------------------------------

// csDecisionsAddParams covers all four CrowdSec decision scopes.
//
//	scope=ip      value=203.0.113.7           single address
//	scope=range   value=203.0.113.0/24        CIDR block
//	scope=country value=IL                    ISO 3166-1 alpha-2 code
//	scope=as      value=AS64500 or value=64500 ASN, optional AS/as prefix
//
// Country bans rely on the GeoIP2 enricher (crowdsecurity/geoip-enrich)
// which crowdsec installs by default on fresh hosts. If an operator
// disabled it the decision still materialises but the bouncer has no
// way to resolve country → IP, so the ban is inert. The UI leaves that
// operational concern to the CrowdSec hub tab.
type csDecisionsAddParams struct {
	Scope    string `json:"scope"`
	Value    string `json:"value"`
	Duration string `json:"duration"`
	Reason   string `json:"reason"`
}

type csDecisionsAddResponse struct {
	ID int `json:"id"`
}

var csCountryRE = regexp.MustCompile(`^[A-Z]{2}$`)
var csASNRE = regexp.MustCompile(`^\d+$`)

// cscliScope maps our lowercase scope names to cscli's canonical
// capitalisation. cscli accepts lowercase in most commands but the
// `--scope` flag is case-sensitive for Country + AS in some 1.7.x
// builds — keep the mapping explicit so we don't debug a regression
// across upstream versions.
var cscliScope = map[string]string{
	"ip":      "Ip",
	"range":   "Range",
	"country": "Country",
	"as":      "AS",
}

// parseCrowdsecDuration validates a decision/allowlist duration. CrowdSec
// accepts Go durations plus a leading whole-day component ("7d", "7d12h") that
// Go's time.ParseDuration rejects. Returns the equivalent duration and the
// STRING TO PASS TO cscli — days are normalized to a pure Go duration so
// cscli version drift in day-suffix handling can never bite us.
func parseCrowdsecDuration(s string) (time.Duration, string, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, s, nil
	}
	m := csDayDurationRE.FindStringSubmatch(s)
	if m == nil {
		return 0, "", fmt.Errorf("invalid duration %q (examples: 4h, 30m, 7d, 7d12h)", s)
	}
	days, err := strconv.Atoi(m[1])
	if err != nil || days <= 0 {
		return 0, "", fmt.Errorf("invalid day count in duration %q", s)
	}
	total := time.Duration(days) * 24 * time.Hour
	if rest := m[2]; rest != "" {
		rd, rerr := time.ParseDuration(rest)
		if rerr != nil || rd < 0 {
			return 0, "", fmt.Errorf("invalid duration %q (examples: 4h, 30m, 7d, 7d12h)", s)
		}
		total += rd
	}
	return total, total.String(), nil
}

var csDayDurationRE = regexp.MustCompile(`^(\d+)d(.*)$`)

// normalizeASN strips a leading "AS"/"as" and returns the numeric
// portion for validation + value passing.
func normalizeASN(v string) string {
	upper := strings.ToUpper(v)
	return strings.TrimPrefix(upper, "AS")
}

func csDecisionsAddHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csDecisionsAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	canonical, ok := cscliScope[p.Scope]
	if !ok {
		return nil, csInvalidArg("scope must be ip|range|country|as")
	}
	value := strings.TrimSpace(p.Value)
	switch p.Scope {
	case "ip":
		if ip := net.ParseIP(value); ip == nil {
			return nil, csInvalidArg("value must be a valid IP for scope=ip")
		}
	case "range":
		if _, _, err := net.ParseCIDR(value); err != nil {
			return nil, csInvalidArg("value must be a valid CIDR for scope=range")
		}
	case "country":
		value = strings.ToUpper(value)
		if !csCountryRE.MatchString(value) {
			return nil, csInvalidArg("value must be a 2-letter ISO 3166-1 country code for scope=country")
		}
	case "as":
		value = normalizeASN(value)
		if !csASNRE.MatchString(value) {
			return nil, csInvalidArg("value must be an ASN number (e.g. 64500 or AS64500) for scope=as")
		}
	}
	_, durArg, derr := parseCrowdsecDuration(p.Duration)
	if derr != nil {
		return nil, csInvalidArg(fmt.Sprintf("duration: %v", derr))
	}
	if l := len(p.Reason); l < 3 || l > 200 {
		return nil, csInvalidArg("reason length must be 3..200")
	}
	cmd := exec.CommandContext(ctx, "cscli", "decisions", "add",
		"--scope", canonical, "--value", value,
		"--duration", durArg, "--reason", p.Reason)
	if _, err := cmd.CombinedOutput(); err != nil {
		return nil, csInternal("cscli decisions add", err)
	}
	// cscli decisions add doesn't return the new ID — look up by (scope,value).
	id := csLookupDecisionID(ctx, canonical, value)
	return csDecisionsAddResponse{ID: id}, nil
}

func csLookupDecisionID(ctx context.Context, canonicalScope, value string) int {
	out, err := runCscliJSON(ctx, "decisions", "list", "--scope", canonicalScope, "--value", value)
	if err != nil {
		return 0
	}
	var raw []rawDecisionEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return 0
	}
	for _, entry := range raw {
		for _, d := range entry.Decisions {
			if d.Value == value {
				return d.ID
			}
		}
	}
	return 0
}

// ---- security.crowdsec.decisions.delete ------------------------------------

type csDecisionsDeleteParams struct {
	ID int    `json:"id,omitempty"`
	IP string `json:"ip,omitempty"`
}

func csDecisionsDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csDecisionsDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	args := []string{"decisions", "delete"}
	switch {
	case p.ID > 0:
		args = append(args, "--id", strconv.Itoa(p.ID))
	case p.IP != "":
		// A CIDR must go to --range: cscli --ip only matches Ip-scoped
		// decisions, so a Range unban via --ip silently deletes nothing.
		if _, _, err := net.ParseCIDR(p.IP); err == nil {
			args = append(args, "--range", p.IP)
		} else if ip := net.ParseIP(p.IP); ip != nil {
			args = append(args, "--ip", p.IP)
		} else {
			return nil, csInvalidArg("ip must be IP or CIDR")
		}
	default:
		return nil, csInvalidArg("either id or ip required")
	}
	out, err := exec.CommandContext(ctx, "cscli", args...).CombinedOutput()
	if err != nil {
		return nil, csInternal("cscli decisions delete", err)
	}
	// cscli exits 0 even when nothing matched ("0 decision(s) deleted") —
	// surface that as not-found instead of a false deleted:true.
	if n, ok := cscliDeletedCount(string(out)); ok && n == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "no matching decision"}
	}
	return map[string]bool{"deleted": true}, nil
}

// cscliDeletedCount extracts N from cscli's "N decision(s) deleted" output.
// ok=false when the marker is absent (output format drift) — callers must
// then assume success rather than fail a real deletion.
func cscliDeletedCount(out string) (int, bool) {
	m := csDeletedCountRE.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

var csDeletedCountRE = regexp.MustCompile(`(\d+)\s+decision\(?s?\)?\s+deleted`)

// ---- security.crowdsec.bouncers.list ---------------------------------------

type csBouncer struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Revoked  bool   `json:"revoked"`
	LastPull string `json:"last_pull"`
}

type csBouncersListResponse struct {
	Bouncers []csBouncer `json:"bouncers"`
}

func csBouncersListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	out, err := runCscliJSON(ctx, "bouncers", "list")
	if err != nil {
		// LAPI not yet reachable (fresh box pre-init, db migration in
		// progress, or stopped service) — return empty rather than 502
		// so the UI's Bouncers section renders a clean "no bouncers".
		return csBouncersListResponse{Bouncers: []csBouncer{}}, nil
	}
	resp := csBouncersListResponse{Bouncers: []csBouncer{}}
	var raw []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Revoked  bool   `json:"revoked"`
		LastPull string `json:"last_pull"`
	}
	if err := json.Unmarshal(out, &raw); err == nil {
		for _, r := range raw {
			resp.Bouncers = append(resp.Bouncers, csBouncer(r))
		}
	}
	return resp, nil
}

// ---- security.crowdsec.blocklists.list --------------------------------------

// csBlocklistEntry summarises one community blocklist as seen from the
// engine's perspective: its scenario name + how many active decisions
// it currently contributes. Subscriptions themselves are managed at
// app.crowdsec.net; this view tells the operator which lists are
// actually pulling IPs onto their box right now.
type csBlocklistEntry struct {
	Name      string `json:"name"`       // scenario, e.g. "lists:firehol_level1"
	Count     int    `json:"count"`      // active decisions from this list
	LatestEnd string `json:"latest_end"` // last decision expiry seen for the list
}

type csBlocklistsResponse struct {
	Blocklists []csBlocklistEntry `json:"blocklists"`
	Total      int                `json:"total"`
}

// csBlocklistsListHandler aggregates every active decision by
// (origin, scenario). Queries cscli per-origin because the bare
// `cscli decisions list -o json` groups by alert and reports each
// outer alert's top-level origin only — so CAPI decisions inside a
// firehol-import alert never appear. Querying with --origin <X>
// instead returns every matching decision regardless of which
// alert it lives under. Operator gets a complete picture: CAPI
// (Central API pulls), lists (community blocklist scenarios),
// cscli-import (manual firehol bulk import), crowdsec (local
// detection), console (decisions pushed from app.crowdsec.net),
// manual (cscli decisions add).
//
// Name format: "<origin>/<scenario>".
// blocklistsCache holds a pre-computed snapshot updated by a
// background goroutine started at agent boot. UI hits the handler →
// returns the cached snapshot in microseconds, never triggers cscli.
// The refresher itself runs every 5 minutes (or on-demand at boot).
//
// Rationale: 22k+ decisions × 6 origins × every UI tab click was
// melting crowdsec at ~300% CPU. Background refresh decouples UI
// frequency from cscli load. Empty result during boot warm-up is
// fine — UI shows "no active decisions yet" until first tick.
var (
	blocklistsCacheMu      sync.RWMutex
	blocklistsCacheValue   csBlocklistsResponse
	blocklistsCacheUpdated time.Time
	// blocklistsRefreshing guards the synchronous Refresh-button path:
	// the 30s throttle is a check-then-act, so without an in-flight
	// CAS two concurrent Refresh clicks both pass the throttle and
	// fire two parallel cscli storms — the exact thundering-herd the
	// cache exists to prevent.
	blocklistsRefreshing atomic.Bool
)

const blocklistsRefreshInterval = 30 * time.Minute

func csBlocklistsListHandler(_ context.Context, _ json.RawMessage) (any, error) {
	blocklistsCacheMu.RLock()
	defer blocklistsCacheMu.RUnlock()
	return blocklistsCacheValue, nil
}

// csBlocklistsRefreshHandler forces a synchronous snapshot refresh.
// Throttled: refuses with cached data if the last refresh ran less
// than 30s ago — operator-spam-proof.
func csBlocklistsRefreshHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	blocklistsCacheMu.RLock()
	tooRecent := time.Since(blocklistsCacheUpdated) < 30*time.Second && !blocklistsCacheUpdated.IsZero()
	blocklistsCacheMu.RUnlock()
	// CAS in-flight guard. Only the goroutine that flips false->true
	// runs the refresh; concurrent Refresh clicks fall through to the
	// cached snapshot instead of stacking parallel cscli runs.
	if !tooRecent && blocklistsRefreshing.CompareAndSwap(false, true) {
		defer blocklistsRefreshing.Store(false)
		blocklistsRefreshOnce(ctx)
	}
	blocklistsCacheMu.RLock()
	defer blocklistsCacheMu.RUnlock()
	return blocklistsCacheValue, nil
}

// blocklistsRefreshOnce does one cscli pass + replaces the cache atomically.
// Errors are swallowed — keep the last good snapshot on transient failures.
func blocklistsRefreshOnce(ctx context.Context) {
	// Raw-CSV snapshot (security_crowdsec_blocklist.go) — replaces the
	// old 6×(100k JSON objects) every-5-min pass that pegged crowdsec
	// at 100% CPU. Errors inside are swallowed; keep the last good
	// snapshot on transient failure.
	resp := refreshBlocklistsSnapshot(ctx)
	sort.Slice(resp.Blocklists, func(i, j int) bool {
		return resp.Blocklists[i].Count > resp.Blocklists[j].Count
	})
	blocklistsCacheMu.Lock()
	blocklistsCacheValue = resp
	blocklistsCacheUpdated = time.Now()
	blocklistsCacheMu.Unlock()
}

// StartBlocklistsRefresher kicks off the background refresh goroutine.
// Called once by the agent's main on startup.
func StartBlocklistsRefresher(ctx context.Context) {
	go func() {
		// Initial pull, then tick.
		blocklistsRefreshOnce(ctx)
		t := time.NewTicker(blocklistsRefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				blocklistsRefreshOnce(ctx)
			}
		}
	}()
}

// ---- security.crowdsec.metrics ---------------------------------------------

type csMetricsResponse struct {
	Parsed          int `json:"parsed"`
	Unparsed        int `json:"unparsed"`
	Buckets         int `json:"buckets"`
	DecisionsActive int `json:"decisions_active"`
	AlertsTotal     int `json:"alerts_total"`
}

func csMetricsHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// `cscli metrics -o json` + `cscli decisions list` + `cscli alerts
	// list` are independent — fan them out so the slowest call (usually
	// decisions list after a Firehol import drops 100k+ rows into the
	// table) doesn't serialize the other two.
	//
	// decisions list is bounded with --limit 100000 so a runaway hub
	// pull doesn't push the page into a 30s+ timeout window. UI shows
	// the count "as known"; the cap is generous enough that real
	// active-ban counts don't exceed it under normal operation.

	type result struct {
		raw  map[string]any
		dlen int
		alen int
		err  error
		kind string
	}

	resCh := make(chan result, 3)

	go func() {
		out, err := runCscliJSON(ctx, "metrics")
		if err != nil {
			resCh <- result{kind: "metrics", err: err}
			return
		}
		var raw map[string]any
		_ = json.Unmarshal(out, &raw)
		resCh <- result{kind: "metrics", raw: raw}
	}()

	go func() {
		d, err := runCscliJSON(ctx, "decisions", "list", "--limit", "100000")
		if err != nil {
			resCh <- result{kind: "decisions", err: err}
			return
		}
		var entries []rawDecisionEntry
		_ = json.Unmarshal(d, &entries)
		count := 0
		for _, e := range entries {
			count += len(e.Decisions)
		}
		resCh <- result{kind: "decisions", dlen: count}
	}()

	go func() {
		a, err := runCscliJSON(ctx, "alerts", "list")
		if err != nil {
			resCh <- result{kind: "alerts", err: err}
			return
		}
		var alerts []any
		_ = json.Unmarshal(a, &alerts)
		resCh <- result{kind: "alerts", alen: len(alerts)}
	}()

	resp := csMetricsResponse{}
	// "metrics" is the only fatal one — without it the dashboard has
	// no Parsed / Unparsed / Buckets to render. decisions + alerts
	// counts default to 0 on their individual failure so a slow LAPI
	// doesn't tank the whole tab.
	var metricsErr error
	for i := 0; i < 3; i++ {
		r := <-resCh
		switch r.kind {
		case "metrics":
			if r.err != nil {
				metricsErr = r.err
				continue
			}
			resp.Parsed = sumInt(r.raw, "parsers", "")
			resp.Unparsed = sumInt(r.raw, "unparsed", "")
			resp.Buckets = sumInt(r.raw, "buckets", "")
		case "decisions":
			if r.err == nil {
				resp.DecisionsActive = r.dlen
			}
		case "alerts":
			if r.err == nil {
				resp.AlertsTotal = r.alen
			}
		}
	}
	if metricsErr != nil {
		return nil, csInternal("cscli metrics", metricsErr)
	}
	return resp, nil
}

// sumInt walks a {section: {key: {counter: N, ...}}} structure and sums all numeric
// leaf values under the named section. Handles cscli's verbose metrics output
// without coupling us to the full schema.
func sumInt(raw map[string]any, section, _ string) int {
	v, ok := raw[section].(map[string]any)
	if !ok {
		return 0
	}
	total := 0
	for _, sub := range v {
		switch s := sub.(type) {
		case map[string]any:
			for _, leaf := range s {
				if f, ok := leaf.(float64); ok {
					total += int(f)
				}
			}
		case float64:
			total += int(s)
		}
	}
	return total
}

// ---- security.crowdsec.hub.list --------------------------------------------

type csHubListParams struct {
	Type string `json:"type,omitempty"`
}

type csHubItem struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
}

type csHubListResponse struct {
	Items []csHubItem `json:"items"`
}

var validHubTypes = map[string]bool{
	"collections": true, "parsers": true, "scenarios": true, "postoverflows": true, "appsec-rules": true, "appsec-configs": true,
}

func csHubListHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csHubListParams
	_ = json.Unmarshal(params, &p)
	if p.Type != "" && !validHubTypes[p.Type] {
		return nil, csInvalidArg("type must be collections|parsers|scenarios|postoverflows|appsec-rules|appsec-configs")
	}
	// `cscli hub list -a` returns ALL hub items (installed + available);
	// without -a only installed items come back. We need the full set so
	// the UI can show "available to install" entries.
	out, err := runCscliJSON(ctx, "hub", "list", "-a")
	if err != nil {
		return nil, csInternal("cscli hub list -a", err)
	}
	// cscli omits `installed` / `enabled` boolean fields. Installed state
	// is derived from `local_path` being non-empty (item exists on disk),
	// and enabled state from the `status` string containing "enabled".
	resp := csHubListResponse{Items: []csHubItem{}}
	var raw map[string][]struct {
		Name      string `json:"name"`
		LocalPath string `json:"local_path"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(out, &raw); err == nil {
		for typ, items := range raw {
			if p.Type != "" && typ != p.Type {
				continue
			}
			for _, it := range items {
				installed := it.LocalPath != ""
				enabled := installed && strings.Contains(it.Status, "enabled") && !strings.Contains(it.Status, "disabled")
				resp.Items = append(resp.Items, csHubItem{
					Name: it.Name, Type: typ, Installed: installed, Enabled: enabled,
				})
			}
		}
	}
	return resp, nil
}

// ---- security.crowdsec.hub.{install,remove} -------------------------------
//
// Wraps `cscli <type> install|remove <name>` for curated free hub items.
// Reloads crowdsec on success so newly installed parsers/scenarios take
// effect without operator intervention. Type is whitelisted against
// validHubTypes to prevent shell smuggling via the cscli verb.

type csHubMutateParams struct {
	Type string `json:"type"`
	Name string `json:"name"`
	// Force allows reinstall when already present (cscli --force).
	Force bool `json:"force,omitempty"`
}

// hubItemNameRE matches `crowdsecurity/sshd`, `crowdsecurity/appsec-virtual-patching`,
// `myorg/my-collection.v2`. Rejects shell metacharacters by construction.
var hubItemNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func csHubInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csHubMutateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg("invalid params")
	}
	if !validHubTypes[p.Type] {
		return nil, csInvalidArg("type must be collections|parsers|scenarios|postoverflows|appsec-rules|appsec-configs")
	}
	if !hubItemNameRE.MatchString(p.Name) {
		return nil, csInvalidArg("name must match <author>/<item>")
	}
	args := []string{p.Type, "install", p.Name}
	if p.Force {
		args = append(args, "--force")
	}
	cmd := exec.CommandContext(ctx, "cscli", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, csInternal(fmt.Sprintf("cscli %s install: %s", p.Type, strings.TrimSpace(string(out))), err)
	}
	// GH #711: a failed reload means the newly-installed item is NOT live yet —
	// don't report success, or the UI claims protection that isn't loaded.
	if out, err := exec.CommandContext(ctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		return nil, csInternal(fmt.Sprintf("installed %s %s but crowdsec reload failed (item not live): %s", p.Type, p.Name, strings.TrimSpace(string(out))), err)
	}
	return map[string]any{"type": p.Type, "name": p.Name, "installed": true}, nil
}

func csHubRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csHubMutateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg("invalid params")
	}
	if !validHubTypes[p.Type] {
		return nil, csInvalidArg("type must be collections|parsers|scenarios|postoverflows|appsec-rules|appsec-configs")
	}
	if !hubItemNameRE.MatchString(p.Name) {
		return nil, csInvalidArg("name must match <author>/<item>")
	}
	cmd := exec.CommandContext(ctx, "cscli", p.Type, "remove", p.Name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, csInternal(fmt.Sprintf("cscli %s remove: %s", p.Type, strings.TrimSpace(string(out))), err)
	}
	// GH #711: a failed reload means the removed item may STILL be loaded.
	if out, err := exec.CommandContext(ctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		return nil, csInternal(fmt.Sprintf("removed %s %s but crowdsec reload failed (item may still be live): %s", p.Type, p.Name, strings.TrimSpace(string(out))), err)
	}
	return map[string]any{"type": p.Type, "name": p.Name, "installed": false}, nil
}

// ---- security.crowdsec.appsec.geoblock.{get,set} --------------------------

// appsecRulePath is where our single server-wide geoblock rule lives.
// Written by appsecGeoblockSetHandler, read by appsecGeoblockGetHandler.
// install.sh `install_crowdsec_appsec` seeds the file at mode=off so
// crowdsec can parse the appsec-config on first boot.
//
// M27 fix: pre_eval hooks live in appsec-CONFIG, not appsec-rules.
// Earlier path /etc/crowdsec/appsec-rules/jabali-geoblock.yaml was
// loaded as a rule but the pre_eval hook never fired (rules use a
// different schema with `rules:` block, not `pre_eval:`).
const appsecRulePath = "/etc/crowdsec/appsec-configs/jabali-appsec.yaml"

type csAppSecGeoblockGetResponse struct {
	Mode      string   `json:"mode"`
	Countries []string `json:"countries"`
}

type csAppSecGeoblockSetParams struct {
	Mode      string   `json:"mode"`
	Countries []string `json:"countries"`
}

// csAppSecGeoblockModes is the closed enum the UI + panel-api + agent
// all share. Keep in sync with the ADR.
var csAppSecGeoblockModes = map[string]struct{}{
	"off":   {},
	"allow": {},
	"deny":  {},
}

// csCountryCodeRE pins 2-letter ISO 3166-1 alpha-2. CrowdSec's
// GeoIPEnrich returns ISO codes in this shape; anything else fails the
// `not in [...]` filter silently.
var csCountryCodeRE = regexp.MustCompile(`^[A-Z]{2}$`)

func csAppSecGeoblockGetHandler(_ context.Context, _ json.RawMessage) (any, error) {
	resp := csAppSecGeoblockGetResponse{Mode: "off", Countries: []string{}}
	body, err := os.ReadFile(appsecRulePath)
	if err != nil {
		// Missing rule file → mode off. install.sh seeds it, so this
		// only fires on systems that predate M26 AppSec or that the
		// operator has hand-deleted.
		if os.IsNotExist(err) {
			return resp, nil
		}
		return nil, csInternal("read appsec rule", err)
	}
	// The rule file carries `# jabali-mode: <mode>` and
	// `# jabali-countries: <csv>` as comment markers so we don't
	// roundtrip through a full YAML parser for the readback.
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "# jabali-mode:"):
			resp.Mode = strings.TrimSpace(strings.TrimPrefix(line, "# jabali-mode:"))
		case strings.HasPrefix(line, "# jabali-countries:"):
			csv := strings.TrimSpace(strings.TrimPrefix(line, "# jabali-countries:"))
			if csv != "" {
				resp.Countries = strings.Split(csv, ",")
			}
		}
	}
	if _, ok := csAppSecGeoblockModes[resp.Mode]; !ok {
		resp.Mode = "off"
	}
	return resp, nil
}

func csAppSecGeoblockSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csAppSecGeoblockSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if _, ok := csAppSecGeoblockModes[p.Mode]; !ok {
		return nil, csInvalidArg("mode must be off|allow|deny")
	}
	// Uppercase + dedupe + validate country codes.
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(p.Countries))
	for _, c := range p.Countries {
		code := strings.ToUpper(strings.TrimSpace(c))
		if code == "" {
			continue
		}
		if !csCountryCodeRE.MatchString(code) {
			return nil, csInvalidArg(fmt.Sprintf("country %q must be a 2-letter ISO 3166-1 code", c))
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		cleaned = append(cleaned, code)
	}
	if (p.Mode == "allow" || p.Mode == "deny") && len(cleaned) == 0 {
		return nil, csInvalidArg("mode " + p.Mode + " requires at least one country")
	}
	body := renderAppSecGeoblockRule(p.Mode, cleaned)
	if err := os.MkdirAll("/etc/crowdsec/appsec-configs", 0o755); err != nil {
		return nil, csInternal("mkdir appsec-configs", err)
	}
	// Best-effort cleanup of pre-M27-fix path. Safe to leave if absent.
	_ = os.Remove("/etc/crowdsec/appsec-rules/jabali-geoblock.yaml")
	tmp := appsecRulePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return nil, csInternal("write appsec rule tmp", err)
	}
	if err := os.Rename(tmp, appsecRulePath); err != nil {
		return nil, csInternal("rename appsec rule", err)
	}
	// Reload — SIGHUP re-reads rules without dropping the LAPI socket.
	// `systemctl reload crowdsec` sends SIGHUP when ExecReload is set
	// (crowdsec.service ships that). If reload is not wired (older
	// packaging) fall back to restart.
	if out, err := exec.CommandContext(ctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		if out2, err2 := exec.CommandContext(ctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err2 != nil {
			return nil, csInternal("systemctl reload/restart crowdsec",
				fmt.Errorf("reload: %v %s; restart: %v %s", err, out, err2, out2))
		}
	}
	return csAppSecGeoblockGetResponse{Mode: p.Mode, Countries: cleaned}, nil
}

// renderAppSecGeoblockRule emits the YAML rule body. The filter uses
// CrowdSec's expr syntax with GeoIPEnrich — see
// https://doc.crowdsec.net/docs/next/appsec/rules_examples/#5-geoblocking.
// The two `# jabali-...` markers at the top let the get handler parse
// (mode, countries) back out without a YAML round-trip.
func renderAppSecGeoblockRule(mode string, countries []string) string {
	// Single source: internal/appseccfg (ADR-0083 sibling; the
	// ADR-0102 admin-API allowlist is now canonical here, not a
	// second hand-written copy that drifts vs install.sh). The agent
	// uses its fixed inband set; install.sh presence-gates its own
	// set through the same Render().
	//
	// Webmail allowlist: load the panel-api reconciler's state file
	// so a geoblock toggle never wipes the webmail-vhost on_match
	// allowlist that the panel last wrote. Missing file → nil → no
	// webmail filter; the panel reconciler writes it within ~60s
	// of email_enabled changing, so any gap is brief.
	webmailHosts, _ := appseccfg.LoadWebmailHosts(appseccfg.WebmailHostsPath)
	return appseccfg.Render(appseccfg.Opts{
		Mode:      mode,
		Countries: countries,
		Inband: []string{
			"crowdsecurity/base-config",
			"crowdsecurity/vpatch-*",
			"crowdsecurity/generic-*",
		},
		AdminAllowlist: true,
		PanelHost:      appsecPanelHost(),
		WebmailHosts:   webmailHosts,
	})
}

// ---- M27 Step 2: security.crowdsec.allowlists.{list,add,remove} -----------

// jabaliAllowlistName is the single server-wide allowlist jabali manages
// (ADR-0061). Pinned so repeated add/remove calls are idempotent.
const jabaliAllowlistName = "jabali-admin-allowlist"

type csAllowlistEntryWire struct {
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

// cscli allowlists inspect payload (v1.7.x). Top-level includes name + items.
type rawAllowlistInspect struct {
	Items []struct {
		Value       string `json:"value"`
		Description string `json:"description"`
		CreatedAt   string `json:"created_at"`
	} `json:"items"`
}

func csAllowlistsListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	out, err := runCscliJSON(ctx, "allowlists", "inspect", jabaliAllowlistName)
	if err != nil {
		// cscli exits nonzero when the allowlist doesn't exist. Surface
		// as empty list — first list before first add is a normal state.
		return map[string]any{"items": []csAllowlistEntryWire{}}, nil
	}
	var raw rawAllowlistInspect
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, csInternal("parse cscli allowlists inspect", err)
	}
	items := make([]csAllowlistEntryWire, 0, len(raw.Items))
	for _, it := range raw.Items {
		items = append(items, csAllowlistEntryWire{
			Value:     it.Value,
			Reason:    it.Description,
			CreatedAt: it.CreatedAt,
		})
	}
	return map[string]any{"items": items}, nil
}

type csAllowlistAddParams struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
	// Expiration is an optional Go duration (e.g. "168h"). Empty = permanent.
	// Used by the auto-whitelist-on-login flow to time-box user IPs.
	Expiration string `json:"expiration,omitempty"`
}

// ensureJabaliAllowlist creates the named allowlist if missing. Idempotent:
// if cscli reports "already exists" (nonzero exit + distinctive stderr)
// we treat it as success. We don't rely on the exit code alone because
// cscli versions differ.
func ensureJabaliAllowlist(ctx context.Context) error {
	check := exec.CommandContext(ctx, "cscli", "allowlists", "inspect", jabaliAllowlistName)
	if err := check.Run(); err == nil {
		return nil
	}
	create := exec.CommandContext(ctx, "cscli", "allowlists", "create",
		jabaliAllowlistName, "-d", "jabali-managed admin allowlist")
	out, err := create.CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			return nil
		}
		return fmt.Errorf("cscli allowlists create: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func csAllowlistsAddHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csAllowlistAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	value := strings.TrimSpace(p.Value)
	if value == "" {
		return nil, csInvalidArg("value is required")
	}
	// Validate IP or CIDR. Reject everything else (country, ASN — those
	// are decision scopes, not allowlist-value shapes).
	if net.ParseIP(value) == nil {
		if _, _, err := net.ParseCIDR(value); err != nil {
			return nil, csInvalidArg("value must be an IP or CIDR")
		}
	}
	if l := len(p.Reason); l < 3 || l > 200 {
		return nil, csInvalidArg("reason length must be 3..200")
	}
	expArg := ""
	if exp := strings.TrimSpace(p.Expiration); exp != "" {
		_, norm, derr := parseCrowdsecDuration(exp)
		if derr != nil {
			return nil, csInvalidArg("expiration must be a duration (e.g. \"168h\", \"7d\")")
		}
		expArg = norm
	}
	if err := addToJabaliAllowlist(ctx, value, p.Reason, expArg); err != nil {
		return nil, csInternal("allowlist add", err)
	}
	return map[string]any{"value": value}, nil
}

// addToJabaliAllowlist adds (or TTL-refreshes) value in the single jabali
// allowlist. Shared by the security.crowdsec.allowlists.add verb and the
// GH #598 login watcher. Caller pre-validates value (IP/CIDR), reason length,
// and expiration format; this focuses on the idempotent cscli mechanics.
// A re-add of an existing value is treated as success.
func addToJabaliAllowlist(ctx context.Context, value, reason, expiration string) error {
	if err := ensureJabaliAllowlist(ctx); err != nil {
		return fmt.Errorf("ensure allowlist: %w", err)
	}
	args := []string{"allowlists", "add", jabaliAllowlistName, value, "-d", reason}
	if expiration != "" {
		// cscli `add` of an existing value is a no-op and does NOT refresh the
		// expiration. For the time-boxed login path we want each re-login to
		// slide the TTL forward, so best-effort remove first, then re-add.
		_ = exec.CommandContext(ctx, "cscli", "allowlists", "remove", jabaliAllowlistName, value).Run()
		args = append(args, "-e", expiration)
	}
	out, err := exec.CommandContext(ctx, "cscli", args...).CombinedOutput()
	if err != nil && expiration != "" {
		// The remove above already happened: a failed re-add would leave a
		// previously-allowlisted value DROPPED (an admin IP falling out of the
		// allowlist mid-session = self-lockout). Retry once, then restore
		// without the TTL flag — preserving the pre-call allowlisted state is
		// strictly safer than silently losing it; the entry stays visible in
		// the panel either way.
		out, err = exec.CommandContext(ctx, "cscli", args...).CombinedOutput()
		if err != nil {
			base := args[:len(args)-2] // strip "-e", expiration
			if rout, rerr := exec.CommandContext(ctx, "cscli", base...).CombinedOutput(); rerr == nil {
				return fmt.Errorf("cscli allowlists add with -e %s failed (%s); entry restored WITHOUT expiration", expiration, strings.TrimSpace(string(out)))
			} else {
				out = append(out, rout...)
			}
		}
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// Re-adding an already-allowlisted value is a no-op success for the
		// idempotent login refresh path.
		if strings.Contains(strings.ToLower(msg), "already") {
			return nil
		}
		return fmt.Errorf("cscli allowlists add: %s: %w", msg, err)
	}
	return nil
}

type csAllowlistRemoveParams struct {
	Value string `json:"value"`
}

func csAllowlistsRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csAllowlistRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	value := strings.TrimSpace(p.Value)
	if value == "" {
		return nil, csInvalidArg("value is required")
	}
	cmd := exec.CommandContext(ctx, "cscli", "allowlists", "remove",
		jabaliAllowlistName, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "allowlist entry not found"}
		}
		return nil, csInternal(fmt.Sprintf("cscli allowlists remove: %s", strings.TrimSpace(string(out))), err)
	}
	return map[string]any{}, nil
}

// ---- M27 Step 3: security.crowdsec.alerts.{list,inspect} -----------------

type csAlertWire struct {
	ID             int    `json:"id"`
	Scenario       string `json:"scenario"`
	SourceIP       string `json:"source_ip"`
	SourceScope    string `json:"source_scope"`
	SourceValue    string `json:"source_value"`
	EventsCount    int    `json:"events_count"`
	DecisionsCount int    `json:"decisions_count"`
	StartedAt      string `json:"started_at"`
	StoppedAt      string `json:"stopped_at"`
	MachineID      string `json:"machine_id"`
}

type rawAlertEntry struct {
	ID          int    `json:"id"`
	Scenario    string `json:"scenario"`
	MachineID   string `json:"machine_id"`
	StartAt     string `json:"start_at"`
	StopAt      string `json:"stop_at"`
	EventsCount int    `json:"events_count"`
	Source      struct {
		IP    string `json:"ip"`
		Scope string `json:"scope"`
		Value string `json:"value"`
	} `json:"source"`
	Decisions []json.RawMessage `json:"decisions"`
}

func csAlertsListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// Cap to 100 alerts within the last 24h. Server-chosen to avoid
	// OOMing the panel on a busy host; operator can still `cscli alerts
	// list --all` from the host if they need more.
	out, err := runCscliJSON(ctx, "alerts", "list", "--since", "24h", "--limit", "100")
	if err != nil {
		return nil, csInternal("cscli alerts list", err)
	}
	var raw []rawAlertEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, csInternal("parse cscli alerts list", err)
	}
	items := make([]csAlertWire, 0, len(raw))
	for _, a := range raw {
		items = append(items, csAlertWire{
			ID:             a.ID,
			Scenario:       a.Scenario,
			SourceIP:       a.Source.IP,
			SourceScope:    a.Source.Scope,
			SourceValue:    a.Source.Value,
			EventsCount:    a.EventsCount,
			DecisionsCount: len(a.Decisions),
			StartedAt:      a.StartAt,
			StoppedAt:      a.StopAt,
			MachineID:      a.MachineID,
		})
	}
	return map[string]any{"items": items}, nil
}

// csAlertsTimeseriesHandler returns bucketed alert counts over a
// rolling window. UI renders this as the "Alerts over time" chart on
// the Security → CrowdSec engine dashboard. Bucket size is fixed per
// window (hour for 24h, day for 7d/30d) so the response stays compact
// regardless of alert volume.
// csAlertsTopSourcesHandler returns the top-N source IPs (or country/
// AS for non-IP scopes) by alert count over a rolling window. UI uses
// this on the engine-dashboard "Top sources" card. Like timeseries,
// this leans on cscli alerts list + server-side aggregation so the
// payload stays compact.
type csAlertsTopSourcesParams struct {
	Since string `json:"since"` // "24h" | "7d" | "30d"
	Limit int    `json:"limit"` // 1..50, default 10
}

func csAlertsTopSourcesHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csAlertsTopSourcesParams
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if p.Since == "" {
		p.Since = "24h"
	}
	switch p.Since {
	case "24h", "7d", "30d":
	default:
		return nil, csInvalidArg("since must be 24h|7d|30d")
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}
	if p.Limit > 50 {
		p.Limit = 50
	}

	out, err := runCscliJSON(ctx, "alerts", "list", "--since", p.Since, "--limit", "0")
	if err != nil {
		return nil, csInternal("cscli alerts list top-sources", err)
	}
	var raw []rawAlertEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, csInternal("parse alerts json", err)
	}

	type agg struct {
		Value     string
		Scope     string
		Count     int
		Scenarios map[string]struct{}
	}
	byKey := make(map[string]*agg, 64)
	for _, a := range raw {
		key := a.Source.Scope + ":" + a.Source.Value
		if a.Source.Value == "" {
			key = a.Source.Scope + ":" + a.Source.IP
		}
		if e, ok := byKey[key]; ok {
			e.Count++
			if a.Scenario != "" {
				e.Scenarios[a.Scenario] = struct{}{}
			}
			continue
		}
		val := a.Source.Value
		if val == "" {
			val = a.Source.IP
		}
		byKey[key] = &agg{
			Value:     val,
			Scope:     a.Source.Scope,
			Count:     1,
			Scenarios: map[string]struct{}{a.Scenario: {}},
		}
	}

	flat := make([]*agg, 0, len(byKey))
	for _, e := range byKey {
		flat = append(flat, e)
	}
	sort.Slice(flat, func(i, j int) bool {
		if flat[i].Count != flat[j].Count {
			return flat[i].Count > flat[j].Count
		}
		return flat[i].Value < flat[j].Value
	})
	if len(flat) > p.Limit {
		flat = flat[:p.Limit]
	}
	items := make([]map[string]any, 0, len(flat))
	for _, e := range flat {
		scenarios := make([]string, 0, len(e.Scenarios))
		for s := range e.Scenarios {
			scenarios = append(scenarios, s)
		}
		sort.Strings(scenarios)
		items = append(items, map[string]any{
			"value":     e.Value,
			"scope":     e.Scope,
			"count":     e.Count,
			"scenarios": scenarios,
		})
	}
	return map[string]any{"items": items, "since": p.Since, "limit": p.Limit}, nil
}

type csAlertsTimeseriesParams struct {
	Since string `json:"since"` // "24h" | "7d" | "30d"
}

func csAlertsTimeseriesHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csAlertsTimeseriesParams
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if p.Since == "" {
		p.Since = "7d"
	}
	switch p.Since {
	case "24h", "7d", "30d":
	default:
		return nil, csInvalidArg("since must be 24h|7d|30d")
	}

	out, err := runCscliJSON(ctx, "alerts", "list", "--since", p.Since, "--limit", "0")
	if err != nil {
		return nil, csInternal("cscli alerts list timeseries", err)
	}
	var raw []rawAlertEntry
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, csInternal("parse alerts json", err)
	}

	bucketSize := time.Hour
	pointsHint := 24
	if p.Since == "7d" {
		bucketSize = 24 * time.Hour
		pointsHint = 7
	} else if p.Since == "30d" {
		bucketSize = 24 * time.Hour
		pointsHint = 30
	}

	buckets := make(map[time.Time]int, pointsHint)
	for _, a := range raw {
		ts, err := time.Parse(time.RFC3339, a.StartAt)
		if err != nil {
			continue
		}
		buckets[ts.UTC().Truncate(bucketSize)]++
	}

	now := time.Now().UTC().Truncate(bucketSize)
	for i := 0; i < pointsHint; i++ {
		t := now.Add(-time.Duration(pointsHint-1-i) * bucketSize)
		if _, ok := buckets[t]; !ok {
			buckets[t] = 0
		}
	}

	keys := make([]time.Time, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	points := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		points = append(points, map[string]any{
			"ts":    k.Format(time.RFC3339),
			"count": buckets[k],
		})
	}
	return map[string]any{
		"buckets":     points,
		"bucket_size": bucketSize.String(),
		"since":       p.Since,
	}, nil
}

type csAlertsInspectParams struct {
	ID int `json:"id"`
}

func csAlertsInspectHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csAlertsInspectParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if p.ID <= 0 {
		return nil, csInvalidArg("id must be a positive integer")
	}
	out, err := runCscliJSON(ctx, "alerts", "inspect", strconv.Itoa(p.ID), "--details")
	if err != nil {
		return nil, csInternal("cscli alerts inspect", err)
	}
	// Passthrough — the detail shape is rich (meta + events + decisions)
	// and the UI renders it via Descriptions + nested Table.
	var raw json.RawMessage = out
	return map[string]any{"alert": raw}, nil
}

// ---- M27 Step 6: security.crowdsec.scenarios.list + profiles.{get,set} ----

const (
	profilesPath       = "/etc/crowdsec/profiles.yaml"
	profilesBeginMark  = "# jabali-begin-overrides"
	profilesEndMark    = "# jabali-end-overrides"
	profilesWarningCmt = "# DO NOT HAND-EDIT — rewritten by jabali on Save. Edits inside these markers are lost.\n# To add manual profiles, place them AFTER the jabali-end-overrides line below."
)

type rawScenarioEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type csScenarioWire struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func csScenariosListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	out, err := runCscliJSON(ctx, "scenarios", "list")
	if err != nil {
		return nil, csInternal("cscli scenarios list", err)
	}
	var wrap struct {
		Scenarios []rawScenarioEntry `json:"scenarios"`
	}
	if err := json.Unmarshal(out, &wrap); err != nil {
		return nil, csInternal("parse scenarios", err)
	}
	items := make([]csScenarioWire, 0, len(wrap.Scenarios))
	for _, s := range wrap.Scenarios {
		items = append(items, csScenarioWire{Name: s.Name, Description: s.Description})
	}
	return map[string]any{"items": items}, nil
}

type csProfileOverride struct {
	Scenario string `json:"scenario"`
	Action   string `json:"action"` // captcha | off
}

// installedScenarioNames returns the set of installed CrowdSec scenario names
// (GH #680) so profile overrides can be rejected for scenarios that don't
// exist — an unknown scenario in profiles.yaml is a silent no-op that hides a
// misconfiguration.
func installedScenarioNames(ctx context.Context) (map[string]bool, error) {
	out, err := runCscliJSON(ctx, "scenarios", "list")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Scenarios []rawScenarioEntry `json:"scenarios"`
	}
	if err := json.Unmarshal(out, &wrap); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(wrap.Scenarios))
	for _, sc := range wrap.Scenarios {
		set[sc.Name] = true
	}
	return set, nil
}

// scanOverridesFromProfiles parses the jabali marker-bounded block and
// returns the overrides previously persisted. Everything outside the
// markers is ignored.
func scanOverridesFromProfiles() ([]csProfileOverride, error) {
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []csProfileOverride{}, nil
		}
		return nil, err
	}
	text := string(raw)
	start := strings.Index(text, profilesBeginMark)
	end := strings.Index(text, profilesEndMark)
	if start < 0 || end < 0 || end < start {
		return []csProfileOverride{}, nil
	}
	block := text[start:end]
	// Minimal parse: find scenario filter + decision type per doc.
	// Format produced by renderOverrides is deterministic so we can
	// scan for `GetScenario() == "<name>"` + `type: <action>`.
	scenRE := regexp.MustCompile(`GetScenario\(\) == "([^"]+)"`)
	typeRE := regexp.MustCompile(`type:\s*(captcha|bypass)`)
	scens := scenRE.FindAllStringSubmatch(block, -1)
	types := typeRE.FindAllStringSubmatch(block, -1)
	out := make([]csProfileOverride, 0, len(scens))
	for i, s := range scens {
		if i >= len(types) {
			break
		}
		action := "off"
		if types[i][1] == "captcha" {
			action = "captcha"
		}
		out = append(out, csProfileOverride{Scenario: s[1], Action: action})
	}
	return out, nil
}

func csProfilesGetHandler(_ context.Context, _ json.RawMessage) (any, error) {
	overrides, err := scanOverridesFromProfiles()
	if err != nil {
		return nil, csInternal("read profiles", err)
	}
	return map[string]any{"overrides": overrides}, nil
}

// renderOverridesBlock produces the jabali-managed marker block. Each
// override becomes one profile doc. "off" uses decision type=bypass
// (CrowdSec's built-in "skip this alert" remediation).
func renderOverridesBlock(overrides []csProfileOverride) string {
	if len(overrides) == 0 {
		return fmt.Sprintf("%s\n%s\n%s\n", profilesBeginMark, profilesWarningCmt, profilesEndMark)
	}
	var b strings.Builder
	b.WriteString(profilesBeginMark)
	b.WriteString("\n")
	b.WriteString(profilesWarningCmt)
	b.WriteString("\n---\n")
	for i, o := range overrides {
		action := "captcha"
		if o.Action == "off" {
			action = "bypass"
		}
		// Sanitize scenario name for the profile `name:` field — alnum
		// + dashes + slashes stay; everything else → '-'.
		safe := regexp.MustCompile(`[^A-Za-z0-9_/.-]`).ReplaceAllString(o.Scenario, "-")
		b.WriteString(fmt.Sprintf("name: jabali-override-%s\n", safe))
		b.WriteString(fmt.Sprintf("filters:\n - Alert.Remediation == true && Alert.GetScenario() == %q\n", o.Scenario))
		b.WriteString(fmt.Sprintf("decisions:\n - type: %s\n   duration: 4h\n", action))
		b.WriteString("on_success: break\n")
		if i < len(overrides)-1 {
			b.WriteString("---\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(profilesEndMark)
	b.WriteString("\n")
	return b.String()
}

type csProfilesSetParams struct {
	Overrides []csProfileOverride `json:"overrides"`
}

func csProfilesSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csProfilesSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	installedScenarios, sErr := installedScenarioNames(ctx)
	if sErr != nil {
		return nil, csInternal("list installed scenarios", sErr)
	}
	for _, o := range p.Overrides {
		if o.Action != "captcha" && o.Action != "off" {
			return nil, csInvalidArg(`action must be "captcha" or "off"`)
		}
		if o.Scenario == "" {
			return nil, csInvalidArg("scenario name required")
		}
		if !installedScenarios[o.Scenario] {
			return nil, csInvalidArg("unknown scenario (not installed): " + o.Scenario)
		}
	}
	raw, err := os.ReadFile(profilesPath)
	if err != nil {
		return nil, csInternal("read profiles.yaml", err)
	}
	text := string(raw)
	newBlock := renderOverridesBlock(p.Overrides)

	// Strip any existing jabali block (marker-bounded).
	startIdx := strings.Index(text, profilesBeginMark)
	endIdx := strings.Index(text, profilesEndMark)
	if startIdx >= 0 && endIdx >= 0 && endIdx > startIdx {
		// Include trailing newline after endMark if present.
		endOfLine := strings.Index(text[endIdx:], "\n")
		if endOfLine >= 0 {
			endIdx = endIdx + endOfLine + 1
		} else {
			endIdx = endIdx + len(profilesEndMark)
		}
		text = text[:startIdx] + text[endIdx:]
	}
	// Insert new block at the very top so jabali profiles evaluate first.
	text = newBlock + "---\n" + strings.TrimLeft(text, "\n")

	bak := profilesPath + ".bak"
	_ = os.WriteFile(bak, raw, 0o644)
	if err := os.WriteFile(profilesPath, []byte(text), 0o644); err != nil {
		return nil, csInternal("write profiles.yaml", err)
	}

	// Pre-flight: `crowdsec -t` (exists in v1.7.x per probe).
	if out, terr := exec.CommandContext(ctx, "crowdsec", "-t").CombinedOutput(); terr != nil {
		// Restore backup + surface error.
		_ = os.WriteFile(profilesPath, raw, 0o644)
		return nil, csInternal(fmt.Sprintf("crowdsec -t: %s", strings.TrimSpace(string(out))), terr)
	}
	if err := exec.CommandContext(ctx, "systemctl", "reload", "crowdsec").Run(); err != nil {
		_ = os.WriteFile(profilesPath, raw, 0o644)
		_ = exec.CommandContext(ctx, "systemctl", "reload", "crowdsec").Run()
		return nil, csInternal("systemctl reload crowdsec after write", err)
	}
	return map[string]any{"overrides": p.Overrides}, nil
}

// ---- M27 Step 5: security.crowdsec.captcha.apply -------------------------

const bouncerConfPath = "/etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf"

type csCaptchaApplyParams struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	SiteKey   string `json:"site_key"`
	SecretKey string `json:"secret_key"`
}

var validCaptchaProviders = map[string]bool{
	"":          true, // empty when disabled
	"hcaptcha":  true,
	"recaptcha": true,
	"turnstile": true,
}

// rewriteBouncerConfKeys updates specific KEY=value lines in the bouncer
// conf WITHOUT rewriting the whole file — the file was authored by
// install_crowdsec_nginx_bouncer (M26) with AppSec settings that must
// survive. Each key in `updates` replaces the existing line; missing
// keys are appended.
func rewriteBouncerConfKeys(path string, updates map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	seen := make(map[string]bool, len(updates))
	for i, line := range lines {
		for key, val := range updates {
			prefix := key + "="
			if strings.HasPrefix(line, prefix) {
				lines[i] = prefix + val
				seen[key] = true
			}
		}
	}
	for key, val := range updates {
		if !seen[key] {
			lines = append(lines, key+"="+val)
		}
	}
	out := strings.Join(lines, "\n")
	tmp, err := os.CreateTemp("", "bouncer-conf-*.tmp")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func csCaptchaApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p csCaptchaApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if !validCaptchaProviders[p.Provider] {
		return nil, csInvalidArg("provider must be hcaptcha|recaptcha|turnstile")
	}
	// When disabling, clear provider + set fallback back to ban.
	updates := map[string]string{}
	if p.Enabled {
		if p.Provider == "" {
			return nil, csInvalidArg("provider required when enabled")
		}
		if len(p.SiteKey) == 0 || len(p.SiteKey) > 512 {
			return nil, csInvalidArg("site_key length must be 1..512")
		}
		if len(p.SecretKey) == 0 || len(p.SecretKey) > 512 {
			return nil, csInvalidArg("secret_key required when enabling")
		}
		// GH #684: the keys are written as KEY=value lines into the bouncer conf;
		// a newline or other control char would inject additional config lines.
		hasCtrl := func(str string) bool {
			return strings.ContainsFunc(str, func(r rune) bool { return r < 0x20 || r == 0x7f })
		}
		if hasCtrl(p.Provider) || hasCtrl(p.SiteKey) || hasCtrl(p.SecretKey) {
			return nil, csInvalidArg("provider/site_key/secret_key must not contain newlines or control characters")
		}
		updates["CAPTCHA_PROVIDER"] = p.Provider
		updates["SITE_KEY"] = p.SiteKey
		updates["SECRET_KEY"] = p.SecretKey
		updates["FALLBACK_REMEDIATION"] = "captcha"
	} else {
		updates["CAPTCHA_PROVIDER"] = ""
		updates["SITE_KEY"] = ""
		updates["SECRET_KEY"] = ""
		updates["FALLBACK_REMEDIATION"] = "ban"
	}
	// Back up to .bak; restore on nginx-t failure.
	bakPath := bouncerConfPath + ".bak"
	if existing, err := os.ReadFile(bouncerConfPath); err == nil {
		_ = os.WriteFile(bakPath, existing, 0o600)
	}
	if err := rewriteBouncerConfKeys(bouncerConfPath, updates); err != nil {
		return nil, csInternal("rewrite bouncer conf", err)
	}
	if err := exec.CommandContext(ctx, "nginx", "-t").Run(); err != nil {
		// Restore from bak + surface the error to the operator.
		if bak, rerr := os.ReadFile(bakPath); rerr == nil {
			_ = os.WriteFile(bouncerConfPath, bak, 0o600)
		}
		return nil, csInternal("nginx -t failed after bouncer conf rewrite", err)
	}
	if err := exec.CommandContext(ctx, "systemctl", "reload", "nginx").Run(); err != nil {
		return nil, csInternal("systemctl reload nginx", err)
	}
	return map[string]any{"enabled": p.Enabled}, nil
}

func init() {
	Default.Register("security.crowdsec.status", csStatusHandler)
	Default.Register("security.crowdsec.decisions.list", csDecisionsListHandler)
	Default.Register("security.crowdsec.decisions.add", csDecisionsAddHandler)
	Default.Register("security.crowdsec.decisions.delete", csDecisionsDeleteHandler)
	Default.Register("security.crowdsec.bouncers.list", csBouncersListHandler)
	Default.Register("security.crowdsec.blocklists.list", csBlocklistsListHandler)
	Default.Register("security.crowdsec.blocklists.refresh", csBlocklistsRefreshHandler)
	Default.Register("security.crowdsec.metrics", csMetricsHandler)
	Default.Register("security.crowdsec.hub.list", csHubListHandler)
	Default.Register("security.crowdsec.hub.install", csHubInstallHandler)
	Default.Register("security.crowdsec.hub.remove", csHubRemoveHandler)
	Default.Register("security.crowdsec.appsec.geoblock.get", csAppSecGeoblockGetHandler)
	Default.Register("security.crowdsec.appsec.geoblock.set", csAppSecGeoblockSetHandler)
	Default.Register("security.crowdsec.allowlists.list", csAllowlistsListHandler)
	Default.Register("security.crowdsec.allowlists.add", csAllowlistsAddHandler)
	Default.Register("security.crowdsec.allowlists.remove", csAllowlistsRemoveHandler)
	Default.Register("security.crowdsec.alerts.list", csAlertsListHandler)
	Default.Register("security.crowdsec.alerts.inspect", csAlertsInspectHandler)
	Default.Register("security.crowdsec.alerts.timeseries", csAlertsTimeseriesHandler)
	Default.Register("security.crowdsec.alerts.top_sources", csAlertsTopSourcesHandler)
	Default.Register("security.crowdsec.captcha.apply", csCaptchaApplyHandler)
	Default.Register("security.crowdsec.scenarios.list", csScenariosListHandler)
	Default.Register("security.crowdsec.profiles.get", csProfilesGetHandler)
	Default.Register("security.crowdsec.profiles.set", csProfilesSetHandler)
}

// appsecPanelHost returns the panel FQDN used to scope the AppSec admin
// allowlist (GH #706). The panel hostname is the system hostname (M6.4); on
// error we return "" so the renderer falls back to path-only (never breaks the
// operator's phpMyAdmin/adminer access).
func appsecPanelHost() string {
	if h, err := os.Hostname(); err == nil {
		return strings.TrimSpace(h)
	}
	return ""
}
