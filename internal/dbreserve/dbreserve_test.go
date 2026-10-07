package dbreserve

import "testing"

func TestDatabase(t *testing.T) {
	for name, want := range map[string]bool{
		"mysql": true, "MySQL": true, "sys": true, "crowdsec": true, "jabali": true,
		"jabali_panel": true, "JABALI_kratos": true, "template1": true,
		"alice_wp": false, "jabalidemo_wp": false, "sysadmin_db": false, "": false,
	} {
		if got := Database(name); got != want {
			t.Errorf("Database(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestUser(t *testing.T) {
	for name, want := range map[string]bool{
		"root": true, "ROOT": true, "mariadb.sys": true, "jabali": true, "jabali_pdns": true,
		"jabali-stalwart-ro": true, "jb_s_alice_wp": true, "debian-sys-maint": true,
		"alice_u": false, "rootkit_db": false, "jabalidemo_wp": false, "jb_alice": false, "": false,
	} {
		if got := User(name); got != want {
			t.Errorf("User(%q) = %v, want %v", name, got, want)
		}
	}
}
