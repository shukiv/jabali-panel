// Package execstub writes the no-op binaries the agent's exec seams run under
// `go test` in place of a real host tool (pdnsutil, certbot: GH #994 / #1160).
//
// Each test binary that links one of those packages needs the stub, and the
// packages have no TestMain to remove a temporary directory afterwards. So
// the stub lives in one directory per user, shared by every test binary that
// user runs, instead of a new temporary directory per run.
package execstub

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// noOpScript drains stdin and exits 0 with no output.
const noOpScript = "#!/bin/sh\ncat >/dev/null 2>&1\nexit 0\n"

// NoOpBinary returns the path of a no-op stand-in for the tool name.
//
// The stub is in <tmp>/jabali-<name>-stub-<uid>, a directory only this user
// can write. Another user can create that directory first, since its name is
// known, so it is used only when it is a real directory this user owns with
// no group or other access. Otherwise the stub goes in a new temporary
// directory, as before. The script is written to a temporary file and renamed
// into place every time, so a stale or changed stub is replaced and a test
// binary running at the same time never sees a half-written one.
func NoOpBinary(name string) (string, error) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("jabali-%s-stub-%d", name, os.Getuid()))
	if err := privateDir(dir); err != nil {
		tmp, terr := os.MkdirTemp("", "jabali-"+name+"-stub-")
		if terr != nil {
			return "", terr
		}
		dir = tmp
	}
	f, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(noOpScript)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(f.Name(), 0o700)); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	p := filepath.Join(dir, name)
	if err := os.Rename(f.Name(), p); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return p, nil
}

// privateDir makes dir, or finds it, as a directory only this user can use.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return fmt.Errorf("%s belongs to another user", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is open to other users", dir)
	}
	return nil
}
