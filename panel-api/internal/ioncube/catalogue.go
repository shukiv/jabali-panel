// Package ioncube is the pinned catalogue + fetch logic for the ionCube
// Loader. panel-api owns the download (ADR-0050: the agent has no outbound
// HTTPS); the agent receives verified bytes and installs the .so.
//
// State is live (mirrors ADR-0031): the catalogue only pins WHAT a trusted
// loader is (URL + per-version sha256), never WHICH versions a box has
// installed — that is read from the filesystem by the agent.
package ioncube

import (
	"fmt"
	"regexp"
	"sort"
)

// LoaderVersion is the ionCube Loader release this catalogue's checksums pin.
// Bumping the loader = new tarball → new per-version sha256 → a panel release
// (same cadence contract as the ADR-0031 extension catalogue).
const LoaderVersion = "15.5.1"

// TarballURL is ionCube's official Linux x86-64 loaders archive. It is a
// rolling "latest" URL; integrity is enforced by verifying each extracted .so
// against SHA256ByVersion below, NOT by trusting the URL. A loader bump upstream
// makes the sha mismatch → install hard-fails with a clear "catalogue out of
// date" message rather than installing an unverified blob.
const TarballURL = "https://downloads.ioncube.com/loader_downloads/ioncube_loaders_lin_x86-64.tar.gz"

// soNameFor is the file name of the non-thread-safe Linux x86-64 loader for a
// PHP version inside the tarball's ioncube/ directory.
func soNameFor(version string) string {
	return "ioncube_loader_lin_" + version + ".so"
}

// SHA256ByVersion pins the sha256 of each shipped loader .so (ionCube Loader
// v15.5.1, non-TS, lin x86-64). Keys are the PHP versions jabali offers ionCube
// for. Adding a PHP version or bumping the loader updates this map + the
// LoaderVersion constant in the same change.
var sha256ByVersion = map[string]string{
	"7.4": "a8b2de9ceb50e813e22e31cfd6ebed891dae70e44cfa4c00efb268160d68630d",
	"8.1": "ff3a9b407592a116907180c22378c7a8b4aaec0b071b2027aad244d6d0a6f30d",
	"8.2": "2da620c94158eb8d89c3cacbeb79ad671a783a58d61ee28fce29567ea8c8d31d",
	"8.3": "4bdbb9bba091bedcf995b06a8296e2db761bc593d048b1905d3b018df46a3fcf",
	"8.4": "17cc326b10182b1716f722823972083119f4826c74298c1dd1dfb6f9eddb353b",
	"8.5": "96c89075a07d0ef8c4b2f28a7d5facb0f2597069da2276a50f4c7f3920a7d4bb",
}

var versionRegex = regexp.MustCompile(`^\d+\.\d+$`)

// ValidVersion reports whether jabali ships an ionCube loader for a PHP version.
// Panel-api pre-validates here; the agent re-validates the version format at the
// trust boundary.
func ValidVersion(version string) bool {
	if !versionRegex.MatchString(version) {
		return false
	}
	_, ok := sha256ByVersion[version]
	return ok
}

// SupportedVersions returns the PHP versions with a pinned loader, sorted.
func SupportedVersions() []string {
	vs := make([]string, 0, len(sha256ByVersion))
	for v := range sha256ByVersion {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	return vs
}

// SHA256For returns the pinned loader sha256 for a PHP version.
func SHA256For(version string) (string, error) {
	sum, ok := sha256ByVersion[version]
	if !ok {
		return "", fmt.Errorf("ionCube loader not cataloged for PHP %s (supported: %v)", version, SupportedVersions())
	}
	return sum, nil
}
