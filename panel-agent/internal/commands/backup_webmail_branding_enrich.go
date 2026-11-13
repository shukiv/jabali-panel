package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// webmailBrandingDir is where panel-api keeps each domain's webmail branding
// pictures (GH #2502), one directory per domain id; tests swap it.
var webmailBrandingDir = "/var/lib/jabali-panel/webmail-branding"

// webmailBrandingBackupCap bounds all of an account's pictures in one
// backup; tests swap it.
var webmailBrandingBackupCap = backup.MaxWebmailBrandingBackupBytes

// webmailBrandingDomainIDRe is a ULID, the only directory name under
// webmailBrandingDir.
var webmailBrandingDomainIDRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// enrichWebmailBranding fills in each domain's webmail branding pictures
// (GH #2502), so a restore on another server shows them. panel-api's row
// names each picture by the first 12 hex digits of its SHA-256
// (webmailbranding.Encode); a file is carried only when it is a regular file,
// not a link, and that hash matches. The backup then holds exactly the
// picture the row names, and the panel can't have the agent read anything
// else into a backup its owner can download. A picture that can't be carried
// is left out with a warning, and the restore shows the server-wide one.
func enrichWebmailBranding(meta *backup.AccountMetadata) (warnings []string) {
	if meta == nil {
		return nil
	}
	total := 0
	for i := range meta.Domains {
		d := &meta.Domains[i]
		b := d.WebmailBranding
		if b == nil {
			continue
		}
		for _, p := range []struct {
			asset, hash string
			data        *[]byte
		}{
			{"logo-light", b.LogoLight, &b.LogoLightPNG},
			{"logo-dark", b.LogoDark, &b.LogoDarkPNG},
			{"favicon", b.Favicon, &b.FaviconPNG},
		} {
			// Only what this function reads goes into the backup.
			*p.data = nil
			if p.hash == "" {
				continue
			}
			data, err := readWebmailBrandingPicture(d.ID, p.asset, p.hash)
			if err == nil && total+len(data) > webmailBrandingBackupCap {
				err = fmt.Errorf("the account's pictures are over %d bytes", webmailBrandingBackupCap)
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s %s left out: %v", d.Name, p.asset, err))
				continue
			}
			total += len(data)
			*p.data = data
		}
	}
	return warnings
}

// readWebmailBrandingPicture reads the domain's picture if its hash is hash.
func readWebmailBrandingPicture(domainID, asset, hash string) ([]byte, error) {
	if !webmailBrandingDomainIDRe.MatchString(domainID) {
		return nil, errors.New("not a domain id")
	}
	// From directory handles, never through a link: the panel's user owns
	// these directories.
	dfd, err := unix.Open(webmailBrandingDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dfd)
	ddfd, err := unix.Openat(dfd, domainID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(ddfd)
	// O_NONBLOCK: a FIFO in its place must not hang the backup.
	fd, err := unix.Openat(ddfd, asset+".png", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), asset+".png")
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, backup.MaxWebmailBrandingPictureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > backup.MaxWebmailBrandingPictureBytes {
		return nil, fmt.Errorf("larger than %d MiB", backup.MaxWebmailBrandingPictureBytes>>20)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:])[:12] != hash {
		return nil, errors.New("it is not the picture the branding names")
	}
	return data, nil
}
