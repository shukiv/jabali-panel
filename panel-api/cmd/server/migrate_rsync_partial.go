package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// rsyncPartialNote returns a summary line when migration.rsync_remote_home
// reports a partial copy, or "" for a full one. The agent returns success
// for rsync exit 23 (some files could not be read or transferred) and 24
// (source files vanished during the copy, e.g. a live Maildir), so without
// this line a partial copy would read as complete.
func rsyncPartialNote(label string, raw json.RawMessage) string {
	var r struct {
		Partial  bool     `json:"partial"`
		ExitCode int      `json:"exit_code"`
		Errors   []string `json:"errors"`
	}
	if json.Unmarshal(raw, &r) != nil || !r.Partial {
		return ""
	}
	why := "some files could not be read or transferred"
	if r.ExitCode == 24 {
		why = "some source files changed or vanished during the copy; a re-run picks them up"
	}
	msg := fmt.Sprintf("%s: partial copy (rsync exit %d: %s)", label, r.ExitCode, why)
	if len(r.Errors) > 0 {
		msg += ": " + strings.Join(r.Errors, "; ")
	}
	return msg
}
