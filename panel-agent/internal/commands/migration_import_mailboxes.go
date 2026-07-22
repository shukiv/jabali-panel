// migration.import_mailboxes — push extracted Maildir messages into
// Stalwart via JMAP Blob/upload + Email/import (M35 cPanel restore
// mailboxes per-area writer; upgrades the panel-side observation
// stub at panel-api/internal/migrate/cpanel/restore_mail.go to
// real ingest).
//
// Per-mailbox workflow:
//  1. Resolve accountId by email (existing accountIDByEmail helper)
//  2. Resolve INBOX mailboxId via Mailbox/query (role=inbox)
//  3. For each .eml file in cur/ + new/:
//     a. POST raw bytes to /jmap/upload → blobId
//     b. Email/import with blobId + mailboxIds:{<inbox>:true} +
//     keywords:{$seen:true for cur/, none for new/} +
//     receivedAt parsed from Maildir filename
//  4. Record bytes + count in MailboxImportResult
//
// Idempotent on resume: pushMaildirSlots skips any message whose
// Message-ID is already present in the target mailbox (Stalwart's
// Email/import does NOT dedup, so we dedup explicitly). We don't track
// per-message progress in
// migration_stages (would 10x the row count); operator sees per-
// mailbox count + bytes summary in the manifest_json warnings.
//
// SECURITY: src_dir is path-validated against /var/lib/jabali-
// migrations/ prefix (same as migration_import_home). Refuses any
// path outside the staging root.
package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

const (
	migrationMailboxTimeout         = 4 * time.Hour
	migrationMailboxJMAPCallTimeout = 30 * time.Second
	// migrationMailboxMessageCap caps per-message size at 64 MiB.
	// Stalwart's default is 50 MiB; bumped slightly so a slightly-
	// over-default attachment doesn't fail the whole mailbox.
	migrationMailboxMessageCap = 64 << 20
)

type migrationImportMailboxesParams struct {
	JobID      string `json:"job_id"`
	SrcMailDir string `json:"src_mail_dir"` // /var/lib/jabali-migrations/<id>/extracted/cp/<u>/homedir/mail
	// OwnerEmail is the cPanel-owner's default mailbox address
	// (<user>@<primary-domain>). Set when the operator wants the
	// owner's INBOX + Maildir+ subfolders at the root of the
	// mail/ tree imported under that address. When empty, the
	// handler skips owner-mailbox detection — only per-domain
	// dirs are processed.
	OwnerEmail string `json:"owner_email,omitempty"`
	// DestUser, when set, additionally permits src_mail_dir under
	// /home/<DestUser>/mail — CyberPanel Maildirs are rsynced into the migrated
	// user's home (out-of-tarball /home/vmail source), not the staging tree.
	// Reads stay symlink-safe (scoped RESOLVE_BENEATH this root).
	DestUser string `json:"dest_user,omitempty"`
}

type migrationImportMailboxesResult struct {
	MailboxesProcessed int      `json:"mailboxes_processed"`
	MessagesImported   int64    `json:"messages_imported"`
	MessagesSkipped    int64    `json:"messages_skipped"`
	BytesImported      int64    `json:"bytes_imported"`
	Skipped            []string `json:"skipped,omitempty"`
}

func init() {
	Default.Register("migration.import_mailboxes", migrationImportMailboxesHandler)
}

func migrationImportMailboxesHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationImportMailboxesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error(),
		}
	}
	if p.JobID == "" || p.SrcMailDir == "" {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "job_id, src_mail_dir required",
		}
	}
	srcAbs, err := filepath.Abs(p.SrcMailDir)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "src_mail_dir not absolute: " + err.Error(),
		}
	}
	allowedRoots := migrationStagingRoots
	if p.DestUser != "" {
		if strings.Contains(p.DestUser, "/") || strings.Contains(p.DestUser, "..") {
			return nil, &agentwire.AgentError{
				Code: agentwire.CodeInvalidArgument, Message: "dest_user must be a bare username",
			}
		}
		allowedRoots = append(append([]string{}, migrationStagingRoots...), "/home/"+p.DestUser+"/mail")
	}
	underRoot := false
	for _, root := range allowedRoots {
		if strings.HasPrefix(srcAbs+"/", root+"/") {
			underRoot = true
			break
		}
	}
	if !underRoot {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("src_mail_dir must live under %s, got %q", strings.Join(allowedRoots, " or "), srcAbs),
		}
	}

	return importMaildirTree(ctx, srcAbs, p.OwnerEmail, allowedRoots)
}

// importMaildirTree imports every <domain>/<local>/{cur,new,.Sub} Maildir
// under srcAbs into the matching Stalwart account via JMAP Email/import
// (Message-ID dedup → idempotent). When ownerEmail is set, a Maildir
// directly at srcAbs (cPanel owner layout) imports under that address.
// Shared by migration.import_mailboxes (operator-supplied path, prefix-
// guarded by the caller) and account-restore (internally-derived path,
// ADR-0123). Applies its own 4h ceiling.
func importMaildirTree(ctx context.Context, srcAbs, ownerEmail string, allowedRoots []string) (*migrationImportMailboxesResult, error) {
	subctx, cancel := context.WithTimeout(ctx, migrationMailboxTimeout)
	defer cancel()

	res := &migrationImportMailboxesResult{}

	// Owner default mailbox — cPanel stores the user's primary
	// mailbox directly under mail/{cur,new,tmp,.Drafts,...} rather
	// than under a per-domain subdir. Import it under ownerEmail
	// when supplied so messages aren't silently dropped.
	if ownerEmail != "" {
		if _, ok := looksLikeMailMaildir(srcAbs); ok {
			n, b, skipped, err := importOneMailbox(subctx, ownerEmail, srcAbs, allowedRoots)
			if err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("owner_mailbox %s: %v", ownerEmail, err))
			} else {
				res.MailboxesProcessed++
				res.MessagesImported += n
				res.BytesImported += b
				res.Skipped = append(res.Skipped, skipped...)
			}
		}
	}

	// Layout: cp/<user>/homedir/mail/<domain>/<localpart>/{cur,new,tmp}/
	// SrcMailDir points at .../homedir/mail.
	domains, err := os.ReadDir(srcAbs)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("read mail root %s: %v", srcAbs, err),
		}
	}
	for _, dom := range domains {
		if !dom.IsDir() {
			continue
		}
		domPath := filepath.Join(srcAbs, dom.Name())
		users, err := os.ReadDir(domPath)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("read domain %s: %v", dom.Name(), err))
			continue
		}
		for _, u := range users {
			if !u.IsDir() {
				continue
			}
			userPath := filepath.Join(domPath, u.Name())
			maildirPath, ok := looksLikeMailMaildir(userPath)
			if !ok {
				continue
			}
			email := fmt.Sprintf("%s@%s", u.Name(), dom.Name())
			n, b, skipped, err := importOneMailbox(subctx, email, maildirPath, allowedRoots)
			if err != nil {
				// Don't fail the whole job on one mailbox — record
				// + skip. Operator inspects manifest_json + can
				// re-run if needed.
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", email, err))
				continue
			}
			res.MailboxesProcessed++
			res.MessagesImported += n
			res.BytesImported += b
			res.Skipped = append(res.Skipped, skipped...)
		}
	}
	return res, nil
}

// looksLikeMailMaildir checks for cur/ or new/ direct children
// (cpanel + Hestia layout: <local>/{cur,new,tmp}). When that
// fails, tries <local>/Maildir/{cur,new}/ — DA layout. Returns
// the path that contains cur+new (either userPath or
// userPath/Maildir) plus a bool indicating whether a Maildir-
// shaped tree was found.
func looksLikeMailMaildir(path string) (string, bool) {
	for _, marker := range []string{"cur", "new"} {
		if st, err := os.Stat(filepath.Join(path, marker)); err == nil && st.IsDir() {
			return path, true
		}
	}
	// DA: extra Maildir/ subdir.
	dapath := filepath.Join(path, "Maildir")
	for _, marker := range []string{"cur", "new"} {
		if st, err := os.Stat(filepath.Join(dapath, marker)); err == nil && st.IsDir() {
			return dapath, true
		}
	}
	// Subfolder-only mailbox: empty INBOX (no root cur/new) but a
	// Maildir++ .<Sub> with cur/new still holds mail. Treat the dir as a
	// maildir so importOneMailbox processes the (empty) INBOX + every
	// subfolder, instead of dropping the whole mailbox.
	if entries, err := os.ReadDir(path); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), ".") {
				continue
			}
			for _, marker := range []string{"cur", "new"} {
				if st, serr := os.Stat(filepath.Join(path, e.Name(), marker)); serr == nil && st.IsDir() {
					return path, true
				}
			}
		}
	}
	return "", false
}

// importOneMailbox pushes every .eml-shaped message in cur/ + new/
// into the destination Stalwart account's INBOX. Returns
// (messages_imported, bytes_imported, skipped, error).
//
// Email/import is per-message because Stalwart's blob upload limit
// is per-blob, not per-batch. Pipelining 10-100 imports per JMAP
// call would be a follow-up optimisation; v1 sequential import is
// correct + bounded.
func importOneMailbox(ctx context.Context, destEmail, maildir string, allowedRoots []string) (int64, int64, []string, error) {
	// Ensure domain + account exist in Stalwart before trying to import.
	// accountEnsureInRegistry is idempotent: creates domain then account
	// when absent, no-ops when they already exist.
	if err := accountEnsureInRegistry(ctx, destEmail); err != nil {
		return 0, 0, nil, fmt.Errorf("ensure Stalwart account %q: %w", destEmail, err)
	}
	accountID, err := accountIDByEmail(ctx, destEmail)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("resolve account %q: %w", destEmail, err)
	}
	if accountID == "" {
		return 0, 0, nil, fmt.Errorf("destination account %q not found in Stalwart after ensure", destEmail)
	}
	inboxID, err := mailboxIDByRole(ctx, accountID, "inbox")
	if err != nil {
		return 0, 0, nil, fmt.Errorf("resolve inbox: %w", err)
	}

	var imported, bytes int64
	var skipped []string

	// Push INBOX (mailroot itself: cur/ + new/).
	in, ib, isk := pushMaildirSlots(ctx, accountID, inboxID, maildir, &skipped, allowedRoots)
	imported += in
	bytes += ib
	skipped = append(skipped, isk...)

	// Push every Maildir+ subfolder (.Drafts, .Junk, .Sent, .Trash,
	// .spam, .Archive, …). Each is a sibling dir of cur/new at
	// `<maildir>/.<Name>/{cur,new,tmp}`. The leading dot is the
	// Maildir++ subfolder marker; the friendly name we use for the
	// JMAP mailbox strips the dot + collapses nested dots to slashes
	// per RFC 5256 §5.1.
	entries, _ := os.ReadDir(maildir)
	// Load the account's mailbox tree once so the Maildir++ hierarchy
	// resolves in-memory (by name+parentId), without relying on
	// server-side Mailbox/query filtering. Best-effort: nil on error →
	// every level is created fresh.
	var nodes []mailboxNode
	if len(entries) > 0 {
		nodes, _ = loadMailboxNodes(ctx, accountID)
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// Skip the Maildir spec dirs (none start with dot but defensive).
		if e.Name() == "cur" || e.Name() == "new" || e.Name() == "tmp" {
			continue
		}
		subDir := filepath.Join(maildir, e.Name())
		// Maildir++ encodes hierarchy as ".Parent.Child.Grandchild"; split
		// into segments and ensure the whole chain so a nested folder
		// restores nested, not flattened to its leaf name.
		raw := strings.TrimPrefix(e.Name(), ".")
		segments := strings.Split(raw, ".")
		mboxID, err := ensureMailboxPath(ctx, accountID, segments, &nodes)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("ensure mailbox %q: %v", raw, err))
			continue
		}
		sn, sb, ssk := pushMaildirSlots(ctx, accountID, mboxID, subDir, &skipped, allowedRoots)
		imported += sn
		bytes += sb
		skipped = append(skipped, ssk...)
	}

	return imported, bytes, skipped, nil
}

// pushMaildirSlots imports every .eml-shaped message under
// <maildir>/cur + <maildir>/new into the named Stalwart mailbox.
// Returns (messages_imported, bytes_imported, skipped).
func pushMaildirSlots(ctx context.Context, accountID, mailboxID, maildir string, _ *[]string, allowedRoots []string) (int64, int64, []string) {
	var imported, bytes int64
	var skipped []string
	// Idempotency: Stalwart's Email/import does NOT dedup on Message-ID
	// (a re-run would duplicate every message), so we skip any message
	// whose Message-ID is already present in the target mailbox. The set
	// also absorbs messages imported earlier in this same run.
	seen := existingMessageIDs(ctx, accountID, mailboxID)
	for _, sub := range []string{"cur", "new"} {
		dir := filepath.Join(maildir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		seenSlot := sub == "cur" // Maildir spec: cur/ holds already-read mail
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := e.Info()
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("stat %s: %v", path, err))
				continue
			}
			if info.Size() > migrationMailboxMessageCap {
				skipped = append(skipped, fmt.Sprintf("oversized:%s:%d", path, info.Size()))
				continue
			}
			if mid := messageIDFromFile(path, allowedRoots); mid != "" {
				if seen[mid] {
					continue // already in mailbox → idempotent skip
				}
				seen[mid] = true
			}
			// Flags: trust the Maildir ":2,<flags>" suffix when present
			// (S/F/R/D → $seen/$flagged/$answered/$draft); otherwise fall
			// back to the slot (cur=seen). Restores $flagged/$answered/$draft
			// that the seen-only path dropped.
			var keywords map[string]bool
			if flags, hasInfo := maildirInfoFlags(e.Name()); hasInfo {
				keywords = maildirFlagsToKeywords(flags)
			} else {
				keywords = map[string]bool{}
				if seenSlot {
					keywords["$seen"] = true
				}
			}
			n, err := importOneMessage(ctx, accountID, mailboxID, path, info.Size(), keywords, info.ModTime(), allowedRoots)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			imported++
			bytes += n
		}
	}
	return imported, bytes, skipped
}

// maildirInfoFlags extracts the Maildir info-flag letters from a
// filename. A Maildir "cur" file is "<base>:2,<flags>"; "new" files have
// no info part. Returns (flags, true) when the ":2," marker is present
// (even with empty flags), else ("", false) so the caller falls back to
// the slot (cur=seen). This "trust the suffix when present" rule also
// fixes real cPanel cur/ messages without an S flag being marked seen.
func maildirInfoFlags(name string) (string, bool) {
	i := strings.LastIndex(name, ":2,")
	if i < 0 {
		return "", false
	}
	return name[i+3:], true
}

// maildirFlagsToKeywords maps Maildir info-flag letters to JMAP keywords
// (inverse of keywordsToMaildirFlags). Unknown letters (incl. 'T'
// trashed, 'P' passed) are ignored.
func maildirFlagsToKeywords(flags string) map[string]bool {
	kw := map[string]bool{}
	for _, c := range flags {
		switch c {
		case 'S':
			kw["$seen"] = true
		case 'F':
			kw["$flagged"] = true
		case 'R':
			kw["$answered"] = true
		case 'D':
			kw["$draft"] = true
		}
	}
	return kw
}

// maildirSubfolderRole maps cpanel/Hestia Maildir+ subfolder names
// to their JMAP "role" attribute so Stalwart's clients render the
// right icon + behavior. Empty string for unrecognised names →
// Stalwart treats the mailbox as a plain user folder.
func maildirSubfolderRole(name string) string {
	switch strings.ToLower(name) {
	case "drafts":
		return "drafts"
	case "sent":
		return "sent"
	case "trash":
		return "trash"
	case "junk", "spam":
		return "junk"
	case "archive", "archives":
		return "archive"
	}
	return ""
}

// mailboxNode is an in-memory view of one JMAP mailbox, used to resolve a
// Maildir++ hierarchy path to ids without relying on Mailbox/query
// filtering by name/parentId. We fetch all mailboxes once, then match
// locally and append as levels are created.
type mailboxNode struct {
	id, name, role, parentID string
}

// loadMailboxNodes fetches every mailbox in the account (id, name, role,
// parentId) for in-memory hierarchy resolution.
func loadMailboxNodes(ctx context.Context, accountID string) ([]mailboxNode, error) {
	var resp struct {
		List []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Role     string `json:"role"`
			ParentID string `json:"parentId"`
		} `json:"list"`
	}
	args := map[string]any{
		"accountId":  accountID,
		"ids":        nil,
		"properties": []string{"id", "name", "role", "parentId"},
	}
	// JAB-29: Mailbox/get is a mail method — Stalwart rejects it (unknownMethod)
	// unless urn:ietf:params:jmap:mail is in the request's `using` set, which the
	// base jmapCall list omits. Advertise it here like the Email/* calls do.
	if err := jmapCallWith(ctx, "urn:ietf:params:jmap:mail", "Mailbox/get", args, &resp); err != nil {
		return nil, err
	}
	nodes := make([]mailboxNode, 0, len(resp.List))
	for _, m := range resp.List {
		nodes = append(nodes, mailboxNode{id: m.ID, name: m.Name, role: m.Role, parentID: m.ParentID})
	}
	return nodes, nil
}

// ensureMailboxPath ensures every level of a Maildir++ segment path
// (e.g. ["Archive","2026","Q1"]) exists under the account, creating
// missing levels with parentId set, and returns the leaf mailbox id.
// Resolution is in-memory against `nodes` (updated as levels are
// created), so it never depends on server-side name/parentId filtering.
// Only the top-level segment may map to a JMAP role (Sent/Drafts/...).
func ensureMailboxPath(ctx context.Context, accountID string, segments []string, nodes *[]mailboxNode) (string, error) {
	parentID := "" // "" == account root
	var leafID string
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		role := ""
		if i == 0 {
			role = maildirSubfolderRole(seg)
		}
		var found string
		for _, n := range *nodes {
			if n.parentID != parentID {
				continue
			}
			if strings.EqualFold(n.name, seg) || (role != "" && n.role == role) {
				found = n.id
				break
			}
		}
		if found == "" {
			id, err := createMailboxUnder(ctx, accountID, seg, role, parentID)
			if err != nil {
				return "", err
			}
			found = id
			*nodes = append(*nodes, mailboxNode{id: id, name: seg, role: role, parentID: parentID})
		}
		parentID = found
		leafID = found
	}
	if leafID == "" {
		return "", fmt.Errorf("empty mailbox path")
	}
	return leafID, nil
}

// createMailboxUnder creates a JMAP mailbox with an optional role and
// parentId (account root when parentID is "").
func createMailboxUnder(ctx context.Context, accountID, name, role, parentID string) (string, error) {
	const createID = "newmbox"
	body := map[string]any{"name": name}
	if role != "" {
		body["role"] = role
	}
	if parentID != "" {
		body["parentId"] = parentID
	}
	args := map[string]any{
		"accountId": accountID,
		"create":    map[string]any{createID: body},
	}
	var result jmapSetResult
	// Mailbox/set (like Mailbox/get/query + Email/*) is a mail-capability method:
	// Stalwart rejects it with "requires capability urn:ietf:params:jmap:mail"
	// unless it's in the request's `using` set. Surfaced in the CyberPanel mail
	// E2E creating the Junk/Deleted auto-folders (JAB-29 family).
	if err := jmapCallWith(ctx, "urn:ietf:params:jmap:mail", "Mailbox/set", args, &result); err != nil {
		return "", fmt.Errorf("Mailbox/set create: %w", err)
	}
	if reason, ok := result.NotCreated[createID]; ok {
		return "", fmt.Errorf("Mailbox/set notCreated: %s", string(reason))
	}
	raw, ok := result.Created[createID]
	if !ok {
		return "", fmt.Errorf("Mailbox/set: no created entry for %q", createID)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("Mailbox/set decode: %w", err)
	}
	return created.ID, nil
}

// mailboxIDByRole resolves the mailbox ID with the given role
// (e.g. "inbox") in the named account. Stalwart auto-creates an
// INBOX on account.create; we expect role=inbox to always return
// exactly one row.
func mailboxIDByRole(ctx context.Context, accountID, role string) (string, error) {
	var resp struct {
		IDs []string `json:"ids"`
	}
	// JAB-29: Mailbox/query requires urn:ietf:params:jmap:mail in `using`
	// (mailbox message import failed at "resolve inbox" without it).
	if err := jmapCallWith(ctx, "urn:ietf:params:jmap:mail", "Mailbox/query", map[string]any{
		"accountId": accountID,
		"filter":    map[string]any{"role": role},
		"limit":     1,
	}, &resp); err != nil {
		return "", err
	}
	if len(resp.IDs) == 0 {
		return "", fmt.Errorf("no mailbox with role=%q in account %s", role, accountID)
	}
	return resp.IDs[0], nil
}

// importOneMessage = blob upload + Email/import in two HTTP round-
// trips. Returns the bytes uploaded.
func importOneMessage(ctx context.Context, accountID, mailboxID, path string, size int64, keywords map[string]bool, receivedAt time.Time, allowedRoots []string) (int64, error) {
	blobID, err := uploadBlob(ctx, accountID, path, allowedRoots)
	if err != nil {
		return 0, fmt.Errorf("blob/upload: %w", err)
	}
	if keywords == nil {
		keywords = map[string]bool{}
	}
	receivedAtStr := receivedAt.UTC().Format(time.RFC3339)
	args := map[string]any{
		"accountId": accountID,
		"emails": map[string]any{
			"m0": map[string]any{
				"blobId":     blobID,
				"mailboxIds": map[string]bool{mailboxID: true},
				"keywords":   keywords,
				"receivedAt": receivedAtStr,
			},
		},
	}
	var resp struct {
		Created    map[string]json.RawMessage `json:"created"`
		NotCreated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description,omitempty"`
		} `json:"notCreated"`
	}
	if err := jmapCallWith(ctx, "urn:ietf:params:jmap:mail", "Email/import", args, &resp); err != nil {
		return size, err
	}
	if nc, ok := resp.NotCreated["m0"]; ok {
		// Common notCreated reasons: alreadyExists (Stalwart
		// dedup on Message-ID), tooLarge, invalidMailbox.
		// alreadyExists is non-fatal; treat as success.
		if nc.Type == "alreadyExists" {
			return size, nil
		}
		return size, fmt.Errorf("Email/import notCreated: %s: %s", nc.Type, nc.Description)
	}
	return size, nil
}

// uploadBlob streams the file at `path` to Stalwart's /jmap/upload/<accountId>
// endpoint. Returns the produced blobId.
func uploadBlob(ctx context.Context, accountID, path string, allowedRoots []string) (string, error) {
	f, err := openMaildirFileInStaging(path, allowedRoots)
	if err != nil {
		return "", err
	}
	defer f.Close()

	url := stalwartAdminURLFunc() + "/jmap/upload/" + accountID
	subctx, cancel := context.WithTimeout(ctx, migrationMailboxJMAPCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(subctx, http.MethodPost, url, f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "message/rfc822")
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(jmapAdminUser, token)

	resp, err := stalwartHTTPClientFunc().Do(req)
	if err != nil {
		return "", fmt.Errorf("post upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		BlobID string `json:"blobId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	if out.BlobID == "" {
		return "", errors.New("upload returned empty blobId")
	}
	return out.BlobID, nil
}

// jmapCallWith is jmapCall with an extra capability URN appended to
// the `using` array — Email/import requires
// urn:ietf:params:jmap:mail beyond the base set jmapCall sends.
// jmapUsingWith returns the base `using` set plus one extra capability URN.
// Kept as a seam so a regression test can assert mail methods advertise
// urn:ietf:params:jmap:mail (JAB-29).
func jmapUsingWith(extraCap string) []string {
	return append(append([]string{}, jmapUsing...), extraCap)
}

func jmapCallWith(ctx context.Context, extraCap, method string, args any, out any) error {
	body := jmapRequestBody{
		Using: jmapUsingWith(extraCap),
		MethodCalls: []jmapMethodCall{
			{Name: method, Args: args, CallID: "c0"},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal jmap request: %w", err)
	}
	url := stalwartAdminURLFunc() + jmapAPIPath
	subctx, cancel := context.WithTimeout(ctx, migrationMailboxJMAPCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(subctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(jmapAdminUser, token)

	resp, err := stalwartHTTPClientFunc().Do(req)
	if err != nil {
		return fmt.Errorf("jmap call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jmap HTTP %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var parsed jmapResponseBody
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("unparseable response: %w", err)
	}
	if len(parsed.MethodResponses) != 1 {
		return fmt.Errorf("jmap returned %d method responses, want 1", len(parsed.MethodResponses))
	}
	mr := parsed.MethodResponses[0]
	rawArgs, ok := mr.Args.(json.RawMessage)
	if !ok {
		// jmapMethodCall.Args is decoded as json.RawMessage by
		// the UnmarshalJSON in the jmap client (mailbox_jmap.go).
		// On the rare path where the type slipped, marshal the
		// `any` back to JSON so the rest of this function can
		// branch on the contents.
		b, mErr := json.Marshal(mr.Args)
		if mErr != nil {
			return fmt.Errorf("jmap response args not RawMessage and remarshal failed: %w", mErr)
		}
		rawArgs = b
	}
	if mr.Name == "error" {
		return fmt.Errorf("jmap error: %s", string(rawArgs))
	}
	if out != nil {
		return json.Unmarshal(rawArgs, out)
	}
	return nil
}

// existingMessageIDs returns the set of normalized Message-IDs already
// present in the mailbox, so a re-import can skip them (Stalwart does
// not dedup Email/import itself). Best-effort: a query error yields an
// empty set (import proceeds, may duplicate — never drops).
func existingMessageIDs(ctx context.Context, accountID, mailboxID string) map[string]bool {
	set := map[string]bool{}
	position := 0
	for {
		var q struct {
			IDs []string `json:"ids"`
		}
		qa := map[string]any{
			"accountId": accountID,
			"filter":    map[string]any{"inMailbox": mailboxID},
			"position":  position,
			"limit":     500,
		}
		if err := jmapCallWith(ctx, "urn:ietf:params:jmap:mail", "Email/query", qa, &q); err != nil {
			return set
		}
		if len(q.IDs) == 0 {
			return set
		}
		var g struct {
			List []struct {
				MessageID []string `json:"messageId"`
			} `json:"list"`
		}
		ga := map[string]any{
			"accountId":  accountID,
			"ids":        q.IDs,
			"properties": []string{"messageId"},
		}
		if err := jmapCallWith(ctx, "urn:ietf:params:jmap:mail", "Email/get", ga, &g); err != nil {
			return set
		}
		for _, m := range g.List {
			for _, mid := range m.MessageID {
				set[normMsgID(mid)] = true
			}
		}
		if len(q.IDs) < 500 {
			return set
		}
		position += 500
	}
}

// messageIDFromFile reads the Message-ID header from an RFC822 file.
// Returns "" when absent (such messages can't be deduped — they import).
func messageIDFromFile(path string, allowedRoots []string) string {
	f, err := openMaildirFileInStaging(path, allowedRoots)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break // end of header block
		}
		if len(line) >= 11 && strings.EqualFold(line[:11], "message-id:") {
			return normMsgID(line[11:])
		}
	}
	return ""
}

// normMsgID strips angle brackets + whitespace and lowercases, matching
// the bracket-stripped form Stalwart returns in the messageId property.
func normMsgID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.ToLower(strings.TrimSpace(s))
}

// openMaildirFileInStaging opens a staged Maildir message escape-proof (openat2
// RESOLVE_BENEATH under the migration staging root), so a symlinked message
// file or a symlinked cur/new directory in a malicious source cannot redirect
// the root agent into reading a host file and uploading it into a mailbox
// (Gitea #477). ReadDir/Stat above may follow a symlink, but this open is the
// gate: a symlinked component is refused here.
func openMaildirFileInStaging(path string, allowedRoots []string) (*os.File, error) {
	scope, err := filesafe.NewScope("migration", "migration", allowedRoots)
	if err != nil {
		return nil, err
	}
	return scope.OpenInScope(path, os.O_RDONLY, 0)
}
