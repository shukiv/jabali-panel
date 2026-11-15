package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dockerAppLogScanEntry is one immediate DataRoot subdirectory of a docker app:
// its size and whether it holds *.log files, directly or one directory below
// (JAB-121 phase 3).
type dockerAppLogScanEntry struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	HasLogs bool   `json:"has_logs"`
}

type dockerAppLogScanResult struct {
	Entries []dockerAppLogScanEntry `json:"entries"`
}

// docker_app.log_scan (JAB-121 phase 3) reports the size of each immediate
// DataRoot subdirectory of one app and whether it holds *.log files, directly
// or one directory below. Purely READ-ONLY — the reconciler's fallback scanner
// uses it to WARN about log dirs that grow unmanaged (hold logs but have no
// rotate policy) or oversized (rotated yet huge). It never deletes anything.
func dockerAppLogScanHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse: " + err.Error()}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return dockerAppLogScanResult{}, nil // no data dir yet — nothing to scan
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "readdir: " + err.Error()}
	}
	res := dockerAppLogScanResult{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		bytes, _ := dirSizeBytes(ctx, sub) // best-effort; 0 on du failure
		res.Entries = append(res.Entries, dockerAppLogScanEntry{
			Name:    e.Name(),
			Bytes:   bytes,
			HasLogs: dockerAppSubdirHasLogs(sub),
		})
	}
	return res, nil
}

// dockerAppSubdirHasLogs reports whether sub holds *.log files, directly or
// one directory below (GH #2501: EuroOffice's supervisord writes
// docservice/out.log, converter/out.log, ...). Best-effort like the size
// scan: a glob error counts as "no logs".
func dockerAppSubdirHasLogs(sub string) bool {
	for _, pattern := range []string{
		filepath.Join(sub, "*.log"),
		filepath.Join(sub, "*", "*.log"),
	} {
		if logs, _ := filepath.Glob(pattern); len(logs) > 0 {
			return true
		}
	}
	return false
}

func init() {
	Default.Register("docker_app.log_scan", dockerAppLogScanHandler)
}
