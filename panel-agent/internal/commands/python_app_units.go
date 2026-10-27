package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// app.python.list_units — the app id of every app unit on the host (GH
// #2289). A unit that outlives its app row keeps running, and keeps the port
// the panel then gives the next app: the panel's delete and a reconciler apply
// can cross, and a remove can fail. The reconciler compares these ids with the
// app rows and removes, with app.python.remove, a unit that has had no row
// for a while.
type pythonAppListUnitsResponse struct {
	AppIDs []string `json:"app_ids"`
}

func pythonAppListUnitsHandler(_ context.Context, _ json.RawMessage) (any, error) {
	ids, err := listPythonAppUnitIDs(pythonAppUnitDir)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("list app units: %v", err)}
	}
	return pythonAppListUnitsResponse{AppIDs: ids}, nil
}

// listPythonAppUnitIDs returns the app id of every jabali-app-<id>.service
// file in dir, in name order. Only regular files count, since apply writes
// each unit with os.WriteFile, and only ids appIDRe accepts, since apply
// refuses any other.
func listPythonAppUnitIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		id, ok := strings.CutPrefix(e.Name(), "jabali-app-")
		if !ok {
			continue
		}
		if id, ok = strings.CutSuffix(id, ".service"); ok && appIDRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func init() {
	Default.Register("app.python.list_units", pythonAppListUnitsHandler)
}
