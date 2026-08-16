package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// exit3Err returns a genuine *exec.ExitError with code 3 (what restic
// returns when at least one source file could not be read).
func exit3Err(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 3").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("could not synthesize an exit-3 error: %v", err)
	}
	return err
}

// GH #454: restic exit 3 (unreadable file) still produces a valid snapshot
// + summary. Backup must return that summary as success-with-warnings, not
// discard the whole stage.
func TestBackup_Exit3IsPartialReadNotFailure(t *testing.T) {
	stdout := `{"message_type":"error","error":{"Op":"open","Path":"/home/u/secret","Err":13},"item":"/home/u/secret"}` + "\n" +
		`{"message_type":"summary","data_added":2002159,"total_bytes_processed":2000009,"snapshot_id":"abc123"}` + "\n"
	r := &fakeRunner{stdout: []byte(stdout), err: exit3Err(t)}
	c := newClient(t, r)

	s, err := c.Backup(context.Background(), BackupOpts{Paths: []string{"/home/u"}})
	if err != nil {
		t.Fatalf("exit 3 should not be a hard error, got %v", err)
	}
	if s.TotalBytesProcessed != 2000009 || s.SnapshotID != "abc123" {
		t.Errorf("summary not parsed from exit-3 output: %+v", s)
	}
	if len(s.Warnings) == 0 {
		t.Error("expected read warnings on a partial-read backup")
	}
}

// Exit 3 with no summary means no snapshot was created — a real failure.
func TestBackup_Exit3NoSummaryFails(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`{"message_type":"status"}` + "\n"), err: exit3Err(t)}
	c := newClient(t, r)
	if _, err := c.Backup(context.Background(), BackupOpts{Paths: []string{"/x"}}); err == nil {
		t.Error("exit 3 with no summary must fail")
	}
}

// Any other non-zero exit (e.g. fatal error) stays a failure.
func TestBackup_NonExit3StaysFailure(t *testing.T) {
	summary := `{"message_type":"summary","total_bytes_processed":10,"snapshot_id":"x"}` + "\n"
	r := &fakeRunner{stdout: []byte(summary), err: errors.New("restic died")}
	c := newClient(t, r)
	if _, err := c.Backup(context.Background(), BackupOpts{Paths: []string{"/x"}}); err == nil {
		t.Error("a non-exit-3 runner error must propagate even if stdout has a summary")
	}
}

// fakeRunner records every Run call and returns canned responses.
type fakeRunner struct {
	calls  []fakeCall
	stdout []byte
	stderr []byte
	err    error
}

type fakeCall struct {
	bin   string
	args  []string
	env   []string
	stdin string
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, env []string, stdin io.Reader) ([]byte, []byte, error) {
	c := fakeCall{bin: name, args: append([]string(nil), args...), env: env}
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		c.stdin = string(b)
	}
	f.calls = append(f.calls, c)
	return f.stdout, f.stderr, f.err
}

func newClient(t *testing.T, runner *fakeRunner) *Client {
	t.Helper()
	return New(ResticConfig{
		Repo:         "/var/lib/jabali-backups/repo",
		PasswordFile: "/etc/jabali-panel/restic-repo.password",
		Bin:          "restic",
		Runner:       runner,
	})
}

func TestSnapshots_EmptyRepo(t *testing.T) {
	r := &fakeRunner{stdout: []byte("null\n")}
	c := newClient(t, r)
	snaps, err := c.Snapshots(context.Background(), nil)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("want empty, got %d", len(snaps))
	}
	if got := r.calls[0].args; got[0] != "--repo" || got[2] != "--password-file" ||
		got[4] != "--retry-lock" || got[6] != "snapshots" {
		t.Fatalf("unexpected args: %v", got)
	}
}

// GH #1045: every invocation must carry --retry-lock so a delete that
// races a running backup waits for the repo lock instead of dying with
// restic exit 11 ("repository is already locked exclusively").
func TestBaseArgs_RetryLock(t *testing.T) {
	r := &fakeRunner{stdout: []byte("null\n")}
	c := newClient(t, r)
	if _, err := c.Snapshots(context.Background(), nil); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	joined := strings.Join(r.calls[0].args, " ")
	if !strings.Contains(joined, "--retry-lock 2m") {
		t.Fatalf("missing --retry-lock: %v", joined)
	}
}

func TestSnapshots_TagFilter(t *testing.T) {
	r := &fakeRunner{stdout: []byte("[]")}
	c := newClient(t, r)
	_, err := c.Snapshots(context.Background(), []Tag{
		MakeTag(TagKeyJobID, "01J5JOB"),
		MakeTag(TagKeyStage, StageHome),
	})
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	args := strings.Join(r.calls[0].args, " ")
	if !strings.Contains(args, "--tag job-id=01J5JOB") {
		t.Fatalf("missing job-id tag in: %s", args)
	}
	if !strings.Contains(args, "--tag stage=home") {
		t.Fatalf("missing stage tag in: %s", args)
	}
}

func TestBackup_StdinFiltersStdinFilename(t *testing.T) {
	summary := []byte(`{"message_type":"status"}` + "\n" +
		`{"message_type":"summary","snapshot_id":"abc123","data_added":42,"total_bytes_processed":100}` + "\n")
	r := &fakeRunner{stdout: summary}
	c := newClient(t, r)
	s, err := c.Backup(context.Background(), BackupOpts{
		Stdin:     strings.NewReader("hello"),
		StdinName: "manifest.json",
		Tags:      []Tag{TagBlanket, MakeTag(TagKeyStage, StageManifest)},
	})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if s.SnapshotID != "abc123" || s.DataAdded != 42 {
		t.Fatalf("summary parse: %+v", s)
	}
	args := strings.Join(r.calls[0].args, " ")
	if !strings.Contains(args, "--stdin --stdin-filename manifest.json") {
		t.Fatalf("expected --stdin --stdin-filename manifest.json, got %s", args)
	}
	if r.calls[0].stdin != "hello" {
		t.Fatalf("stdin not forwarded; got %q", r.calls[0].stdin)
	}
}

func TestBackup_NoSummaryEmitsError(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`{"message_type":"status"}` + "\n")}
	c := newClient(t, r)
	_, err := c.Backup(context.Background(), BackupOpts{Paths: []string{"/tmp"}})
	if err == nil || !strings.Contains(err.Error(), "no summary line") {
		t.Fatalf("want missing summary, got %v", err)
	}
}

func TestBackup_RequiresPathsOrStdin(t *testing.T) {
	r := &fakeRunner{}
	c := newClient(t, r)
	_, err := c.Backup(context.Background(), BackupOpts{})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestForget_BuildsPolicyFlags(t *testing.T) {
	r := &fakeRunner{stdout: []byte("ok")}
	c := newClient(t, r)
	if _, err := c.Forget(context.Background(), ForgetOpts{
		KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6,
		Tags:  []Tag{TagBlanket},
		Prune: true,
	}); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	args := strings.Join(r.calls[0].args, " ")
	want := "--keep-daily 7 --keep-weekly 4 --keep-monthly 6 --tag jabali --prune"
	if !strings.Contains(args, want) {
		t.Fatalf("want %s\n got %s", want, args)
	}
}

func TestRestoreRequiresFields(t *testing.T) {
	c := newClient(t, &fakeRunner{})
	for _, tc := range []RestoreOpts{
		{},
		{SnapshotID: "abc"},
		{Target: "/tmp"},
	} {
		if err := c.Restore(context.Background(), tc); err == nil {
			t.Fatalf("want error for %+v", tc)
		}
	}
}

func TestSnapshots_PassesThroughErr(t *testing.T) {
	wantErr := errors.New("exec failed")
	r := &fakeRunner{err: wantErr, stderr: []byte("something")}
	c := newClient(t, r)
	if _, err := c.Snapshots(context.Background(), nil); err == nil {
		t.Fatal("want error")
	}
}

func TestSnapshots_Roundtrip(t *testing.T) {
	one := Snapshot{
		ID:       "abc12345",
		ShortID:  "abc12345",
		Time:     time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		Hostname: "host",
		Tags:     []string{"jabali", "kind=account_backup", "stage=home"},
		Paths:    []string{"/home/u"},
	}
	body, _ := json.Marshal([]Snapshot{one})
	c := newClient(t, &fakeRunner{stdout: body})
	got, err := c.Snapshots(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "abc12345" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestForgetIDs_BuildsArgs(t *testing.T) {
	r := &fakeRunner{stdout: []byte("ok")}
	c := newClient(t, r)
	if _, err := c.ForgetIDs(context.Background(), []string{"aaa", "bbb"}, true); err != nil {
		t.Fatalf("ForgetIDs: %v", err)
	}
	args := strings.Join(r.calls[0].args, " ")
	if !strings.Contains(args, "forget aaa bbb") || !strings.Contains(args, "--prune") {
		t.Fatalf("args = %q", args)
	}
}

func TestForgetIDs_EmptyIsNoop(t *testing.T) {
	r := &fakeRunner{stdout: []byte("ok")}
	c := newClient(t, r)
	if _, err := c.ForgetIDs(context.Background(), nil, true); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("empty ids must not invoke restic, got %d calls", len(r.calls))
	}
}

// TestBackup_ExtraExcludeFiles guards GH #454: a per-account exclude file
// (~/.backupignore) is layered onto the global one as a second --exclude-file,
// and empty entries are dropped.
func TestBackup_ExtraExcludeFiles(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`{"message_type":"summary","snapshot_id":"s1","total_bytes_processed":1,"data_added":1}` + "\n")}
	c := newClient(t, r)
	_, _ = c.Backup(context.Background(), BackupOpts{
		Paths:             []string{"/home/u"},
		ExcludeFile:       "/etc/jabali-panel/restic-excludes.list",
		ExtraExcludeFiles: []string{"/home/u/.backupignore", ""},
	})
	if len(r.calls) == 0 {
		t.Fatal("restic was not invoked")
	}
	args := strings.Join(r.calls[0].args, " ")
	if !strings.Contains(args, "--exclude-file /etc/jabali-panel/restic-excludes.list") {
		t.Errorf("global exclude-file missing:\n%s", args)
	}
	if !strings.Contains(args, "--exclude-file /home/u/.backupignore") {
		t.Errorf("per-account exclude-file missing (GH #454):\n%s", args)
	}
	if strings.HasSuffix(strings.TrimSpace(args), "--exclude-file") {
		t.Errorf("empty ExtraExcludeFiles entry leaked a bare flag:\n%s", args)
	}
}
