package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testMailShadowJob = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func mailShadowTestWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func goodMailShadowParams() migrationPullMailShadowParams {
	return migrationPullMailShadowParams{
		JobID:      testMailShadowJob,
		Host:       "198.51.100.7",
		SSHUser:    "root",
		SecretPath: migrationSecretsBaseDir + "/" + testMailShadowJob + ".env",
		SrcAccount: "acct",
	}
}

func TestMailShadowParams_Accepted(t *testing.T) {
	p := goodMailShadowParams()
	home, user, secret, aerr := validateMailShadowParams(&p)
	if aerr != nil || home != "/home/acct" || user != "root" || secret != p.SecretPath {
		t.Fatalf("got home=%q user=%q secret=%q err=%v", home, user, secret, aerr)
	}
	p.SrcHome, p.SSHUser = "/home2/acct", ""
	if home, user, _, aerr = validateMailShadowParams(&p); aerr != nil || home != "/home2/acct" || user != "root" {
		t.Fatalf("/home2 home: got home=%q user=%q err=%v", home, user, aerr)
	}
}

func TestMailShadowParams_Refused(t *testing.T) {
	for name, mut := range map[string]func(*migrationPullMailShadowParams){
		"no job":             func(p *migrationPullMailShadowParams) { p.JobID = "" },
		"bad job":            func(p *migrationPullMailShadowParams) { p.JobID = "../x" },
		"host option":        func(p *migrationPullMailShadowParams) { p.Host = "-oProxyCommand=x" },
		"host space":         func(p *migrationPullMailShadowParams) { p.Host = "a b" },
		"ssh user option":    func(p *migrationPullMailShadowParams) { p.SSHUser = "-oProxyCommand=x" },
		"account slash":      func(p *migrationPullMailShadowParams) { p.SrcAccount = "a/b" },
		"account dotdot":     func(p *migrationPullMailShadowParams) { p.SrcAccount = ".." },
		"home outside /home": func(p *migrationPullMailShadowParams) { p.SrcHome = "/var/acct" },
		"home other account": func(p *migrationPullMailShadowParams) { p.SrcHome = "/home/other" },
		"home traversal":     func(p *migrationPullMailShadowParams) { p.SrcHome = "/home/x/../acct" },
		"home deeper":        func(p *migrationPullMailShadowParams) { p.SrcHome = "/home/acct/etc" },
		"secret other job": func(p *migrationPullMailShadowParams) {
			p.SecretPath = migrationSecretsBaseDir + "/01BX5ZZKBKACTAV9WEVGEMMVRZ.env"
		},
		"secret outside dir": func(p *migrationPullMailShadowParams) { p.SecretPath = "/etc/shadow" },
		"secret dotdot":      func(p *migrationPullMailShadowParams) { p.SecretPath = migrationSecretsBaseDir + "/../../shadow" },
	} {
		p := goodMailShadowParams()
		mut(&p)
		if _, _, _, aerr := validateMailShadowParams(&p); aerr == nil {
			t.Errorf("%s: accepted %+v", name, p)
		}
	}
}

// withMailShadowStaging points the verb at a temp staging root holding the
// job dir and returns the job dir.
func withMailShadowStaging(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := mailShadowStagingRoot
	mailShadowStagingRoot = root
	t.Cleanup(func() { mailShadowStagingRoot = old })
	job := filepath.Join(root, testMailShadowJob)
	if err := os.Mkdir(job, 0o700); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestPrepareMailShadowDir_FreshAndStale(t *testing.T) {
	job := withMailShadowStaging(t)
	dest, aerr := prepareMailShadowDir(testMailShadowJob)
	if aerr != nil || dest != filepath.Join(job, mailShadowDirName) {
		t.Fatalf("dest=%q err=%v", dest, aerr)
	}
	if fi, err := os.Lstat(dest); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("mail-shadow not a 0700 dir: %v %v", fi, err)
	}
	// An earlier run's file must not survive a re-run.
	mailShadowTestWrite(t, filepath.Join(dest, "old.example", "shadow"), "x")
	if _, aerr = prepareMailShadowDir(testMailShadowJob); aerr != nil {
		t.Fatalf("re-run: %v", aerr)
	}
	if _, err := os.Lstat(filepath.Join(dest, "old.example")); !os.IsNotExist(err) {
		t.Fatalf("stale entry survived: %v", err)
	}
}

func TestPrepareMailShadowDir_RefusesSymlinkOrFile(t *testing.T) {
	job := withMailShadowStaging(t)
	target := t.TempDir()
	mailShadowTestWrite(t, filepath.Join(target, "keep"), "x")
	if err := os.Symlink(target, filepath.Join(job, mailShadowDirName)); err != nil {
		t.Fatal(err)
	}
	if _, aerr := prepareMailShadowDir(testMailShadowJob); aerr == nil {
		t.Fatal("symlinked mail-shadow accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("symlink target touched: %v", err)
	}

	if err := os.Remove(filepath.Join(job, mailShadowDirName)); err != nil {
		t.Fatal(err)
	}
	mailShadowTestWrite(t, filepath.Join(job, mailShadowDirName), "x")
	if _, aerr := prepareMailShadowDir(testMailShadowJob); aerr == nil {
		t.Fatal("mail-shadow as a regular file accepted")
	}
}

func TestPrepareMailShadowDir_RefusesMissingOrSymlinkedJobDir(t *testing.T) {
	job := withMailShadowStaging(t)
	if _, aerr := prepareMailShadowDir("01BX5ZZKBKACTAV9WEVGEMMVRZ"); aerr == nil {
		t.Fatal("missing job dir accepted")
	}
	if err := os.Remove(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), job); err != nil {
		t.Fatal(err)
	}
	if _, aerr := prepareMailShadowDir(testMailShadowJob); aerr == nil {
		t.Fatal("symlinked job dir accepted")
	}
}

// The filters themselves, run through a real (local) rsync: only real
// <domain>/shadow files one level down come over. A symlinked domain dir or
// shadow file on the source copies nothing.
func TestMailShadowFilters_LocalRsyncCopiesOnlyRealShadowFiles(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	src, dst := t.TempDir(), t.TempDir()
	outside := t.TempDir()
	mailShadowTestWrite(t, filepath.Join(outside, "shadow"), "outside:hash\n")
	mailShadowTestWrite(t, filepath.Join(src, "a.example", "shadow"), "box:hash\n")
	mailShadowTestWrite(t, filepath.Join(src, "a.example", "passwd"), "box:x\n")
	mailShadowTestWrite(t, filepath.Join(src, "deep.example", "sub", "shadow"), "x\n")
	mailShadowTestWrite(t, filepath.Join(src, "toplevel"), "x\n")
	if err := os.Symlink(outside, filepath.Join(src, "linkdir.example")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "linkfile.example"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "shadow"), filepath.Join(src, "linkfile.example", "shadow")); err != nil {
		t.Fatal(err)
	}

	args := append(mailShadowFilterArgs(), src+"/", dst+"/")
	if out, err := exec.Command("rsync", args...).CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v: %s", err, out)
	}

	walk := func() (files, dirs []string) {
		_ = filepath.Walk(dst, func(p string, fi os.FileInfo, err error) error {
			if err != nil || p == dst {
				return nil
			}
			rel := strings.TrimPrefix(p, dst+"/")
			if fi.IsDir() {
				dirs = append(dirs, rel)
			} else {
				files = append(files, rel) // includes symlinks (Walk uses Lstat)
			}
			return nil
		})
		return files, dirs
	}
	// rsync alone: the only non-directory it writes is the real shadow file.
	// (A dir whose shadow was a skipped symlink can be left empty; -m does
	// not prune it. No content and no link comes over.)
	if files, _ := walk(); !reflect.DeepEqual(files, []string{"a.example/shadow"}) {
		t.Fatalf("rsync copied %q, want only a.example/shadow", files)
	}
	// The pipeline as the verb runs it: rsync, then the sweep.
	if _, err := sweepMailShadowDir(dst); err != nil {
		t.Fatal(err)
	}
	if files, dirs := walk(); !reflect.DeepEqual(files, []string{"a.example/shadow"}) || !reflect.DeepEqual(dirs, []string{"a.example"}) {
		t.Fatalf("after sweep: files %q dirs %q", files, dirs)
	}
	if fi, _ := os.Lstat(filepath.Join(dst, "a.example", "shadow")); fi == nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("shadow not a 0600 regular file: %v", fi)
	}
}

func TestSweepMailShadowDir_KeepsOnlyShadowFiles(t *testing.T) {
	dst := t.TempDir()
	mailShadowTestWrite(t, filepath.Join(dst, "a.example", "shadow"), "box:hash\n")
	mailShadowTestWrite(t, filepath.Join(dst, "a.example", "passwd"), "x")
	mailShadowTestWrite(t, filepath.Join(dst, "stray"), "x")
	mailShadowTestWrite(t, filepath.Join(dst, "dirshadow.example", "shadow", "x"), "x")
	if err := os.MkdirAll(filepath.Join(dst, "empty.example"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "link.example"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dst, "link.example", "shadow")); err != nil {
		t.Fatal(err)
	}
	mailShadowTestWrite(t, filepath.Join(dst, "big.example", "shadow"), strings.Repeat("x", mailShadowMaxBytes+1))

	res, err := sweepMailShadowDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 1 || entries[0].Name() != "a.example" {
		t.Fatalf("left behind %v", entries)
	}
	sub, _ := os.ReadDir(filepath.Join(dst, "a.example"))
	if len(sub) != 1 || sub[0].Name() != "shadow" {
		t.Fatalf("a.example holds %v", sub)
	}
	if res.Domains != 1 || res.Files != 1 || res.Removed != 5 {
		t.Fatalf("counts %+v", res)
	}
}

// The reply carries counts and names only — never what a shadow file holds.
func TestMailShadowResult_CountsOnly(t *testing.T) {
	dst := t.TempDir()
	mailShadowTestWrite(t, filepath.Join(dst, "a.example", "shadow"), "box:$6$saltvalue$hashvalue:19000::::::\n")
	res, err := sweepMailShadowDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	res.Unreadable = []string{"b.example"}
	raw, _ := json.Marshal(res)
	for _, leak := range []string{"saltvalue", "hashvalue", "$6$", "box:"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("reply leaks %q: %s", leak, raw)
		}
	}
}

func TestMailShadowUnreadableDomains(t *testing.T) {
	out := strings.Join([]string{
		`rsync: opendir "/home/acct/etc/b.example" failed: Permission denied (13)`,
		`rsync: [sender] opendir "/home/acct/etc/c.example" failed: Permission denied (13)`,
		`rsync: [sender] send_files failed to open "/home/acct/etc/d.example/shadow": Permission denied (13)`,
		`rsync: opendir "/home/other/etc/x.example" failed: Permission denied (13)`,
		`rsync error: some files/attrs were not transferred (see previous errors) (code 23)`,
		`a.example/shadow`,
	}, "\n")
	got := mailShadowUnreadableDomains(out, "/home/acct/etc")
	if want := []string{"b.example", "c.example", "d.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := mailShadowUnreadableDomains("a.example/shadow\n", "/home/acct/etc"); got != nil {
		t.Fatalf("clean run: got %q", got)
	}
}

// The argv: account-run source side for a root login, the fixed filters, no
// option that would follow or recreate a symlink, and <home>/etc/ as source.
func TestMailShadowRsyncArgs(t *testing.T) {
	opt, msg := sourceRsyncPathOpt("root", "/home/acct", false, "/home/acct")
	if msg != "" {
		t.Fatal(msg)
	}
	args := mailShadowRsyncArgs(opt, "-p 22", "root@198.51.100.7", "/home/acct/etc", "/staging/job/mail-shadow")
	joined := strings.Join(args, " ")
	for _, banned := range []string{"-a", "-l", "-L", "-D", "--links", "--copy-links", "--copy-unsafe-links", "--devices", "--specials"} {
		for _, a := range args {
			if a == banned {
				t.Fatalf("argv carries %s: %q", banned, args)
			}
		}
	}
	for _, want := range []string{"--include=/*/", "--include=/*/shadow", "--exclude=*", "--max-size=1m", "--chmod=D0700,F0600", "runuser -u"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv lacks %q: %q", want, args)
		}
	}
	if n := len(args); args[n-2] != "root@198.51.100.7:/home/acct/etc/" || args[n-1] != "/staging/job/mail-shadow/" {
		t.Fatalf("src/dest = %q %q", args[n-2], args[n-1])
	}
}

// A run that fails after the dir was made (here: the secret file is not
// there) leaves no mail-shadow dir behind.
func TestPullMailShadowHandler_FailedRunLeavesNothing(t *testing.T) {
	job := withMailShadowStaging(t)
	raw, _ := json.Marshal(goodMailShadowParams())
	if _, err := migrationPullMailShadowHandler(context.Background(), raw); err == nil {
		t.Fatal("want an error without a secret file")
	}
	if _, err := os.Lstat(filepath.Join(job, mailShadowDirName)); !os.IsNotExist(err) {
		t.Fatalf("mail-shadow left behind: %v", err)
	}
}
