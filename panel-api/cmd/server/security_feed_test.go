package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// feedRepo is a checkout with main m0..m3 (m2 carries the fix), a side commit
// built on m1 that never reached main, origin/main at m3 and `stable` at m0.
// It returns the git func and the commits by name; HEAD is left on m0.
func feedRepo(t *testing.T) (gitOutFunc, map[string]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) (string, error) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := git(args...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	c := map[string]string{}
	commit := func(name string) {
		must("commit", "-q", "--allow-empty", "-m", name)
		c[name] = must("rev-parse", "HEAD")
	}
	must("init", "-q", "-b", "main")
	for _, n := range []string{"m0", "m1", "m2", "m3"} {
		commit(n)
	}
	must("update-ref", "refs/remotes/origin/main", c["m3"])
	must("tag", "stable", c["m0"])
	must("checkout", "-q", "-b", "side", c["m1"])
	commit("side")
	must("checkout", "-q", c["m0"])
	return git, c
}

func checkoutAt(t *testing.T, git gitOutFunc, sha string) {
	t.Helper()
	if out, err := git("checkout", "-q", sha); err != nil {
		t.Fatalf("checkout %s: %v: %s", sha, err, out)
	}
}

type hostCalls struct{ apt, update, converger int }

func testHost(git gitOutFunc, calls *hostCalls) securityFeedHost {
	return securityFeedHost{
		Now:            func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
		AptEnabled:     true,
		JabaliEnabled:  true,
		OnStable:       true,
		Signed:         true,
		Git:            git,
		StartApt:       func() error { calls.apt++; return nil },
		StartUpdate:    func() error { calls.update++; return nil },
		StartConverger: func(securityAdvisory) error { calls.converger++; return nil },
	}
}

func jabaliAdvisory(fix, convergerCommit string) securityAdvisory {
	a := securityAdvisory{
		ID: "JSA-2026-001", Published: "2026-10-21", Severity: "critical", Title: "Fix in the panel",
		Jabali: &securityAdvisoryJabali{FixedIn: []string{fix}},
	}
	if convergerCommit != "" {
		a.Jabali.Converger, a.Jabali.ConvergerCommit = "ensure_security_updates", convergerCommit
	}
	return a
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestParseSecurityFeed(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	valid := func() securityAdvisory {
		return securityAdvisory{
			ID: "JSA-2026-001", Published: "2026-10-22", Severity: "critical", Title: "PHP security release",
			Summary: "Remote code execution in PHP.", URL: "https://github.com/shukiv/jabali-panel/discussions/2212",
			Apt: true, Jabali: &securityAdvisoryJabali{FixedIn: []string{sha}, Converger: "ensure_security_updates", ConvergerCommit: sha},
		}
	}
	encode := func(schema int, advs ...securityAdvisory) []byte {
		b, _ := json.Marshal(securityFeed{Schema: schema, Advisories: advs})
		return b
	}
	if advs, err := parseSecurityFeed(encode(1, valid())); err != nil || len(advs) != 1 {
		t.Fatalf("valid feed: %v (%d advisories)", err, len(advs))
	}
	bad := map[string]func(*securityAdvisory){
		"id":                   func(a *securityAdvisory) { a.ID = "CVE-2026-1" },
		"date":                 func(a *securityAdvisory) { a.Published = "22/10/2026" },
		"severity":             func(a *securityAdvisory) { a.Severity = "low" },
		"empty title":          func(a *securityAdvisory) { a.Title = "" },
		"long title":           func(a *securityAdvisory) { a.Title = strings.Repeat("x", 121) },
		"control char":         func(a *securityAdvisory) { a.Summary = "line\nbreak" },
		"foreign url":          func(a *securityAdvisory) { a.URL = "https://example.com/advisory" },
		"no action":            func(a *securityAdvisory) { a.Apt, a.Jabali = false, nil },
		"no fixed_in":          func(a *securityAdvisory) { a.Jabali.FixedIn = nil },
		"short sha":            func(a *securityAdvisory) { a.Jabali.FixedIn = []string{"abc1234"} },
		"converger alone":      func(a *securityAdvisory) { a.Jabali.ConvergerCommit = "" },
		"converger name":       func(a *securityAdvisory) { a.Jabali.Converger = "rm -rf /" },
		"converger not sha":    func(a *securityAdvisory) { a.Jabali.ConvergerCommit = "main" },
		"converger non-ensure": func(a *securityAdvisory) { a.Jabali.Converger = "main" },
	}
	for name, mutate := range bad {
		a := valid()
		mutate(&a)
		if _, err := parseSecurityFeed(encode(1, a)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseSecurityFeed(encode(1, valid(), valid())); err == nil {
		t.Error("duplicate ids: accepted")
	}
	if _, err := parseSecurityFeed(encode(2, valid())); err == nil {
		t.Error("schema 2: accepted")
	}
}

// The feed servers read is this file on main. A feed they cannot parse is
// rejected whole, so a bad advisory must fail here, in the PR that adds it.
func TestSecurityFeedFileInRepoParses(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("../../../security/advisories.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSecurityFeed(body); err != nil {
		t.Fatalf("security/advisories.json: %v", err)
	}
	if len(body) > securityFeedMaxBytes {
		t.Fatalf("security/advisories.json is %d bytes; servers read at most %d", len(body), securityFeedMaxBytes)
	}
}

func TestProcessAdvisory_Apt(t *testing.T) {
	t.Parallel()
	adv := securityAdvisory{ID: "JSA-2026-002", Published: "2026-10-22", Severity: "critical", Title: "PHP", Apt: true}

	var calls hostCalls
	h := testHost(nil, &calls)
	st := &advisoryState{}
	lines := processAdvisory(h, adv, st)
	if calls.apt != 1 || !strings.Contains(joined(lines), "being installed now") {
		t.Fatalf("apt on: calls=%d lines=%q", calls.apt, lines)
	}
	if lines := processAdvisory(h, adv, st); calls.apt != 1 || len(lines) != 0 {
		t.Errorf("second run repeated the apt action: calls=%d lines=%q", calls.apt, lines)
	}

	calls = hostCalls{}
	h.AptEnabled = false
	lines = processAdvisory(h, adv, &advisoryState{})
	if calls.apt != 0 || !strings.Contains(joined(lines), "unattended-upgrade -v") {
		t.Errorf("apt off: calls=%d lines=%q", calls.apt, lines)
	}

	h.AptEnabled = true
	h.StartApt = func() error { return errors.New("unit exists") }
	lines = processAdvisory(h, adv, &advisoryState{})
	if !strings.Contains(joined(lines), "unit exists") || !strings.Contains(joined(lines), "unattended-upgrade -v") {
		t.Errorf("apt start failure: lines=%q", lines)
	}
}

// A server whose build contains the fix does nothing and says nothing.
func TestProcessAdvisory_AlreadyFixed(t *testing.T) {
	git, c := feedRepo(t)
	checkoutAt(t, git, c["m3"])
	var calls hostCalls
	st := &advisoryState{}
	lines := processAdvisory(testHost(git, &calls), jabaliAdvisory(c["m2"], c["m2"]), st)
	if len(lines) != 0 || calls != (hostCalls{}) || !st.Fixed {
		t.Errorf("fixed server: lines=%q calls=%+v fixed=%v", lines, calls, st.Fixed)
	}
}

// A server AT stable waiting for a promotion is updated, never given a
// converger from main — even from a signed feed.
func TestProcessAdvisory_AtStableUpdatesNotConverger(t *testing.T) {
	git, c := feedRepo(t) // HEAD = stable = m0
	var calls hostCalls
	h := testHost(git, &calls)
	st := &advisoryState{}
	adv := jabaliAdvisory(c["m2"], c["m2"])
	lines := processAdvisory(h, adv, st)
	if calls.update != 1 || calls.converger != 0 || !strings.Contains(joined(lines), "update has started") {
		t.Fatalf("at stable: calls=%+v lines=%q", calls, lines)
	}
	if lines := processAdvisory(h, adv, st); calls.update != 1 || len(lines) != 0 {
		t.Errorf("second run: calls=%+v lines=%q", calls, lines)
	}

	calls = hostCalls{}
	h.JabaliEnabled = false
	lines = processAdvisory(h, adv, &advisoryState{})
	if calls.update != 0 || !strings.Contains(joined(lines), "Update now: jabali update") {
		t.Errorf("self-update off: calls=%+v lines=%q", calls, lines)
	}
}

// On the development channel `jabali update` moves to main, which has the
// fix: update, even when the build is past the stable tag.
func TestProcessAdvisory_DevelopmentChannelUpdates(t *testing.T) {
	git, c := feedRepo(t)
	checkoutAt(t, git, c["m1"])
	var calls hostCalls
	h := testHost(git, &calls)
	h.OnStable = false
	processAdvisory(h, jabaliAdvisory(c["m2"], c["m2"]), &advisoryState{})
	if calls.update != 1 || calls.converger != 0 {
		t.Errorf("development channel: calls=%+v", calls)
	}
}

func TestProcessAdvisory_AheadOfStable(t *testing.T) {
	git, c := feedRepo(t)
	checkoutAt(t, git, c["m1"]) // stable (m0) is behind HEAD

	t.Run("signed feed runs the converger once", func(t *testing.T) {
		var calls hostCalls
		h := testHost(git, &calls)
		st := &advisoryState{}
		adv := jabaliAdvisory(c["m2"], c["m2"])
		lines := processAdvisory(h, adv, st)
		if calls.converger != 1 || calls.update != 0 || !strings.Contains(joined(lines), "being applied now") {
			t.Fatalf("calls=%+v lines=%q", calls, lines)
		}
		if lines := processAdvisory(h, adv, st); calls.converger != 1 || len(lines) != 0 {
			t.Errorf("second run: calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("unsigned feed gives the command instead", func(t *testing.T) {
		var calls hostCalls
		h := testHost(git, &calls)
		h.Signed = false
		lines := processAdvisory(h, jabaliAdvisory(c["m2"], c["m2"]), &advisoryState{})
		if calls.converger != 0 || !strings.Contains(joined(lines), "only when the feed is signed") ||
			!strings.Contains(joined(lines), "archive "+c["m2"]) || !strings.Contains(joined(lines), "ensure_security_updates") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("self-update off gives the command instead", func(t *testing.T) {
		var calls hostCalls
		h := testHost(git, &calls)
		h.JabaliEnabled = false
		lines := processAdvisory(h, jabaliAdvisory(c["m2"], c["m2"]), &advisoryState{})
		if calls.converger != 0 || !strings.Contains(joined(lines), "self-update is off") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("converger from an older commit is refused", func(t *testing.T) {
		var calls hostCalls
		lines := processAdvisory(testHost(git, &calls), jabaliAdvisory(c["m2"], c["m0"]), &advisoryState{})
		if calls.converger != 0 || !strings.Contains(joined(lines), "is not built on this server's build") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("converger from a commit not on main is refused", func(t *testing.T) {
		var calls hostCalls
		lines := processAdvisory(testHost(git, &calls), jabaliAdvisory(c["m2"], c["side"]), &advisoryState{})
		if calls.converger != 0 || !strings.Contains(joined(lines), "is not on main") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("no converger: wait for the next stable release", func(t *testing.T) {
		var calls hostCalls
		lines := processAdvisory(testHost(git, &calls), jabaliAdvisory(c["m2"], ""), &advisoryState{})
		if calls != (hostCalls{}) || !strings.Contains(joined(lines), "next stable release") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
}

// A hotfix stable that main has not merged back is neither behind nor ahead
// of a main build. `jabali update` moves the server to it, unless the stable
// release could not serve the database (updateResetTarget); then the update
// changes nothing and only the converger delivers the fix.
func TestProcessAdvisory_DivergedStable(t *testing.T) {
	git, c := feedRepo(t)
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	checkoutAt(t, git, c["m1"])
	mig := filepath.Join(strings.TrimSpace(top), "panel-api", "internal", "db", "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mig, "000001_a.up.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "panel-api"}, {"commit", "-q", "-m", "hotfix"}, {"tag", "-f", "stable"}} {
		if out, err := git(args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	checkoutAt(t, git, c["m2"])

	t.Run("update moves the server to the stable release", func(t *testing.T) {
		var calls hostCalls
		h := testHost(git, &calls)
		h.LiveSchema = 1
		lines := processAdvisory(h, jabaliAdvisory(c["m3"], c["m3"]), &advisoryState{})
		if calls.update != 1 || calls.converger != 0 || !strings.Contains(joined(lines), "update has started") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
	t.Run("update refused: the converger applies the fix", func(t *testing.T) {
		var calls hostCalls
		h := testHost(git, &calls)
		h.LiveSchema = 2
		lines := processAdvisory(h, jabaliAdvisory(c["m3"], c["m3"]), &advisoryState{})
		if calls.update != 0 || calls.converger != 1 ||
			!strings.Contains(joined(lines), "cannot move to the stable release") || !strings.Contains(joined(lines), "being applied now") {
			t.Errorf("calls=%+v lines=%q", calls, lines)
		}
	})
}

// Root runs the converger's script from the state directory, so a directory
// another user could change is refused before anything is written to it.
func TestSecureSecurityFeedDir(t *testing.T) {
	t.Parallel()
	base := privateTempDir(t)

	fresh := filepath.Join(base, "fresh", "sf")
	if err := secureSecurityFeedDir(fresh, base); err != nil {
		t.Fatalf("fresh dir: %v", err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("fresh dir mode: %v %v", fi, err)
	}

	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secureSecurityFeedDir(loose, base); err == nil {
		t.Error("accepted a dir others can read")
	}

	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := secureSecurityFeedDir(link, base); err == nil {
		t.Error("accepted a symlink")
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, trusted := range []string{base, ""} {
		if err := secureSecurityFeedDir(filepath.Join(open, "sf"), trusted); err == nil {
			t.Errorf("accepted a dir under a parent anyone can write (trusted %q)", trusted)
		}
	}
	if err := os.Chmod(open, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := secureSecurityFeedDir(filepath.Join(open, "sf"), base); err != nil {
		t.Errorf("refused a dir under a sticky parent: %v", err)
	}

	// run() checks before it locks, loads or acts.
	var hostRuns int
	r := securityFeedRun{
		FeedURL:   "https://example.invalid/advisories.json",
		StatePath: filepath.Join(link, "state.json"),
		Fetch: func(context.Context, string) ([]byte, error) {
			return []byte(`{"schema":1,"advisories":[{"id":"JSA-2026-001","published":"2026-10-21","severity":"high","title":"t","apt":true}]}`), nil
		},
		Host: func(context.Context, bool) (securityFeedHost, error) {
			hostRuns++
			return securityFeedHost{}, nil
		},
		Publish:    func(context.Context, notifications.Envelope) error { return nil },
		Log:        func(string, ...any) {},
		TrustedDir: base,
	}
	if err := r.run(context.Background()); err == nil || hostRuns != 0 {
		t.Errorf("run with a symlinked state dir: err=%v hostRuns=%d", err, hostRuns)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("wrote through the symlink: %v", entries)
	}
}

// A fresh install must not replay old advisories.
func TestProcessAdvisory_OldAdvisoryIgnored(t *testing.T) {
	t.Parallel()
	var calls hostCalls
	st := &advisoryState{}
	adv := securityAdvisory{ID: "JSA-2026-003", Published: "2026-08-01", Severity: "high", Title: "Old", Apt: true}
	if lines := processAdvisory(testHost(nil, &calls), adv, st); len(lines) != 0 || calls.apt != 0 || !st.Ignored {
		t.Errorf("old advisory: lines=%q calls=%+v ignored=%v", lines, calls, st.Ignored)
	}
}

func TestSecurityFeedRun(t *testing.T) {
	feed, _ := json.Marshal(securityFeed{Schema: 1, Advisories: []securityAdvisory{
		{ID: "JSA-2026-004", Published: "2026-10-22", Severity: "critical", Title: "PHP security release", Apt: true},
	}})
	type result struct {
		err      error
		signed   bool
		hostRuns int
		alerts   []notifications.Envelope
		calls    hostCalls
	}
	root := privateTempDir(t)
	const url = "https://feed.test/advisories.json"
	runOnce := func(t *testing.T, statePath, pubKey string, files map[string][]byte, publishErr error) result {
		t.Helper()
		var res result
		r := securityFeedRun{
			FeedURL: url, StatePath: statePath, PubKey: pubKey,
			TrustedDir: root,
			Fetch: func(_ context.Context, u string) ([]byte, error) {
				if b, ok := files[u]; ok {
					return b, nil
				}
				return nil, errors.New("HTTP 404")
			},
			Host: func(_ context.Context, signed bool) (securityFeedHost, error) {
				res.hostRuns++
				res.signed = signed
				return testHost(nil, &res.calls), nil
			},
			Publish: func(_ context.Context, env notifications.Envelope) error {
				if publishErr != nil {
					return publishErr
				}
				res.alerts = append(res.alerts, env)
				return nil
			},
			Log: func(string, ...any) {},
		}
		res.err = r.run(context.Background())
		return res
	}

	t.Run("unsigned build: acts once, alerts once, state is root-only", func(t *testing.T) {
		state := filepath.Join(root, "s1", "sf", "state.json")
		res := runOnce(t, state, "", map[string][]byte{url: feed}, nil)
		if res.err != nil || res.signed || res.calls.apt != 1 || len(res.alerts) != 1 {
			t.Fatalf("first run: %+v", res)
		}
		if a := res.alerts[0]; a.EventKind != "security.advisory" || a.Severity != "critical" || !strings.Contains(a.Title, "JSA-2026-004") {
			t.Errorf("alert: %+v", a)
		}
		if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("state file: %v %v", fi, err)
		}
		if res := runOnce(t, state, "", map[string][]byte{url: feed}, nil); res.err != nil || res.calls.apt != 0 || len(res.alerts) != 0 {
			t.Errorf("second run: %+v", res)
		}
	})
	t.Run("an alert that could not be queued is sent next run", func(t *testing.T) {
		state := filepath.Join(root, "s2", "state.json")
		if res := runOnce(t, state, "", map[string][]byte{url: feed}, errors.New("redis down")); res.err != nil || res.calls.apt != 1 {
			t.Fatalf("first run: %+v", res)
		}
		res := runOnce(t, state, "", map[string][]byte{url: feed}, nil)
		if res.calls.apt != 0 || len(res.alerts) != 1 || !strings.Contains(res.alerts[0].Body, "being installed now") {
			t.Errorf("second run: %+v", res)
		}
	})

	pub, sig := signFor(t, feed)
	t.Run("signed build: a valid signature makes the feed signed", func(t *testing.T) {
		res := runOnce(t, filepath.Join(root, "s3", "state.json"), pub, map[string][]byte{url: feed, url + ".minisig": []byte(sig)}, nil)
		if res.err != nil || !res.signed || res.calls.apt != 1 {
			t.Errorf("%+v", res)
		}
	})
	t.Run("signed build: a missing signature changes nothing", func(t *testing.T) {
		state := filepath.Join(root, "s4", "state.json")
		res := runOnce(t, state, pub, map[string][]byte{url: feed}, nil)
		if res.err == nil || res.hostRuns != 0 || res.calls != (hostCalls{}) {
			t.Errorf("%+v", res)
		}
		// The journal says why: the signature could not be fetched.
		if res.err != nil && !strings.Contains(res.err.Error(), "signature could not be fetched") {
			t.Errorf("error does not name the missing signature: %v", res.err)
		}
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("state written for a feed that failed verification: %v", err)
		}
	})
	t.Run("signed build: a signature over other bytes changes nothing", func(t *testing.T) {
		tampered := append(append([]byte{}, feed...), ' ')
		res := runOnce(t, filepath.Join(root, "s5", "state.json"), pub, map[string][]byte{url: tampered, url + ".minisig": []byte(sig)}, nil)
		if res.err == nil || res.hostRuns != 0 || res.calls != (hostCalls{}) {
			t.Errorf("%+v", res)
		}
	})
	t.Run("an invalid feed changes nothing", func(t *testing.T) {
		res := runOnce(t, filepath.Join(root, "s6", "state.json"), "", map[string][]byte{url: []byte(`{"schema":1,"advisories":[{"id":"bad"}]}`)}, nil)
		if res.err == nil || res.hostRuns != 0 {
			t.Errorf("%+v", res)
		}
	})
}

// privateTempDir is a temp dir no other user can change, for the state
// directory check (secureSecurityFeedDir) to trust.
func privateTempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}
