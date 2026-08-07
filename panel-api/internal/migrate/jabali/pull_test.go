package jabali

import (
	"context"
	"strings"
	"testing"
	"time"
)

// recordingSession captures every command run and answers from a substring
// router. A route value may be a func(callNo int) string so a command's answer
// can change across calls (e.g. snapshots empty, then carrying a manifest).
type recordingSession struct {
	*session
	cmds  []string
	calls map[string]int
}

func newRecordingSession(routes map[string]any) *recordingSession {
	rs := &recordingSession{
		session: &session{commandTimeout: time.Second},
		calls:   map[string]int{},
	}
	rs.session.run = func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		rs.cmds = append(rs.cmds, cmd)
		for sub, out := range routes {
			if !strings.Contains(cmd, sub) {
				continue
			}
			rs.calls[sub]++
			switch v := out.(type) {
			case string:
				return []byte(v), nil
			case func(int) string:
				return []byte(v(rs.calls[sub])), nil
			}
		}
		return []byte("{}"), nil
	}
	return rs
}

func (rs *recordingSession) ran(sub string) bool {
	for _, c := range rs.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// orderOf returns the index of the first command containing sub, or -1.
func (rs *recordingSession) orderOf(sub string) int {
	for i, c := range rs.cmds {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func TestRunSourceBackup_Sequence(t *testing.T) {
	old := manifestPollInterval
	manifestPollInterval = time.Millisecond
	defer func() { manifestPollInterval = old }()

	rs := newRecordingSession(map[string]any{
		"destination create": `{"id":"01DEST"}`,
		"schedule create":    `{"id":"01SCHED"}`,
		"scheduler tick":     ``, // routed so rs.calls counts it
		// snapshots: empty on the first poll, manifest present on the second —
		// exercises the tick+poll loop rather than a lucky first hit.
		"snapshots --json": func(n int) string {
			if n < 2 {
				return `[]`
			}
			return `[{"tags":["kind=account_backup","stage=manifest","user-id=01SRC"]}]`
		},
	})

	sb, err := RunSourceBackup(context.Background(), rs.session, "alice", "01JOBULIDXXXXXXXXXXXXXXXXX")
	if err != nil {
		t.Fatalf("RunSourceBackup: %v", err)
	}
	if sb.DestName != "migrate-01jobuli" {
		t.Errorf("dest name = %q, want migrate-01jobuli", sb.DestName)
	}
	if sb.ScheduleID != "01SCHED" || sb.DestID != "01DEST" {
		t.Errorf("ids not parsed: %+v", sb)
	}
	if !strings.HasPrefix(sb.RepoDir, "/var/lib/jabali-migrate-src/") {
		t.Errorf("repo dir = %q", sb.RepoDir)
	}

	// The account_backup schedule must be restricted to the one user + created
	// dir before the destination (local dest requires an existing dir).
	if !rs.ran("install -d") {
		t.Error("must create the repo dir before the destination")
	}
	if rs.orderOf("install -d") > rs.orderOf("destination create") {
		t.Error("repo dir must be created before `destination create`")
	}
	if !strings.Contains(rs.cmds[rs.orderOf("schedule create")], "--kind account_backup --user 'alice'") {
		t.Errorf("schedule must be account_backup scoped to alice, got %q", rs.cmds[rs.orderOf("schedule create")])
	}
	if !rs.ran("run-now '01SCHED'") {
		t.Error("must run-now the created schedule by id")
	}
	if rs.calls["scheduler tick"] < 2 {
		t.Errorf("must tick until the manifest lands (>=2), ticked %d", rs.calls["scheduler tick"])
	}
}

func TestRunSourceBackup_RejectsBadUser(t *testing.T) {
	rs := newRecordingSession(map[string]any{})
	if _, err := RunSourceBackup(context.Background(), rs.session, "bad name!", "01JOB"); err == nil {
		t.Error("an invalid source username must be rejected before any source write")
	}
	if len(rs.cmds) != 0 {
		t.Errorf("no source command may run for a bad username, ran %v", rs.cmds)
	}
}

func TestRunSourceBackup_ScheduleIDParseFail(t *testing.T) {
	rs := newRecordingSession(map[string]any{
		"destination create": `{"id":"01DEST"}`,
		"schedule create":    `{}`, // no id
	})
	_, err := RunSourceBackup(context.Background(), rs.session, "alice", "01JOB")
	if err == nil || !strings.Contains(err.Error(), "schedule id") {
		t.Errorf("a schedule create without an id must fail loudly, got %v", err)
	}
}

func TestWaitForManifest_TimesOut(t *testing.T) {
	oldI, oldW := manifestPollInterval, manifestMaxWait
	manifestPollInterval, manifestMaxWait = time.Millisecond, 5*time.Millisecond
	defer func() { manifestPollInterval, manifestMaxWait = oldI, oldW }()

	rs := newRecordingSession(map[string]any{
		"snapshots --json": `[]`, // manifest never appears
	})
	err := rs.session.waitForManifest(context.Background(), "/var/lib/jabali-migrate-src/x")
	if err == nil || !strings.Contains(err.Error(), "no manifest snapshot") {
		t.Errorf("must time out when the manifest never lands, got %v", err)
	}
}

func TestReadBoxPassword(t *testing.T) {
	rs := newRecordingSession(map[string]any{
		"cat '/etc/jabali-panel/restic-repo.password'": "s3cr3t-pw\n",
	})
	pw, err := ReadBoxPassword(context.Background(), rs.session)
	if err != nil {
		t.Fatalf("ReadBoxPassword: %v", err)
	}
	if pw != "s3cr3t-pw" {
		t.Errorf("password not trimmed: %q", pw)
	}

	empty := newRecordingSession(map[string]any{
		"cat '/etc/jabali-panel/restic-repo.password'": "\n",
	})
	if _, err := ReadBoxPassword(context.Background(), empty.session); err == nil {
		t.Error("an empty password file must error")
	}
}

func TestCleanupSource_Order(t *testing.T) {
	rs := newRecordingSession(map[string]any{})
	sb := &SourceBackup{DestName: "migrate-01jobuli", ScheduleID: "01SCHED", RepoDir: "/var/lib/jabali-migrate-src/01jobuli"}
	if errs := CleanupSource(context.Background(), rs.session, sb); len(errs) != 0 {
		t.Fatalf("cleanup errors: %v", errs)
	}
	sched := rs.orderOf("schedule delete '01SCHED' --force")
	dest := rs.orderOf("destination delete 'migrate-01jobuli' --force")
	rm := rs.orderOf("rm -rf -- '/var/lib/jabali-migrate-src/01jobuli'")
	if sched < 0 || dest < 0 || rm < 0 {
		t.Fatalf("missing cleanup command(s): sched=%d dest=%d rm=%d (%v)", sched, dest, rm, rs.cmds)
	}
	// Schedule FIRST (a live daily schedule pointed at a deleted repo dir would
	// fail forever on the source), then destination, then the repo dir.
	if !(sched < dest && dest < rm) {
		t.Errorf("cleanup order must be schedule → destination → repo: sched=%d dest=%d rm=%d", sched, dest, rm)
	}
}
