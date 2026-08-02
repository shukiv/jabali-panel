// Release-tarball update path. See update.go for the source-build path
// that this code supersedes when a release is available for the current
// main tip.
//
// Why tarballs at all:
//
//   The historical update flow (`git fetch` + `npm ci` + `vite build` +
//   `go build` on every host) is the long pole on small VMs — vite
//   transforms 6k+ monaco modules and routinely OOMs on <4 GB hosts,
//   the npm cache fills the rootfs, and the Go cache forces a long
//   reseed when /opt/jabali-panel/.cache/go-* gets pruned. None of
//   that work is per-host: the binaries produced are byte-identical
//   regardless of where they were built, and panel-ui/dist is embedded
//   into jabali-panel via //go:embed.
//
//   The release-tarballs path moves the build to Gitea Actions exactly
//   once per main commit (.gitea/workflows/release.yml + tools/build-
//   release.sh) and ships a ~90MB tarball containing the three Go
//   binaries + a MANIFEST. `jabali update` downloads + verifies the
//   sha256 sidecar + extracts + installs. Update reduces from 5–10min
//   build + 1–2GB memory peak to ~30s download + a few MB.
//
//   The source-build path stays as a fallback for: --from-source flag
//   (explicit operator opt-in), local-modified repos, releases that
//   haven't been built yet on a freshly-pushed main commit, or a Gitea
//   reachability problem when the operator needs to update.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// defaultReleaseAPIBase is the GitHub repo the project is hosted on.
	// Override via JABALI_RELEASE_API_BASE for staging mirrors, internal
	// forks, or air-gapped operators who run their own forge. GitHub's
	// releases API is shape-compatible with the Forgejo one we came from
	// ({tag_name, assets:[{name, browser_download_url, size}]}), and
	// browser_download_url is a plain GET on this public repo.
	defaultReleaseAPIBase = "https://api.github.com/repos/shukiv/jabali-panel"

	// releaseShortSHALen matches the build-release.sh convention.
	// The API returns full SHAs; we truncate to derive the tag name
	// `release-<short_sha>` and the asset name.
	releaseShortSHALen = 7

	// HTTP timeouts: short for the metadata API calls (fast JSON),
	// generous for the tarball download (~90MB on a slow link).
	releaseAPITimeout      = 30 * time.Second
	releaseDownloadTimeout = 10 * time.Minute
)

// releaseAsset is the subset of Gitea's /releases response shape we
// consume. Extra fields are ignored — Gitea adds them over time and we
// don't want to break on schema growth.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type releaseResponse struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// installFromRelease drives the release-tarball update flow end-to-end.
// Returns (installed=true, installedSHA) on success.
// Returns (installed=false, "") on a recoverable error (no release for
// this SHA yet, network blip, missing checksum sidecar) so the caller
// can fall back to source build.
// Returns (installed=false, "") + err on a hard failure (sha256
// mismatch, file install failure, migration error) — those should NOT
// transparently fall back because they indicate corruption or a
// systemic problem.
func installFromRelease(ctx context.Context, repoDir string, log func(string, ...any)) (installed bool, sha string, err error) {
	base := os.Getenv("JABALI_RELEASE_API_BASE")
	if base == "" {
		base = defaultReleaseAPIBase
	}
	webBase := os.Getenv("JABALI_RELEASE_WEB_BASE")
	if webBase == "" {
		webBase = defaultReleaseWebBase
	}

	// 1. Discover the commit we are updating to.
	//
	//    Read it from the local checkout, not GET /branches/main. This
	//    step runs after "git fetch + reset to release channel", so the
	//    working tree already IS the channel tip — free, offline, and
	//    not subject to the 60/hour unauthenticated API quota that was
	//    silently forcing every update into a full source build. See
	//    update_release_direct.go for the full rationale.
	var fullSHA string
	var tarName, sumName, tarURL, sumURL string
	// usedDirect: URLs came from the "latest" CDN shortcut, which is only
	// correct for a host following main. On a MANIFEST mismatch we re-resolve
	// by commit rather than giving up.
	usedDirect := false

	fullSHA, err = localHeadSHA(ctx, repoDir)
	if err == nil {
		log("release: local HEAD = %s", fullSHA)
		// 2. Resolve assets through the release CDN's fixed-name path.
		//    github.com/.../releases/latest/download/<asset> redirects to
		//    the newest release without touching the API. The MANIFEST
		//    check below still verifies it is the build for THIS commit,
		//    so a host sitting on an older commit falls through rather
		//    than silently installing something else.
		tarName = "jabali-release.tar.gz"
		sumName = tarName + ".sha256"
		tarURL, sumURL = latestDownloadURLs(webBase)
		usedDirect = true
	} else {
		// No usable local checkout (bare install, relocated repo). Fall
		// back to the API path, which still works when the quota allows.
		log("release: local HEAD unreadable (%v) — asking the API instead", err)
		fullSHA, err = fetchLatestMainSHA(ctx, base)
		if err != nil {
			log("release: lookup of main HEAD failed: %v", err)
			return false, "", nil
		}
		log("release: latest main = %s", fullSHA)

		// Look up the release for this commit by SCANNING releases for a
		// target_commitish match. A direct GET /releases/tags/release-<short>
		// is not usable: git's short-SHA length is whatever is unique in the
		// repo and drifts as it grows, so the tag name cannot be derived.
		var rel *releaseResponse
		var shortSHA string
		rel, shortSHA, err = findReleaseForCommit(ctx, base, fullSHA)
		if err != nil {
			log("release: no release tarball published yet for %s (%v) — falling back", fullSHA[:7], err)
			return false, "", nil
		}
		tarName = fmt.Sprintf("jabali-release-%s.tar.gz", shortSHA)
		sumName = tarName + ".sha256"
		tarURL = pickAssetURL(rel, tarName)
		sumURL = pickAssetURL(rel, sumName)
		if tarURL == "" || sumURL == "" {
			log("release: release %s is missing %s or %s asset — falling back", rel.TagName, tarName, sumName)
			return false, "", nil
		}
	}

	// 3. Download + verify + extract one candidate, and report whether its
	//    MANIFEST is the build for the commit this host is on.
	stage, err := os.MkdirTemp("/var/lib/jabali-panel", "release-stage-*")
	if err != nil {
		return false, "", fmt.Errorf("release: mkdir staging: %w", err)
	}
	defer os.RemoveAll(stage) // success path also cleans up

	attempt := 0
	stageAndCheck := func(tarURL, sumURL, tarName, sumName string) (string, bool, error) {
		attempt++
		dir := filepath.Join(stage, fmt.Sprintf("try%d", attempt))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", false, fmt.Errorf("release: mkdir staging: %w", err)
		}
		tarPath := filepath.Join(dir, tarName)
		if err := downloadTo(ctx, tarURL, tarPath, releaseDownloadTimeout); err != nil {
			return "", false, fmt.Errorf("release: download tarball: %w", err)
		}
		sumPath := filepath.Join(dir, sumName)
		if err := downloadTo(ctx, sumURL, sumPath, releaseAPITimeout); err != nil {
			return "", false, fmt.Errorf("release: download checksum: %w", err)
		}
		// Verify sha256. Hard-fail on mismatch — the caller MUST NOT fall
		// back to source for a corrupted tarball.
		if err := verifyTarballChecksum(tarPath, sumPath); err != nil {
			return "", false, fmt.Errorf("release: checksum verification failed: %w", err)
		}
		log("release: sha256 verified")

		// JAB-190: the sha256 sidecar comes from the same origin as the
		// tarball, so it proves the download was not corrupted and nothing
		// more — whoever can publish one can publish the other. The signature
		// is checked here, BEFORE extraction, so attacker-controlled bytes are
		// never written to disk by tar on the strength of a checksum alone.
		// No-op on builds with no embedded key; mandatory once there is one.
		if err := verifyReleaseSignature(ctx, tarPath, releaseSignatureURL(tarURL), log); err != nil {
			return "", false, fmt.Errorf("release: %w", err)
		}

		extractDir := filepath.Join(dir, "extracted")
		if err := os.Mkdir(extractDir, 0o755); err != nil {
			return "", false, fmt.Errorf("release: mkdir extract: %w", err)
		}
		if err := extractTarball(ctx, tarPath, extractDir); err != nil {
			return "", false, fmt.Errorf("release: extract: %w", err)
		}
		manifest, err := parseManifest(filepath.Join(extractDir, "MANIFEST"))
		if err != nil {
			return "", false, fmt.Errorf("release: parse MANIFEST: %w", err)
		}
		if manifest["full_sha"] != fullSHA {
			log("release: this tarball is for %s but the host is on %s",
				manifest["full_sha"], fullSHA)
			return extractDir, false, nil
		}
		log("release: tarball MANIFEST: short=%s build_time=%s go=%s",
			manifest["short_sha"], manifest["build_time"], manifest["go_version"])
		return extractDir, true, nil
	}

	extractDir, matched, err := stageAndCheck(tarURL, sumURL, tarName, sumName)
	if err != nil {
		return false, "", err
	}
	if !matched {
		// "latest" is only the right answer for a host following main. A host
		// on a channel tag (stable) resets its checkout to the promoted commit,
		// so latest will never match it — while the release it actually wants
		// is usually published and reachable. Re-resolve BY COMMIT rather than
		// giving up, or every channel host rebuilds from source forever.
		if !usedDirect {
			// Already resolved by commit and it still disagrees: that is a
			// publisher bug, not a channel mismatch.
			return false, "", fmt.Errorf("release: MANIFEST does not match HEAD %s — tag/build mismatch", fullSHA)
		}
		cTar, cSum, cTag, found := byCommitDownloadURLs(ctx, webBase, fullSHA, urlExists)
		if !found {
			log("release: no release published for %s yet — falling back to source", fullSHA[:7])
			return false, "", nil
		}
		log("release: found %s for this commit", cTag)
		asset := "jabali-release-" + strings.TrimPrefix(cTag, "release-") + ".tar.gz"
		extractDir, matched, err = stageAndCheck(cTar, cSum, asset, asset+".sha256")
		if err != nil {
			return false, "", err
		}
		if !matched {
			log("release: by-commit tarball still disagrees — falling back to source")
			return false, "", nil
		}
	}

	// 6. Install binaries via the same path the source-build flow
	//    uses (atomic rename, mode 0755, owner root). Caller is
	//    responsible for restarting services + running migrations
	//    AFTER we return so it can sequence with its own steps.
	for _, binName := range []string{"jabali-panel", "jabali-agent", "jabali-ssh-shell", "jabali-mailhook"} {
		src := filepath.Join(extractDir, "bin", binName)
		if _, err := os.Stat(src); err != nil {
			return false, "", fmt.Errorf("release: tarball missing bin/%s", binName)
		}
		dst := "/usr/local/bin/" + binName
		if err := installBinaryAtomic(src, dst); err != nil {
			return false, "", fmt.Errorf("release: install %s: %w", binName, err)
		}
		log("release: installed %s", dst)
	}

	// 6b. Install the bundled WordPress plugins (GH #406). The agent installs
	//     jabali-cache FROM /usr/local/share/jabali/wp-plugins; ship it in the
	//     tarball so the release-update path matches the source-build bundle.
	//     Best-effort: an older tarball without wp-plugins/ must not fail the
	//     whole update (the binaries are already in).
	if src := filepath.Join(extractDir, "wp-plugins", "jabali-cache"); dirExists(src) {
		const dst = "/usr/local/share/jabali/wp-plugins/jabali-cache"
		_ = os.MkdirAll("/usr/local/share/jabali/wp-plugins", 0o755)
		cmd := exec.CommandContext(ctx, "rsync", "-a", "--delete", "--exclude=.git", src+"/", dst+"/")
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, "", fmt.Errorf("release: install wp-plugins: %v: %s", err, out)
		}
		_ = exec.CommandContext(ctx, "chown", "-R", "root:root", dst).Run()
		log("release: installed %s", dst)
	} else {
		log("release: tarball has no wp-plugins/jabali-cache (older release) — skipping plugin bundle")
	}

	// 6c. Install the docker-app catalog (M48) the panel-api reads at startup.
	//     Same best-effort posture: an older tarball without it just keeps the
	//     existing on-disk catalog.
	if src := filepath.Join(extractDir, "docker-apps"); dirExists(src) {
		const dst = "/usr/local/share/jabali/docker-apps"
		_ = os.MkdirAll(dst, 0o755)
		cmd := exec.CommandContext(ctx, "rsync", "-a", "--delete", "--exclude=.git", src+"/", dst+"/")
		if out, err := cmd.CombinedOutput(); err != nil {
			log("release: install docker-apps failed (non-fatal): %v: %s", err, out)
		} else {
			log("release: installed %s", dst)
		}
	}

	return true, fullSHA, nil
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// fetchLatestMainSHA hits Gitea's branch-info endpoint and returns the
// full commit SHA at the tip of main. Used to decide whether an update
// is even needed before downloading anything.
func fetchLatestMainSHA(ctx context.Context, base string) (string, error) {
	url := base + "/branches/main"
	body, err := httpGetJSON(ctx, url, releaseAPITimeout)
	if err != nil {
		return "", err
	}
	var resp struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode branch info: %w", err)
	}
	if len(resp.Commit.ID) < releaseShortSHALen {
		return "", fmt.Errorf("branch HEAD missing or short: %q", resp.Commit.ID)
	}
	return resp.Commit.ID, nil
}

// fetchReleaseByTag returns the release object for the given tag, or an
// error wrapping a 404 when no release exists yet.
func fetchReleaseByTag(ctx context.Context, base, tag string) (*releaseResponse, error) {
	url := base + "/releases/tags/" + tag
	body, err := httpGetJSON(ctx, url, releaseAPITimeout)
	if err != nil {
		return nil, err
	}
	var resp releaseResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &resp, nil
}

func pickAssetURL(rel *releaseResponse, name string) string {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// httpGetJSON wraps the boilerplate of GET → check status → return body.
// Returns a wrapped error on non-2xx so the caller can distinguish a
// missing release (404) from a network error.
func httpGetJSON(ctx context.Context, url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a small chunk for diagnostics; full body isn't useful
		// here and JSON error envelopes are small.
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GET %s: HTTP %d %s", url, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	return io.ReadAll(resp.Body)
}

// downloadTo streams a URL to disk with a timeout. Caller cleans up the
// file on error; we don't try to delete it ourselves because some
// failure modes (partial download, kernel ENOSPC) leave forensically
// useful content the operator may want to inspect.
func downloadTo(ctx context.Context, url, dst string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

// verifyTarballChecksum re-computes sha256 of tarPath and compares
// against the first 64-hex-char token in sumPath (the `sha256sum`
// output format: "<hex>  <filename>\n").
func verifyTarballChecksum(tarPath, sumPath string) error {
	expected, err := os.ReadFile(sumPath)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(expected))
	if len(fields) < 1 || len(fields[0]) != 64 {
		return fmt.Errorf("checksum file malformed (expected `<64hex>  filename`): %q", expected)
	}
	want := strings.ToLower(fields[0])

	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// extractTarball shells out to `tar -xzf` rather than re-implementing
// tar parsing in Go. The OS tar is already on every supported host
// (it's a Debian baseline package) and bug-for-bug compatible with
// what build-release.sh produces.
func extractTarball(ctx context.Context, tarPath, dest string) error {
	c := exec.CommandContext(ctx, "tar", "-xzf", tarPath, "-C", dest)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar -xzf: %w\n%s", err, out)
	}
	return nil
}

// parseManifest reads the MANIFEST file shipped inside the release
// tarball. Format: one `key=value` per line, no quoting.
func parseManifest(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// installBinaryAtomic copies src to dst+`.new`, fsyncs, and renames
// into place — POSIX rename is atomic on the same filesystem and the
// `.new` suffix means a torn copy never replaces a working binary.
// Mode 0755 root:root matches what `install -m 0755 src dst` produces.
func installBinaryAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		// On error we want the .new gone; on success Rename already
		// removed it.
		_ = os.Remove(tmp)
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(0o755); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chown(tmp, 0, 0); err != nil {
		// Best-effort: if we're not root, return the error so the
		// caller surfaces it instead of silently leaving the binary
		// with wrong owner.
		if !errors.Is(err, os.ErrPermission) {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// findReleaseForCommit scans the repo's releases and returns the one
// whose target_commitish (the commit the tag points at) matches the
// given full SHA. Returns the matched release + the SHA portion of the
// release tag (so the caller can build the tarball asset filename
// `jabali-release-<that>.tar.gz`).
//
// Why a scan instead of a direct tag GET: build-release.sh names the
// tag with `git rev-parse --short HEAD` which returns whatever length
// git decides is currently unique — 7 on a young repo, 8 once the
// commit count crosses a threshold, more for very large repos. Update
// can't predict the length, so it iterates instead.
func findReleaseForCommit(ctx context.Context, base, fullSHA string) (*releaseResponse, string, error) {
	// GitHub uses ?per_page, Forgejo ?limit — send both; each forge ignores
	// the parameter it doesn't recognise, so a JABALI_RELEASE_API_BASE override
	// pointing at a self-hosted Forgejo still works.
	url := base + "/releases?per_page=20&limit=20"
	body, err := httpGetJSON(ctx, url, releaseAPITimeout)
	if err != nil {
		return nil, "", err
	}
	var list []struct {
		releaseResponse
		TargetCommitish string `json:"target_commitish"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, "", fmt.Errorf("decode releases list: %w", err)
	}
	for i := range list {
		if list[i].TargetCommitish != fullSHA {
			continue
		}
		tag := list[i].TagName
		if !strings.HasPrefix(tag, "release-") {
			continue
		}
		short := tag[len("release-"):]
		copy := list[i].releaseResponse
		return &copy, short, nil
	}
	return nil, "", fmt.Errorf("no release tag points at %s", fullSHA)
}
