package commands

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// user_egress_drops.go — GH #713. Read + parse the per-user egress log that
// the nft chains emit, rate-limited 5/min: the enforced chain logs each drop
// (GH #638: `log prefix "jabali-egress-drop-<user> "`), the learning chain each
// new connection it lets through that enforcement would block
// (`log prefix "jabali-egress-learn-<user> "`, GH #2224). Turns the raw count
// into inspectable events: which destination:port:proto a tenant is blocked
// from, or would be. Source is the kernel ring buffer via journalctl -k —
// recent + rotates, not a durable audit trail.

var egressDropUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// egressDropLineRE pulls DST / DPT / PROTO out of a kernel netfilter log line.
var (
	egDST   = regexp.MustCompile(`\bDST=([0-9a-fA-F:.]+)`)
	egDPT   = regexp.MustCompile(`\bDPT=(\d+)`)
	egPROTO = regexp.MustCompile(`\bPROTO=(\S+)`)
)

type egressDropParams struct {
	Username string `json:"username"`
}

type egressDropEvent struct {
	Dest  string `json:"dest"`
	Port  int    `json:"port"`
	Proto string `json:"proto"`
	// Mode is the policy state that logged the line: "enforced" (the
	// packet was dropped) or "learning" (allowed, would be dropped).
	Mode     string `json:"mode"`
	Count    int    `json:"count"`
	LastSeen string `json:"last_seen"`
}

type egressDropsResponse struct {
	Source string            `json:"source"`
	Events []egressDropEvent `json:"events"`
}

func egressDropsHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p egressDropParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	if !egressDropUserRE.MatchString(p.Username) {
		return nil, csInvalidArg("invalid username")
	}
	resp := egressDropsResponse{
		Source: "nftables egress log via kernel ring buffer (journalctl -k): drops while enforced, and connections allowed while learning that enforced mode would block; rate-limited to 5/min per user and rotates with journald — recent activity, not a durable audit trail",
		Events: []egressDropEvent{},
	}
	// Grep the kernel log for this user's drop and learn prefixes. The
	// trailing space keeps user "bob" from matching "bobby". The username
	// regexp admits no regex metacharacters. Bounded lines + no shell.
	out, err := execCommandContext(ctx, "journalctl", "-k", "--no-pager",
		"--output=short-iso", "-n", "2000",
		"-g", "jabali-egress-(drop|learn)-"+p.Username+" ").Output()
	if err != nil {
		// No matches / journalctl unavailable — empty, not an error.
		return resp, nil
	}
	type key struct {
		dest, proto, mode string
		port              int
	}
	agg := map[key]*egressDropEvent{}
	for _, line := range strings.Split(string(out), "\n") {
		var mode string
		switch {
		case strings.Contains(line, "jabali-egress-drop-"+p.Username+" "):
			mode = "enforced"
		case strings.Contains(line, "jabali-egress-learn-"+p.Username+" "):
			mode = "learning"
		default:
			continue
		}
		dm := egDST.FindStringSubmatch(line)
		if dm == nil {
			continue
		}
		port := 0
		if pm := egDPT.FindStringSubmatch(line); pm != nil {
			port, _ = strconv.Atoi(pm[1])
		}
		proto := ""
		if pr := egPROTO.FindStringSubmatch(line); pr != nil {
			proto = pr[1]
		}
		ts := ""
		if f := strings.Fields(line); len(f) > 0 {
			ts = f[0] // short-iso leading timestamp
		}
		k := key{dest: dm[1], proto: proto, mode: mode, port: port}
		if e, ok := agg[k]; ok {
			e.Count++
			e.LastSeen = ts
		} else {
			agg[k] = &egressDropEvent{Dest: dm[1], Port: port, Proto: proto, Mode: mode, Count: 1, LastSeen: ts}
		}
	}
	for _, e := range agg {
		resp.Events = append(resp.Events, *e)
	}
	// Most-recent first, then by count.
	sort.Slice(resp.Events, func(i, j int) bool {
		if resp.Events[i].LastSeen != resp.Events[j].LastSeen {
			return resp.Events[i].LastSeen > resp.Events[j].LastSeen
		}
		return resp.Events[i].Count > resp.Events[j].Count
	})
	return resp, nil
}

func init() {
	Default.Register("security.egress.drops", egressDropsHandler)
}
