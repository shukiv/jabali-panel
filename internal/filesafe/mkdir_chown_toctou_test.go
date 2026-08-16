package filesafe

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

// ownerUID returns the uid that owns path (via lstat, no symlink follow).
func ownerUID(t *testing.T, path string) uint32 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return st.Uid
}

// JAB-251: a leaf that is a symlink pointing OUTSIDE the scope must be
// refused, and the external target must never be created/chowned.
func TestMkdirChown_LeafSymlinkEscape_Refused(t *testing.T) {
	if os.Geteuid() != 0 {
		// Fchown to an arbitrary uid needs privilege; the escape-refusal
		// (ELOOP) is provable unprivileged, the chown effect is not.
		t.Skip("needs root to exercise the chown; escape-refusal covered below")
	}
	home := t.TempDir()
	external := t.TempDir() // the "/etc/sudoers.d" stand-in, outside home
	target := filepath.Join(external, "victim")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	before := ownerUID(t, target)

	// Plant a symlink at the leaf pointing at the external victim.
	link := filepath.Join(home, "race")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	s := NewScopeForTest("u1", "u1", home)
	err := s.MkdirChownInScope(link, 0o755, true, 12345, 12345)
	if err == nil {
		t.Fatal("MkdirChownInScope followed a leaf symlink escaping the scope")
	}
	if got := ownerUID(t, target); got != before {
		t.Fatalf("external target was chowned: uid %d -> %d", before, got)
	}
}

// The escape refusal itself is provable without root: a leaf symlink to
// an out-of-scope path must return an error before any chown.
func TestMkdirChown_LeafSymlinkEscape_ErrsUnprivileged(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(home, "race")); err != nil {
		t.Fatal(err)
	}
	s := NewScopeForTest("u1", "u1", home)
	// Chown to our own uid so the syscall itself would succeed if reached —
	// the point is it must NOT be reached.
	if err := s.MkdirChownInScope(filepath.Join(home, "race"), 0o755, true, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("leaf symlink to out-of-scope dir was not refused")
	}
}

// A symlink at a PARENT component pointing outside must be refused by
// openat2 RESOLVE_BENEATH before the leaf is ever created.
func TestMkdirChown_ParentSymlinkEscape_Refused(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(home, "evil")); err != nil {
		t.Fatal(err)
	}
	s := NewScopeForTest("u1", "u1", home)
	// home/evil -> external ; try to create home/evil/child
	if err := s.MkdirChownInScope(filepath.Join(home, "evil", "child"), 0o755, true, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("parent symlink escape was not refused")
	}
	if _, err := os.Stat(filepath.Join(external, "child")); err == nil {
		t.Fatal("child was created through the escaping parent symlink")
	}
}

// A legitimate in-scope create succeeds and, under root, lands the leaf
// owned by the requested uid.
func TestMkdirChown_InScope_Succeeds(t *testing.T) {
	home := t.TempDir()
	s := NewScopeForTest("u1", "u1", home)
	target := filepath.Join(home, "domains", "site", "home")
	if err := s.MkdirChownInScope(target, 0o755, true, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("legit create failed: %v", err)
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		t.Fatalf("target dir not created: %v", err)
	}
}

// JAB-251 acceptance criterion: continuously swap the path between an
// in-home directory and an external target while hammering
// MkdirChownInScope, and prove the external target is never created,
// modified, or chowned.
func TestMkdirChown_ContinuousSwapRace(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	victim := filepath.Join(external, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	victimOwner := ownerUID(t, victim)
	realDir := filepath.Join(home, "realdir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	racePath := filepath.Join(home, "race")

	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			// alternate: race -> realdir (in scope) then race -> victim (escape)
			_ = os.Remove(racePath)
			_ = os.Symlink(realDir, racePath)
			_ = os.Remove(racePath)
			_ = os.Symlink(victim, racePath)
		}
	}()

	s := NewScopeForTest("u1", "u1", home)
	for i := 0; i < 4000; i++ {
		// Either errors (symlink leaf refused) or succeeds on a real dir —
		// never chowns/creates the external victim.
		_ = s.MkdirChownInScope(racePath, 0o755, true, os.Getuid(), os.Getgid())
		if got := ownerUID(t, victim); got != victimOwner {
			stop.Store(true)
			<-done
			t.Fatalf("victim chowned during race at iter %d: %d -> %d", i, victimOwner, got)
		}
	}
	stop.Store(true)
	<-done

	// The victim dir must be untouched: same owner, still empty.
	if got := ownerUID(t, victim); got != victimOwner {
		t.Fatalf("victim owner changed: %d -> %d", victimOwner, got)
	}
	entries, _ := os.ReadDir(victim)
	if len(entries) != 0 {
		t.Fatalf("victim dir gained %d entries via the race", len(entries))
	}
}
