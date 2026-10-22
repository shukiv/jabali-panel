package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const (
	stagedTargetSHA = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	stagedOtherSHA  = "ffffffffffffffffffffffffffffffffffffffff"
)

// releaseTarball builds a gzipped tar holding a MANIFEST that names fullSHA.
func releaseTarball(t *testing.T, fullSHA string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := "short_sha=" + fullSHA[:9] + "\nfull_sha=" + fullSHA + "\nbuild_time=2026-10-20T00:00:00Z\n"
	if err := tw.WriteHeader(&tar.Header{Name: "MANIFEST", Mode: 0o644, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeReleaseHost serves one release the way GitHub's download paths do:
// /releases/download/release-<9>/jabali-release-<9>.tar.gz[.sha256|.minisig].
type fakeReleaseHost struct {
	tarball []byte
	sum     string // "" = serve the real checksum
	sig     string // "" = no .minisig published
	mu      sync.Mutex
	gets    []string
}

func (f *fakeReleaseHost) serve(t *testing.T, publishedFor string) *httptest.Server {
	t.Helper()
	prefix := "/releases/download/release-" + publishedFor[:9] + "/jabali-release-" + publishedFor[:9] + ".tar.gz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.mu.Lock()
			f.gets = append(f.gets, r.URL.Path)
			f.mu.Unlock()
		}
		switch r.URL.Path {
		case prefix:
			_, _ = w.Write(f.tarball)
		case prefix + ".sha256":
			sum := f.sum
			if sum == "" {
				h := sha256.Sum256(f.tarball)
				sum = hex.EncodeToString(h[:]) + "  jabali-release.tar.gz\n"
			}
			_, _ = w.Write([]byte(sum))
		case prefix + ".minisig":
			if f.sig == "" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(f.sig))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeReleaseHost) downloadedTarball() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.gets {
		if strings.HasSuffix(p, ".tar.gz") {
			return true
		}
	}
	return false
}

func headOK(ctx context.Context, url string) bool { return urlExists(ctx, url) }

func noLog(string, ...any) {}

func descendant(string) (bool, error) { return true, nil }

func TestVerifyResetTarget_NoKeyOrSameCommitVerifiesNothing(t *testing.T) {
	host := &fakeReleaseHost{tarball: releaseTarball(t, stagedTargetSHA)}
	srv := host.serve(t, stagedTargetSHA)
	pub, _ := signFor(t, host.tarball)

	for name, tc := range map[string]struct{ key, current string }{
		"build without a key":        {"", stagedOtherSHA},
		"target already checked out": {pub, stagedTargetSHA},
	} {
		t.Run(name, func(t *testing.T) {
			staged, stay, err := verifyResetTarget(context.Background(), tc.key, srv.URL, t.TempDir(),
				stagedTargetSHA, tc.current, descendant, headOK, noLog)
			if err != nil || stay || staged != nil {
				t.Fatalf("got staged=%v stay=%v err=%v, want nothing verified and the reset allowed", staged, stay, err)
			}
		})
	}
	if host.downloadedTarball() {
		t.Fatal("downloaded a tarball although there was nothing to verify")
	}
}

func TestVerifyResetTarget_SignedReleaseForTargetAllowsReset(t *testing.T) {
	host := &fakeReleaseHost{tarball: releaseTarball(t, stagedTargetSHA)}
	pub, sig := signFor(t, host.tarball)
	host.sig = sig
	srv := host.serve(t, stagedTargetSHA)

	staged, stay, err := verifyResetTarget(context.Background(), pub, srv.URL, t.TempDir(),
		stagedTargetSHA, stagedOtherSHA, descendant, headOK, noLog)
	if err != nil || stay || staged == nil {
		t.Fatalf("got staged=%v stay=%v err=%v, want the verified release", staged, stay, err)
	}
	defer staged.cleanup()
	if staged.sha != stagedTargetSHA {
		t.Fatalf("staged sha = %s, want %s", staged.sha, stagedTargetSHA)
	}
	if _, err := os.Stat(filepath.Join(staged.extractDir, "MANIFEST")); err != nil {
		t.Fatalf("extracted release has no MANIFEST: %v", err)
	}
	root := staged.root
	staged.cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind (err=%v)", root, err)
	}
}

func TestVerifyResetTarget_NothingSignedYetStaysWithoutDownloading(t *testing.T) {
	t.Run("no release published for the target", func(t *testing.T) {
		host := &fakeReleaseHost{tarball: releaseTarball(t, stagedOtherSHA)}
		srv := host.serve(t, stagedOtherSHA)
		pub, _ := signFor(t, host.tarball)
		staged, stay, err := verifyResetTarget(context.Background(), pub, srv.URL, t.TempDir(),
			stagedTargetSHA, stagedOtherSHA, descendant, headOK, noLog)
		if err != nil || !stay || staged != nil {
			t.Fatalf("got staged=%v stay=%v err=%v, want stay on the current build", staged, stay, err)
		}
		if host.downloadedTarball() {
			t.Fatal("downloaded a tarball for a commit with no release")
		}
	})
	t.Run("release published but not signed yet", func(t *testing.T) {
		host := &fakeReleaseHost{tarball: releaseTarball(t, stagedTargetSHA)}
		srv := host.serve(t, stagedTargetSHA)
		pub, _ := signFor(t, host.tarball)
		staged, stay, err := verifyResetTarget(context.Background(), pub, srv.URL, t.TempDir(),
			stagedTargetSHA, stagedOtherSHA, descendant, headOK, noLog)
		if err != nil || !stay || staged != nil {
			t.Fatalf("got staged=%v stay=%v err=%v, want stay on the current build", staged, stay, err)
		}
		if host.downloadedTarball() {
			t.Fatal("downloaded an unsigned tarball")
		}
	})
}

func TestVerifyResetTarget_FailedChecksRefuseTheUpdate(t *testing.T) {
	good := releaseTarball(t, stagedTargetSHA)
	pub, sig := signFor(t, good)
	_, otherSig := signFor(t, good) // valid, but from a key this build does not trust

	wrongCommit := releaseTarball(t, stagedOtherSHA)
	wrongPub, wrongSig := signFor(t, wrongCommit)

	cases := map[string]struct {
		host *fakeReleaseHost
		key  string
		want string
	}{
		"signed by another key":                {&fakeReleaseHost{tarball: good, sig: otherSig}, pub, "DIFFERENT key"},
		"signature does not match the tarball": {&fakeReleaseHost{tarball: good, sig: wrongSig}, wrongPub, "does not match"},
		"checksum does not match": {&fakeReleaseHost{tarball: good, sig: sig,
			sum: strings.Repeat("0", 64) + "  x\n"}, pub, "checksum"},
		"signed release of another commit under the target's name": {&fakeReleaseHost{tarball: wrongCommit, sig: wrongSig},
			wrongPub, "refusing it"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := tc.host.serve(t, stagedTargetSHA)
			parent := t.TempDir()
			staged, stay, err := verifyResetTarget(context.Background(), tc.key, srv.URL, parent,
				stagedTargetSHA, stagedOtherSHA, descendant, headOK, noLog)
			if err == nil || stay || staged != nil {
				t.Fatalf("got staged=%v stay=%v err=%v, want the update refused", staged, stay, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if left, _ := os.ReadDir(parent); len(left) != 0 {
				t.Fatalf("a refused release left %d staging entries behind", len(left))
			}
		})
	}
}

// A signature says a build is genuine, not that it is current: an old signed
// release must not move a host back onto code with known holes.
func TestVerifyResetTarget_OlderTargetStaysWithoutDownloading(t *testing.T) {
	host := &fakeReleaseHost{tarball: releaseTarball(t, stagedTargetSHA)}
	pub, sig := signFor(t, host.tarball)
	host.sig = sig
	srv := host.serve(t, stagedTargetSHA)

	notNewer := func(string) (bool, error) { return false, nil }
	staged, stay, err := verifyResetTarget(context.Background(), pub, srv.URL, t.TempDir(),
		stagedTargetSHA, stagedOtherSHA, notNewer, headOK, noLog)
	if err != nil || !stay || staged != nil {
		t.Fatalf("got staged=%v stay=%v err=%v, want stay on the current build", staged, stay, err)
	}
	if host.downloadedTarball() {
		t.Fatal("downloaded a release older than the current build")
	}

	broken := func(string) (bool, error) { return false, errors.New("merge-base failed") }
	if _, _, err := verifyResetTarget(context.Background(), pub, srv.URL, t.TempDir(),
		stagedTargetSHA, stagedOtherSHA, broken, headOK, noLog); err == nil {
		t.Fatal("an ancestry check that failed did not stop the update")
	}
}

// The commit has to be vouched for before it becomes the working tree:
// everything after the reset runs from the checkout as root. Pin the order so a
// later edit cannot quietly move the check after the reset.
func TestUpdateGitStep_VerifiesTargetBeforeReset(t *testing.T) {
	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	verify := strings.Index(s, "verifyResetTarget(")
	reset := strings.Index(s, `"git", "reset", "--hard", resetRef`)
	if verify < 0 || reset < 0 {
		t.Fatalf("could not find the verify call (%d) or the reset (%d) in update.go", verify, reset)
	}
	if verify > reset {
		t.Fatal("update.go verifies the target commit AFTER `git reset --hard` — the checkout would already be untrusted code")
	}
	if !regexp.MustCompile(`if stay \{[^}]*resetRef = "HEAD"`).MatchString(s) {
		t.Fatal("update.go no longer keeps the checkout where it is when there is no signed release to move to")
	}
	if !strings.Contains(s, "resetRef = targetSHA") {
		t.Fatal("update.go no longer resets to the exact commit it verified")
	}
	if !strings.Contains(s, "installFromRelease(ctx, repoDir, verifiedRelease,") {
		t.Fatal("the release step no longer installs the tarball the git step verified")
	}
}

// Real git: only a commit built on HEAD counts as newer. An ancestor is older,
// and a commit on a line that split off earlier lacks HEAD's later fixes.
func TestCommitDescendsFrom(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.invalid",
			"-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "head")
	head := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "newer")
	newer := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "side", base)
	git("commit", "-q", "--allow-empty", "-m", "side")
	side := git("rev-parse", "HEAD")

	gitOut := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		return string(out), err
	}
	for name, tc := range map[string]struct {
		target string
		want   bool
	}{
		"descendant of HEAD":    {newer, true},
		"ancestor of HEAD":      {base, false},
		"split off before HEAD": {side, false},
	} {
		got, err := commitDescendsFrom(gitOut, head, tc.target)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v err=%v, want %v", name, got, err, tc.want)
		}
	}
}
