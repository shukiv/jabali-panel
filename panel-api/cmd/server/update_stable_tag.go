package main

import (
	"fmt"
	"strings"
)

// classifyStableTagFetch decides what a failed
// `git fetch --force origin +refs/tags/stable:refs/tags/stable` actually means.
//
// The `stable` tag does not exist on origin until someone runs the first
// `jabali release promote`, so a fetch that fails BECAUSE the ref is not there
// is an ordinary state on a fresh host — the caller stays on the current build
// and says so. That is why the call was written as `_ = asUser(...)` (GH #445).
//
// Every other failure is different in kind, and swallowing it is one of the
// ways a host silently goes backwards (JAB-210). A transport, auth or
// dubious-ownership failure leaves the LOCAL refs/tags/stable untouched — so
// the `rev-parse --verify` immediately after still succeeds, against whatever
// commit was promoted the last time a fetch worked. The updater then resets the
// checkout to that stale commit and hands the release installer a tarball older
// than the binary already on disk. The schema-downgrade guard catches the worst
// outcome, but only after the operator has burned an update cycle on a silent
// no-op, and only when a database is configured.
//
// So: "no such ref" is expected, anything else aborts the update before a
// single binary is touched.
func classifyStableTagFetch(err error, output string) (exists bool, fatal error) {
	if err == nil {
		return true, nil
	}
	if isMissingRemoteRef(output) {
		return false, nil
	}
	return false, fmt.Errorf(
		"fetching the `stable` tag from origin failed: %w\n"+
			"  Refusing to fall back to this host's local copy of refs/tags/stable: it can be older\n"+
			"  than the promoted build, and resetting to it would install a binary older than the\n"+
			"  one running now. Fix the fetch (network, credentials, or `git config --global\n"+
			"  --add safe.directory <repo>` for a root-run update on a non-root checkout) and\n"+
			"  re-run `jabali update` — or switch this host to the development channel.\n"+
			"  git said:\n%s",
		err, indentLines(strings.TrimSpace(output), "    "))
}

// isMissingRemoteRef reports whether git's output says the ref simply is not
// on the remote. The wording has changed across git versions ("couldn't find
// remote ref" on modern git, "Couldn't find remote ref" older, and the
// refspec form can report the ref matched nothing), so match on fragments
// rather than a whole line — and lowercase first, since the leading capital
// differs between versions.
func isMissingRemoteRef(output string) bool {
	o := strings.ToLower(output)
	for _, frag := range []string{
		"couldn't find remote ref",
		"could not find remote ref",
		"matched no remote refs",
		"no such ref",
	} {
		if strings.Contains(o, frag) {
			return true
		}
	}
	return false
}

// indentLines prefixes every line so multi-line git output stays visually
// attached to the error that quotes it.
func indentLines(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
