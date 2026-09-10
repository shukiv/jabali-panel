package cpanel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeyops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeys"
)

// SSHKeyImportResult is returned to the restore-stage caller for
// progress reporting + manifest update.
type SSHKeyImportResult struct {
	Created int
	Skipped []string // dedup hits + parse failures, with reason
}

// ImportSSHKeys walks each authorized_keys file in the parsed
// tarball, splits on newline, and inserts one ssh_keys row per
// valid public key. Lines that fail validation (comments, blank,
// malformed key, non-OpenSSH formats) are skipped + recorded.
//
// Duplicate detection is fingerprint-based — within a single
// authorized_keys file it's common for the same key to appear
// twice (operator hand-edited + cPanel UI both wrote it). We
// keep the first occurrence per fingerprint and record the rest
// as 'duplicate' in Skipped.
//
// Cross-job dedup (same key already in jabali ssh_keys table) is
// the SSHKeyRepository's existing UNIQUE-by-fingerprint guarantee.
// Persistence runs through the shared sshkeyops.RestoreBatch (the
// same lifecycle leaf REST and account restore use), so a conflict
// surfaces as sshkeyops.ErrDuplicate and we record it as
// 'already_present'. The batch carries a nil Scheduler because a
// migration runs without a reconciler; per-owner authorized_keys
// converge on the reconciler's next tick, as the rest of restored
// state does.
//
// targetUserID must be the destination jabali user the restore
// stage created moments earlier. ID is the FK target.
func ImportSSHKeys(ctx context.Context, repo repository.SSHKeyRepository, parsed *ParsedTarball, targetUserID string, preserveSSH bool) (*SSHKeyImportResult, error) {
	if repo == nil {
		return nil, fmt.Errorf("ImportSSHKeys: repo nil")
	}
	if parsed == nil {
		return nil, fmt.Errorf("ImportSSHKeys: parsed tarball nil")
	}
	if targetUserID == "" {
		return nil, fmt.Errorf("ImportSSHKeys: targetUserID empty")
	}

	res := &SSHKeyImportResult{}
	seen := map[string]struct{}{}

	// Route persistence through the shared lifecycle leaf. A migration has no
	// running reconciler, so the batch takes a nil Scheduler and Flush is a
	// no-op; restored keys converge on the next reconciler tick. Deferred so an
	// owner whose rows already persisted is still flushed if a later row aborts.
	batch := sshkeyops.NewRestoreBatch(sshkeyops.Deps{Keys: repo})
	defer batch.Flush()

	for _, akPath := range parsed.SSHAuthorized {
		f, err := os.Open(akPath)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("open %s: %v", akPath, err))
			continue
		}
		scanner := bufio.NewScanner(f)
		// authorized_keys lines can be long (RSA-4096 ~700 bytes;
		// ed25519 ~80; certificates ~2-4 KiB). Bump the scanner
		// buffer so a 4 KiB cert doesn't truncate.
		scanner.Buffer(make([]byte, 0, 4096), 64*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			normalized, fp, err := sshkeys.ParseAndFingerprint(line)
			if err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d parse: %v", akPath, lineNum, err))
				continue
			}
			if _, dup := seen[fp]; dup {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d duplicate fingerprint", akPath, lineNum))
				continue
			}
			seen[fp] = struct{}{}

			// JAB-53: a migration moves data but RESETS access paths unless the
			// operator explicitly opts in. Without --preserve-source-state, do
			// NOT grant SSH access from source authorized_keys (a compromised
			// source could carry attacker/contractor/CI keys forward). Record the
			// fingerprint for the manifest so the admin can re-add trusted keys in
			// the SSH Keys page; never store nothing silently.
			if !preserveSSH {
				res.Skipped = append(res.Skipped, fmt.Sprintf("quarantined:%s:%d fp=%s (SSH access reset; opt in with --preserve-source-state)", akPath, lineNum, fp))
				continue
			}

			row := &models.SSHKey{
				ID:          ids.NewULID(),
				UserID:      targetUserID,
				Name:        keyNameFromComment(normalized),
				PublicKey:   normalized,
				Fingerprint: fp,
				CreatedAt:   time.Now().UTC(),
			}
			if err := batch.Restore(ctx, row); err != nil {
				if errors.Is(err, sshkeyops.ErrDuplicate) {
					res.Skipped = append(res.Skipped, fmt.Sprintf("%s:%d already_present (fp=%s)", akPath, lineNum, fp))
					continue
				}
				_ = f.Close()
				return res, fmt.Errorf("create ssh_keys row (line %d of %s): %w", lineNum, akPath, err)
			}
			res.Created++
		}
		if err := scanner.Err(); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("scan %s: %v", akPath, err))
		}
		_ = f.Close()
	}
	return res, nil
}

// keyNameFromComment pulls the trailing comment off an authorized-
// keys line and returns it as the panel-side display name.
// Falls back to "imported-key" when no comment present.
//
// authorized_keys format: <type> <base64-blob> [comment-with-spaces].
// The normalized form from ParseAndFingerprint preserves the
// comment so we re-split here.
func keyNameFromComment(normalized string) string {
	parts := strings.SplitN(strings.TrimSpace(normalized), " ", 3)
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[2])
	}
	return "imported-key"
}

// ctxIsLive returns false when the caller's context is cancelled.
// Restore-stage long-running paths poll this between batches so
// the operator can cancel mid-run via the admin REST endpoint.
//
//nolint:unused // available for future per-batch chunking
func ctxIsLive(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}
