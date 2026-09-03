package commands

// JAB-376 — Host Quota Snapshot (Agent side). One `repquota` for the whole
// mount replaces the disk-usage sweeper's per-user `quota -u <user>` fan-out
// (~6,000 agent calls + external process launches/hour at 1,000 accounts).
//
// `repquota -O csv <mount>` reads the kernel quota state via quotactl in ONE
// call and emits fixed-column CSV, so it is filesystem-agnostic (ext4, XFS) and
// free of the column-shift hazard the human-readable format has when a user is
// over the soft grace limit. Explicit mount only (ADR-0032) — never
// all-filesystems.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type quotaInventoryParams struct {
	Mount string `json:"mount"`
}

// QuotaInventoryEntry is one user's block usage on the mount. Block figures are
// 1-KiB blocks (repquota's native unit). LimitKB == 0 means unlimited.
type QuotaInventoryEntry struct {
	Username string `json:"username"`
	UsedKB   uint64 `json:"used_kb"`
	LimitKB  uint64 `json:"limit_kb"`
}

type quotaInventoryResponse struct {
	Mount   string                `json:"mount"`
	Entries []QuotaInventoryEntry `json:"entries"`
	// Partial is set when at least one data row failed to parse. The panel
	// consumer must treat a partial inventory as advisory (keep last-good for
	// missing users) rather than authoritative, so a garbled line never clears
	// a real quota alert by looking like "0 used".
	Partial bool `json:"partial"`
}

func quotaInventoryHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p quotaInventoryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if p.Mount == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "mount is required (explicit-mount invariant, ADR-0032)"}
	}
	stdout, stderr, err := runCmd(ctx, "repquota", "-O", "csv", p.Mount)
	if err != nil {
		// repquota exits non-zero when the mount has no quota enabled — surface
		// it so the sweeper can keep last-good instead of zeroing every account.
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("repquota %s failed (is quota enabled on the mount?): %v: %s", p.Mount, err, strings.TrimSpace(string(stderr))),
		}
	}
	entries, partial := parseRepquotaCSV(string(stdout))
	return quotaInventoryResponse{Mount: p.Mount, Entries: entries, Partial: partial}, nil
}

// parseRepquotaCSV parses `repquota -O csv <mount>` output. Columns:
//
//	User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,BlockGrace,FileUsed,FileSoftLimit,FileHardLimit,FileGrace
//
// BlockUsed (index 3) and BlockHardLimit (index 5) are 1-KiB blocks. The header
// row, blank lines, and `#<uid>` users (no passwd name) are skipped. A plain
// strings.Split(",") is deliberate and safe: a POSIX login name cannot contain
// a comma, and every field after the username is numeric or a fixed keyword —
// do NOT "upgrade" this to a CSV library expecting quoting. A row whose
// numeric fields don't parse sets partial=true and is dropped — never emitted as
// a fabricated 0, which would clear a genuine quota alert. Duplicate usernames:
// last wins (the panel consumer logs the collision).
func parseRepquotaCSV(output string) (entries []QuotaInventoryEntry, partial bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue // header preamble / short lines are not data rows
		}
		user := f[0]
		if user == "" || user == "User" || strings.HasPrefix(user, "#") {
			continue // CSV header + numeric-uid-only users (no name)
		}
		used, uErr := strconv.ParseUint(strings.TrimSpace(f[3]), 10, 64)
		limit, lErr := strconv.ParseUint(strings.TrimSpace(f[5]), 10, 64)
		if uErr != nil || lErr != nil {
			partial = true
			continue
		}
		entries = append(entries, QuotaInventoryEntry{Username: user, UsedKB: used, LimitKB: limit})
	}
	return entries, partial
}

func init() {
	Default.Register("system.quota_inventory", quotaInventoryHandler)
}
