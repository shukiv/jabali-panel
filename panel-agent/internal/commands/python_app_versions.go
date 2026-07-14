package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// pythonCandidateVersions is the range of interpreters the probe looks for.
// The panel must only ever offer versions that resolve to a real python3.X
// binary on THIS host — offering an absent one (e.g. 3.11 on a 3.13-only
// Debian 13 box) is what left apps stuck pending→failed and made Restart
// return a cryptic "Unit not found" (GH #357: the venv step shells out to
// `python<ver> -m venv` and dies before the systemd unit is ever written).
var pythonCandidateVersions = []string{"3.8", "3.9", "3.10", "3.11", "3.12", "3.13", "3.14"}

// pythonLookPath is exec.LookPath, indirected so tests can stub which
// interpreters "exist" without touching the host PATH.
var pythonLookPath = exec.LookPath

// PythonVersionsResponse reports the interpreters actually installed here.
type PythonVersionsResponse struct {
	Versions []string `json:"versions"` // installed, ascending, e.g. ["3.11","3.13"]
	Default  string   `json:"default"`  // newest installed; "" when none
}

func pythonVersionsHandler(_ context.Context, _ json.RawMessage) (any, error) {
	found := make([]string, 0, len(pythonCandidateVersions))
	for _, v := range pythonCandidateVersions {
		if _, err := pythonLookPath("python" + v); err == nil {
			found = append(found, v)
		}
	}
	sort.Slice(found, func(i, j int) bool { return pyVerLess(found[i], found[j]) })
	resp := PythonVersionsResponse{Versions: found}
	if len(found) > 0 {
		resp.Default = found[len(found)-1] // newest installed
	}
	return resp, nil
}

// pyVerLess orders "3.9" before "3.10" numerically rather than lexically.
func pyVerLess(a, b string) bool {
	amaj, amin := splitPyVer(a)
	bmaj, bmin := splitPyVer(b)
	if amaj != bmaj {
		return amaj < bmaj
	}
	return amin < bmin
}

func splitPyVer(v string) (int, int) {
	parts := strings.SplitN(v, ".", 2)
	maj, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) == 2 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return maj, minor
}

func init() {
	Default.Register("app.python.versions", pythonVersionsHandler)
}
