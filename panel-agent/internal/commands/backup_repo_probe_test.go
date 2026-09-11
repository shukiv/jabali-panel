package commands

// GH #454: a restic repo that EXISTS but can't be opened (host reinstall
// regenerated the box password while a /backups disk was preserved, or the
// config/key files are corrupt) used to surface as a raw "snapshots probe:
// exit status 1 (stderr: …)" that read as "backups are broken". classifyRepoProbe
// separates that from a genuinely-missing repo (→ init) so the operator gets an
// actionable recovery message. These strings are verbatim restic output
// (captured against 0.16.4, re-checked on 0.18.0).
//
// JAB-405: the "config or key <id> is damaged: ciphertext verification failed"
// signal is split out of the generic unopenable class into
// repoProbeKeyConfigMismatch. A key opened with the password (so the password is
// CORRECT) but its master can't decrypt config — the signature of two concurrent
// `restic init` on one empty repo, NOT a wrong password. The two strings never
// co-occur (verified: wrong-password vs an initialized repo yields
// "wrong password or no key found"; the dual-key race yields the ciphertext
// error), so they carry distinct recovery advice.

import (
	"strings"
	"testing"
)

func TestClassifyRepoProbe(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   repoProbeClass
	}{
		{
			name:   "missing local repo",
			stderr: "fatal: unable to open config file: stat /backups/config: no such file or directory\nis there a repository at the following location?\n/backups",
			want:   repoProbeMissing,
		},
		{
			name:   "missing (older phrasing)",
			stderr: "fatal: repository does not exist: unable to open config file",
			want:   repoProbeMissing,
		},
		{
			name:   "wrong/rotated password (the #454 reinstall case)",
			stderr: "fatal: wrong password or no key found",
			want:   repoProbeUnopenable,
		},
		{
			// The dual-init race (JAB-405): a key opened but its master ≠ config.
			// Distinct from a wrong password — password is correct here.
			name:   "key/config mismatch — ciphertext verification failed",
			stderr: "fatal: config or key eece3d748ff57531b620a3cd0441eb7cde4d1334762dab8c943efeba35c92397 is damaged: ciphertext verification failed",
			want:   repoProbeKeyConfigMismatch,
		},
		{
			// A bare "is damaged" without the ciphertext phrase (index/pack
			// corruption) must NOT be mistaken for the key/config race — it keeps
			// the generic unopenable hint, not the "count your key files" advice.
			// (Synthetic fixture — pins classifier specificity, not a captured
			// restic string, unlike the ciphertext case above.)
			name:   "bare is-damaged (not ciphertext) stays unopenable",
			stderr: "fatal: pack 1a2b3c is damaged: run `restic repair index`",
			want:   repoProbeUnopenable,
		},
		{
			name:   "unknown failure surfaces raw",
			stderr: "fatal: unable to connect: connection refused",
			want:   repoProbeOther,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyRepoProbe(c.stderr); got != c.want {
				t.Errorf("classifyRepoProbe(%q) = %d, want %d", c.stderr, got, c.want)
			}
		})
	}
}

// The key/config-mismatch message is the JAB-405 deliverable: it must tell the
// operator the password is CORRECT (do not touch it) and give the key-file-count
// recovery — and must NOT repeat the wrong-password "restore the ORIGINAL
// password" advice, which is what shipped for this symptom before the split.
func TestRepoUnopenableMessage_MismatchArm(t *testing.T) {
	const pw = "/etc/jabali-panel/dest-7.password"
	msg := repoUnopenableMessage(repoProbeKeyConfigMismatch,
		"sftp:puzzle@host:/home/puzzle/jabali", pw,
		"config or key abcd is damaged: ciphertext verification failed", nil, nil)

	for _, want := range []string{
		pw,               // names the exact password file — do not touch it
		"CORRECT",        // password is correct
		"do NOT restore", // anti-footgun: don't restore/regenerate the password
		"keys/",          // recovery hinges on the key-file count
		"MORE THAN ONE",  // >1 key → the race → move the named key aside
		"exactly ONE",    // 1 key → genuine corruption → fresh dir
		"FRESH empty",    // recovery destination for the corrupt-config path
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("mismatch message missing %q\ngot: %s", want, msg)
		}
	}
	// Must NOT carry the wrong-password recovery — that advice destroys nothing
	// but sends the operator to restore a password that is already correct.
	for _, banned := range []string{"reinstalled or regenerated", "restore the ORIGINAL"} {
		if strings.Contains(msg, banned) {
			t.Errorf("mismatch message must not contain wrong-password advice %q\ngot: %s", banned, msg)
		}
	}
	// Single paragraph — it also renders in a short UI toast.
	if strings.Contains(msg, "\n") {
		t.Errorf("mismatch message must be one line (no newline)\ngot: %s", msg)
	}
}

func TestRepoUnopenableMessage_WrongPasswordArm(t *testing.T) {
	const pw = "/etc/jabali-panel/restic-repo.password"
	msg := repoUnopenableMessage(repoProbeUnopenable,
		"/backups", pw, "wrong password or no key found", nil, nil)

	for _, want := range []string{
		pw, // names the password file to restore
		"reinstalled or regenerated",
		"restore the ORIGINAL",
		"FRESH empty",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("wrong-password message missing %q\ngot: %s", want, msg)
		}
	}
	// Must NOT carry the race-specific "count your key files" recovery.
	for _, banned := range []string{"MORE THAN ONE", "password is CORRECT", "do NOT restore"} {
		if strings.Contains(msg, banned) {
			t.Errorf("wrong-password message must not contain mismatch advice %q\ngot: %s", banned, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("wrong-password message must be one line (no newline)\ngot: %s", msg)
	}
}
