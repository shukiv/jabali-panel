package models

import "testing"

func TestNormalizeBackupKindsCSV(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"local", "local", false},
		{" LOCAL , s3 ", "local,s3", false},
		{"local,local,s3", "local,s3", false}, // dedup
		{"local,bogus", "", true},             // unknown kind
		{"ftp", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeBackupKindsCSV(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("NormalizeBackupKindsCSV(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("NormalizeBackupKindsCSV(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostingPackageBackupHelpers(t *testing.T) {
	off := HostingPackage{MaxBackups: 0, AllowedBackupDestinationKinds: "local,s3"}
	if off.BackupsEnabled() {
		t.Error("MaxBackups=0 must report BackupsEnabled=false")
	}
	if off.AllowsBackupKind("local") {
		t.Error("backups disabled must reject every kind, even allowed ones")
	}

	on := HostingPackage{MaxBackups: 5, AllowedBackupDestinationKinds: "local, S3"}
	if !on.BackupsEnabled() {
		t.Error("MaxBackups>0 must report BackupsEnabled=true")
	}
	if got := on.AllowedBackupKinds(); len(got) != 2 || got[0] != "local" || got[1] != "s3" {
		t.Errorf("AllowedBackupKinds = %v, want [local s3]", got)
	}
	if !on.AllowsBackupKind("s3") || !on.AllowsBackupKind("LOCAL") {
		t.Error("AllowsBackupKind must match allowed kinds case-insensitively")
	}
	if on.AllowsBackupKind("b2") {
		t.Error("AllowsBackupKind must reject a kind not in the allow-list")
	}
}
