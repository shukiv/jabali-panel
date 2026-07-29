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
const LoaderVersion = "15.5.0"

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
// v15.5.0, non-TS, lin x86-64). Keys are the PHP versions jabali offers ionCube
// for. Adding a PHP version or bumping the loader updates this map + the
// LoaderVersion constant in the same change.
var sha256ByVersion = map[string]string{
	"7.4": "c02556b30a25f4370b4e7ece73f83db5952068d4ee8a1165f8baf9df79070258",
	"8.1": "380f2ecad4ba295f66ebd88a758b55a75fc567b17b852e95f4788b0b588ebf98",
	"8.2": "8b6c18809f5d83a0f69cc29e7e0c05a6e21ae6f484e5e8db7d6aca140aefa68a",
	"8.3": "5698913066b2f205fe3d14d32d908df322262237c7890f8ccbbd99ad9af801ce",
	"8.4": "e2a653cad6623968e82ca19a9031039d85808cf07a7ac929b222c25af8eabcf1",
	"8.5": "ad4b4e37cb2a270cb736ba8676521121ba3b56a7bc82287808b46b046d9ba3d4",
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
