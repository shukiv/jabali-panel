package commands

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: an uploaded backup is extracted with its symlinks kept, because a
// home or an app's data folder legitimately holds them and the restore copies
// those trees with rsync, which keeps a link as a link. Root reads everything
// else in the staged tree by path: the manifest, the metadata, the database
// dumps, the mail tree and the stage folders themselves. So an extracted upload
// may hold a symlink only inside a home or an app's data folder, and the
// stages reach what they read without following one.

// checkStagedLinks refuses an extracted upload that holds a symlink anywhere
// but strictly inside a home or an app's data folder under root. staging is the
// whole extracted tree; root is its per-stage root.
func checkStagedLinks(staging, root string) error {
	return filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if rel, rerr := filepath.Rel(root, p); rerr == nil && stagedLinkAllowed(filepath.ToSlash(rel)) {
			return nil
		}
		rel, _ := filepath.Rel(staging, p)
		return fmt.Errorf("archive rejected: symlink %q is outside a home or an app's data folder", rel)
	})
}

// stagedLinkAllowed reports whether rel, a slash path relative to the
// per-stage root, lies strictly inside a home (home/home/<user>/…) or an
// app's data folder (docker/var/lib/jabali/docker-apps/<slug>/…). The home,
// the app's folder and every folder above them must be real folders: the
// restore copies their contents by path.
func stagedLinkAllowed(rel string) bool {
	parts := strings.Split(rel, "/")
	homes := []string{backup.StageHome, "home"}
	apps := append([]string{backup.StageDocker}, strings.Split(strings.TrimPrefix(dockerAppDataRoot, "/"), "/")...)
	for _, prefix := range [][]string{homes, apps} {
		// The prefix, the user or slug, then at least one more name.
		if len(parts) >= len(prefix)+2 && slices.Equal(parts[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// stagedEntry returns an error unless path lies at or below root, every
// folder from root down to path is a real folder rather than a symlink, and
// path itself is a folder (wantDir) or a regular file. It follows no symlink.
func stagedEntry(root, path string, wantDir bool) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	cur := root
	fi, err := os.Lstat(cur)
	if err != nil {
		return err
	}
	if rel != "." {
		for _, name := range strings.Split(rel, string(filepath.Separator)) {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a folder", cur)
			}
			cur = filepath.Join(cur, name)
			if fi, err = os.Lstat(cur); err != nil {
				return err
			}
		}
	}
	// Lstat reports a symlink as neither a folder nor a regular file.
	switch {
	case wantDir && !fi.IsDir():
		return fmt.Errorf("%s is not a folder", path)
	case !wantDir && !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

// openStagedFile opens the regular file at path below root for reading,
// following no symlink on the way.
func openStagedFile(root, path string) (*os.File, error) {
	if err := stagedEntry(root, path, false); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// readStagedFile reads the regular file at path below root, following no
// symlink on the way.
func readStagedFile(root, path string) ([]byte, error) {
	f, err := openStagedFile(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
