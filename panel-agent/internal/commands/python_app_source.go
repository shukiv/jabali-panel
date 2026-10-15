// Python app catalog entries of kind=source (GH #2156) install a full upstream
// app, such as the sic link aggregator, instead of a framework starter. Its
// code is a GitHub commit tarball: the agent downloads it as root (tenant
// egress is filtered), checks it against the sha256 the catalog pins, and
// unpacks it into app_root AS THE TENANT. system_libs names the shared
// libraries an app loads at runtime (Wand → libMagickWand); the agent installs
// the distro's package on demand, the way ensurePythonVenvPackage does for
// python3.X-venv (GH #357), so a library one app needs isn't on every server.
package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

var (
	// pythonSourceURLRe mirrors pyframeworks.SourceURLRe: the only source a
	// scaffold fetches is a GitHub commit tarball (a 40-hex commit, never a
	// branch). Tests point it at a TLS test server.
	pythonSourceURLRe = regexp.MustCompile(`^https://codeload\.github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/tar\.gz/[0-9a-f]{40}$`)
	pythonSourceSHARe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// pythonSourceTransport carries the download; tests swap in a test server's.
	pythonSourceTransport http.RoundTripper = http.DefaultTransport
	// pythonSourceMaxBytes caps the archive (sic's is under 1 MiB).
	pythonSourceMaxBytes int64 = 64 << 20
)

const (
	pythonSourceTimeout = 5 * time.Minute
	// pythonSourceMarker records the pin of the archive a scaffold unpacked into
	// app_root, so a retry after a later step failed may unpack it again.
	pythonSourceMarker = ".jabali-source"
)

func validPythonSource(url, sum string) error {
	if !pythonSourceURLRe.MatchString(url) {
		return csInvalidArg("source_url must be a GitHub commit tarball (https://codeload.github.com/<owner>/<repo>/tar.gz/<commit>)")
	}
	if !pythonSourceSHARe.MatchString(sum) {
		return csInvalidArg("source_sha256 must be a lowercase hex sha256")
	}
	return nil
}

// fetchPythonSource downloads url into a root-only temp file and returns its
// path once its sha256 matches want. Redirects are refused, so the download
// never leaves the host the URL names. On any error nothing is left on disk.
func fetchPythonSource(ctx context.Context, url, want string) (string, error) {
	client := &http.Client{
		Transport: pythonSourceTransport,
		Timeout:   pythonSourceTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the source archive redirects elsewhere; redirects are not followed")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp("", "jabali-pysrc-*.tar.gz")
	if err != nil {
		return "", err
	}
	path := f.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, pythonSourceMaxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = fmt.Errorf("download %s: %w", url, err)
	case n > pythonSourceMaxBytes:
		err = fmt.Errorf("the source archive is larger than %d MiB", pythonSourceMaxBytes>>20)
	default:
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			err = fmt.Errorf("the source archive's sha256 %s does not match the catalog's %s", got, want)
		}
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// checkPythonSourceRoot refuses to unpack an app over the tenant's own files:
// app_root may hold only the venv the scaffold made, or files an earlier
// attempt of the same pin unpacked (its marker names that pin).
func checkPythonSourceRoot(appRoot, sum string) error {
	entries, err := os.ReadDir(appRoot)
	if err != nil {
		return fmt.Errorf("read app directory: %w", err)
	}
	foreign := ""
	for _, e := range entries {
		if e.Name() != "venv" && e.Name() != pythonSourceMarker {
			foreign = e.Name()
			break
		}
	}
	if foreign == "" || pythonSourceMarkerIs(filepath.Join(appRoot, pythonSourceMarker), sum) {
		return nil
	}
	return csInvalidArg(fmt.Sprintf("app directory %s is not empty (it holds %s); this app is unpacked into a new directory", appRoot, foreign))
}

// pythonSourceMarkerIs reports whether the marker at path names sum. The
// marker sits in the tenant's tree, so it is opened without following a
// symlink and read only up to a line.
func pythonSourceMarkerIs(path, sum string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(f, 128))
	return strings.TrimSpace(string(b)) == sum
}

// extractPythonSourceAsUser unpacks the verified archive into app_root as the
// tenant, so the files land tenant-owned and the kernel bounds every write to
// what the tenant could already write.
func extractPythonSourceAsUser(ctx context.Context, username, appRoot, archive string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := execCommandContext(ctx, "sudo", "-u", username, "-H", "tar", "-xzf", "-", "-C", appRoot,
		"--strip-components=1", "--no-same-owner", "--no-same-permissions")
	cmd.Stdin = f
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unpack: %v: %s", err, lastLines(string(out), 6))
	}
	return nil
}

// scaffoldPythonSource is the kind=source step of app.python.scaffold.
func scaffoldPythonSource(ctx context.Context, p pythonScaffoldParams) error {
	if err := checkPythonSourceRoot(p.AppRoot, p.SourceSHA256); err != nil {
		return err
	}
	if err := writeAsUser(ctx, p.Username, filepath.Join(p.AppRoot, pythonSourceMarker), p.SourceSHA256+"\n"); err != nil {
		return scaffoldErr("write source marker: %v", err)
	}
	archive, err := fetchPythonSource(ctx, p.SourceURL, p.SourceSHA256)
	if err != nil {
		return scaffoldErr("source: %v", err)
	}
	defer os.Remove(archive)
	if err := extractPythonSourceAsUser(ctx, p.Username, p.AppRoot, archive); err != nil {
		return scaffoldErr("source: %v", err)
	}
	return nil
}

// pythonSystemLib is how the agent finds and installs one system_libs name.
type pythonSystemLib struct {
	soPrefix  string         // the library's ldconfig name prefix
	aptPrefix string         // the apt package names to look among
	pkgRe     *regexp.Regexp // the runtime package among them (not -dev, not HDRI)
}

// pythonSystemLibs is the fixed set of libraries an entry may ask for; a
// catalog entry never names a package itself.
var pythonSystemLibs = map[string]pythonSystemLib{
	// Wand (ImageMagick's Python binding) loads libMagickWand-{6,7}.Q16 with
	// ctypes. Ubuntu 24.04 ships libmagickwand-6.q16-7t64, Debian 13
	// libmagickwand-7.q16-10, Debian 12 libmagickwand-6.q16-6.
	"magickwand": {soPrefix: "libMagickWand-", aptPrefix: "libmagickwand-", pkgRe: regexp.MustCompile(`^libmagickwand-[0-9]+\.q16-[0-9]+(t64)?$`)},
}

func validPythonSystemLibs(libs []string) error {
	for _, lib := range libs {
		if _, ok := pythonSystemLibs[lib]; !ok {
			return csInvalidArg(fmt.Sprintf("system_libs: unknown library %q", lib))
		}
	}
	return nil
}

// pythonSystemLibMu keeps two apps from running the install unit at once.
var pythonSystemLibMu sync.Mutex

// ensurePythonSystemLibs installs the package of each library that ldconfig
// doesn't list yet. Cheap when they are all there (one ldconfig -p).
func ensurePythonSystemLibs(ctx context.Context, libs []string) error {
	if err := validPythonSystemLibs(libs); err != nil {
		return err
	}
	if len(libs) == 0 {
		return nil
	}
	pythonSystemLibMu.Lock()
	defer pythonSystemLibMu.Unlock()
	for _, name := range libs {
		lib := pythonSystemLibs[name]
		if ldconfigLists(ctx, lib.soPrefix) {
			continue
		}
		pkg, err := pythonSystemLibPackage(ctx, lib)
		if err != nil {
			return systemLibError(fmt.Errorf("%s: %w", name, err))
		}
		cmd := execCommandContext(ctx, "systemd-run",
			"--pipe", "--wait", "--quiet", "--collect",
			"--unit=jabali-python-lib-install",
			"--service-type=oneshot",
			"--setenv=DEBIAN_FRONTEND=noninteractive", "--",
			"apt-get", "install", "-y", "-q", "--no-install-recommends", pkg)
		if out, err := cmd.CombinedOutput(); err != nil {
			return systemLibError(fmt.Errorf("install %s: %v: %s", pkg, err, lastLines(string(out), 6)))
		}
		if !ldconfigLists(ctx, lib.soPrefix) {
			return systemLibError(fmt.Errorf("%s is installed but ldconfig lists no %s* library", pkg, lib.soPrefix))
		}
	}
	return nil
}

func ldconfigLists(ctx context.Context, prefix string) bool {
	out, err := execCommandContext(ctx, "ldconfig", "-p").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

// pythonSystemLibPackage picks the newest runtime package apt knows of.
func pythonSystemLibPackage(ctx context.Context, lib pythonSystemLib) (string, error) {
	out, err := execCommandContext(ctx, "apt-cache", "pkgnames", lib.aptPrefix).Output()
	if err != nil {
		return "", fmt.Errorf("apt-cache pkgnames %s: %w", lib.aptPrefix, err)
	}
	var found []string
	for _, name := range strings.Fields(string(bytes.TrimSpace(out))) {
		if lib.pkgRe.MatchString(name) {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("apt knows no %s* runtime package on this server", lib.aptPrefix)
	}
	sort.Slice(found, func(i, j int) bool { return versionLess(found[i], found[j]) })
	return found[len(found)-1], nil
}

// versionLess orders names by their numeric runs (6.q16-7 < 7.q16-10).
func versionLess(a, b string) bool {
	na, nb := numericRuns(a), numericRuns(b)
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	if len(na) != len(nb) {
		return len(na) < len(nb)
	}
	return a < b
}

var digitsRe = regexp.MustCompile(`[0-9]+`)

func numericRuns(s string) []int {
	var out []int
	for _, m := range digitsRe.FindAllString(s, -1) {
		n, _ := strconv.Atoi(m)
		out = append(out, n)
	}
	return out
}

// systemLibError reports a failed install as an internal agent error.
func systemLibError(err error) error {
	return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "system library: " + err.Error()}
}
