package commands

import (
	"context"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// moodleHomeRoot is where account home directories live; a variable so tests
// can point it at a temp dir.
var moodleHomeRoot = "/home"

// lookupAccountIDs returns the account's uid and the gid of its own group
// (named after it, as the installers' chgrp and systemd-run --gid use it).
var lookupAccountIDs = func(name string) (uid, gid uint32, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, 0, err
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	gid64, err := strconv.ParseUint(g.Gid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint32(uid64), uint32(gid64), nil
}

// LockMoodleDataParents puts every Moodle install's .moodledata directory in
// its account's own group at 0750. Installs made before the data root became
// owner-only have .moodledata in group www-data (the domain directory is
// setgid www-data) and a data root of 0777 inside it, so the web server's
// group could write every course file. Locking the parent takes that group off
// all of them without touching Moodle's files or config.php.
//
// Runs once at agent start, best-effort. A directory already in the account's
// group at 0750 is left alone, so after the first start this only reads. Only a
// real directory owned by the account its path names is changed, and the
// change runs as that account, so nothing the account planted can redirect it.
func LockMoodleDataParents(ctx context.Context, log *slog.Logger) {
	dirs, _ := filepath.Glob(filepath.Join(moodleHomeRoot, "*", "domains", "*", ".moodledata"))
	for _, dir := range dirs {
		rel, err := filepath.Rel(moodleHomeRoot, dir)
		if err != nil {
			continue
		}
		username, _, _ := strings.Cut(rel, string(filepath.Separator))
		if !usernameRe.MatchString(username) {
			continue
		}
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		uid, gid, err := lookupAccountIDs(username)
		if err != nil || st.Uid != uid {
			continue
		}
		if st.Gid == gid && fi.Mode().Perm() == 0o750 {
			continue
		}
		for _, args := range [][]string{
			{"chgrp", "--no-dereference", username, dir},
			{"chmod", "0750", dir},
		} {
			if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, username, args...), 0); err != nil {
				log.Warn("lock moodledata", "dir", dir, "err", err, "output", truncateStr(string(out), 256))
				break
			}
		}
	}
}
