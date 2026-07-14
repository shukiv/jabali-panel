package imap

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	// maxMessageBytes caps a single fetched message. Mirrors the agent's
	// migrationMailboxMessageCap so we don't stage a message the importer
	// will reject as oversized.
	maxMessageBytes = 512 * 1024 * 1024 // 512 MiB
	fetchTimeout    = 6 * time.Hour
)

// FolderFetch is the per-folder outcome of a fetch.
type FolderFetch struct {
	Remote   string `json:"remote"`  // remote IMAP folder name
	Maildir  string `json:"maildir"` // staging subdir written ("" = INBOX root)
	Messages int64  `json:"messages"`
	Bytes    int64  `json:"bytes"`
}

// FetchResult aggregates a fetch into a staging Maildir.
type FetchResult struct {
	Folders       []FolderFetch `json:"folders"`
	TotalMessages int64         `json:"total_messages"`
	TotalBytes    int64         `json:"total_bytes"`
	// Skipped records folders intentionally not fetched (e.g. Gmail's
	// "All Mail", which is a superset of every other folder and would
	// duplicate the whole account) or that errored mid-fetch.
	Skipped []string `json:"skipped,omitempty"`
}

// Fetch connects (SSRF-guarded, TLS), logs in, and streams every
// selectable folder into a staging Maildir under stagingDir, laid out
// for the shared migration.import_mailboxes agent step: INBOX at the
// Maildir root ({cur,new,tmp}); other folders as Maildir++ ".Sub" dirs;
// \Seen messages in cur/, unseen in new/. If folders is nil it lists
// them itself. It is the network entrypoint; fetchWithClient holds the
// testable core.
func Fetch(ctx context.Context, cfg ProbeConfig, folders []Folder, stagingDir string) (*FetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	client, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return fetchWithClient(ctx, client, cfg.Username, cfg.Password, folders, stagingDir)
}

func fetchWithClient(ctx context.Context, c *imapclient.Client, username, password string, folders []Folder, stagingDir string) (*FetchResult, error) {
	if err := c.Login(username, password).Wait(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuth, err)
	}
	defer func() { _ = c.Logout().Wait() }()

	if folders == nil {
		var err error
		if folders, err = listFolders(c); err != nil {
			return nil, err
		}
	}

	res := &FetchResult{}
	for _, f := range folders {
		if !f.Selectable {
			continue
		}
		sub, skip := maildirSub(f)
		if skip {
			res.Skipped = append(res.Skipped, f.Name)
			continue
		}
		targetDir := stagingDir
		if sub != "" {
			targetDir = filepath.Join(stagingDir, "."+sub)
		}
		// Authoritative containment guard: after the join, the target must
		// still resolve inside stagingDir. Defends against any path-escape
		// that slipped past maildirSub's sanitisation.
		if rel, rerr := filepath.Rel(stagingDir, targetDir); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: unsafe folder name", f.Name))
			continue
		}
		n, b, err := selectAndWrite(ctx, c, f.Name, targetDir)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", f.Name, err))
			continue
		}
		res.Folders = append(res.Folders, FolderFetch{Remote: f.Name, Maildir: sub, Messages: n, Bytes: b})
		res.TotalMessages += n
		res.TotalBytes += b
	}
	return res, nil
}

// maildirSub maps a remote folder to its Maildir++ subfolder name. The
// INBOX lands at the Maildir root (""); SPECIAL-USE roles map to the
// canonical names the importer + Stalwart expect; ordinary folders keep
// their name with the server's hierarchy delimiter translated to the
// Maildir++ dot. Returns skip=true for folders we deliberately don't
// migrate (Gmail "All Mail" superset, or a name that normalises empty).
func maildirSub(f Folder) (sub string, skip bool) {
	switch f.Role {
	case "inbox":
		return "", false
	case "sent":
		return "Sent", false
	case "drafts":
		return "Drafts", false
	case "junk":
		return "Junk", false
	case "trash":
		return "Trash", false
	case "archive":
		return "Archive", false
	case "all":
		// Gmail's All Mail contains a copy of every message in every
		// other folder — migrating it duplicates the whole account.
		return "", true
	}
	raw := f.Name
	if f.Delim != 0 {
		raw = strings.ReplaceAll(raw, string(f.Delim), ".")
	}
	// f.Name is the remote server's LIST output — untrusted. Neutralise
	// every OS path separator + NUL so the name can't escape the staging
	// dir when it becomes a Maildir++ ".<sub>" segment (a malicious or
	// buggy source server on a non-"/" delimiter could otherwise return
	// e.g. "../../etc/x"). Then reject residual traversal / empties.
	// fetchWithClient also verifies containment after the join.
	raw = strings.ReplaceAll(raw, "/", ".")
	raw = strings.ReplaceAll(raw, "\\", ".")
	raw = strings.ReplaceAll(raw, "\x00", "")
	raw = strings.Trim(raw, ".")
	if raw == "" || strings.Contains(raw, "..") {
		return "", true
	}
	return raw, false
}

// selectAndWrite SELECTs one remote folder (read-only) and streams every
// message into the Maildir at targetDir.
func selectAndWrite(ctx context.Context, c *imapclient.Client, remoteName, targetDir string) (int64, int64, error) {
	for _, d := range []string{"tmp", "new", "cur"} {
		if err := os.MkdirAll(filepath.Join(targetDir, d), 0o700); err != nil {
			return 0, 0, fmt.Errorf("mkdir maildir: %w", err)
		}
	}

	sel, err := c.Select(remoteName, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, 0, fmt.Errorf("select: %w", err)
	}
	if sel.NumMessages == 0 {
		return 0, 0, nil
	}

	seqSet := imap.SeqSet{}
	seqSet.AddRange(1, sel.NumMessages)
	fetchCmd := c.Fetch(seqSet, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		// Peek so fetching the body does NOT set \Seen on the source —
		// a migration must never mutate the remote account's flags.
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	})
	defer fetchCmd.Close()

	var count, bytes int64
	for {
		if err := ctx.Err(); err != nil {
			return count, bytes, err
		}
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}

		var uid imap.UID
		var seen bool
		var idate time.Time
		var body []byte
		var oversized bool

		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch it := item.(type) {
			case imapclient.FetchItemDataUID:
				uid = it.UID
			case imapclient.FetchItemDataFlags:
				for _, fl := range it.Flags {
					if fl == imap.FlagSeen {
						seen = true
					}
				}
			case imapclient.FetchItemDataInternalDate:
				idate = it.Time
			case imapclient.FetchItemDataBodySection:
				// The literal MUST be consumed before advancing the
				// stream. LimitReader + 1 lets us detect oversize.
				if it.Literal != nil {
					b, rerr := io.ReadAll(io.LimitReader(it.Literal, maxMessageBytes+1))
					if rerr != nil {
						return count, bytes, fmt.Errorf("read message body: %w", rerr)
					}
					if int64(len(b)) > maxMessageBytes {
						oversized = true
					} else {
						body = b
					}
				}
			}
		}

		if oversized || len(body) == 0 {
			continue
		}
		if werr := writeMaildirMessage(targetDir, seen, idate, uid, body); werr != nil {
			return count, bytes, werr
		}
		count++
		bytes += int64(len(body))
	}
	if err := fetchCmd.Close(); err != nil {
		return count, bytes, fmt.Errorf("fetch: %w", err)
	}
	return count, bytes, nil
}

// writeMaildirMessage writes one message into <targetDir>/cur (\Seen) or
// <targetDir>/new (unseen). The UID makes the filename unique within the
// folder; the :2,S info suffix marks Seen (Maildir convention), though
// the importer keys Seen on the cur/ vs new/ slot.
func writeMaildirMessage(targetDir string, seen bool, idate time.Time, uid imap.UID, body []byte) error {
	slot := "new"
	name := fmt.Sprintf("%d.U%d.jabali", idate.Unix(), uint32(uid))
	if seen {
		slot = "cur"
		name += ":2,S"
	}
	path := filepath.Join(targetDir, slot, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if !idate.IsZero() {
		_ = os.Chtimes(path, idate, idate)
	}
	return nil
}
