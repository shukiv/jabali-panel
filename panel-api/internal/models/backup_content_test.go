package models

import (
	"reflect"
	"testing"
)

func TestValidBackupContent(t *testing.T) {
	for _, c := range []string{"", "full", "files", "database", "folders"} {
		if !ValidBackupContent(c) {
			t.Errorf("ValidBackupContent(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"all", "db", "FULL", "home", "x"} {
		if ValidBackupContent(c) {
			t.Errorf("ValidBackupContent(%q) = true, want false", c)
		}
	}
}

func TestNormalizeBackupContent(t *testing.T) {
	if got := NormalizeBackupContent(""); got != BackupContentFull {
		t.Errorf("NormalizeBackupContent(\"\") = %q, want %q", got, BackupContentFull)
	}
	if got := NormalizeBackupContent("database"); got != "database" {
		t.Errorf("NormalizeBackupContent(\"database\") = %q, want database", got)
	}
}

// ApplyBackupContent must empty the lists the selected content excludes so the
// agent — which backs up whatever db/mailbox lists it is handed — doesn't
// capture data the tenant deselected (GH #454). This is the load-bearing wiring
// for scheduled content; a regression here silently over-backs-up.
func TestApplyBackupContent(t *testing.T) {
	dbs := []string{"d1", "d2"}
	mbs := []string{"m1"}
	pg := []string{"p1"}

	tests := []struct {
		content         string
		wantDB, wantMB  []string
		wantPG          []string
	}{
		{"full", dbs, mbs, pg},
		{"", dbs, mbs, pg}, // empty behaves as full at the switch default
		{"database", dbs, nil, pg},
		{"files", nil, nil, nil},
		{"folders", nil, nil, nil},
	}
	for _, tt := range tests {
		gotDB, gotMB, gotPG := ApplyBackupContent(tt.content, dbs, mbs, pg)
		if !reflect.DeepEqual(gotDB, tt.wantDB) || !reflect.DeepEqual(gotMB, tt.wantMB) || !reflect.DeepEqual(gotPG, tt.wantPG) {
			t.Errorf("ApplyBackupContent(%q) = (%v,%v,%v), want (%v,%v,%v)",
				tt.content, gotDB, gotMB, gotPG, tt.wantDB, tt.wantMB, tt.wantPG)
		}
	}
}
