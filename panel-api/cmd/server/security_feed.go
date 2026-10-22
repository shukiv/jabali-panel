package main

// Security advisory feed: the third way a security fix reaches a server
// before the monthly stable promotion.
//
// ensure_security_updates (install.sh) installs vendor security updates every
// day, and a hotfix release (release-hotfix.yml) moves `stable` to a fix in
// Jabali's own code. Both still wait for a daily timer: the package lists
// refresh once a day, unattended-upgrades runs at the time set on the Updates
// page, and the panel self-update runs once a night. An advisory in this feed
// makes a server act within the hour instead.
//
// The feed is security/advisories.json on main, so publishing an advisory is
// a reviewed commit (docs/runbooks/security-advisory.md).
// jabali-security-feed.timer runs `jabali security feed-check` hourly, as
// root. For each advisory it has not finished with, it:
//   - "apt": refreshes the package lists and runs unattended-upgrades now,
//     when OS security auto-updates are on (Updates page; on by default);
//   - "jabali": does nothing when HEAD already contains one of fixed_in.
//     Otherwise it starts the panel update now, even where panel self-update
//     is off (it is opt-in): a security fix installs itself. A server on the stable channel that runs a build
//     AHEAD of stable cannot get the fix that way: `jabali update` never
//     moves it back. There the advisory's converger, an install.sh function
//     from a newer main commit, can apply the fix's server-side part. That is
//     the only action that runs code the server is not on, so it runs only
//     from a feed signed with the release key (ADR-0162);
//   - alerts the admins (notification kind security.advisory) with what it
//     did and, for anything it could not do, the command to run.
//
// "apt" and "jabali" act only where security advisories apply automatically
// (Updates page, security_enabled; on by default). Where an admin turned that
// off, the alert gives the commands instead.
//
// Each action runs at most once per advisory; the state file says which.
// While this build embeds no release key the feed's signature is not checked
// and the converger is refused: the alert carries the manual command instead.
// The other actions only make a server do sooner what it does anyway.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/releasesig"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

const (
	defaultSecurityFeedURL = "https://raw.githubusercontent.com/shukiv/jabali-panel/main/security/advisories.json"
	// Root's own directory: not under /var/lib/jabali-panel, which the
	// service user owns (secureSecurityFeedDir).
	securityFeedStatePath = "/var/lib/jabali-security-feed/state.json"
	securityFeedMaxBytes  = 256 << 10
	// An advisory published this long before a server first sees it is
	// recorded and left alone: a fresh install must not replay old alerts,
	// and the fixes in it reached the server through its own updates.
	securityAdvisoryMaxAge = 30 * 24 * time.Hour
	securityAdvisoryURL    = "https://github.com/shukiv/jabali-panel/"
)

var (
	advisoryIDRe    = regexp.MustCompile(`^JSA-[0-9]{4}-[0-9]{3,6}$`)
	commitSHARe     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	convergerNameRe = regexp.MustCompile(`^ensure_[a-z0-9_]{1,64}$`)
)

type securityFeed struct {
	Schema     int                `json:"schema"`
	Advisories []securityAdvisory `json:"advisories"`
}

type securityAdvisory struct {
	ID        string `json:"id"`
	Published string `json:"published"` // YYYY-MM-DD
	Severity  string `json:"severity"`  // critical | high
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	URL       string `json:"url"`
	// Apt: install pending OS and vendor security updates now.
	Apt    bool                    `json:"apt"`
	Jabali *securityAdvisoryJabali `json:"jabali"`
}

// securityAdvisoryJabali describes a fix in Jabali's own code.
type securityAdvisoryJabali struct {
	// FixedIn lists commits that contain the fix: the main commit and the
	// stable hotfix commit. A server whose HEAD contains any of them is fixed.
	FixedIn []string `json:"fixed_in"`
	// Converger, from ConvergerCommit's install.sh, applies the fix's
	// server-side part on a server ahead of stable. Optional.
	Converger       string `json:"converger"`
	ConvergerCommit string `json:"converger_commit"`
}

// parseSecurityFeed reads and checks the whole feed. One bad advisory
// rejects it all: a server acts on a feed it fully understands or not at all.
func parseSecurityFeed(body []byte) ([]securityAdvisory, error) {
	var f securityFeed
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	if f.Schema != 1 {
		return nil, fmt.Errorf("feed schema %d is not one this build reads (1); update the panel", f.Schema)
	}
	seen := map[string]bool{}
	for i, a := range f.Advisories {
		if err := a.validate(); err != nil {
			return nil, fmt.Errorf("advisory %d (%q): %w", i, a.ID, err)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("advisory %s is listed twice", a.ID)
		}
		seen[a.ID] = true
	}
	return f.Advisories, nil
}

func (a securityAdvisory) validate() error {
	if !advisoryIDRe.MatchString(a.ID) {
		return errors.New("id must look like JSA-2026-001")
	}
	if _, err := time.Parse("2006-01-02", a.Published); err != nil {
		return errors.New("published must be a YYYY-MM-DD date")
	}
	if a.Severity != "critical" && a.Severity != "high" {
		return errors.New("severity must be critical or high")
	}
	if err := checkAdvisoryText("title", a.Title, 1, 120); err != nil {
		return err
	}
	if err := checkAdvisoryText("summary", a.Summary, 0, 600); err != nil {
		return err
	}
	if a.URL != "" && !strings.HasPrefix(a.URL, securityAdvisoryURL) {
		return fmt.Errorf("url must start with %s", securityAdvisoryURL)
	}
	if !a.Apt && a.Jabali == nil {
		return errors.New("the advisory asks for nothing: set apt or jabali")
	}
	if j := a.Jabali; j != nil {
		if len(j.FixedIn) == 0 || len(j.FixedIn) > 10 {
			return errors.New("jabali.fixed_in must list 1 to 10 commits")
		}
		for _, c := range j.FixedIn {
			if !commitSHARe.MatchString(c) {
				return fmt.Errorf("jabali.fixed_in %q is not a full commit SHA", c)
			}
		}
		if (j.Converger == "") != (j.ConvergerCommit == "") {
			return errors.New("jabali.converger and jabali.converger_commit go together")
		}
		if j.Converger != "" {
			if !convergerNameRe.MatchString(j.Converger) {
				return errors.New("jabali.converger must be an install.sh ensure_* function")
			}
			if !commitSHARe.MatchString(j.ConvergerCommit) {
				return errors.New("jabali.converger_commit is not a full commit SHA")
			}
		}
	}
	return nil
}

func checkAdvisoryText(field, s string, minLen, maxLen int) error {
	n := len([]rune(s))
	if n < minLen || n > maxLen {
		return fmt.Errorf("%s must be %d to %d characters", field, minLen, maxLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s has a control character", field)
		}
	}
	return nil
}

// verifySecurityFeed checks the feed's minisign signature against pubKey.
// No key embedded: nothing to check, and the caller treats the feed as
// unsigned. A key embedded: the signature is required.
func verifySecurityFeed(pubKey string, body []byte, sig []byte, sigErr error) (signed bool, err error) {
	if strings.TrimSpace(pubKey) == "" {
		return false, nil
	}
	pk, err := releasesig.ParsePublicKey(pubKey)
	if err != nil {
		return false, fmt.Errorf("embedded release signing key is unusable: %w", err)
	}
	if sigErr != nil {
		return false, fmt.Errorf("this build requires a signed security feed and the signature could not be fetched: %w", sigErr)
	}
	if err := pk.Verify(body, string(sig)); err != nil {
		return false, fmt.Errorf("security feed signature: %w", err)
	}
	return true, nil
}

// advisoryState records what has been done for one advisory.
type advisoryState struct {
	FirstSeen time.Time `json:"first_seen"`
	// Ignored: published long before this server first saw it.
	Ignored bool `json:"ignored,omitempty"`
	// Fixed: HEAD contains one of jabali.fixed_in.
	Fixed bool `json:"fixed,omitempty"`
	// The *Done flags mean the action ran, or the admin was told how to run it.
	AptDone       bool `json:"apt_done,omitempty"`
	UpdateDone    bool `json:"update_done,omitempty"`
	ConvergerDone bool `json:"converger_done,omitempty"`
	// Unsent holds alert lines whose notification could not be queued yet.
	Unsent []string `json:"unsent,omitempty"`
}

type securityFeedState struct {
	Advisories map[string]*advisoryState `json:"advisories"`
}

// securityOffNote opens the alert line for an action the admin turned off.
const securityOffNote = "Security advisories do not apply automatically on this server (Updates page)."

// securityFeedHost is the server as an advisory sees it.
type securityFeedHost struct {
	Now             func() time.Time
	AptEnabled      bool // OS security auto-updates (Updates page)
	SecurityEnabled bool // apply security advisories automatically (Updates page)
	OnStable        bool // release channel is stable
	Signed          bool // the feed's signature was verified with the embedded key
	LiveSchema      uint // the database's schema version; 0 when it could not be read
	// Git runs git in the panel checkout, as the service user.
	Git            gitOutFunc
	StartApt       func() error
	StartUpdate    func() error
	StartConverger func(securityAdvisory) error
}

// processAdvisory takes the actions an advisory still needs and returns the
// lines to tell the admins. It records what it did in st.
func processAdvisory(h securityFeedHost, adv securityAdvisory, st *advisoryState) []string {
	if st.FirstSeen.IsZero() {
		st.FirstSeen = h.Now()
		if pub, err := time.Parse("2006-01-02", adv.Published); err == nil && st.FirstSeen.Sub(pub) > securityAdvisoryMaxAge {
			st.Ignored = true
		}
	}
	if st.Ignored {
		return nil
	}
	var lines []string
	if adv.Apt && !st.AptDone {
		st.AptDone = true
		switch {
		case !h.SecurityEnabled:
			lines = append(lines, securityOffNote+" Install the updates now: apt-get update && unattended-upgrade -v")
		case !h.AptEnabled:
			lines = append(lines, "OS security auto-updates are off on this server. Install the updates now: apt-get update && unattended-upgrade -v")
		default:
			if err := h.StartApt(); err != nil {
				lines = append(lines, fmt.Sprintf("Could not start the OS security updates (%v). Install them now: apt-get update && unattended-upgrade -v", err))
			} else {
				lines = append(lines, "OS security updates are being installed now.")
			}
		}
	}
	if adv.Jabali != nil && !st.Fixed {
		if headContainsAny(h.Git, adv.Jabali.FixedIn) {
			st.Fixed = true
		} else {
			lines = append(lines, jabaliAdvisoryAction(h, adv, st)...)
		}
	}
	return lines
}

func jabaliAdvisoryAction(h securityFeedHost, adv securityAdvisory, st *advisoryState) []string {
	// held says why `jabali update` would leave this server where it is, so
	// an update cannot deliver the fix. It asks updateResetTarget, the
	// update's own decision: stay on a build the stable release is behind,
	// or refuse a stable release that could not serve this server's database
	// (a main build next to a hotfix stable that main has not merged back).
	// A server AT stable waiting for a promotion moves, so it gets the update
	// and never a converger from main.
	held := ""
	if h.OnStable {
		target, _, err := updateResetTarget(h.Git, "refs/tags/stable", true, h.LiveSchema)
		switch {
		case err != nil:
			held = fmt.Sprintf("`jabali update` keeps this server where it is: it cannot move to the stable release (%v).", err)
		case target == "HEAD":
			held = "This server runs a build ahead of the stable release, so `jabali update` keeps it where it is."
		}
	}
	if held == "" {
		if st.UpdateDone {
			return nil
		}
		st.UpdateDone = true
		if !h.SecurityEnabled {
			return []string{securityOffNote + " Update now: jabali update"}
		}
		if err := h.StartUpdate(); err != nil {
			return []string{fmt.Sprintf("Could not start the panel update (%v). Update now: jabali update", err)}
		}
		return []string{"The panel update has started; it installs the fixed release."}
	}

	if st.ConvergerDone {
		return nil
	}
	st.ConvergerDone = true
	j := adv.Jabali
	if j.Converger == "" {
		return []string{held + " The fix reaches it with the next stable release."}
	}
	manual := convergerManualCommand(adv)
	switch {
	case !h.Signed:
		return []string{held + " This build applies a fix from a newer commit only when the feed is signed. Apply the fix's server-side part now: " + manual}
	case !h.SecurityEnabled:
		return []string{held + " " + securityOffNote + " Apply the fix's server-side part now: " + manual}
	}
	if err := checkConvergerCommit(h.Git, j.ConvergerCommit); err != nil {
		return []string{fmt.Sprintf("%s Not applying the fix automatically: %v. Apply it now: %s", held, err, manual)}
	}
	if err := h.StartConverger(adv); err != nil {
		return []string{fmt.Sprintf("%s Could not start the fix (%v). Apply it now: %s", held, err, manual)}
	}
	return []string{fmt.Sprintf("%s The fix's server-side part (%s from %s) is being applied now; the rest arrives with the next stable release.",
		held, j.Converger, j.ConvergerCommit[:9])}
}

// headContainsAny reports whether HEAD contains any of the commits. A commit
// the checkout does not have yet does not count.
func headContainsAny(git gitOutFunc, commits []string) bool {
	for _, c := range commits {
		if _, err := git("merge-base", "--is-ancestor", c, "HEAD"); err == nil {
			return true
		}
	}
	return false
}

// checkConvergerCommit allows a converger only from a commit on main that is
// built on this server's HEAD: newer code than the server runs, never older.
func checkConvergerCommit(git gitOutFunc, commit string) error {
	if _, err := git("merge-base", "--is-ancestor", "HEAD", commit); err != nil {
		return fmt.Errorf("%s is not built on this server's build", commit[:9])
	}
	if _, err := git("merge-base", "--is-ancestor", commit, "refs/remotes/origin/main"); err != nil {
		return fmt.Errorf("%s is not on main", commit[:9])
	}
	return nil
}

// convergerManualCommand is what an admin runs to apply a converger by hand,
// the way jabali-security-converger does it.
func convergerManualCommand(adv securityAdvisory) string {
	dir := "/root/" + strings.ToLower(adv.ID)
	return fmt.Sprintf("sudo -u jabali git -C %[1]s fetch -q origin main && mkdir -p %[2]s && "+
		"sudo -u jabali git -C %[1]s archive %[3]s | tar -x -C %[2]s && "+
		"bash -c 'cd %[2]s && source ./install.sh >/dev/null 2>&1; REPO_DIR=$PWD; %[4]s'",
		defaultRepoDir, dir, adv.Jabali.ConvergerCommit, adv.Jabali.Converger)
}

// advisoryEnvelope is the admin alert for an advisory.
func advisoryEnvelope(adv securityAdvisory, lines []string) notifications.Envelope {
	severity := models.NotificationSeverityError
	if adv.Severity == "critical" {
		severity = models.NotificationSeverityCritical
	}
	var b strings.Builder
	if adv.Summary != "" {
		b.WriteString(adv.Summary)
		b.WriteString("\n\n")
	}
	b.WriteString(strings.Join(lines, "\n"))
	if adv.URL != "" {
		b.WriteString("\n\nDetails: ")
		b.WriteString(adv.URL)
	}
	return notifications.Envelope{
		EventKind: "security.advisory",
		Severity:  severity,
		Title:     "Security advisory " + adv.ID + ": " + adv.Title,
		Body:      b.String(),
		Deeplink:  "/jabali-admin/updates",
	}
}

// securityFeedRun is one feed check with everything it touches injected.
type securityFeedRun struct {
	FeedURL   string
	StatePath string
	PubKey    string
	Fetch     func(ctx context.Context, url string) ([]byte, error)
	// Host is built after the feed verified, so it knows whether it was signed.
	Host    func(ctx context.Context, signed bool) (securityFeedHost, error)
	Publish func(ctx context.Context, env notifications.Envelope) error
	Log     func(format string, args ...any)
	// TrustedDir, when set, is where the check of the state directory's
	// parents stops: tests, whose temp dirs sit under the user's own
	// group-writable directories. Empty: every parent up to /.
	TrustedDir string
}

// run fetches, verifies and parses the feed, then handles each advisory.
// A feed it cannot fetch, verify or read changes nothing.
func (r securityFeedRun) run(ctx context.Context) error {
	body, err := r.Fetch(ctx, r.FeedURL)
	if err != nil {
		return fmt.Errorf("fetch security feed: %w", err)
	}
	var sig []byte
	var sigErr error
	if strings.TrimSpace(r.PubKey) != "" {
		sig, sigErr = r.Fetch(ctx, r.FeedURL+".minisig")
	}
	signed, err := verifySecurityFeed(r.PubKey, body, sig, sigErr)
	if err != nil {
		return err
	}
	advisories, err := parseSecurityFeed(body)
	if err != nil {
		return err
	}
	if len(advisories) == 0 {
		r.Log("security feed: no advisories")
		return nil
	}

	// Root writes the converger's script next to the state and runs it, so
	// the directory must be one only root can change.
	if err := secureSecurityFeedDir(filepath.Dir(r.StatePath), r.TrustedDir); err != nil {
		return err
	}
	unlock, err := lockSecurityFeedState(r.StatePath)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := loadSecurityFeedState(r.StatePath)
	if err != nil {
		return err
	}
	host, err := r.Host(ctx, signed)
	if err != nil {
		return err
	}
	for _, adv := range advisories {
		st := state.Advisories[adv.ID]
		if st == nil {
			st = &advisoryState{}
			state.Advisories[adv.ID] = st
		}
		lines := append(st.Unsent, processAdvisory(host, adv, st)...)
		st.Unsent = nil
		if len(lines) == 0 {
			continue
		}
		r.Log("security feed: %s: %s", adv.ID, strings.Join(lines, " | "))
		if err := r.Publish(ctx, advisoryEnvelope(adv, lines)); err != nil {
			r.Log("security feed: %s: could not queue the admin alert (%v); trying again next run", adv.ID, err)
			st.Unsent = lines
		}
	}
	return saveSecurityFeedState(r.StatePath, state)
}

// secureSecurityFeedDir creates dir if it is missing and refuses it unless no
// other user can change it: a real directory (not a symlink) owned by this
// process's user (root in production) with no group or other access, and
// every directory above it, up to trusted or /, owned by root or this user
// and writable by nobody else, unless sticky (as /tmp is).
func secureSecurityFeedDir(dir, trusted string) error {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("security feed state dir: %w", err)
	}
	uid := uint32(os.Geteuid())
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("security feed state dir: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.IsDir() || st.Uid != uid || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("security feed state dir %s must be a directory owned by uid %d with mode 0700 (it is %v, uid %d); refusing to use it", dir, uid, fi.Mode(), statUID(st))
	}
	for p := filepath.Dir(dir); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("security feed state dir: %w", err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		othersWrite := fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0
		if !ok || !fi.IsDir() || (st.Uid != 0 && st.Uid != uid) || othersWrite {
			return fmt.Errorf("security feed state dir %s is under %s, which another user can change (%v, uid %d); refusing to use it", dir, p, fi.Mode(), statUID(st))
		}
		if p == filepath.Dir(p) || (trusted != "" && p == filepath.Clean(trusted)) {
			return nil
		}
	}
}

func statUID(st *syscall.Stat_t) int64 {
	if st == nil {
		return -1
	}
	return int64(st.Uid)
}

func lockSecurityFeedState(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("security feed lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("security feed lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func loadSecurityFeedState(path string) (*securityFeedState, error) {
	s := &securityFeedState{Advisories: map[string]*advisoryState{}}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read security feed state: %w", err)
	}
	b, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("read security feed state: %w", err)
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("security feed state %s is corrupt (%w); move it aside to start over", path, err)
	}
	if s.Advisories == nil {
		s.Advisories = map[string]*advisoryState{}
	}
	return s, nil
}

// saveSecurityFeedState writes the state atomically, readable by root only.
func saveSecurityFeedState(path string, s *securityFeedState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("write security feed state: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write security feed state: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
