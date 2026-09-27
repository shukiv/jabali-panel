package appsecops

import (
	"sort"
	"strings"
	"time"
)

// explain.go — the AppSec false-positive triage view. Shared by
// `jabali appsec explain` and the admin UI (GH #1649), so both answer "which
// rule blocked this?" the same way.
//
// /var/log/crowdsec.log records only the outcome of a block, with no rule, no
// URI and no matched variable. The data is in `cscli alerts inspect -d` meta,
// which the agent verb EventsVerb flattens. This groups those events by rules +
// host + URI so the pattern behind a complaint is visible, and an exclusion can
// be written against a rule that actually fired.

// EventsVerb is the agent verb that returns recent AppSec blocks.
const EventsVerb = "security.crowdsec.appsec.events"

// Event is one blocked request, as the agent verb reports it.
type Event struct {
	Timestamp  string   `json:"timestamp"`
	SourceIP   string   `json:"source_ip"`
	TargetHost string   `json:"target_host"`
	TargetURI  string   `json:"target_uri"`
	Action     string   `json:"action"`
	RuleName   string   `json:"rule_name"`
	RuleIDs    []string `json:"rule_ids"`
	Country    string   `json:"country"`
	ASNOrg     string   `json:"asn_org"`
}

// InlineBlock is a 403 that never became a ban, so cscli has no alert for it.
// crowdsec.log is its only record: score families and source IP, no rule id
// and no URI.
type InlineBlock struct {
	Timestamp string `json:"timestamp"`
	SourceIP  string `json:"source_ip"`
	Scores    string `json:"scores"`
}

// EventsResponse is the agent verb's result.
type EventsResponse struct {
	Events        []Event       `json:"events"`
	InlineBlocks  []InlineBlock `json:"inline_blocks"`
	AlertsScanned int           `json:"alerts_scanned"`
}

// crsInfraRules are the CRS rules that appear on almost every block but are
// never the thing to exclude. Naming them is the point of the triage view: a
// raw id list like "901340, 930130, 2546897341, 980170" gives no clue that only
// one of those actually scored.
//
//	901340 — enables request-body inspection. Scores nothing. ADR-0124 exists
//	         because this id was excluded in a first triage, to no effect.
//	949110 — the inbound anomaly threshold rule. It is what returns 403, but it
//	         is an effect of the score, not a detection. Excluding it disables
//	         blocking wholesale.
//	980170 — reports the final score. Bookkeeping.
var crsInfraRules = map[string]string{
	"901340": "body-inspection enabler — scores nothing, never exclude this",
	"949110": "anomaly threshold reached — the blocker, not a detection",
	"980170": "score reporting — bookkeeping",
}

// InfraRule is a CRS rule that rides along on a block without being the
// detection behind it.
type InfraRule struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

// AnnotateRules splits an id list into the detections worth acting on and the
// infrastructure rules that ride along on every block.
func AnnotateRules(ids []string) (detections []string, infra []InfraRule) {
	detections = []string{}
	infra = []InfraRule{}
	for _, id := range ids {
		if note, ok := crsInfraRules[id]; ok {
			infra = append(infra, InfraRule{ID: id, Note: note})
			continue
		}
		detections = append(detections, id)
	}
	return detections, infra
}

// Pattern is every block that shares one rule list, host and URI.
type Pattern struct {
	RuleIDs     []string    `json:"rule_ids"`
	Detections  []string    `json:"detections"`
	Infra       []InfraRule `json:"infra"`
	Host        string      `json:"host"`
	URI         string      `json:"uri"`
	Count       int         `json:"count"`
	DistinctIPs int         `json:"distinct_ips"`
	FirstAt     string      `json:"first_at"`
	LastAt      string      `json:"last_at"`
}

// GroupEvents groups events by rule list + host + URI, most blocks first. Ties
// are ordered by host, then URI, then rule list, so the same events always
// give the same order.
//
// Many distinct source IPs on one URI reads as a false positive. One IP across
// many URIs usually reads as an actual attacker.
func GroupEvents(events []Event) []Pattern {
	type group struct {
		p   Pattern
		ips map[string]bool
	}
	groups := map[string]*group{}
	order := []string{}
	for _, e := range events {
		key := strings.Join(e.RuleIDs, ",") + "|" + e.TargetHost + "|" + e.TargetURI
		g, ok := groups[key]
		if !ok {
			det, infra := AnnotateRules(e.RuleIDs)
			g = &group{
				p: Pattern{
					RuleIDs: append([]string{}, e.RuleIDs...), Detections: det, Infra: infra,
					Host: e.TargetHost, URI: e.TargetURI,
					FirstAt: e.Timestamp, LastAt: e.Timestamp,
				},
				ips: map[string]bool{},
			}
			groups[key] = g
			order = append(order, key)
		}
		g.p.Count++
		g.ips[e.SourceIP] = true
		if timestampBefore(e.Timestamp, g.p.FirstAt) {
			g.p.FirstAt = e.Timestamp
		}
		if timestampBefore(g.p.LastAt, e.Timestamp) {
			g.p.LastAt = e.Timestamp
		}
	}

	out := make([]Pattern, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		g.p.DistinctIPs = len(g.ips)
		out = append(out, g.p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		return strings.Join(a.RuleIDs, ",") < strings.Join(b.RuleIDs, ",")
	})
	return out
}

// timestampBefore reports whether a is earlier than b. cscli writes RFC 3339;
// a value that does not parse is never earlier or later than another, so the
// first one seen stays.
func timestampBefore(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return false
	}
	return ta.Before(tb)
}

// InlineBlockGroup is every inline 403 from one source IP.
type InlineBlockGroup struct {
	SourceIP   string `json:"source_ip"`
	Count      int    `json:"count"`
	LastAt     string `json:"last_at"`
	LastScores string `json:"last_scores"`
}

// GroupInlineBlocks groups inline 403s by source IP, in the order each IP
// first appears. The agent reads crowdsec.log oldest first, so "last" is the
// latest block from that IP.
func GroupInlineBlocks(blocks []InlineBlock) []InlineBlockGroup {
	byIP := map[string]int{}
	out := []InlineBlockGroup{}
	for _, b := range blocks {
		i, seen := byIP[b.SourceIP]
		if !seen {
			i = len(out)
			byIP[b.SourceIP] = i
			out = append(out, InlineBlockGroup{SourceIP: b.SourceIP})
		}
		out[i].Count++
		out[i].LastAt = b.Timestamp
		out[i].LastScores = b.Scores
	}
	return out
}
