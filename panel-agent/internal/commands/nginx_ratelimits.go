package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.ratelimits.apply — renders the single http{}-scope fragment
// that declares every `limit_req_zone` and `limit_conn_zone` a domain
// refers to. Per-vhost `limit_req` and `limit_conn` directives live
// inside each vhost file (wired via writeVhost); this command manages
// only the zone DECLARATIONS.
//
// M43 framing (2026-05-04): per-vhost rate limits are an ANTI-NOISE
// pre-filter (scraping resistance, burst smoothing) NOT a security
// layer. CrowdSec scenarios + AppSec own behavioural rate / attack
// detection. Don't lean on rate_limit_rps for security — see
// docs/security/decision-brains.md and ADR-0089.
//
// The fragment lives at /etc/nginx/conf.d/00-jabali-ratelimits.conf —
// the 00- prefix forces alphabetical load before any other conf.d file
// (review finding M11) so zone names referenced by vhost includes are
// always defined by the time nginx parses them.
//
// Atomic-write + nginx -t gate: we write to a .new file, run nginx -t
// against the candidate config, swap only on pass. Rollback on failure.
// Same shape as the existing domain vhost flow.

// ulidRegex keeps zone names safe. Domain IDs are ULIDs (26 chars,
// Crockford alphabet). Anything else in the input is a bug upstream —
// reject loud.
var ulidRegex = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

type rateLimitDomain struct {
	DomainID        string `json:"domain_id"`
	RateLimitRPS    uint32 `json:"rate_limit_rps"`    // 0 = no rl_ zone emitted
	ConnectionLimit uint32 `json:"connection_limit"` // 0 = no cn_ zone emitted
}

type nginxRateLimitsApplyParams struct {
	Domains []rateLimitDomain `json:"domains"`
	// ZoneSizeKB sets the shared memory size for each zone. Default 10240
	// (10 MB) → ~218k unique IPs tracked per zone. Documented formula in
	// runbook; per-domain tuning is a v2 feature.
	ZoneSizeKB uint32 `json:"zone_size_kb"`
}

type nginxRateLimitsApplyResponse struct {
	FragmentPath string `json:"fragment_path"`
	ZoneCount    int    `json:"zone_count"`
	NoChange     bool   `json:"no_change,omitempty"`
	Rolled       bool   `json:"rolled_back,omitempty"`
}

const nginxRatelimitsFragmentPath = "/etc/nginx/conf.d/00-jabali-ratelimits.conf"

func nginxRateLimitsApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxRateLimitsApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	for _, d := range p.Domains {
		if !ulidRegex.MatchString(d.DomainID) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid domain_id %q: expected ULID", d.DomainID),
			}
		}
	}

	content := BuildNginxRateLimitFragment(p.Domains, p.ZoneSizeKB)
	zoneCount := strings.Count(content, "limit_req_zone") + strings.Count(content, "limit_conn_zone")

	// Idempotent read-compare.
	existing, _ := os.ReadFile(nginxRatelimitsFragmentPath)
	if bytes.Equal(existing, []byte(content)) {
		return &nginxRateLimitsApplyResponse{
			FragmentPath: nginxRatelimitsFragmentPath,
			ZoneCount:    zoneCount,
			NoChange:     true,
		}, nil
	}

	// JAB-71: serialize stage→swap→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	// Stage the candidate beside the live file so nginx -t can
	// validate the whole http{} block with the new zones present.
	// We swap atomic after validation passes.
	tmpPath := nginxRatelimitsFragmentPath + ".new"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write candidate fragment: %v", err),
		}
	}

	// Back up current (if any) then swap new into place. nginx -t
	// reads the *live* path, so the sequence is:
	//   1. back up live → .bak
	//   2. rename new → live
	//   3. nginx -t
	//   4. if fail: rename .bak → live; return error
	//   5. remove .bak
	backupPath := nginxRatelimitsFragmentPath + ".bak"
	_ = os.Remove(backupPath)
	if _, err := os.Stat(nginxRatelimitsFragmentPath); err == nil {
		if err := os.Rename(nginxRatelimitsFragmentPath, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("backup live fragment: %v", err),
			}
		}
	}
	if err := os.Rename(tmpPath, nginxRatelimitsFragmentPath); err != nil {
		// Can't swap — restore backup if we made one.
		_ = os.Rename(backupPath, nginxRatelimitsFragmentPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("swap fragment: %v", err),
		}
	}

	// nginx -t validates; if it fails we've still got .bak to roll back.
	testCmd := execCommandContext(ctx, "nginx", "-t")
	var testOut bytes.Buffer
	testCmd.Stdout = &testOut
	testCmd.Stderr = &testOut
	if err := testCmd.Run(); err != nil {
		// Roll back.
		_ = os.Remove(nginxRatelimitsFragmentPath)
		if _, bakErr := os.Stat(backupPath); bakErr == nil {
			_ = os.Rename(backupPath, nginxRatelimitsFragmentPath)
		}
		return nil, nginxTestFailure("nginx.ratelimits (rolled back)", testOut.String())
	}
	// nginx -t passed — discard the backup.
	_ = os.Remove(backupPath)

	return &nginxRateLimitsApplyResponse{
		FragmentPath: nginxRatelimitsFragmentPath,
		ZoneCount:    zoneCount,
	}, nil
}

// BuildNginxRateLimitFragment is the pure render function. Exported for
// reuse by the panel-api reconciler (no agent round-trip needed just
// to see what the rendered fragment WOULD be — handy for `--dry-run`).
//
// Domains are sorted by ID so the output is deterministic; identical
// input always produces byte-identical output, which is what makes
// the idempotent read-compare possible.
func BuildNginxRateLimitFragment(domains []rateLimitDomain, zoneSizeKB uint32) string {
	if zoneSizeKB == 0 {
		zoneSizeKB = 10240 // 10 MB default — ADR-0032 §10
	}
	// Sort for determinism.
	sorted := make([]rateLimitDomain, len(domains))
	copy(sorted, domains)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].DomainID < sorted[j].DomainID })

	var b strings.Builder
	b.WriteString("# Auto-generated by jabali reconciler — do not edit.\n")
	b.WriteString("# Zone declarations for per-domain rate + connection limits (M18).\n")
	b.WriteString("# Per-vhost `limit_req` / `limit_conn` directives live in each site's vhost file.\n\n")

	hasAny := false
	for _, d := range sorted {
		if d.RateLimitRPS > 0 {
			// nginx expects "Nr/s" or "Nr/m" — emit r/s which keeps the
			// zone granularity fine-grained. burst handling is on the
			// `limit_req` side (in the vhost), not the zone declaration.
			fmt.Fprintf(&b,
				"limit_req_zone $binary_remote_addr zone=rl_%s:%dk rate=%dr/s;\n",
				d.DomainID, zoneSizeKB, d.RateLimitRPS,
			)
			hasAny = true
		}
		if d.ConnectionLimit > 0 {
			fmt.Fprintf(&b,
				"limit_conn_zone $binary_remote_addr zone=cn_%s:%dk;\n",
				d.DomainID, zoneSizeKB,
			)
			hasAny = true
		}
	}
	if !hasAny {
		// Empty fragment is valid — nginx is happy with an empty file.
		// Leave the header comment so the file's purpose is obvious.
	}
	return b.String()
}

// BuildRateLimitDirectives returns the per-vhost snippet that a domain's
// server block needs inside its `server { ... }` context. Empty string
// when both inputs are zero — caller interpolates it blindly.
//
// burst=rps*2 nodelay: absorbs short bursts without delaying requests
// up to 2× the rate, then sheds with 503. "nodelay" is key — the
// default is to queue, which we don't want for request-rate limits
// that protect the backend from sustained flood.
func BuildRateLimitDirectives(domainID string, rateLimitRPS, connectionLimit uint32) string {
	if rateLimitRPS == 0 && connectionLimit == 0 {
		return ""
	}
	if !ulidRegex.MatchString(domainID) {
		// Defense: caller is expected to have validated, but belt-and-braces.
		return ""
	}
	var b strings.Builder
	if rateLimitRPS > 0 {
		fmt.Fprintf(&b, "    limit_req zone=rl_%s burst=%d nodelay;\n", domainID, rateLimitRPS*2)
	}
	if connectionLimit > 0 {
		fmt.Fprintf(&b, "    limit_conn cn_%s %d;\n", domainID, connectionLimit)
	}
	return b.String()
}

func init() {
	Default.Register("nginx.ratelimits.apply", nginxRateLimitsApplyHandler)
}
