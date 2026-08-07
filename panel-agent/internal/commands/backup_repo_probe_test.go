package commands

// GH #454: a restic repo that EXISTS but can't be opened (host reinstall
// regenerated the box password while a /backups disk was preserved, or the
// config/key files are corrupt) used to surface as a raw "snapshots probe:
// exit status 1 (stderr: …)" that read as "backups are broken". classifyRepoProbe
// separates that from a genuinely-missing repo (→ init) so the operator gets an
// actionable recovery message. These strings are verbatim restic 0.16.4 output.

import "testing"

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
			name:   "damaged config — the reporter's exact error",
			stderr: "fatal: config or key eece3d748ff57531b620a3cd0441eb7cde4d1334762dab8c943efeba35c92397 is damaged: ciphertext verification failed",
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
