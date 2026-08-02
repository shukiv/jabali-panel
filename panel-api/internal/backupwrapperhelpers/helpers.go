// Package backupwrapperhelpers translates a panel-api BackupDestination
// row into the inputs the internal/backup wrapper needs (URL, extra
// `-o key=value` flags, env file path). Lives here rather than inside
// internal/backup to keep that package free of GORM models.
package backupwrapperhelpers

import (
	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// SFTPWireParams returns the TYPED SFTP inputs to send to the agent, or nil
// for a destination that needs none.
//
// SECURITY (JAB-194): agent requests carry these fields, never a pre-built
// `-o sftp.command=...` string. That option is the command restic executes to
// reach an SFTP backend, so shipping it as an opaque string over the wire gave
// anything that could reach the agent arbitrary command execution as root —
// and it cannot be made safe by filtering, because the dangerous key is the one
// we legitimately use. The agent calls SFTPCommandFlag itself from these
// fields, so no wire value ever becomes an executed command.
//
// ResticOptionsFor below still exists for panel-LOCAL restic invocations, where
// the panel builds argv for a process it runs itself from its own database row.
func SFTPWireParams(d *models.BackupDestination) map[string]any {
	if d == nil || d.Kind != models.BackupDestinationKindSFTP {
		return nil
	}
	s := d.ExtraOptionsTyped().SFTP
	if s == nil {
		return nil
	}
	return map[string]any{
		"host":     s.Host,
		"user":     s.User,
		"port":     s.Port,
		"path":     s.Path,
		"auth":     s.Auth,
		"key_path": s.KeyPath,
	}
}

// ResticOptionsFor returns the `-o key=value` flag bodies (without the
// `-o` prefix; callers prepend) for one destination. Currently only
// SFTP needs overrides; other backends return an empty slice.
//
// For panel-LOCAL restic invocations only. Do NOT put the result in an agent
// request — see SFTPWireParams (JAB-194).
func ResticOptionsFor(d *models.BackupDestination) []string {
	if d == nil {
		return nil
	}
	opts := d.ExtraOptionsTyped()
	if d.Kind == models.BackupDestinationKindSFTP && opts.SFTP != nil {
		s := opts.SFTP
		flag := internalbackup.SFTPCommandFlag(internalbackup.SFTPInputs{
			Host:    s.Host,
			User:    s.User,
			Port:    s.Port,
			Path:    s.Path,
			Auth:    s.Auth,
			KeyPath: s.KeyPath,
		})
		if flag != "" {
			return []string{flag}
		}
	}
	return nil
}
