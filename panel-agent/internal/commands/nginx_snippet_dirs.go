package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.snippet_dirs.prune removes the per-domain snippet dirs
// (/etc/nginx/jabali/<name>/) of names the panel no longer has. Domain deletes
// made before domain.delete removed that dir left them behind. Nothing includes
// such a dir until a domain with that name is created again, and then its
// vhost would.
//
// The panel sends every domain name it has. A dir is removed only when its name
// is a domain name, is not in that list, and has no vhost in sites-available:
// a name with a vhost is an agent site, and an orphan site is never removed on
// a guess (JAB-236). Nothing includes a removed dir, so no reload follows.

type snippetDirsPruneParams struct {
	Keep []string `json:"keep"`
}

type snippetDirsPruneResult struct {
	Removed []string `json:"removed"`
	Failed  []string `json:"failed,omitempty"`
}

func snippetDirsPruneHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p snippetDirsPruneParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	// With no names to keep, every dir without a vhost would look gone. A
	// panel with no domains has nothing worth pruning.
	if len(p.Keep) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "keep is required"}
	}
	// Without sites-available, every name would look like it has no vhost.
	if fi, err := os.Stat(nginxSitesDir); err != nil || !fi.IsDir() {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: nginxSitesDir + " is not a directory"}
	}
	keep := make(map[string]bool, len(p.Keep))
	for _, n := range p.Keep {
		keep[n] = true
	}

	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	res := snippetDirsPruneResult{Removed: []string{}}
	entries, err := os.ReadDir(nginxJabaliDir)
	if os.IsNotExist(err) {
		return res, nil
	}
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read %s: %v", nginxJabaliDir, err)}
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !domainRegex.MatchString(name) || keep[name] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(nginxSitesDir, name+".conf")); !os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(nginxJabaliDir, name)); err != nil {
			res.Failed = append(res.Failed, name)
			continue
		}
		res.Removed = append(res.Removed, name)
	}
	return res, nil
}

func init() {
	Default.Register("nginx.snippet_dirs.prune", snippetDirsPruneHandler)
}
