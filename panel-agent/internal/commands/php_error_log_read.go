package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"golang.org/x/sys/unix"
)

// php.error_log.read — the panel's Logs page reads a domain's PHP error log
// (php_error_log.go) through this command. The file sits in the tenant's
// ~/logs (0750, tenant-owned), which panel-api cannot read, and the tenant
// controls every name in that dir. So the agent reads it, as root, without
// following anything the tenant could plant:
//
//   - each component is opened relative to the last with O_NOFOLLOW, so a
//     symlinked logs dir or log file is refused, not followed;
//   - O_NONBLOCK, so a FIFO planted under the log's name cannot hang the read;
//   - the file must be a regular file owned by the tenant, so a hard link to
//     a file the tenant does not own shows nothing.
//
// One call returns either the last lines (offset < 0, the first read of a
// view) or what was appended since offset, which is how the live view polls.
// A changed inode or a file shorter than offset means it was rotated: the read
// starts over from the tail.

const (
	// phpErrorLogReadMax bounds one read, and so one response.
	phpErrorLogReadMax = 256 << 10
	// phpErrorLogLineMax cuts a single overlong line.
	phpErrorLogLineMax = 8 << 10
)

type phpErrorLogReadParams struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	// Offset < 0 reads the last Lines lines; >= 0 reads what follows it.
	Offset int64 `json:"offset"`
	// Ino is the inode of the previous read, 0 for none.
	Ino   uint64 `json:"ino,omitempty"`
	Lines int    `json:"lines,omitempty"`
}

type phpErrorLogReadResponse struct {
	Lines []string `json:"lines"`
	// Offset is where the next read continues.
	Offset int64  `json:"offset"`
	Ino    uint64 `json:"ino"`
	// Missing: no log yet (the domain logged nothing since the last rotation,
	// or ~/logs does not exist).
	Missing bool `json:"missing,omitempty"`
}

func phpErrorLogReadHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p phpErrorLogReadParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if phpErrorLogPath(p.Username, p.Domain) == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username or domain"}
	}
	lines := p.Lines
	if lines <= 0 {
		lines = 50
	}
	if lines > 2000 {
		lines = 2000
	}
	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "unknown user"}
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "bad uid"}
	}
	resp, err := readPHPErrorLog(p.Username, p.Domain, uint32(uid), p.Offset, p.Ino, lines)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return resp, nil
}

// openPHPErrorLog opens /home/<user>/logs/<domain>.php-error.log one component
// at a time without following links. A missing dir or file returns
// os.ErrNotExist.
func openPHPErrorLog(username, domain string) (*os.File, error) {
	const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	home, err := unix.Open(filepath.Join(userLogsHomeRoot, username), dirFlags, 0)
	if err != nil {
		return nil, openErr("home", err)
	}
	defer unix.Close(home)
	logs, err := unix.Openat(home, "logs", dirFlags, 0)
	if err != nil {
		return nil, openErr("logs dir", err)
	}
	defer unix.Close(logs)
	fd, err := unix.Openat(logs, domain+".php-error.log", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openErr("log", err)
	}
	return os.NewFile(uintptr(fd), domain+".php-error.log"), nil
}

func openErr(what string, err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return os.ErrNotExist
	case errors.Is(err, unix.ELOOP):
		return fmt.Errorf("the %s is a symbolic link", what)
	case errors.Is(err, unix.ENOTDIR):
		return fmt.Errorf("the %s is not a directory", what)
	default:
		return fmt.Errorf("open %s: %w", what, err)
	}
}

func readPHPErrorLog(username, domain string, uid uint32, offset int64, ino uint64, lines int) (phpErrorLogReadResponse, error) {
	resp := phpErrorLogReadResponse{Lines: []string{}}
	f, err := openPHPErrorLog(username, domain)
	if errors.Is(err, os.ErrNotExist) {
		resp.Missing = true
		return resp, nil
	}
	if err != nil {
		return resp, err
	}
	defer f.Close()

	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return resp, fmt.Errorf("stat log: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return resp, errors.New("the log is not a regular file")
	}
	if st.Uid != uid {
		return resp, errors.New("the log is not owned by the account")
	}
	size := st.Size
	resp.Ino = st.Ino
	resp.Offset = size

	if offset >= 0 && st.Ino == ino && offset <= size {
		// Follow: what was appended since offset, complete lines only.
		end := offset + phpErrorLogReadMax
		if end > size {
			end = size
		}
		buf := make([]byte, end-offset)
		if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
			return resp, fmt.Errorf("read log: %w", err)
		}
		cut := bytes.LastIndexByte(buf, '\n') + 1
		if cut == 0 && len(buf) < phpErrorLogReadMax {
			// A line still being written: wait for its end.
			resp.Offset = offset
			return resp, nil
		}
		if cut == 0 {
			cut = len(buf) // one line longer than a read: take it as is
		}
		resp.Lines = splitLogLines(buf[:cut])
		resp.Offset = offset + int64(cut)
		return resp, nil
	}

	// Tail: the last lines of at most the last phpErrorLogReadMax bytes.
	start := size - phpErrorLogReadMax
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return resp, fmt.Errorf("read log: %w", err)
	}
	if start > 0 {
		// Drop the line the read started inside.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	all := splitLogLines(buf)
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	resp.Lines = all
	return resp, nil
}

// splitLogLines splits on newlines, drops a trailing empty line and cuts each
// line to phpErrorLogLineMax bytes.
func splitLogLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return []string{}
	}
	out := strings.Split(s, "\n")
	for i, l := range out {
		l = strings.TrimRight(l, "\r")
		if len(l) > phpErrorLogLineMax {
			l = l[:phpErrorLogLineMax] + " …"
		}
		out[i] = l
	}
	return out
}

func init() {
	Default.Register("php.error_log.read", phpErrorLogReadHandler)
}
