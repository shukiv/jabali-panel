package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// app_manifest.go — GH #946.
//
// Deleting an app left files behind. Reported against ITFlow, but it is not an
// ITFlow bug: thirteen app deleters remove a HARDCODED list of top-level entries
// when the app is installed at the docroot root, e.g.
//
//	var itflowTopLevel = []string{".git", ".github", "admin", "api", ...}
//
// The list is a snapshot of what upstream's tree looked like when the deleter
// was written. It cannot know about anything the app creates while running —
// uploads, caches, logs, generated config — nor about directories upstream added
// since. Every one of those survives the delete, and the operator is left
// picking files out of public_html by hand.
//
// The list exists for a good reason: at the docroot root a blind `rm -rf` would
// take a docroot that may hold other content with it. So the fix is not to
// delete more aggressively, it is to know exactly what WE put there.
//
// This records the top-level entries an install actually created, by diffing the
// install root immediately before and after the app's installer runs, and sweeps
// them on delete. It sits in the app.install / app.delete dispatch, so all
// thirteen apps get it without touching a single per-app handler — and the
// per-app lists stay as the fallback for installs made before this existed.
//
// Keyed by install root rather than install ID: every installer already sends
// docroot + subdirectory (the agent cannot install without them), whereas
// install_id is plumbed per-app and not uniformly present.

// appManifestDir holds one manifest per install root. Outside any docroot on
// purpose: inside, it would itself become a file the app owns, tenant-writable
// and liable to be swept by the very cleanup it describes.
const appManifestDir = "/var/lib/jabali-panel/app-manifests"

// appManifest is the on-disk record. Root is stored so a manifest can be read
// back and understood without reversing the hash.
type appManifest struct {
	Root    string   `json:"root"`
	AppType string   `json:"app_type,omitempty"`
	Entries []string `json:"entries"`
}

// appDispatchPaths are the fields every app installer and deleter already
// carries. Parsed loosely — a handler that omits one simply opts out.
type appDispatchPaths struct {
	AppType      string `json:"app_type"`
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
}

func (p appDispatchPaths) installRoot() string {
	if p.Docroot == "" {
		return ""
	}
	sub := strings.Trim(p.Subdirectory, "/")
	if sub == "" {
		return filepath.Clean(p.Docroot)
	}
	return filepath.Join(filepath.Clean(p.Docroot), sub)
}

func parseAppDispatchPaths(params json.RawMessage) appDispatchPaths {
	var p appDispatchPaths
	_ = json.Unmarshal(params, &p)
	return p
}

// manifestPathFor hashes the install root so the filename is fixed-length and
// free of separators, whatever the docroot looks like.
func manifestPathFor(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(appManifestDir, hex.EncodeToString(sum[:])+".json")
}

// topLevelEntries returns the names directly inside root. A missing root is not
// an error — it means the installer is about to create it, and everything in it
// afterwards is ours.
func topLevelEntries(root string) map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range ents {
		out[e.Name()] = true
	}
	return out
}

// writeAppManifest records the entries an install created.
//
// Best-effort: a manifest that cannot be written must never fail the install.
// The cost of losing it is falling back to the per-app list, which is exactly
// today's behaviour.
func writeAppManifest(root, appType string, entries []string) {
	if root == "" || len(entries) == 0 {
		return
	}
	if err := os.MkdirAll(appManifestDir, 0o700); err != nil {
		return
	}
	sort.Strings(entries)
	b, err := json.Marshal(appManifest{Root: root, AppType: appType, Entries: entries})
	if err != nil {
		return
	}
	// Root-owned and root-only: the tenant must not be able to edit the list of
	// paths a privileged delete will later remove.
	_ = os.WriteFile(manifestPathFor(root), b, 0o600)
}

func readAppManifest(root string) (appManifest, bool) {
	if root == "" {
		return appManifest{}, false
	}
	b, err := os.ReadFile(manifestPathFor(root))
	if err != nil {
		return appManifest{}, false
	}
	var m appManifest
	if json.Unmarshal(b, &m) != nil {
		return appManifest{}, false
	}
	return m, len(m.Entries) > 0
}

func removeAppManifestFile(root string) {
	if root == "" {
		return
	}
	_ = os.Remove(manifestPathFor(root))
}

// safeManifestEntry rejects anything that is not a plain name directly inside
// root.
//
// The manifest is written by this process from a directory listing, so entries
// are already bare names — but this runs `rm -rf` as the tenant against whatever
// it is handed, and a stored path that escapes the install root would be a
// privileged delete of an arbitrary location. Cheap to check, catastrophic to
// omit.
func safeManifestEntry(root, name string) (string, bool) {
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	if strings.ContainsRune(name, filepath.Separator) || strings.Contains(name, "\x00") {
		return "", false
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "" || cleanRoot == "/" {
		return "", false
	}
	target := filepath.Join(cleanRoot, name)
	if target == cleanRoot || !strings.HasPrefix(target, cleanRoot+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

// sweepAppManifest removes the recorded entries that still exist.
//
// Runs as the tenant, like the per-app deleters, so ownership and AppArmor
// confinement are unchanged. Returns how many entries it removed, for the log.
func sweepAppManifest(ctx context.Context, osUser, root string) int {
	if osUser == "" {
		return 0
	}
	m, ok := readAppManifest(root)
	if !ok {
		return 0
	}
	removed := 0
	for _, name := range m.Entries {
		target, ok := safeManifestEntry(root, name)
		if !ok {
			continue
		}
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		cmd := buildSystemdRunCmd(ctx, osUser, "rm", "-rf", target)
		if cmd.Run() == nil {
			removed++
		}
	}
	return removed
}
