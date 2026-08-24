package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// GH #1169 gap 4 — ownership normalization after a cross-host panel-config restore.
//
// rsync preserves the SOURCE box's NUMERIC uid/gid, but system accounts
// (jabali, jabali-mail, …) can hold DIFFERENT ids on the two boxes (allocation
// order). So after a DR restore a file the source's `jabali` (uid 996) owned is
// now owned by whatever local account happens to hold 996 — often nobody — and
// the panel, running as the LOCAL `jabali` (uid 997), can't read sso.key or its
// secrets: the panel half-breaks (the #331 drill's exact symptom).
//
// The fix resolves each source id → NAME (from the restore's own os_users.json)
// → LOCAL id, and chowns by name, preserving the exact per-file ownership INTENT.
// This is deliberately NOT a blanket chown-to-jabali: /etc/jabali-panel is
// multi-owner (root:jabali, jabali:jabali, jabali:jabali-mail, …) and a blanket
// chown would widen group-read on a secret — a security regression.

// remapID maps a source numeric id to the LOCAL id for the same NAME. Returns
// (localID, true) only when the source id has a known name that resolves locally
// to a DIFFERENT id; (0, false) when the id is unknown or already matches. Pure,
// so the remap decision is unit-tested without touching the filesystem.
func remapID(srcID int, srcIDToName map[int]string, localNameToID map[string]int) (int, bool) {
	name, ok := srcIDToName[srcID]
	if !ok {
		return 0, false
	}
	local, ok := localNameToID[name]
	if !ok || local == srcID {
		return 0, false
	}
	return local, true
}

// normalizePanelConfigOwnership re-points /etc/jabali-panel ownership by name
// after the panel_config rsync, using the source passwd/group captured in the
// os_users stage. Best-effort: a missing/garbled os_users.json or a failed chown
// warns but never fails the restore. Returns operator-visible applied/warnings.
func normalizePanelConfigOwnership(stagingRoot, target string) (applied, warnings []string) {
	src := filepath.Join(stagingRoot, "os_users", "os_users.json")
	raw, err := os.ReadFile(src) // #nosec G304 — server-controlled stage dir
	if err != nil {
		return nil, []string{fmt.Sprintf("ownership normalize: no os_users.json (%v); left panel-config ownership as-restored", err)}
	}
	var bundle osUsersBundle
	if jerr := json.Unmarshal(raw, &bundle); jerr != nil {
		return nil, []string{fmt.Sprintf("ownership normalize: parse os_users.json: %v", jerr)}
	}

	srcUIDToName := map[int]string{}
	for _, line := range bundle.Passwd {
		if e, perr := parsePasswdLine(line); perr == nil {
			srcUIDToName[e.UID] = e.Name
		}
	}
	srcGIDToName := map[int]string{}
	for _, line := range bundle.Group {
		if name, gid, _, perr := parseGroupLine(line); perr == nil {
			srcGIDToName[gid] = name
		}
	}

	// Local name→id, resolved once per name (cache misses too, so an unknown
	// name isn't looked up on every file).
	localUID := map[string]int{}
	resolveUID := func(name string) (int, bool) {
		if id, seen := localUID[name]; seen {
			return id, id >= 0
		}
		u, lerr := user.Lookup(name)
		if lerr != nil {
			localUID[name] = -1
			return 0, false
		}
		id, _ := strconv.Atoi(u.Uid)
		localUID[name] = id
		return id, true
	}
	localGID := map[string]int{}
	resolveGID := func(name string) (int, bool) {
		if id, seen := localGID[name]; seen {
			return id, id >= 0
		}
		g, lerr := user.LookupGroup(name)
		if lerr != nil {
			localGID[name] = -1
			return 0, false
		}
		id, _ := strconv.Atoi(g.Gid)
		localGID[name] = id
		return id, true
	}

	remapped := 0
	_ = filepath.Walk(target, func(p string, info os.FileInfo, werr error) error {
		if werr != nil || info == nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		curUID, curGID := int(st.Uid), int(st.Gid)
		newUID, newGID := -1, -1
		if name, ok := srcUIDToName[curUID]; ok {
			if lid, ok2 := resolveUID(name); ok2 && lid != curUID {
				newUID = lid
			}
		}
		if name, ok := srcGIDToName[curGID]; ok {
			if lid, ok2 := resolveGID(name); ok2 && lid != curGID {
				newGID = lid
			}
		}
		if newUID < 0 && newGID < 0 {
			return nil
		}
		// Lchown with -1 leaves the unchanged half alone and never follows a
		// symlink into another tree.
		if cerr := os.Lchown(p, newUID, newGID); cerr != nil {
			warnings = append(warnings, fmt.Sprintf("ownership normalize %s: %v", p, cerr))
			return nil
		}
		remapped++
		return nil
	})
	if remapped > 0 {
		applied = append(applied, fmt.Sprintf("normalized ownership on %d panel-config file(s) by name (cross-host uid/gid shift)", remapped))
	}
	return applied, warnings
}
