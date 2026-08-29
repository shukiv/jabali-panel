package commands

import (
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

func u32(v uint32) *uint32 { return &v }

func TestValidShadowHash(t *testing.T) {
	ok := []string{
		"$6$abc123$XyZ./def", "$y$j9T$salt$hash", "$2b$10$abcdefABCDEF0123456789./",
		"!", "*", "!!", "!$6$abc$def",
	}
	for _, h := range ok {
		if !validShadowHash(h) {
			t.Errorf("expected %q to be a valid shadow hash", h)
		}
	}
	bad := []string{
		"",                        // empty
		"hash:root:$6$x",          // ':' — second shadow line injection
		"hash\nroot:$6$x",         // newline injection
		"has space",               // whitespace
		"pass\tword",              // tab
	}
	for _, h := range bad {
		if validShadowHash(h) {
			t.Errorf("expected %q to be REJECTED", h)
		}
	}
}

func TestValidFtpUsername(t *testing.T) {
	if !validFtpUsername("shop_deploy") || !validFtpUsername("t1_x") {
		t.Error("legit subaccount usernames must pass")
	}
	for _, bad := range []string{"", "../etc", "a/b", "UPPER", "with:colon", "sp ace"} {
		if validFtpUsername(bad) {
			t.Errorf("unsafe username %q must be rejected", bad)
		}
	}
}

func TestFtpRestoreCred_RoundTrip(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	now := time.Unix(1_700_000_000, 0)

	if err := writeFtpRestoreCred("shop_deploy", u32(50001), "$6$salt$hash", now); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Wrong uid → refused + file deleted (hijack guard).
	if h, ok := consumeFtpRestoreCred("shop_deploy", u32(99999), now); ok || h != "" {
		t.Fatalf("uid mismatch must not return a hash, got %q ok=%v", h, ok)
	}
	// And it's gone now (one-shot delete even on mismatch).
	if _, ok := consumeFtpRestoreCred("shop_deploy", u32(50001), now); ok {
		t.Fatal("a mismatched consume must still delete the file")
	}
}

func TestFtpRestoreCred_MatchAndTTL(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	base := time.Unix(1_700_000_000, 0)

	// Matching uid within TTL → returns the hash and deletes.
	_ = writeFtpRestoreCred("t1_web", u32(50002), "$6$a$b", base)
	if h, ok := consumeFtpRestoreCred("t1_web", u32(50002), base.Add(time.Hour)); !ok || h != "$6$a$b" {
		t.Fatalf("fresh matching consume failed: h=%q ok=%v", h, ok)
	}
	if _, ok := consumeFtpRestoreCred("t1_web", u32(50002), base); ok {
		t.Fatal("consume must be one-shot")
	}

	// Stale (past TTL) → refused.
	_ = writeFtpRestoreCred("t1_web", u32(50002), "$6$a$b", base)
	if _, ok := consumeFtpRestoreCred("t1_web", u32(50002), base.Add(ftpRestoreCredTTL+time.Minute)); ok {
		t.Fatal("a stale staged credential must be refused")
	}

	// Legacy (nil uid) matches nil uid.
	_ = writeFtpRestoreCred("t1_legacy", nil, "$6$c$d", base)
	if h, ok := consumeFtpRestoreCred("t1_legacy", nil, base); !ok || h != "$6$c$d" {
		t.Fatalf("legacy nil-uid consume failed: h=%q ok=%v", h, ok)
	}
}

func TestWriteFtpRestoreCred_RejectsUnsafe(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	if err := writeFtpRestoreCred("../evil", u32(1), "$6$a$b", now); err == nil {
		t.Error("unsafe username must be refused")
	}
	if err := writeFtpRestoreCred("t1_x", u32(1), "hash\nroot:$6$x", now); err == nil {
		t.Error("injection-shaped hash must be refused")
	}
}

func TestSweepFtpRestoreCreds(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	_ = writeFtpRestoreCred("t1_fresh", u32(1), "$6$a$b", base)
	_ = writeFtpRestoreCred("t1_stale", u32(2), "$6$c$d", base.Add(-2*ftpRestoreCredTTL))
	sweepFtpRestoreCreds(base)
	// Fresh survives, stale gone.
	if _, ok := consumeFtpRestoreCred("t1_fresh", u32(1), base); !ok {
		t.Error("fresh credential must survive the sweep")
	}
	if _, ok := consumeFtpRestoreCred("t1_stale", u32(2), base); ok {
		t.Error("stale credential must be swept")
	}
}

func TestShadowHashesFromLines(t *testing.T) {
	lines := []string{
		"shop_deploy:$6$salt$hash:19000:0:99999:7:::",
		"t1_locked:!:19000:0:99999:7:::",
		"malformed-no-colon",
		"t1_bad:has space:1::",
	}
	got := shadowHashesFromLines(lines)
	if got["shop_deploy"] != "$6$salt$hash" {
		t.Errorf("shop_deploy hash = %q", got["shop_deploy"])
	}
	if got["t1_locked"] != "!" {
		t.Errorf("locked account hash = %q", got["t1_locked"])
	}
	if _, ok := got["t1_bad"]; ok {
		t.Error("a hash with a space must be dropped")
	}
}

func TestStageFtpRestoreCredentials(t *testing.T) {
	ftpRestoreCredDir = t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	meta := &backup.AccountMetadata{FtpAccounts: []backup.MetadataFtpAccount{
		{Username: "t1_web", UID: u32(50003), Isolated: true, PasswordShadow: "$6$a$b"},
		{Username: "t1_nopw", UID: u32(50004), Isolated: true, PasswordShadow: ""}, // no captured pw → skipped
		{Username: "../evil", PasswordShadow: "$6$c$d"},                            // unsafe → skipped w/ warning
	}}
	staged, skipped := stageFtpRestoreCredentials(meta, now)
	if staged != 1 {
		t.Fatalf("expected 1 staged, got %d", staged)
	}
	if len(skipped) != 1 {
		t.Fatalf("expected 1 skipped (unsafe), got %v", skipped)
	}
	if h, ok := consumeFtpRestoreCred("t1_web", u32(50003), now); !ok || h != "$6$a$b" {
		t.Fatalf("staged credential not consumable: h=%q ok=%v", h, ok)
	}
}
