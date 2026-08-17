package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemUpdateCheckResponse is the wire shape for system.update_check.
// "behind_count" = git rev-list --count HEAD..origin/main, so 0 means
// up-to-date, N>0 means there are N commits to pull.
type systemUpdateCheckResponse struct {
	CurrentSHA    string          `json:"current_sha"`
	RemoteSHA     string          `json:"remote_sha"`
	BehindCount   int             `json:"behind_count"`
	Branch        string          `json:"branch"`
	RecentCommits []commitSummary `json:"recent_commits"`
	// PendingCommits are the commits in HEAD..origin/main — i.e. what a
	// `jabali update` WOULD bring in, newest first (GH #300). Empty when
	// up-to-date. Capped to keep the payload bounded on very stale hosts.
	PendingCommits []commitSummary `json:"pending_commits"`
}

// commitSummary is one recent commit on the installed branch, for the
// "what changed" list under the Jabali Panel card.
type commitSummary struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
}

// recentCommits returns the last 3 commits on HEAD as {sha, subject, date}.
// Best-effort: returns nil on any git error (the UI just shows nothing). Uses
// git's %x09 (tab) field separator so the format string carries no literal
// control bytes; %s is a single line so one commit = one output line.
func recentCommits(ctx context.Context) []commitSummary {
	out, err := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "log", "-3",
		"--no-merges", "--format=%h%x09%cI%x09%s", "HEAD")
	if err != nil {
		return nil
	}
	var commits []commitSummary
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, commitSummary{SHA: parts[0], Date: parts[1], Subject: parts[2]})
	}
	return commits
}

// pendingCommits returns the commits in HEAD..origin/main — what a
// `jabali update` would apply — newest first, as {sha, subject, date}
// (GH #300). Must be called AFTER `git fetch origin main`. Best-effort:
// returns nil on any git error. Capped at 50 so a very stale host can't
// produce an unbounded payload.
func pendingCommits(ctx context.Context, target string) []commitSummary {
	out, err := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "log", "-50",
		"--no-merges", "--format=%h%x09%cI%x09%s", "HEAD.."+target)
	if err != nil {
		return nil
	}
	var commits []commitSummary
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, commitSummary{SHA: parts[0], Date: parts[1], Subject: parts[2]})
	}
	return commits
}

const (
	systemRepoDir     = "/opt/jabali-panel"
	systemServiceUser = "jabali"
)

// runAsServiceUser shells out via `sudo -u jabali` because /opt/jabali-panel
// is owned by the unprivileged service user; git 2.35+ refuses to operate
// on a repo owned by a different uid.
func runAsServiceUser(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-u", systemServiceUser, "-H"}, args...)
	c := execCommandContext(ctx, "sudo", full...)
	out, err := c.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return out, fmt.Errorf("%s: %w (stderr: %s)", strings.Join(args, " "), err, stderr)
	}
	return out, nil
}

// systemUpdateCheckParams carries the operator's release channel (GH #445);
// empty/"development" = track origin/main, "stable" = track the `stable` tag.
type systemUpdateCheckParams struct {
	Channel string `json:"channel"`
}

func systemUpdateCheckHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p systemUpdateCheckParams
	_ = json.Unmarshal(raw, &p) // best-effort; empty = development
	current, err := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// Self-heal the origin remote BEFORE fetching. A client whose origin still
	// points at the retired Codeberg / self-hosted-gitea mirror (git.jabali-
	// panel.com / git.linux-hosting.co.il / codeberg.org) would fetch a frozen
	// ref, report "up to date" forever, and never trigger the update that
	// migrates it — a deadlock, since the migrating rewrite in update.go only
	// runs on an actual update the UI won't offer. Mirror that rewrite here so
	// the *check* itself repoints to GitHub (the source of truth after the
	// 2026-07 cutover), fetches real commits, and surfaces the backlog. No-op
	// once origin is already GitHub. Best-effort — a get-url/set-url hiccup
	// must not fail the check.
	if urlOut, uerr := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "remote", "get-url", "origin"); uerr == nil {
		u := strings.TrimSpace(string(urlOut))
		if strings.Contains(u, "codeberg.org") ||
			strings.Contains(u, "git.linux-hosting.co.il") ||
			strings.Contains(u, "git.jabali-panel.com") {
			_, _ = runAsServiceUser(ctx, "git", "-C", systemRepoDir,
				"remote", "set-url", "origin", "https://github.com/shukiv/jabali-panel.git")
		}
	}

	// Resolve the channel target ref to compare against.
	// Always fetch main (required). It also keeps RemoteSHA/branch sane.
	if _, err := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "fetch", "--quiet", "origin", "main"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	target := "origin/main"
	if p.Channel == "stable" {
		// Best-effort sync of the movable stable tag — it does NOT exist on
		// origin until the first `jabali release promote`, and a combined
		// `fetch main +refs/tags/stable` would hard-fail ("couldn't find
		// remote ref") and break the whole check (GH #445). Separate + ignore.
		_, _ = runAsServiceUser(ctx, "git", "-C", systemRepoDir, "fetch", "--quiet", "--force",
			"origin", "+refs/tags/stable:refs/tags/stable")
		if _, terr := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "rev-parse", "--verify", "--quiet", "refs/tags/stable^{commit}"); terr == nil {
			target = "refs/tags/stable"
		} else {
			// No stable build promoted yet: nothing to move to. Compare HEAD
			// to itself -> behind 0 -> "up to date" (conservative).
			target = "HEAD"
		}
	}
	remote, err := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "rev-parse", target)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	behind, err := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "rev-list", "--count", "HEAD.."+target)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	branchOut, _ := runAsServiceUser(ctx, "git", "-C", systemRepoDir, "rev-parse", "--abbrev-ref", "HEAD")

	return systemUpdateCheckResponse{
		CurrentSHA:     strings.TrimSpace(string(current)),
		RemoteSHA:      strings.TrimSpace(string(remote)),
		BehindCount:    parseIntSafe(strings.TrimSpace(string(behind))),
		Branch:         strings.TrimSpace(string(branchOut)),
		RecentCommits:  recentCommits(ctx),
		PendingCommits: pendingCommits(ctx, target),
	}, nil
}

func parseIntSafe(s string) int {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func init() {
	Default.Register("system.update_check", systemUpdateCheckHandler)
}
