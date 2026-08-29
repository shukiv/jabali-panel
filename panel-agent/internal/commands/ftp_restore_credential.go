// GH #1361: FTP subaccount password preservation across an ACCOUNT restore.
//
// An FTP subaccount's login password lives ONLY in /etc/shadow — never in the
// panel DB. So restoring the ftp_accounts row rebuilds the account but the
// reconciler, having no password, sets a random throwaway (the tenant must
// reset). To preserve the original password we ride it in the backup metadata
// bundle (agent-enriched, since panel-api can't read /etc/shadow) and hand it
// back to the reconciler through a short-lived, root-only staging file that
// the ftpaccount.create path consumes when it recreates the account.
//
// The staging file is the bridge from the (untrusted) restic metadata to the
// root-only shadow write, so every value is validated before it is written or
// consumed, the file is keyed by (username,uid) to prevent a stale hash being
// handed to a later same-named account, and it carries a timestamp so an
// orphan (account never reprovisioned) expires instead of lurking forever.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// ftpRestoreCredDir holds the staged shadow hashes. Root-only (0700); each
// file 0600. Never backed up (written fresh at restore time, consumed on the
// next reconciler tick). A var (not const) so tests can point it at a temp dir.
var ftpRestoreCredDir = "/var/lib/jabali/ftp-restore-creds"

// ftpRestoreCredTTL bounds how long a staged hash may sit unconsumed. A file
// older than this is ignored + deleted — an account that never got
// reprovisioned must not silently hand its old password to some future
// same-named account (a create the tenant did much later).
const ftpRestoreCredTTL = 24 * time.Hour

// safeFtpUsernameRe gates a value before it becomes a filename or a
// getent/chpasswd argument. FTP subaccount usernames are <tenant>_<label>,
// both system-username-shaped; this rejects path traversal, ':' (shadow field
// separator) and whitespace/control bytes.
var safeFtpUsernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,63}$`)

// shadowHashRe is the crypt(3) hash charset ($id$salt$hash) plus the lock
// sentinels (!, *, !!, !$6$…). Deliberately excludes ':', newline and
// whitespace, so a poisoned metadata bundle can't inject a second shadow line
// through the `user:hash` chpasswd -e stdin.
var shadowHashRe = regexp.MustCompile(`^[A-Za-z0-9./$!*]{1,512}$`)

// validFtpUsername reports whether s is safe as a filename + shell argument.
func validFtpUsername(s string) bool { return safeFtpUsernameRe.MatchString(s) }

// validShadowHash reports whether h is a plausible, injection-free crypt hash.
func validShadowHash(h string) bool { return shadowHashRe.MatchString(h) }

// ftpRestoreCred is the on-disk staging record.
type ftpRestoreCred struct {
	UID  *uint32 `json:"uid,omitempty"`
	Hash string  `json:"hash"`
	TS   int64   `json:"ts"` // unix seconds when staged
}

// credFilePath maps a username to its staging file. Caller MUST have validated
// the username first (validFtpUsername).
func credFilePath(username string) string {
	return filepath.Join(ftpRestoreCredDir, username)
}

// uidEqual compares two optional uids (both nil = equal; one nil = not equal).
func uidEqual(a, b *uint32) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// sweepFtpRestoreCreds removes stale staged files (older than the TTL). Best
// effort — errors are ignored. Called before a restore stages fresh files so
// the dir never accumulates orphans across restores.
func sweepFtpRestoreCreds(now time.Time) {
	entries, err := os.ReadDir(ftpRestoreCredDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(ftpRestoreCredDir, e.Name())
		b, rerr := os.ReadFile(p) //nolint:gosec // fixed root-only dir
		if rerr != nil {
			continue
		}
		var c ftpRestoreCred
		if json.Unmarshal(b, &c) != nil || now.Unix()-c.TS >= int64(ftpRestoreCredTTL.Seconds()) {
			_ = os.Remove(p)
		}
	}
}

// writeFtpRestoreCred stages one account's shadow hash for the reconciler to
// pick up. Rejects an unsafe username or hash (returns an error the caller
// logs + skips — never fatal to the restore). The uid pins the record to the
// exact account identity so a same-named-later account can't consume it.
func writeFtpRestoreCred(username string, uid *uint32, hash string, now time.Time) error {
	if !validFtpUsername(username) {
		return fmt.Errorf("ftp restore cred: unsafe username %q", username)
	}
	if !validShadowHash(hash) {
		return fmt.Errorf("ftp restore cred %q: unsafe shadow hash", username)
	}
	if err := os.MkdirAll(ftpRestoreCredDir, 0o700); err != nil {
		return fmt.Errorf("ftp restore cred dir: %w", err)
	}
	body, err := json.Marshal(ftpRestoreCred{UID: uid, Hash: hash, TS: now.Unix()})
	if err != nil {
		return err
	}
	return os.WriteFile(credFilePath(username), body, 0o600)
}

// consumeFtpRestoreCred returns the staged shadow hash for (username,uid) and
// deletes the file. It returns ok=false — and still deletes — when there is no
// file, the uid does not match, the record is stale, or the value fails
// re-validation. Fail-closed: any doubt means "no staged credential, use the
// throwaway", never "hand over a maybe-wrong hash".
func consumeFtpRestoreCred(username string, uid *uint32, now time.Time) (string, bool) {
	if !validFtpUsername(username) {
		return "", false
	}
	p := credFilePath(username)
	b, err := os.ReadFile(p) //nolint:gosec // fixed root-only dir, validated name
	if err != nil {
		return "", false
	}
	// One-shot: remove regardless of outcome so a rejected/stale file can't be
	// retried or linger.
	defer func() { _ = os.Remove(p) }()
	var c ftpRestoreCred
	if json.Unmarshal(b, &c) != nil {
		return "", false
	}
	if !uidEqual(c.UID, uid) {
		return "", false
	}
	if now.Unix()-c.TS >= int64(ftpRestoreCredTTL.Seconds()) {
		return "", false
	}
	if !validShadowHash(c.Hash) {
		return "", false
	}
	return c.Hash, true
}

// enrichFtpCredentials fills PasswordShadow on each metadata FTP account from
// /etc/shadow. Done agent-side (panel-api can't read shadow) — the same
// pattern as enrichKratos. A row whose username fails validation, or has no
// shadow entry, is left with an empty PasswordShadow (restore falls back to a
// throwaway for it) rather than aborting the whole metadata stage.
func enrichFtpCredentials(meta *backup.AccountMetadata) error {
	if meta == nil || len(meta.FtpAccounts) == 0 {
		return nil
	}
	keep := make(map[string]struct{}, len(meta.FtpAccounts))
	for _, a := range meta.FtpAccounts {
		if validFtpUsername(a.Username) {
			keep[a.Username] = struct{}{}
		}
	}
	if len(keep) == 0 {
		return nil
	}
	lines, err := readUsernameKeyedFile("/etc/shadow", keep)
	if err != nil {
		return fmt.Errorf("read /etc/shadow: %w", err)
	}
	hashByUser := shadowHashesFromLines(lines)
	for i := range meta.FtpAccounts {
		if h, ok := hashByUser[meta.FtpAccounts[i].Username]; ok {
			meta.FtpAccounts[i].PasswordShadow = h
		}
	}
	return nil
}

// shadowHashesFromLines parses `user:hash:...` /etc/shadow lines into a
// username→hash map, keeping only well-formed, injection-free hashes.
func shadowHashesFromLines(lines []string) map[string]string {
	out := make(map[string]string, len(lines))
	for _, ln := range lines {
		fields := strings.SplitN(ln, ":", 3)
		if len(fields) < 2 {
			continue
		}
		if validShadowHash(fields[1]) {
			out[fields[0]] = fields[1]
		}
	}
	return out
}

// stageFtpRestoreCredentials writes a staging file per metadata FTP account
// that carries a shadow hash. Called from the restore handler after the stages
// materialize; the reconciler consumes them when it recreates each account.
// Best effort per account — a bad row warns (via the returned skipped list)
// and is skipped, never aborting the restore.
func stageFtpRestoreCredentials(meta *backup.AccountMetadata, now time.Time) (staged int, skipped []string) {
	if meta == nil || len(meta.FtpAccounts) == 0 {
		return 0, nil
	}
	sweepFtpRestoreCreds(now)
	for _, a := range meta.FtpAccounts {
		if a.PasswordShadow == "" {
			continue // no captured password — reconciler uses a throwaway
		}
		if err := writeFtpRestoreCred(a.Username, a.UID, a.PasswordShadow, now); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", a.Username, err))
			continue
		}
		staged++
	}
	return staged, skipped
}
