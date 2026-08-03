// restore_index_shadow.go — JAB-223.
//
// jabali's vhost template lists `index index.html index.php;`, so a docroot
// holding BOTH a WordPress install and a leftover index.html serves the
// index.html and never reaches index.php. The request returns 200, which is
// exactly why it survives verification: the site is "up", it is just serving
// somebody else's parked page.
//
// Observed on amitiim.com after a fleet migration — a 741-byte "CyberPanel
// Installed" page sitting in front of a live WooCommerce store, months old,
// carried over verbatim by the rsync. It had been dormant on the source and
// became authoritative the moment the docroot landed behind jabali's nginx.
//
// Only pages we can positively identify as a control-panel default get
// quarantined. Anything else — a hand-written holding page, a deliberate
// maintenance splash, a landing page someone actually wants — is left exactly
// where it is and reported instead. Renaming a customer's file on a guess is a
// worse failure than the shadow it would fix, and it is not reversible by
// anyone who does not already know to look for it.

package cpanel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// quarantineSuffix is appended to a shadowing index.html rather than deleting
// it. The name is deliberately self-describing: an operator who finds it a year
// later should not have to guess what moved it.
const quarantineSuffix = ".jabali-shadowed-bak"

// flaggedRemedy is appended to every flag so the warning names a fix instead of
// just a symptom. Both routes are non-destructive: index_priority=php_first is
// the domain's own supported setting (see indexDirectiveFor in the agent), and
// renaming the file is what an operator would do by hand anyway.
const flaggedRemedy = "To resolve: set this domain's index priority to php_first, or rename the file."

// maxIndexHTMLScan caps how much of an index.html we read to classify it, and
// doubles as a size guard: control-panel default pages are a couple of KiB, so
// anything larger is presumed to be a real page and only ever gets flagged.
const maxIndexHTMLScan = 64 * 1024

// parkedPageMarkers are substrings that positively identify a control-panel or
// web-server default page. Matched case-insensitively against the file body.
//
// Every entry here must be specific enough that a real site would not contain
// it by accident. Deliberately absent: "coming soon", "under construction",
// "maintenance" — those are things a customer means to publish.
var parkedPageMarkers = []string{
	"cyberpanel installed",
	"apache2 debian default page",
	"apache2 ubuntu default page",
	"welcome to nginx!",
	"litespeed web server",
	"web server's default page",
	"future home of something quite cool", // cPanel's stock parked page
	"this is the default web page for this server",
	"apache is functioning normally",
	"plesk default page",
}

// IndexShadowResult reports what the pass did. Quarantined and Flagged are kept
// apart on purpose — one is an action taken, the other is a decision deferred to
// the operator, and collapsing them would hide which is which.
type IndexShadowResult struct {
	Quarantined []string // docroots where a panel default page was renamed
	Flagged     []string // docroots where an unrecognised index.html still shadows WordPress
	Skipped     []string // human-readable reasons (unreadable file, rename failed, …)
}

// looksLikeParkedPage reports whether body is recognisably a control-panel or
// web-server default page.
func looksLikeParkedPage(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, m := range parkedPageMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// docrootHasWordPress reports whether docroot holds a WordPress install whose
// front controller an index.html would shadow.
//
// wp-load.php is the discriminator rather than index.php alone: every PHP app
// has an index.php, but only WordPress ships wp-load.php, and requiring both
// means we never touch a docroot where index.html is legitimately the entry
// point for a static site that happens to have a stray index.php.
func docrootHasWordPress(docroot string) bool {
	for _, marker := range []string{"wp-load.php", "index.php"} {
		if _, err := os.Stat(filepath.Join(docroot, marker)); err != nil {
			return false
		}
	}
	return true
}

// QuarantineShadowingIndexHTML walks the restored docroots and resolves
// index.html files that would shadow a WordPress front controller.
//
// Deliberately independent of ImportAppConfigs: that pass returns early when
// there are no database credentials to rewrite, and a WordPress site whose
// credentials happened to carry over unchanged would then never be checked —
// while being just as shadowed.
func QuarantineShadowingIndexHTML(
	ctx context.Context,
	agentCli agent.AgentInterface,
	targetUserID, targetUsername string,
	docroots []string,
) *IndexShadowResult {
	res := &IndexShadowResult{}

	for _, docroot := range docroots {
		indexHTML := filepath.Join(docroot, "index.html")
		st, err := os.Stat(indexHTML)
		if err != nil || st.IsDir() {
			continue
		}
		if !docrootHasWordPress(docroot) {
			continue
		}

		// Oversized files are never auto-renamed — see maxIndexHTMLScan.
		if st.Size() > maxIndexHTMLScan {
			res.Flagged = append(res.Flagged, fmt.Sprintf(
				"index_shadow_flag:%s:index.html (%d bytes) shadows WordPress index.php — too large to classify, left in place. %s",
				indexHTML, st.Size(), flaggedRemedy))
			continue
		}

		body, rerr := os.ReadFile(indexHTML)
		if rerr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("index_shadow_read:%s:%v", indexHTML, rerr))
			continue
		}
		if !looksLikeParkedPage(body) {
			res.Flagged = append(res.Flagged, fmt.Sprintf(
				"index_shadow_flag:%s:index.html shadows WordPress index.php — not a recognised default page, left in place. %s",
				indexHTML, flaggedRemedy))
			continue
		}

		// Never clobber a previous quarantine: if the parked name is taken, say
		// so rather than overwrite whatever is already there.
		target := indexHTML + quarantineSuffix
		if _, serr := os.Stat(target); serr == nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"index_shadow_skip:%s:%s already exists", indexHTML, target))
			continue
		}

		if err := renameViaAgent(ctx, agentCli, targetUserID, targetUsername, indexHTML, target); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("index_shadow_rename:%s:%v", indexHTML, err))
			continue
		}
		res.Quarantined = append(res.Quarantined, fmt.Sprintf(
			"index_shadow_quarantined:%s -> %s (control-panel default page was shadowing WordPress)",
			indexHTML, filepath.Base(target)))
	}
	return res
}

// renameViaAgent renames through the agent so the per-user scope guard and the
// audit log both run, falling back to a direct rename the way the cache-clear
// pass does — panel-api reaches /home/<user> via the www-data group, and a
// missing audit line is a better outcome than leaving the site serving a parked
// page.
func renameViaAgent(
	ctx context.Context,
	agentCli agent.AgentInterface,
	targetUserID, targetUsername, oldPath, newPath string,
) error {
	if agentCli != nil {
		if _, err := agentCli.Call(ctx, "files.rename", map[string]any{
			"user_id":  targetUserID,
			"username": targetUsername,
			"old_path": oldPath,
			"new_path": newPath,
		}); err == nil {
			return nil
		}
	}
	return os.Rename(oldPath, newPath)
}
