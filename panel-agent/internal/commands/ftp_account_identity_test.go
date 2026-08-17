package commands

import "testing"

func TestFtpMarkerOwner(t *testing.T) {
	tests := []struct {
		gecos      string
		wantOwner  string
		wantMarked bool
	}{
		{"jabali-ftp-account", "", true},                  // bare legacy marker
		{"jabali-ftp-account,,,", "", true},               // bare + GECOS comma extras
		{"jabali-ftp-account=shop", "shop", true},         // owner-encoded
		{"jabali-ftp-account=shop,,,", "shop", true},      // owner-encoded + extras
		{"jabali-ftp-account=user_01k", "user_01k", true}, // owner with underscore
		{"jabali-ftp-account=", "", false},                // empty owner = not a valid marker
		{"ops backup alias", "", false},                   // operator alias
		{"", "", false},
	}
	for _, tc := range tests {
		owner, marked := ftpMarkerOwner(tc.gecos)
		if owner != tc.wantOwner || marked != tc.wantMarked {
			t.Errorf("ftpMarkerOwner(%q) = (%q,%v), want (%q,%v)", tc.gecos, owner, marked, tc.wantOwner, tc.wantMarked)
		}
	}
}

func TestFtpAliasOwnedBy(t *testing.T) {
	shop := &ftpTenant{Username: "shop", UID: 1002}
	shopOther := &ftpTenant{Username: "shop_other", UID: 1004}

	tests := []struct {
		name     string
		gecos    string
		aliasUID int
		tenant   *ftpTenant
		want     bool
	}{
		// Owner-encoded: matched by owner, independent of uid.
		{"isolated of shop", "jabali-ftp-account=shop", 1000000002, shop, true},
		{"isolated of shop not owned by shop_other", "jabali-ftp-account=shop", 1000000002, shopOther, false},
		// Prefix collision: shop_other_deploy is owned by shop_other, NEVER shop.
		{"collision: owned by shop_other", "jabali-ftp-account=shop_other", 1000000003, shopOther, true},
		{"collision: NOT owned by shop", "jabali-ftp-account=shop_other", 1000000003, shop, false},
		// Bare legacy marker: matched only by shared uid.
		{"legacy same-uid of shop", "jabali-ftp-account", 1002, shop, true},
		{"legacy wrong uid", "jabali-ftp-account", 1004, shop, false},
		// No marker: never owned.
		{"unmarked", "ops alias", 1002, shop, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ftpAliasOwnedBy(tc.gecos, tc.aliasUID, tc.tenant); got != tc.want {
				t.Errorf("ftpAliasOwnedBy(%q,%d,%q) = %v, want %v", tc.gecos, tc.aliasUID, tc.tenant.Username, got, tc.want)
			}
		})
	}
}

// TestClassifyFtpAliasesIsolated proves the reaper attributes owner-encoded
// (isolated, separate-uid) aliases by their GECOS owner — including the
// prefix-collision case the same-uid model could not have disambiguated.
func TestClassifyFtpAliasesIsolated(t *testing.T) {
	passwd := "" +
		"shop:x:1002:1002::/home/shop:/bin/bash\n" +
		"shop_other:x:1004:1004::/home/shop_other:/bin/bash\n" +
		// isolated alias of shop: own uid, owner-encoded GECOS, jail home
		"shop_printer:x:1000000002:1000000002:jabali-ftp-account=shop:/var/lib/jabali-ftp-jails/shop/shop_printer:/usr/sbin/nologin\n" +
		// isolated alias of shop_other: prefix "shop_" collides with tenant shop,
		// but the owner marker attributes it to shop_other, NOT shop.
		"shop_other_deploy:x:1000000003:1000000003:jabali-ftp-account=shop_other:/var/lib/jabali-ftp-jails/shop_other/shop_other_deploy:/usr/sbin/nologin\n" +
		// a legacy bare-marker same-uid alias still works alongside isolated ones
		"shop_legacy:x:1002:1002:jabali-ftp-account:/home/shop/legacy:/usr/sbin/nologin\n"

	owned, skipped := classifyFtpAliases(passwd)
	got := map[string]string{}
	for _, o := range owned {
		got[o.Username] = o.TenantUsername
	}
	if got["shop_printer"] != "shop" {
		t.Errorf("shop_printer owner = %q, want shop", got["shop_printer"])
	}
	if got["shop_other_deploy"] != "shop_other" {
		t.Errorf("shop_other_deploy owner = %q, want shop_other (prefix-collision must not attribute to shop)", got["shop_other_deploy"])
	}
	if got["shop_legacy"] != "shop" {
		t.Errorf("shop_legacy owner = %q, want shop", got["shop_legacy"])
	}
	if len(owned) != 3 {
		t.Errorf("expected 3 owned, got %d: %v", len(owned), got)
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped-unmarked, got %d", skipped)
	}
}

// TestFtpOwnedAliasNamesLockPath pins the JAB-254 suspension lock path: an
// isolated (separate-uid) alias must be enumerated for locking, a prefix-
// colliding OTHER tenant's isolated alias must NOT, and a legacy same-uid alias
// still is.
func TestFtpOwnedAliasNamesLockPath(t *testing.T) {
	passwd := "" +
		"shop:x:1002:1002::/home/shop:/bin/bash\n" +
		"shop_legacy:x:1002:1002:jabali-ftp-account:/home/shop/a:/usr/sbin/nologin\n" +
		"shop_printer:x:1000000002:1000000002:jabali-ftp-account=shop:/var/lib/jabali-ftp-jails/shop/shop_printer:/usr/sbin/nologin\n" +
		// prefix "shop_" collides but this is shop_other's isolated alias:
		"shop_other_deploy:x:1000000003:1000000003:jabali-ftp-account=shop_other:/var/lib/jabali-ftp-jails/shop_other/shop_other_deploy:/usr/sbin/nologin\n"

	names := ftpOwnedAliasNames(passwd, &ftpTenant{Username: "shop", UID: 1002})
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["shop_legacy"] || !got["shop_printer"] {
		t.Fatalf("shop's own aliases (legacy + isolated) must be locked; got %v", names)
	}
	if got["shop_other_deploy"] {
		t.Fatalf("shop_other's isolated alias must NOT be in shop's lock set (cross-tenant lock); got %v", names)
	}
	if len(names) != 2 {
		t.Fatalf("expected exactly shop_legacy + shop_printer, got %v", names)
	}
}

// TestClassifyFtpAliasesForgedPrefix: an owner-encoded marker whose username is
// not namespaced under the encoded owner is dropped (not attributed), guarding
// against a stamping bug / forgery attributing an alias to the wrong tenant.
func TestClassifyFtpAliasesForgedPrefix(t *testing.T) {
	passwd := "" +
		"shop:x:1002:1002::/home/shop:/bin/bash\n" +
		// username "evil_deploy" but GECOS claims owner "shop" — prefix mismatch
		"evil_deploy:x:1000000009:1000000009:jabali-ftp-account=shop:/var/lib/jabali-ftp-jails/shop/evil:/usr/sbin/nologin\n"
	owned, _ := classifyFtpAliases(passwd)
	for _, o := range owned {
		if o.Username == "evil_deploy" {
			t.Fatalf("mis-prefixed owner-encoded alias must NOT be attributed; got owner %q", o.TenantUsername)
		}
	}
}

// TestFtpAliasOwnedBySystemUID: an owner-encoded marker on a SYSTEM uid must
// never be claimed as a subaccount (root-created mistake / forgery guard).
func TestFtpAliasOwnedBySystemUID(t *testing.T) {
	shop := &ftpTenant{Username: "shop", UID: 1002}
	if ftpAliasOwnedBy("jabali-ftp-account=shop", 0, shop) {
		t.Fatal("owner marker on uid 0 must NOT be owned")
	}
	if ftpAliasOwnedBy("jabali-ftp-account=shop", 1, shop) {
		t.Fatal("owner marker on a system uid must NOT be owned")
	}
	// valid: tenant uid + isolated uid
	if !ftpAliasOwnedBy("jabali-ftp-account=shop", 1002, shop) {
		t.Fatal("owner marker on the tenant uid must be owned")
	}
	if !ftpAliasOwnedBy("jabali-ftp-account=shop", 1000000002, shop) {
		t.Fatal("owner marker on an isolated uid must be owned")
	}
}
