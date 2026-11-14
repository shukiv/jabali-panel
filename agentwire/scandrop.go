package agentwire

import (
	"errors"
	"regexp"
	"strings"
)

// Scanner drop folders (GH #2503). A scanner logs in by FTP or SFTP to its own
// login, homed at a drop folder in the domain's directory, outside
// public_html. panel-api picks the folder and asks the agent to create it
// (scandrop.prepare), then on every pass asks the agent to move finished files
// from it into the shared Files resource's Stalwart folder (scandrop.ingest).
// Both sides use ScanDropDir and ValidScanDropDir, so they cannot drift apart.

var (
	// scanDropDomainRe is a lower-case domain name (IDNs as xn--).
	scanDropDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	// scanDropNameRe is the drop folder's own name: the shared resource's
	// address local part. It never starts with a dot, so it is never "." or "..".
	scanDropNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// ErrScanDropDir is a drop folder path that is not ScanDropDir's shape.
var ErrScanDropDir = errors.New("agentwire: not a scanner drop folder path")

// ScanDropDir is the drop folder for the shared resource named name on domain,
// under the tenant's home: <home>/domains/<domain>/scans/<name>.
func ScanDropDir(tenantHome, domain, name string) (string, error) {
	if !scanDropDomainRe.MatchString(domain) || len(domain) > 253 || !scanDropNameRe.MatchString(name) {
		return "", ErrScanDropDir
	}
	return strings.TrimRight(tenantHome, "/") + "/domains/" + domain + "/scans/" + name, nil
}

// ValidScanDropDir reports whether dir is exactly a ScanDropDir under
// tenantHome, and returns its domain and name.
func ValidScanDropDir(tenantHome, dir string) (domain, name string, err error) {
	prefix := strings.TrimRight(tenantHome, "/") + "/domains/"
	rest, ok := strings.CutPrefix(dir, prefix)
	if !ok || tenantHome == "" {
		return "", "", ErrScanDropDir
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "scans" {
		return "", "", ErrScanDropDir
	}
	want, err := ScanDropDir(tenantHome, parts[0], parts[2])
	if err != nil || want != dir {
		return "", "", ErrScanDropDir
	}
	return parts[0], parts[2], nil
}

// ScanDropPrepareParams is scandrop.prepare's input: make DropPath and its
// scans folder, owned by the tenant.
type ScanDropPrepareParams struct {
	TenantUsername string `json:"tenant_username"`
	DropPath       string `json:"drop_path"`
}

// ScanDropIngestParams is scandrop.ingest's input.
type ScanDropIngestParams struct {
	TenantUsername string `json:"tenant_username"`
	// ScannerUsername is the scanner's login. Files it or the tenant owns are
	// taken; any other owner is left alone.
	ScannerUsername string `json:"scanner_username"`
	DropPath        string `json:"drop_path"`
	// HostEmail is the shared resource's host principal; FolderID is its
	// shared folder node (shared_resources.collection_id).
	HostEmail string `json:"host_email"`
	FolderID  string `json:"folder_id"`
	// MaxFiles is the most files one call moves.
	MaxFiles int `json:"max_files"`
	// MaxFolderBytes caps the bytes kept in the host's files; a file that
	// would pass it stays in the drop folder.
	MaxFolderBytes int64 `json:"max_folder_bytes"`
}

// ScanDropIngestResult is scandrop.ingest's output.
type ScanDropIngestResult struct {
	// Moved files are in the Stalwart folder and gone from the drop folder.
	Moved int `json:"moved"`
	// Waiting files are still being written, or past MaxFiles: a later call
	// takes them.
	Waiting int `json:"waiting"`
	// Problem says why files the owner can act on stay in the drop folder
	// (too big, folder full); "" when none.
	Problem string `json:"problem,omitempty"`
}

// ScanDropMaxFiles bounds ScanDropIngestParams.MaxFiles.
const ScanDropMaxFiles = 50
