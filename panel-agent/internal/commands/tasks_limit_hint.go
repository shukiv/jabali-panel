package commands

import (
	"context"
	"fmt"
	"strings"
)

// tasksLimitHint names the account's Max tasks limit when the unit's slice is
// at it. That limit (the package's or user's max_tasks, written as TasksMax on
// jabali-user-<u>.slice) is the usual cause behind systemd's "Failed to spawn
// executor: Resource temporarily unavailable", and nothing in that message
// points to the panel setting. GH #1820: a package with Max Tasks 20 and four
// Python apps left the PHP pools and a new app unable to start, and the errors
// only ever said "exit status 1".
//
// The unit's own Slice property decides which slice to check, so a unit that
// is not in a user slice never gets the hint. Returns "" when the slice has no
// limit or still has room. The text is read by admins and tenants alike (a
// Python app's last_error shows on the tenant's page), so it states the fact
// rather than telling the reader to change a setting they may not own.
func tasksLimitHint(ctx context.Context, unit string) string {
	props, err := systemctlShow(ctx, unit, "Slice")
	if err != nil {
		return ""
	}
	slice := props["Slice"]
	if !strings.HasPrefix(slice, "jabali-user-") || !strings.HasSuffix(slice, ".slice") {
		return ""
	}
	props, err = systemctlShow(ctx, slice, "TasksCurrent", "TasksMax")
	if err != nil {
		return ""
	}
	// TasksMax=infinity parses to 0: no limit, no hint.
	current, limit := parseUintProperty(props["TasksCurrent"]), parseUintProperty(props["TasksMax"])
	if limit == 0 || current < limit {
		return ""
	}
	return fmt.Sprintf("the account has reached its Max tasks limit (%d of %d in use), so no new process can start until one exits or an administrator raises the limit", current, limit)
}

// tasksLimitSuffix is tasksLimitHint ready to append to an error message.
func tasksLimitSuffix(ctx context.Context, unit string) string {
	if hint := tasksLimitHint(ctx, unit); hint != "" {
		return "; " + hint
	}
	return ""
}
