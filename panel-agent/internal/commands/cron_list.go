package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// cronListParams represents the input to cron.list.
type cronListParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

// cronListResponse represents the output from cron.list.
type cronListResponse struct {
	UnitFiles []string `json:"unit_files"` // Job IDs (strip prefix/suffix from filenames)
}

// cronListHandler lists all cron job unit files for a user.
func cronListHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cronListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate inputs
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username required",
		}
	}

	// Look where cron.apply writes the units: the user's systemd search path
	// (GH #273). This read the old /etc/jabali-panel/cron-units directory
	// instead, so the reconciler never saw a unit to sweep. The home is
	// tenant-writable, so it is read through a home-rooted scope that refuses
	// a planted symlink (GH #475). An account with no home on this box has
	// nothing there. Units written before #273 may still sit in the legacy
	// directory, which root owns.
	ids := map[string]bool{}
	if u, err := lookupCronUser(p.Username); err == nil {
		scope, err := filesafe.NewScope(p.Username, p.Username, []string{u.HomeDir})
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "scope init: " + err.Error()}
		}
		entries, err := scope.ReadDirInScope(cronUnitsDir(u))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to read %s: %v", cronUnitsDir(u), err),
			}
		}
		for _, e := range entries {
			if id := cronTimerJobID(e.Name); id != "" && !e.IsDir {
				ids[id] = true
			}
		}
	}

	legacyDir := filepath.Join(legacyCronUnitsRoot, p.Username)
	entries, err := os.ReadDir(legacyDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read cron-units directory: %v", err),
		}
	}
	for _, e := range entries {
		if id := cronTimerJobID(e.Name()); id != "" && !e.IsDir() {
			ids[id] = true
		}
	}

	jobIDs := make([]string, 0, len(ids))
	for id := range ids {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)
	return &cronListResponse{UnitFiles: jobIDs}, nil
}

// cronTimerJobID returns the job id of a "jabali-cron-<id>.timer" file name,
// or "" for any other name. Only ULIDs count: cron.remove refuses anything
// else, so a stray file would otherwise fail the sweep every tick.
func cronTimerJobID(name string) string {
	if !strings.HasPrefix(name, "jabali-cron-") || !strings.HasSuffix(name, ".timer") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "jabali-cron-"), ".timer")
	if !ulidRE.MatchString(id) {
		return ""
	}
	return id
}

// init registers the cron.list command.
func init() {
	Default.Register("cron.list", cronListHandler)
}
