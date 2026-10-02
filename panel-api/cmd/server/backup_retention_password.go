package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// resticRepo is one destination plus the password file that opens its
// repository, as resolved by destPasswords for this run.
type resticRepo struct {
	*models.BackupDestination
	PasswordFile string
}

// args is the global-flag prefix every retention restic call needs: the repo
// URL, the password file, and the destination's -o options. An empty URL is
// the agent's default local repository, as for a backup: the "Local"
// destination the default local backup schedule creates (GH #1240) has no URL.
func (r resticRepo) args() []string {
	repo := r.URL
	if repo == "" {
		repo = internalbackup.DefaultRepo
	}
	args := []string{"--repo", repo, "--password-file", r.PasswordFile}
	for _, opt := range backupwrapperhelpers.ResticOptionsFor(r.BackupDestination) {
		if opt == "" {
			continue
		}
		args = append(args, "-o", opt)
	}
	return args
}

// retentionSSOKey loads the key that unseals a destination's password_enc.
// It does not log: the CLI logger writes to stdout, which --json output owns.
// A var so tests can supply their own key.
var retentionSSOKey = func() (*ssokey.Key, error) {
	if sharedCfg == nil || sharedCfg.SSO.KeyPath == "" {
		return nil, errors.New("sso.key_path is not configured")
	}
	k, err := ssokey.Load(sharedCfg.SSO.KeyPath)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// destPasswords resolves, once per CLI run, the password file that opens each
// destination's repository.
//
// A destination whose password was rotated (Admin → Backups → Destinations →
// Rotate password) is opened only by its own restic key: the rotation adds
// the new key and removes the old one, and stores the new password sealed in
// password_enc. The shared /etc/jabali-panel/restic-repo.password no longer
// opens that repository, and once every destination is rotated the
// reconciler deletes the file. So a rotated destination's password is
// unsealed and written to a 0600 file in a 0700 temp directory that close
// removes. It never goes into argv or the environment.
type destPasswords struct {
	key    *ssokey.Key
	keyErr error
	loaded bool
	dir    string
	files  map[string]string // destination ID -> password file
}

func newDestPasswords() *destPasswords {
	return &destPasswords{files: map[string]string{}}
}

func (p *destPasswords) repo(d *models.BackupDestination) (resticRepo, error) {
	if len(d.PasswordEnc) == 0 {
		if err := checkSharedResticPasswordFile(); err != nil {
			return resticRepo{}, err
		}
		return resticRepo{d, resticPasswordFile}, nil
	}
	if f, ok := p.files[d.ID]; ok {
		return resticRepo{d, f}, nil
	}
	if !p.loaded {
		p.key, p.keyErr = retentionSSOKey()
		p.loaded = true
	}
	if p.keyErr != nil {
		return resticRepo{}, fmt.Errorf("destination %s (%s) has a rotated repository password, but the SSO key that unseals it could not be loaded: %w", d.ID, d.Name, p.keyErr)
	}
	plain, err := p.key.Open(d.PasswordEnc)
	if err != nil {
		return resticRepo{}, fmt.Errorf("unseal the repository password of destination %s (%s): %w", d.ID, d.Name, err)
	}
	defer clear(plain)
	if p.dir == "" {
		dir, err := os.MkdirTemp("", "jabali-restic-pw-")
		if err != nil {
			return resticRepo{}, fmt.Errorf("create password temp dir: %w", err)
		}
		p.dir = dir
	}
	f := filepath.Join(p.dir, strconv.Itoa(len(p.files)))
	if err := os.WriteFile(f, plain, 0o600); err != nil {
		return resticRepo{}, fmt.Errorf("write password file for destination %s (%s): %w", d.ID, d.Name, err)
	}
	p.files[d.ID] = f
	return resticRepo{d, f}, nil
}

// close removes every password file this run wrote.
func (p *destPasswords) close() {
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// checkSharedResticPasswordFile verifies the shared password file that opens
// every destination not yet rotated.
func checkSharedResticPasswordFile() error {
	fi, err := os.Stat(resticPasswordFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", resticPasswordFile, err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("%s is empty (regenerate via install_backup_foundation)", resticPasswordFile)
	}
	return nil
}
