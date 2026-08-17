package commands

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// M47 Wave 1 (ADR-0103). Stalwart 0.16's queue is the `QueuedMessage`
// generic-object type — there is NO /api/queue/messages route in 0.16
// (that was 0.15). The agent shells `stalwart-cli` exactly as it
// shells `cscli` for crowdsec (ADR-0045), reusing mailbox_jmap.go's
// URL/token plumbing + `admin` Basic-auth (token from
// /etc/jabali-panel/stalwart-admin.token).

type mailQueueEntry struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`        // QueuedMessage.returnPath (MAIL FROM)
	Recipients []string `json:"recipients"`  // envelope rcpt addresses (map keys)
	Size       int64    `json:"size"`
	Priority   int      `json:"priority"`
	NextRetry  string   `json:"next_retry"`  // RFC3339, "" if null/unset
	FromIP     string   `json:"from_ip"`
}

// rawQueuedMessage mirrors the QueuedMessage KINDS from
// `stalwart-cli describe QueuedMessage` (feedback_schema_enumerate_kinds).
type rawQueuedMessage struct {
	ID             string                     `json:"id"`
	ReturnPath     string                     `json:"returnPath"`
	Size           int64                      `json:"size"`
	Priority       int                        `json:"priority"`
	NextRetry      *string                    `json:"nextRetry"`
	ReceivedFromIP string                     `json:"receivedFromIp"`
	Recipients     map[string]json.RawMessage `json:"recipients"`
}

// parseQueuedMessages tolerates the cli --json envelope as a bare
// array OR an {items|data:[...]} wrapper (the mx queue was empty at
// pin time so the exact wrapper is confirmed on first non-empty
// queue; per-object KINDS are schema-authoritative and pinned by
// the contract test). Blank/empty input → no messages.
func parseQueuedMessages(out []byte) ([]mailQueueEntry, error) {
	s := strings.TrimSpace(string(out))
	if s == "" || s == "null" || s == "[]" {
		return []mailQueueEntry{}, nil
	}
	var raw []rawQueuedMessage
	if s[0] == '[' {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, err
		}
	} else {
		var w struct {
			Items []rawQueuedMessage `json:"items"`
			Data  []rawQueuedMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(s), &w); err != nil {
			return nil, err
		}
		raw = w.Items
		if raw == nil {
			raw = w.Data
		}
	}
	entries := make([]mailQueueEntry, 0, len(raw))
	for _, m := range raw {
		e := mailQueueEntry{
			ID: m.ID, From: m.ReturnPath, Size: m.Size,
			Priority: m.Priority, FromIP: m.ReceivedFromIP,
		}
		if m.NextRetry != nil {
			e.NextRetry = *m.NextRetry
		}
		for rcpt := range m.Recipients {
			e.Recipients = append(e.Recipients, rcpt)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

var queueIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// sanitizeQueueID guards the id passed to `stalwart-cli delete|update
// QueuedMessage <id>`. exec.Command is not a shell so this is
// defence-in-depth, but reject anything that isn't a bounded
// alnum/_-/ token.
func sanitizeQueueID(id string) error {
	if !queueIDRe.MatchString(id) {
		return csInvalidArg("invalid queue message id")
	}
	return nil
}

// runStalwartCLI shells stalwart-cli with env Basic-auth. The token
// is passed via STALWART_PASSWORD env (never argv, never logged).
func runStalwartCLI(ctx context.Context, args ...string) ([]byte, error) {
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return nil, csInternal("stalwart-admin token unreadable", err)
	}
	cmd := execCommandContext(ctx, "stalwart-cli", args...)
	cmd.Env = append(os.Environ(),
		"STALWART_URL="+stalwartAdminURLFunc(),
		"STALWART_USER="+jmapAdminUser,
		"STALWART_PASSWORD="+token,
	)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		msg := strings.TrimSpace(string(out))
		// Never echo the token; it is only ever in env, but scrub
		// defensively in case a future cli prints its config.
		msg = strings.ReplaceAll(msg, token, "<redacted>")
		if len(msg) > 512 {
			msg = msg[:512] + "…(truncated)"
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "stalwart-cli " + args[0] + ": " + msg}
	}
	return out, nil
}

type mailQueueIDParams struct {
	ID string `json:"id"`
}

func mailQueueListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	out, err := runStalwartCLI(ctx, "query", "QueuedMessage", "--json")
	if err != nil {
		return nil, err
	}
	entries, perr := parseQueuedMessages(out)
	if perr != nil {
		return nil, csInternal("parse QueuedMessage json", perr)
	}
	return map[string]any{"data": entries, "total": len(entries)}, nil
}

func mailQueueRetryHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailQueueIDParams
	_ = json.Unmarshal(params, &p)
	if err := sanitizeQueueID(p.ID); err != nil {
		return nil, err
	}
	// Reschedule = set nextRetry to now → Stalwart attempts immediately.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := runStalwartCLI(ctx, "update", "QueuedMessage", p.ID, "nextRetry="+now); err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "rescheduled": true}, nil
}

func mailQueueDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailQueueIDParams
	_ = json.Unmarshal(params, &p)
	if err := sanitizeQueueID(p.ID); err != nil {
		return nil, err
	}
	if _, err := runStalwartCLI(ctx, "delete", "QueuedMessage", p.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "deleted": true}, nil
}

func init() {
	Default.Register("mail.queue.list", mailQueueListHandler)
	Default.Register("mail.queue.retry", mailQueueRetryHandler)
	Default.Register("mail.queue.delete", mailQueueDeleteHandler)
}
