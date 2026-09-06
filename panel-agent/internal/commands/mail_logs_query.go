package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail_logs_query.go — GH #239 mail delivery log viewer.
//
// Stalwart's "message delivery history" (the x:Trace JMAP method the first
// implementation used) is an ENTERPRISE-only feature — the community
// edition jabali ships returns `forbidden`, which the old handler silently
// swallowed, so the Mail > Logs tab was always empty.
//
// Logging/tracing to a file IS a community feature, so we read mail logs
// from a Stalwart Log file tracer instead. install/stalwart/apply-plan.json
// provisions a Log tracer (id jabali-delivery-log) that writes — with an
// include-only event filter, so the file stays small — one line per message
// to /var/log/stalwart/delivery.<date>:
//
//	2026-06-19T22:11:43Z INFO Queued message for delivery (queue.message-queued) \
//	  listenerId = "smtp", ..., from = "a@x", to = ["b@y", "c@z"], size = 1662, ...
//
// The systemd unit's LogsDirectory=stalwart guarantees the directory exists
// and is writable inside the ProtectSystem=strict sandbox (the original
// breakage: the dir was absent AND not in ReadWritePaths, so every Stalwart
// file tracer silently wrote nothing).
//
// We parse the `queue.message-queued` lines — one per accepted message, with
// from / to[] / size — into the {timestamp, from, to, size} rows the panel
// already renders. The agent runs as root, so it reads the
// jabali-mail:jabali-mail 0750 log files directly.

const (
	mailLogPrefix         = "delivery"
	mailLogEventQueued    = "(queue.message-queued)"
	// mailLogEventAuthQueued is the queue event Stalwart emits for an
	// AUTHENTICATED submission — i.e. a tenant sending mail (GH #1416).
	// Inbound relay produces queue.message-queued; a user send produces this
	// distinct event, so it must be treated as a queued line too or per-domain
	// "Sent" (and the Mail > Logs outbound rows) never see a single user send.
	mailLogEventAuthQueued = "(queue.authenticated-message-queued)"
	mailLogEventCompleted  = "(delivery.completed)"
	mailLogEventFailed     = "(delivery.failed)"
	// mailLogMaxScan bounds memory: at most this many matching lines are
	// parsed across all log files before filtering. Newest files are read
	// first, so the cap drops only the oldest history.
	mailLogMaxScan = 50000
)

// mailLogDir is the Stalwart Log tracer output directory. A var (not const)
// so tests can point it at a temp dir.
var mailLogDir = "/var/log/stalwart"

// mailLogsQueryParams — panel → agent.
type mailLogsQueryParams struct {
	FromDate        *string  `json:"from_date"` // RFC 3339 or YYYY-MM-DD
	ToDate          *string  `json:"to_date"`
	SenderPrefix    *string  `json:"sender_prefix"`    // case-insensitive substring on From
	RecipientPrefix *string  `json:"recipient_prefix"` // case-insensitive substring on any recipient
	DomainNames     []string `json:"domain_names"`     // scope — From or a recipient must match ONE
	Limit           int      `json:"limit"`
	Offset          int      `json:"offset"`
}

type mailLogEntry struct {
	Timestamp string `json:"timestamp"`
	From      string `json:"from"`
	To        string `json:"to"` // recipients joined with ", "
	Size      int    `json:"size"`
	// Status (GH #262): delivery outcome correlated by queueId across the
	// queued + delivery.completed/failed tracer events. "delivered" /
	// "failed" / "queued" (queued = accepted, no terminal event seen yet).
	Status string `json:"status"`
	// QueueID (GH #261) lets the UI fetch a per-message delivery trail.
	QueueID string `json:"queue_id,omitempty"`
}

type mailLogsQueryResponse struct {
	Entries []mailLogEntry `json:"entries"`
	Total   int            `json:"total"`
}

var (
	reMailLogFrom = regexp.MustCompile(`\bfrom = "([^"]*)"`)
	reMailLogTo   = regexp.MustCompile(`\bto = \[([^\]]*)\]`)
	reMailLogSize = regexp.MustCompile(`\bsize = (\d+)`)
	reMailLogQID  = regexp.MustCompile(`\bqueueId = (\d+)`)
)

// parsedMailLine is the parse result with recipients kept split for accurate
// per-recipient domain-scope matching (the wire entry joins them for display).
type parsedMailLine struct {
	entry      mailLogEntry
	recipients []string
	ts         time.Time
	queueID    string // for dedup across queued/completed lines of one message
	queued     bool   // true = queue.message-queued (canonical, preferred on dedup)
	rank       int    // status precedence: 0 queued < 1 failed < 2 delivered (GH #262)
}

// parseMailLogLine extracts a message record from a single Stalwart Log
// tracer line. Returns ok=false for any non-message line (lifecycle events,
// blank lines) so callers can skip them.
func parseMailLogLine(line string) (parsedMailLine, bool) {
	queued := strings.Contains(line, mailLogEventQueued) ||
		strings.Contains(line, mailLogEventAuthQueued)
	if !queued &&
		!strings.Contains(line, mailLogEventCompleted) &&
		!strings.Contains(line, mailLogEventFailed) {
		return parsedMailLine{}, false
	}
	sp := strings.IndexByte(line, ' ')
	if sp <= 0 {
		return parsedMailLine{}, false
	}
	tsStr := line[:sp]
	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return parsedMailLine{}, false
	}

	from := ""
	if m := reMailLogFrom.FindStringSubmatch(line); m != nil {
		from = m[1]
	}

	var recipients []string
	if m := reMailLogTo.FindStringSubmatch(line); m != nil {
		for _, p := range strings.Split(m[1], ",") {
			p = strings.Trim(strings.TrimSpace(p), `"`)
			if p != "" {
				recipients = append(recipients, p)
			}
		}
	}

	size := 0
	if m := reMailLogSize.FindStringSubmatch(line); m != nil {
		size, _ = strconv.Atoi(m[1])
	}

	qid := ""
	if m := reMailLogQID.FindStringSubmatch(line); m != nil {
		qid = m[1]
	}

	rank := 0 // queued
	if strings.Contains(line, mailLogEventCompleted) {
		rank = 2 // delivered
	} else if strings.Contains(line, mailLogEventFailed) {
		rank = 1 // failed
	}
	return parsedMailLine{
		entry: mailLogEntry{
			Timestamp: tsStr,
			From:      from,
			To:        strings.Join(recipients, ", "),
			Size:      size,
			QueueID:   qid,
		},
		recipients: recipients,
		ts:         ts,
		queueID:    qid,
		queued:     queued,
		rank:       rank,
	}, true
}

// mailLogFiles returns the delivery log files newest-first. Matches the
// tracer prefix exactly so the sibling stalwart.log tracer's files are not
// read. Missing directory (logging not yet provisioned) → empty, no error.
func mailLogFiles() []string {
	dents, err := os.ReadDir(mailLogDir)
	if err != nil {
		return nil
	}
	var files []string
	for _, d := range dents {
		if d.IsDir() {
			continue
		}
		name := d.Name()
		if name == mailLogPrefix || strings.HasPrefix(name, mailLogPrefix+".") {
			files = append(files, filepath.Join(mailLogDir, name))
		}
	}
	// Dated suffixes sort lexically == chronologically; reverse = newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files
}

// parseDateBound parses a from/to filter value as RFC3339 or YYYY-MM-DD.
func parseDateBound(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func mailLogsQueryHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailLogsQueryParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	// GH #239: self-heal the delivery tracer. Installs that UPDATED (rather than
	// fresh-installed after the tracer was added to the apply-plan) never got it,
	// so /var/log/stalwart/delivery.<date> is never written and the tab stays
	// empty. Create it if missing — best-effort; new mail then starts logging.
	if err := ensureDeliveryTracer(ctx); err != nil {
		slog.WarnContext(ctx, "mail logs: ensure delivery tracer failed", "err", err)
	}

	var fromBound, toBound time.Time
	if p.FromDate != nil {
		if t, ok := parseDateBound(*p.FromDate); ok {
			fromBound = t
		}
	}
	if p.ToDate != nil {
		if t, ok := parseDateBound(*p.ToDate); ok {
			// Date-only "to" means the whole day is included.
			if len(*p.ToDate) <= 10 {
				t = t.Add(24*time.Hour - time.Second)
			}
			toBound = t
		}
	}

	senderSub := ""
	if p.SenderPrefix != nil {
		senderSub = strings.ToLower(strings.TrimSpace(*p.SenderPrefix))
	}
	recipientSub := ""
	if p.RecipientPrefix != nil {
		recipientSub = strings.ToLower(strings.TrimSpace(*p.RecipientPrefix))
	}

	matched := make([]mailLogEntry, 0, 128)
	// Dedup queued + completed lines of the same message by queueId. Stalwart
	// emits queue.message-queued for relayed mail and delivery.completed for
	// (some) local mail; a message may produce one or both. Prefer the queued
	// line (canonical accept time/size) when both are present.
	seen := map[string]int{}
	matchedQueued := []bool{}
	statusRank := []int{} // parallel to matched; max delivery rank per row (GH #262)
	scanned := 0

files:
	for _, f := range mailLogFiles() {
		if err := ctx.Err(); err != nil {
			break
		}
		fh, err := os.Open(f) //nolint:gosec // fixed log dir, agent-owned
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if scanned >= mailLogMaxScan {
				fh.Close()
				break files
			}
			pl, ok := parseMailLogLine(sc.Text())
			if !ok {
				continue
			}
			scanned++
			if !fromBound.IsZero() && pl.ts.Before(fromBound) {
				continue
			}
			if !toBound.IsZero() && pl.ts.After(toBound) {
				continue
			}
			if senderSub != "" && !strings.Contains(strings.ToLower(pl.entry.From), senderSub) {
				continue
			}
			if recipientSub != "" && !strings.Contains(strings.ToLower(pl.entry.To), recipientSub) {
				continue
			}
			if len(p.DomainNames) > 0 && !mailLineInScope(pl, p.DomainNames) {
				continue
			}
			if pl.queueID != "" {
				if idx, dup := seen[pl.queueID]; dup {
					if pl.rank > statusRank[idx] {
						statusRank[idx] = pl.rank
					}
					if pl.queued && !matchedQueued[idx] {
						matched[idx] = pl.entry
						matchedQueued[idx] = true
					}
					continue
				}
				seen[pl.queueID] = len(matched)
			}
			matched = append(matched, pl.entry)
			matchedQueued = append(matchedQueued, pl.queued)
			statusRank = append(statusRank, pl.rank)
		}
		fh.Close()
	}

	// Stamp the correlated delivery status (GH #262) before sorting.
	for i := range matched {
		switch statusRank[i] {
		case 2:
			matched[i].Status = "delivered"
		case 1:
			matched[i].Status = "failed"
		default:
			matched[i].Status = "queued"
		}
	}

	// Newest first. Files are already newest-first and Stalwart appends in
	// time order, so a stable sort by timestamp desc gives a global order.
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Timestamp > matched[j].Timestamp
	})

	total := len(matched)
	start := p.Offset
	if start > total {
		start = total
	}
	end := start + p.Limit
	if end > total {
		end = total
	}
	page := matched[start:end]
	if page == nil {
		page = []mailLogEntry{}
	}
	return mailLogsQueryResponse{Entries: page, Total: total}, nil
}

// mailLineInScope reports whether the message's sender or any recipient
// belongs to one of the caller's domains.
func mailLineInScope(pl parsedMailLine, domains []string) bool {
	for _, d := range domains {
		if containsDomain(pl.entry.From, d) {
			return true
		}
		for _, r := range pl.recipients {
			if containsDomain(r, d) {
				return true
			}
		}
	}
	return false
}

// containsDomain reports whether addr's domain part equals domain exactly.
func containsDomain(addr, domain string) bool {
	if addr == "" || domain == "" {
		return false
	}
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || at == len(addr)-1 {
		return false
	}
	return strings.EqualFold(addr[at+1:], domain)
}

// ensureDeliveryTracer creates the #jabali-delivery-log Stalwart Log tracer if
// no tracer with prefix "delivery" exists. Mirrors install/stalwart/
// apply-plan.json.tmpl. Idempotent + change-gated (reloads only on create).
// deliveryTracerEvents is the include-only event set the delivery Log tracer
// must carry. queue.message-queued is inbound relay + local delivery;
// queue.authenticated-message-queued (GH #1416) is a tenant sending mail — a
// distinct Stalwart event, so without it per-domain "Sent" and the Mail > Logs
// outbound rows never see a single user send.
func deliveryTracerEvents() map[string]any {
	return map[string]any{
		"queue.message-queued":               true,
		"queue.authenticated-message-queued": true,
		"delivery.completed":                 true,
		"delivery.failed":                    true,
	}
}

// deliveryTracerBackfillOnce bounds the GH #1416 event back-fill — which ends
// in a settings reload — to a single attempt per agent process. Belt against a
// tracer whose `events` can't be read back in the shape we expect: without it,
// a false "missing the event" read would reload on EVERY mail.logs_query.
var deliveryTracerBackfillOnce sync.Once

func ensureDeliveryTracer(ctx context.Context) error {
	var qr jmapQueryResult
	if err := jmapCall(ctx, "x:Tracer/query", map[string]any{}, &qr); err != nil {
		return fmt.Errorf("tracer query: %w", err)
	}
	if len(qr.IDs) > 0 {
		var gr jmapGetResult
		if err := jmapCall(ctx, "x:Tracer/get", map[string]any{"ids": qr.IDs}, &gr); err != nil {
			return fmt.Errorf("tracer get: %w", err)
		}
		for i, raw := range gr.List {
			var t struct {
				ID     string          `json:"id"`
				Prefix string          `json:"prefix"`
				Events map[string]bool `json:"events"`
			}
			if json.Unmarshal(raw, &t) != nil || t.Prefix != "delivery" {
				continue
			}
			// Already carries the authenticated-send event — nothing to do.
			if t.Events["queue.authenticated-message-queued"] {
				return nil
			}
			// GH #1416: a tracer provisioned before authenticated sends were
			// tracked is stranded by the old "already provisioned → return" path.
			// Patch the event set in place (once per process — see the Once).
			id := t.ID
			if id == "" && i < len(qr.IDs) {
				id = qr.IDs[i]
			}
			var patchErr error
			deliveryTracerBackfillOnce.Do(func() {
				patchErr = patchDeliveryTracerEvents(ctx, id)
			})
			return patchErr
		}
	}
	args := map[string]any{"create": map[string]any{"jabali-delivery-log": map[string]any{
		"@type": "Log", "enable": true, "level": "info",
		"path": mailLogDir, "prefix": mailLogPrefix, "rotate": "daily",
		"ansi": false, "multiline": false, "lossy": false,
		"eventsPolicy": "include",
		"events":       deliveryTracerEvents(),
	}}}
	var res jmapSetResult
	if err := jmapCall(ctx, "x:Tracer/set", args, &res); err != nil {
		return fmt.Errorf("tracer create: %w", err)
	}
	if reason, bad := res.NotCreated["jabali-delivery-log"]; bad {
		return fmt.Errorf("tracer create refused: %s", string(reason))
	}
	return reloadStalwartSettings(ctx)
}

// patchDeliveryTracerEvents rewrites an existing delivery tracer's include-only
// event set to the current desired set (GH #1416 back-fill) and reloads.
func patchDeliveryTracerEvents(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("delivery tracer missing id, cannot patch events")
	}
	upd := map[string]any{"update": map[string]any{id: map[string]any{
		"eventsPolicy": "include",
		"events":       deliveryTracerEvents(),
	}}}
	var res jmapSetResult
	if err := jmapCall(ctx, "x:Tracer/set", upd, &res); err != nil {
		return fmt.Errorf("tracer update events: %w", err)
	}
	if reason, bad := res.NotUpdated[id]; bad {
		return fmt.Errorf("tracer update refused: %s", string(reason))
	}
	return reloadStalwartSettings(ctx)
}

func init() {
	Default.Register("mail.logs_query", mailLogsQueryHandler)
}
