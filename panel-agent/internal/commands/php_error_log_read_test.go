package commands

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// phpErrLogFixture points userLogsHomeRoot at a temp dir with <user>/logs and
// returns the logs dir, the current user (the files the test writes are owned
// by it, as a tenant's are by the tenant) and its uid.
func phpErrLogFixture(t *testing.T) (logs, name string, uid uint32) {
	t.Helper()
	u, err := user.Current()
	if err != nil || !phpPoolUsernameRegex.MatchString(u.Username) {
		t.Skip("current user does not fit the pool username shape")
	}
	id, _ := strconv.ParseUint(u.Uid, 10, 32)
	home := t.TempDir()
	prev := userLogsHomeRoot
	userLogsHomeRoot = home
	t.Cleanup(func() { userLogsHomeRoot = prev })
	logs = filepath.Join(home, u.Username, "logs")
	if err := os.MkdirAll(logs, 0o750); err != nil {
		t.Fatal(err)
	}
	return logs, u.Username, uint32(id)
}

func TestReadPHPErrorLog_TailAndFollow(t *testing.T) {
	logs, name, uid := phpErrLogFixture(t)
	path := filepath.Join(logs, "example.com.php-error.log")

	got, err := readPHPErrorLog(name, "example.com", uid, -1, 0, 50)
	if err != nil || !got.Missing || len(got.Lines) != 0 {
		t.Fatalf("no file yet: want missing, got %+v %v", got, err)
	}

	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = readPHPErrorLog(name, "example.com", uid, -1, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 50 || got.Lines[0] != "line 31" || got.Lines[49] != "line 80" {
		t.Fatalf("tail: want lines 31..80, got %d lines %q", len(got.Lines), got.Lines)
	}
	if got.Offset != int64(b.Len()) || got.Ino == 0 {
		t.Fatalf("tail: offset %d ino %d", got.Offset, got.Ino)
	}

	// Follow: only what was appended, and a half-written line waits.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("line 81\nline 8")
	f.Close()
	next, err := readPHPErrorLog(name, "example.com", uid, got.Offset, got.Ino, 50)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(next.Lines, "|") != "line 81" {
		t.Fatalf("follow: want [line 81], got %q", next.Lines)
	}
	again, _ := readPHPErrorLog(name, "example.com", uid, next.Offset, next.Ino, 50)
	if len(again.Lines) != 0 || again.Offset != next.Offset {
		t.Fatalf("a partial line must wait: %+v", again)
	}

	// Rotation: a new file (new inode) starts over from its own tail, even
	// when it already grew past the old offset (only the inode tells).
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	var fresh strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&fresh, "fresh %d\n", i)
	}
	if int64(fresh.Len()) <= again.Offset {
		t.Fatalf("fixture: the new file must outgrow the old offset %d", again.Offset)
	}
	if err := os.WriteFile(path, []byte(fresh.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	rot, err := readPHPErrorLog(name, "example.com", uid, again.Offset, again.Ino, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rot.Lines) != 50 || rot.Lines[0] != "fresh 51" || rot.Lines[49] != "fresh 100" || rot.Ino == again.Ino {
		t.Fatalf("after rotation: want the new file's last 50 lines, got %d lines %q", len(rot.Lines), rot.Lines)
	}

	// Truncated in place (offset past the end): start over too.
	if err := os.WriteFile(path, []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, _ := readPHPErrorLog(name, "example.com", uid, rot.Offset, rot.Ino, 50)
	if strings.Join(tr.Lines, "|") != "t" {
		t.Fatalf("after truncation: got %+v", tr)
	}
}

func TestReadPHPErrorLog_TailOfLargeFileDropsPartialLine(t *testing.T) {
	logs, name, uid := phpErrLogFixture(t)
	line := strings.Repeat("x", 99) + "\n"
	body := strings.Repeat(line, (phpErrorLogReadMax/len(line))+50)
	if err := os.WriteFile(filepath.Join(logs, "big.example.php-error.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readPHPErrorLog(name, "big.example", uid, -1, 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range got.Lines {
		if len(l) != 99 {
			t.Fatalf("a line cut by the read window leaked through: %d bytes", len(l))
		}
	}
}

func TestReadPHPErrorLog_RefusesWhatTheTenantCanPlant(t *testing.T) {
	logs, name, uid := phpErrLogFixture(t)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("root only\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(secret, filepath.Join(logs, "link.example.php-error.log")); err != nil {
		t.Fatal(err)
	}
	if got, err := readPHPErrorLog(name, "link.example", uid, -1, 0, 50); err == nil {
		t.Errorf("a symlinked log was followed: %+v", got)
	}

	if err := syscall.Mkfifo(filepath.Join(logs, "fifo.example.php-error.log"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readPHPErrorLog(name, "fifo.example", uid, -1, 0, 50)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO must be refused")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO under the log's name hung the read")
	}

	if err := os.WriteFile(filepath.Join(logs, "other.example.php-error.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPHPErrorLog(name, "other.example", uid+1, -1, 0, 50); err == nil {
		t.Error("a log not owned by the account must be refused")
	}

	// A symlinked logs dir is refused as well.
	home := filepath.Dir(logs)
	if err := os.Rename(logs, logs+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(logs+".real", filepath.Join(home, "logs")); err != nil {
		t.Fatal(err)
	}
	if got, err := readPHPErrorLog(name, "other.example", uid, -1, 0, 50); err == nil {
		t.Errorf("a symlinked logs dir was followed: %+v", got)
	}
}

func TestPHPErrorLogReadHandler_Validates(t *testing.T) {
	for _, params := range []string{
		`{"username":"../x","domain":"example.com","offset":-1}`,
		`{"username":"alice","domain":"../etc","offset":-1}`,
		`{"username":"alice","domain":"a..b","offset":-1}`,
		`not json`,
	} {
		if _, err := phpErrorLogReadHandler(context.Background(), []byte(params)); err == nil {
			t.Errorf("params %s: want an error", params)
		}
	}
}

func TestSplitLogLines(t *testing.T) {
	if got := splitLogLines([]byte("")); len(got) != 0 {
		t.Errorf("empty: %q", got)
	}
	if got := splitLogLines([]byte("a\r\nb\n")); strings.Join(got, "|") != "a|b" {
		t.Errorf("crlf: %q", got)
	}
	long := strings.Repeat("y", phpErrorLogLineMax+10)
	if got := splitLogLines([]byte(long + "\n")); len(got[0]) > phpErrorLogLineMax+8 {
		t.Errorf("an overlong line was not cut: %d", len(got[0]))
	}
}
