package models

import "testing"

// TestSFTPPasswordWriteAllowed_GatesAuthAndKind pins the JAB-310 AC5 password
// gate, the single owner shared by the REST and CLI update paths: an sftp
// password (SSHPASS) is an independent credential write, but it must only land
// on an sftp destination whose effective auth is "password". Writing an SSHPASS
// to a key-auth (or non-sftp) destination is meaningless and must be rejected,
// not silently stored.
func TestSFTPPasswordWriteAllowed_GatesAuthAndKind(t *testing.T) {
	if err := SFTPPasswordWriteAllowed(BackupDestinationKindSFTP, SFTPAuthPassword); err != nil {
		t.Errorf("password write must be allowed for a password-auth sftp dest: %v", err)
	}
	if err := SFTPPasswordWriteAllowed(BackupDestinationKindSFTP, SFTPAuthKey); err == nil {
		t.Error("password write must be rejected for a key-auth destination")
	}
	if err := SFTPPasswordWriteAllowed(BackupDestinationKindSFTP, ""); err == nil {
		t.Error("password write must be rejected when auth is unset (defaults to key)")
	}
	if err := SFTPPasswordWriteAllowed(BackupDestinationKindLocal, SFTPAuthPassword); err == nil {
		t.Error("password write must be rejected for a non-sftp destination kind")
	}
}
