// Package panelrepo builds the git command line for the panel checkout.
//
// Since #2399 (ADR-0180) the checkout is root-owned, .git included, and git
// refuses a repository another uid owns ("detected dubious ownership"), so
// git run as the service user fails there (GH #2467). Root runs it the way
// `jabali update` does (gitRootCmd): without system or global config, with
// hooks, fsmonitor and pager off. A checkout from before #2399 still belongs
// to the service user, and git runs as that user, as before.
package panelrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Dir is the panel checkout.
const Dir = "/opt/jabali-panel"

// ServiceUser owns a checkout from before #2399.
const ServiceUser = "jabali"

// OwnerUID returns the uid that owns dir's .git, without following a
// symlink. Tests replace it.
var OwnerUID = func(dir string) (uint32, error) {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("stat %s: no unix ownership info", filepath.Join(dir, ".git"))
	}
	return st.Uid, nil
}

// GitArgv returns the command line that runs git with args against the
// checkout at dir, as the user git accepts there.
func GitArgv(dir string, args ...string) ([]string, error) {
	uid, err := OwnerUID(dir)
	if err != nil {
		return nil, err
	}
	if uid == 0 {
		argv := []string{
			"env", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"git", "-C", dir,
			"-c", "core.hooksPath=/dev/null",
			"-c", "core.fsmonitor=false",
			"-c", "core.pager=cat",
		}
		return append(argv, args...), nil
	}
	return append([]string{"sudo", "-u", ServiceUser, "-H", "git", "-C", dir}, args...), nil
}
