package cpanel

// mail_shadow.go — read cPanel's per-domain mail password files.
//
// cPanel keeps each mailbox's password hash in <home>/etc/<domain>/shadow,
// one `local:hash:…` line per mailbox. A full cpmove carries the file under
// homedir/etc; an SSH pull stages it with the agent's migration.pull_mail_shadow
// (parsed.MailShadowDir). Either way the tree is source-controlled content, so
// the file is opened with openat2 RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS below a
// base opened O_NOFOLLOW: a symlinked domain dir or shadow file is refused, not
// followed (a `shadow -> /etc/shadow` link would otherwise put this host's
// root hash on a mailbox). Hashes are never logged; summaries carry counts.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
)

// mailShadowMaxBytes caps one domain's shadow file.
const mailShadowMaxBytes = 1 << 20

// lockoutCause says why a migrated mailbox got a fresh password instead of
// its source one. The zero value is "no entry".
type lockoutCause int

const (
	lockoutNoSourceEntry      lockoutCause = iota // the source has no password entry for the mailbox
	lockoutPreserveOff                            // the operator did not ask to keep source passwords
	lockoutUnverifiableScheme                     // an entry exists, in a scheme Stalwart cannot verify
	lockoutLockedOnSource                         // the entry is locked/suspended on the source (! or *)
	lockoutSourceUnreadable                       // the domain's password file was not readable by the account
	lockoutCopyFailed                             // an SSH pull did not copy the password files
)

// sourceMailCred is what the source holds for one mailbox's password: a
// Stalwart-verifiable hash, or why there is none.
type sourceMailCred struct {
	hash  string
	cause lockoutCause // meaningful only when hash == ""
}

// mailShadowSource locates the cPanel <domain>/shadow files: base is a
// trusted directory (opened O_NOFOLLOW) and relDir the path below it to the
// directory holding <domain>/shadow. A zero value means there are none.
type mailShadowSource struct {
	base, relDir string
	// unreadable lists domains whose shadow the pull could not read on the
	// source (the account had no access).
	unreadable map[string]bool
	// notCopied: an SSH pull should have staged the files and did not.
	notCopied bool
}

// mailShadowSourceFor picks the shadow files for a restore: the staged copy
// of an SSH pull, else homedir/etc inside an offline cpmove extract. A
// homedir outside the extract dir is not read.
func mailShadowSourceFor(parsed *ParsedTarball) mailShadowSource {
	if parsed == nil {
		return mailShadowSource{}
	}
	unreadable := map[string]bool{}
	for _, d := range parsed.MailShadowUnreadable {
		unreadable[domainops.NormalizeDomainName(d)] = true
	}
	if parsed.MailShadowDir != "" {
		return mailShadowSource{base: parsed.MailShadowDir, relDir: ".", unreadable: unreadable}
	}
	// An SSH pull ships no homedir, so there is nothing to fall back to.
	if parsed.MailShadowNotCopied {
		return mailShadowSource{unreadable: unreadable, notCopied: true}
	}
	if parsed.ExtractDir == "" || parsed.HomeDir == "" {
		return mailShadowSource{unreadable: unreadable}
	}
	rel, err := filepath.Rel(parsed.ExtractDir, parsed.HomeDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return mailShadowSource{unreadable: unreadable}
	}
	return mailShadowSource{base: parsed.ExtractDir, relDir: filepath.Join(rel, "etc"), unreadable: unreadable}
}

// cpanelMailShadow is one domain's shadow file, parsed.
type cpanelMailShadow struct {
	hashes       map[string]string // local part → Stalwart-verifiable hash
	unverifiable map[string]bool   // entry present in a scheme Stalwart cannot verify
	locked       map[string]bool   // entry present, locked/suspended on the source
	present      bool              // the file was found and read
	refused      string            // why the file was not read ("" if read or absent)
}

// read loads <domain>/shadow from the source, or reports why it could not.
// A missing file is not an error (the domain simply has no entries).
func (s mailShadowSource) read(domain string) cpanelMailShadow {
	out := cpanelMailShadow{hashes: map[string]string{}, unverifiable: map[string]bool{}, locked: map[string]bool{}}
	if s.base == "" {
		return out
	}
	domain = domainops.NormalizeDomainName(domain)
	if err := domainops.ValidateDomainName(domain); err != nil {
		out.refused = fmt.Sprintf("%q is not a valid domain name — its password file was not read", domain)
		return out
	}
	data, err := openShadowBeneath(s.base, filepath.Join(s.relDir, domain, "shadow"))
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return out
		}
		out.refused = fmt.Sprintf("%s: password file not read: %v", domain, err)
		return out
	}
	out.present = true
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 || parts[0] == "" {
			continue
		}
		local, raw := parts[0], parts[1]
		if canon, _, cErr := mailaddr.Canonicalise(local + "@" + domain); cErr != nil || canon != strings.ToLower(local) {
			continue
		}
		local = strings.ToLower(local)
		switch {
		case strings.HasPrefix(raw, "!") || strings.HasPrefix(raw, "*"):
			out.locked[local] = true
		case normalizeSourceMailHash(raw) != "":
			out.hashes[local] = normalizeSourceMailHash(raw)
		default:
			out.unverifiable[local] = true
		}
	}
	return out
}

// cred is the source credential for local@<this domain>.
func (m cpanelMailShadow) cred(local string, unreadable bool) sourceMailCred {
	local = strings.ToLower(local)
	switch {
	case m.hashes[local] != "":
		return sourceMailCred{hash: m.hashes[local]}
	case m.locked[local]:
		return sourceMailCred{cause: lockoutLockedOnSource}
	case m.unverifiable[local]:
		return sourceMailCred{cause: lockoutUnverifiableScheme}
	case unreadable:
		return sourceMailCred{cause: lockoutSourceUnreadable}
	}
	return sourceMailCred{cause: lockoutNoSourceEntry}
}

// summary is the counts-only line for the run summary ("" when nothing was
// read and nothing refused).
func (m cpanelMailShadow) summary(domain string) string {
	if m.refused != "" {
		return "mail_passwords: " + m.refused
	}
	if !m.present {
		return ""
	}
	return fmt.Sprintf("mail_passwords: %s: %d source password hash(es) read, %d in a scheme the mail server cannot verify, %d locked on the source",
		domain, len(m.hashes), len(m.unverifiable), len(m.locked))
}

// openShadowBeneath reads base/rel: base is opened O_NOFOLLOW, and rel is
// resolved by openat2 strictly beneath it with no symlink in any component.
// Only a regular file within mailShadowMaxBytes is read.
func openShadowBeneath(base, rel string) ([]byte, error) {
	bfd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(bfd)
	fd, err := unix.Openat2(bfd, rel, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if fi.Size() > mailShadowMaxBytes {
		return nil, fmt.Errorf("larger than %d bytes", mailShadowMaxBytes)
	}
	return io.ReadAll(io.LimitReader(f, mailShadowMaxBytes+1))
}
