package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// scandrop.go — GH #2503 scanner drop folders. A scanner logs in by FTP or
// SFTP to its own login, homed at <home>/domains/<domain>/scans/<name>.
// scandrop.prepare makes that folder; scandrop.ingest moves each finished file
// from it into the shared Files resource's Stalwart folder and then removes
// the drop copy, so the members it is granted to see the scan in webmail.
//
// The drop folder is tenant-writable and the agent is root, so nothing here
// acts by path: the folder is opened beneath the tenant home (openat2
// RESOLVE_BENEATH), every file is opened O_NOFOLLOW from that descriptor, and
// only regular files the tenant or the scanner login owns are read. A file the
// tenant could not read is therefore never copied out.

const (
	// scanDropSettle is how long a file must go unchanged before it is
	// taken: SFTP takes no lock while a file arrives, and a scanner may
	// pause between pages.
	scanDropSettle = 60 * time.Second
	// scanDropPassBudget stops a pass from starting another upload; the
	// rest wait for the next pass.
	scanDropPassBudget = 90 * time.Second
	// scanDropUploadTimeout bounds one file's upload to Stalwart.
	scanDropUploadTimeout = 10 * time.Minute
	// scanDropMaxEntries bounds how many names one pass reads from the
	// drop folder, so a folder full of junk cannot stall the agent.
	scanDropMaxEntries = 10000
	// scanDropDefaultMaxUpload is used when Stalwart's session does not
	// say how large an upload it takes.
	scanDropDefaultMaxUpload = 50_000_000
	// scanDropPage is how many nodes one FileNode/query or /get asks for.
	scanDropPage = 256
)

var (
	// scanDropNow is the clock the settle check reads (test seam).
	scanDropNow = time.Now
	// scanDropHTTPClient uploads scans. It carries no client timeout: each
	// upload has its own deadline, longer than the 5s the JMAP calls use.
	scanDropHTTPClient = func() *http.Client { return &http.Client{} }
	// scanDropFolderIDRe is a Stalwart node id.
	scanDropFolderIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// scanDropPartialSuffixes mark names a client writes while a file is
	// still arriving.
	scanDropPartialSuffixes = []string{".part", ".partial", ".tmp", ".temp", ".filepart", ".crdownload", "~"}
)

func scanDropPrepareHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p agentwire.ScanDropPrepareParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	if _, _, err := agentwire.ValidScanDropDir(tenant.HomeDir, p.DropPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("drop_path %q is not a scanner drop folder under %s", p.DropPath, tenant.HomeDir)}
	}
	if err := prepareScanDrop(tenant, p.DropPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return map[string]any{"ok": true, "drop_path": p.DropPath}, nil
}

// prepareScanDrop makes <home>/domains/<domain>/scans/<name>. The domain's
// own folders are made only when missing (a domain on the older layout keeps
// its files elsewhere) and are never re-owned; scans/ and the drop folder are
// the tenant's, 0750. Every step runs through the openat2-confined scope, so
// a symlink the tenant plants cannot point root's mkdir or chown elsewhere.
func prepareScanDrop(tenant *ftpTenant, dropPath string) error {
	scope, err := filesafe.NewScope(tenant.Username, tenant.Username, []string{tenant.HomeDir})
	if err != nil {
		return fmt.Errorf("home scope for %q: %w", tenant.HomeDir, err)
	}
	scansDir := filepath.Dir(dropPath)
	domainDir := filepath.Dir(scansDir)
	for _, dir := range []string{filepath.Dir(domainDir), domainDir} {
		li, err := scope.ExistsInScope(dir)
		if err != nil {
			return fmt.Errorf("check %q: %w", dir, err)
		}
		if li == nil {
			if err := scope.MkdirChownInScope(dir, 0o755, false, tenant.UID, tenant.GID); err != nil {
				return fmt.Errorf("create %q: %w", dir, err)
			}
			continue
		}
		if !li.IsDir {
			return fmt.Errorf("%q is not a folder", dir)
		}
	}
	for _, dir := range []string{scansDir, dropPath} {
		if err := scope.MkdirChownInScope(dir, 0o750, false, tenant.UID, tenant.GID); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}
	return nil
}

func scanDropIngestHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p agentwire.ScanDropIngestParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	if _, _, err := agentwire.ValidScanDropDir(tenant.HomeDir, p.DropPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("drop_path %q is not a scanner drop folder under %s", p.DropPath, tenant.HomeDir)}
	}
	scanner, aerr := requireFtpSubaccount(tenant, p.ScannerUsername)
	if aerr != nil {
		return nil, aerr
	}
	scannerUID, err := strconv.ParseUint(scanner.Uid, 10, 32)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("scanner login %q uid %q", p.ScannerUsername, scanner.Uid)}
	}
	if _, err := requireEmail(p.HostEmail); err != nil {
		return nil, err
	}
	if !scanDropFolderIDRe.MatchString(p.FolderID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "folder_id is not a node id"}
	}
	if p.MaxFiles < 1 || p.MaxFiles > agentwire.ScanDropMaxFiles {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("max_files must be 1..%d", agentwire.ScanDropMaxFiles)}
	}
	if p.MaxFolderBytes <= 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "max_folder_bytes must be positive"}
	}

	scope, err := filesafe.NewScope(tenant.Username, tenant.Username, []string{tenant.HomeDir})
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("home scope: %v", err)}
	}
	dir, err := scope.OpenDirInScope(p.DropPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "the drop folder is missing: turn the scanner off and on again to make it"}
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("open drop folder: %v", err)}
	}
	defer dir.Close()

	owners := map[uint32]bool{uint32(tenant.UID): true, uint32(scannerUID): true}
	host := func(ctx context.Context) (string, error) { return accountIDByEmail(ctx, p.HostEmail) }
	res, err := ingestScanDrop(ctx, dir, owners, host, p)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return res, nil
}

// scanDropFile is a drop folder entry ready to be taken.
type scanDropFile struct {
	name string
	st   unix.Stat_t
}

// ingestScanDrop moves up to p.MaxFiles settled files from dir into the
// host's folder. dir is the drop folder, already opened confined; owners are
// the uids whose files may be taken; host resolves the host's account id, and
// runs only when a file is ready.
func ingestScanDrop(ctx context.Context, dir *os.File, owners map[uint32]bool, host func(context.Context) (string, error), p agentwire.ScanDropIngestParams) (agentwire.ScanDropIngestResult, error) {
	var res agentwire.ScanDropIngestResult
	ready, waiting, err := scanDropReadyFiles(dir, owners)
	if err != nil {
		return res, err
	}
	res.Waiting = waiting
	if len(ready) == 0 {
		return res, nil
	}
	if len(ready) > p.MaxFiles {
		res.Waiting += len(ready) - p.MaxFiles
		ready = ready[:p.MaxFiles]
	}

	hostID, err := host(ctx)
	if err != nil {
		return res, fmt.Errorf("resolve the shared folder's account: %w", err)
	}
	if hostID == "" {
		return res, errors.New("the shared folder's account does not exist yet")
	}
	folder, err := listScanFolder(ctx, hostID, p.FolderID)
	if err != nil {
		return res, err
	}
	maxUpload := stalwartMaxUploadSize(ctx)

	var problems []string
	start := scanDropNow()
	for i, f := range ready {
		if scanDropNow().Sub(start) > scanDropPassBudget || ctx.Err() != nil {
			res.Waiting += len(ready) - i
			break
		}
		if f.st.Size > maxUpload {
			problems = append(problems, fmt.Sprintf("%s is %s, larger than the %s webmail takes", f.name, scanDropMB(f.st.Size), scanDropMB(maxUpload)))
			continue
		}
		if folder.used+f.st.Size > p.MaxFolderBytes {
			problems = append(problems, fmt.Sprintf("the scans folder is full (%s of %s): delete scans to make room", scanDropMB(folder.used), scanDropMB(p.MaxFolderBytes)))
			res.Waiting += len(ready) - i
			break
		}
		moved, err := moveScan(ctx, dir, f, hostID, p.FolderID, folder)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", f.name, err))
			continue
		}
		if moved {
			res.Moved++
		} else {
			res.Waiting++
		}
	}
	res.Problem = strings.Join(problems, "; ")
	if len(res.Problem) > 480 {
		res.Problem = strings.ToValidUTF8(res.Problem[:480], "") + "…"
	}
	return res, nil
}

// scanDropReadyFiles lists the drop folder's regular files, owned by one of
// owners, that have settled, oldest first. waiting counts the files still
// being written.
func scanDropReadyFiles(dir *os.File, owners map[uint32]bool) (ready []scanDropFile, waiting int, err error) {
	if _, err := dir.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("rewind drop folder: %w", err)
	}
	names, err := dir.Readdirnames(scanDropMaxEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, fmt.Errorf("read drop folder: %w", err)
	}
	now := scanDropNow()
	fd := int(dir.Fd())
	for _, name := range names {
		if scanDropSkipName(name) {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG || !owners[st.Uid] {
			continue
		}
		changed := time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
		if c := time.Unix(st.Ctim.Sec, st.Ctim.Nsec); c.After(changed) {
			changed = c
		}
		if st.Size == 0 || now.Sub(changed) < scanDropSettle {
			waiting++
			continue
		}
		ready = append(ready, scanDropFile{name: name, st: st})
	}
	sort.Slice(ready, func(i, j int) bool {
		a, b := ready[i].st.Mtim, ready[j].st.Mtim
		if a.Sec != b.Sec {
			return a.Sec < b.Sec
		}
		return ready[i].name < ready[j].name
	})
	return ready, waiting, nil
}

// scanDropSkipName reports names never taken: hidden files and the names
// clients give a file still arriving.
func scanDropSkipName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	lower := strings.ToLower(name)
	for _, s := range scanDropPartialSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// scanFolder is the host folder's current files: name → blob, and the bytes
// all of them hold.
type scanFolder struct {
	blobs map[string]string
	used  int64
}

// listScanFolder reads every file node in the host's folder. FileNode/get
// with ids=null stops at Stalwart's maxObjectsInGet without saying so, so the
// ids come from a paged FileNode/query on the folder.
func listScanFolder(ctx context.Context, hostID, folderID string) (*scanFolder, error) {
	var ids []string
	for position := 0; ; {
		var q struct {
			IDs   []string `json:"ids"`
			Total *int     `json:"total"`
		}
		if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/query", map[string]any{
			"accountId":      hostID,
			"filter":         map[string]any{"parentId": folderID},
			"position":       position,
			"limit":          scanDropPage,
			"calculateTotal": true,
		}, &q); err != nil {
			return nil, fmt.Errorf("list the scans folder: %w", err)
		}
		ids = append(ids, q.IDs...)
		position += len(q.IDs)
		if len(q.IDs) == 0 || (q.Total != nil && position >= *q.Total) {
			break
		}
	}
	folder := &scanFolder{blobs: make(map[string]string, len(ids))}
	for len(ids) > 0 {
		n := min(len(ids), scanDropPage)
		var g struct {
			List []struct {
				Name   string `json:"name"`
				Size   int64  `json:"size"`
				BlobID string `json:"blobId"`
			} `json:"list"`
		}
		if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/get", map[string]any{
			"accountId":  hostID,
			"ids":        ids[:n],
			"properties": []string{"name", "size", "blobId"},
		}, &g); err != nil {
			return nil, fmt.Errorf("read the scans folder: %w", err)
		}
		for _, node := range g.List {
			folder.blobs[node.Name] = node.BlobID
			folder.used += node.Size
		}
		ids = ids[n:]
	}
	return folder, nil
}

// moveScan uploads one file and files it in the folder, then removes the drop
// copy. It reports false, with no error, when the file changed while it was
// read: the next pass takes the new content.
func moveScan(ctx context.Context, dir *os.File, f scanDropFile, hostID, folderID string, folder *scanFolder) (bool, error) {
	dirFd := int(dir.Fd())
	fd, err := unix.Openat(dirFd, f.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("open: %w", err)
	}
	file := os.NewFile(uintptr(fd), f.name)
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, fmt.Errorf("stat: %w", err)
	}
	if !scanDropSameFile(&st, &f.st) {
		return false, nil
	}
	// vsftpd holds a write lock on a file it is still receiving.
	lk := unix.Flock_t{Type: unix.F_RDLCK, Whence: io.SeekStart}
	if err := unix.FcntlFlock(uintptr(fd), unix.F_GETLK, &lk); err == nil && lk.Type != unix.F_UNLCK {
		return false, nil
	}

	mediaType := scanDropMediaType(file, f.name)
	blobID, err := uploadScanBlob(ctx, hostID, io.NewSectionReader(file, 0, st.Size), st.Size, mediaType)
	if err != nil {
		return false, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !scanDropSameFile(&after, &st) {
		return false, nil
	}

	name := scanDropNodeName(f.name)
	if !folder.holds(name, blobID) {
		final := folder.freeName(name)
		var set jmapSetResult
		if err := jmapCallWith(ctx, jmapCapFileNode, "FileNode/set", map[string]any{
			"accountId": hostID,
			"create": map[string]any{"s": map[string]any{
				"@type":     "File",
				"parentId":  folderID,
				"name":      final,
				"blobId":    blobID,
				"mediaType": mediaType,
			}},
		}, &set); err != nil {
			return false, fmt.Errorf("file it in the folder: %w", err)
		}
		if _, ok := set.Created["s"]; !ok {
			return false, fmt.Errorf("the folder refused it: %s", string(set.NotCreated["s"]))
		}
		folder.blobs[final] = blobID
		folder.used += st.Size
	}

	// Remove the drop copy only while the name still holds the file just
	// filed: a file written in its place since is a new scan for the next pass.
	var now unix.Stat_t
	if err := unix.Fstatat(dirFd, f.name, &now, unix.AT_SYMLINK_NOFOLLOW); err != nil || !scanDropSameFile(&now, &st) {
		return true, nil
	}
	if err := unix.Unlinkat(dirFd, f.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return true, fmt.Errorf("filed, but the drop copy stays: %w", err)
	}
	return true, nil
}

// scanDropSameFile reports whether a and b describe the same, unchanged
// regular file: same inode, size and modification time. An in-place rewrite
// keeps the inode, so the inode alone is not enough.
func scanDropSameFile(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size &&
		a.Mtim.Sec == b.Mtim.Sec && a.Mtim.Nsec == b.Mtim.Nsec &&
		a.Mode&unix.S_IFMT == unix.S_IFREG
}

// holds reports whether the folder already has this content under name or a
// numbered copy of it — a pass that filed the scan and stopped before removing
// the drop copy.
func (s *scanFolder) holds(name, blobID string) bool {
	for existing, blob := range s.blobs {
		if blob == blobID && (existing == name || scanDropNumberedCopy(existing, name)) {
			return true
		}
	}
	return false
}

// freeName is name, or "base (n).ext" for the first n not taken.
func (s *scanFolder) freeName(name string) string {
	if _, taken := s.blobs[name]; !taken {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; n < 100000; n++ {
		c := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if _, taken := s.blobs[c]; !taken {
			return c
		}
	}
	return fmt.Sprintf("%s (%d)%s", base, scanDropNow().UnixNano(), ext)
}

// scanDropNumberedCopy reports whether existing is "base (n).ext" for name.
func scanDropNumberedCopy(existing, name string) bool {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	mid, ok := strings.CutPrefix(existing, base+" (")
	if !ok {
		return false
	}
	num, ok := strings.CutSuffix(mid, ")"+ext)
	if !ok || num == "" {
		return false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// scanDropNodeName is the name a drop file gets in the folder: valid UTF-8
// with no control characters (a Linux name is any bytes but '/' and NUL).
func scanDropNodeName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
}

// scanDropMediaType is the file's type: from its extension, else sniffed
// from its first bytes.
func scanDropMediaType(f *os.File, name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		if base, _, err := mime.ParseMediaType(t); err == nil {
			return base
		}
	}
	head := make([]byte, 512)
	n, _ := f.ReadAt(head, 0)
	if base, _, err := mime.ParseMediaType(http.DetectContentType(head[:n])); err == nil {
		return base
	}
	return "application/octet-stream"
}

// uploadScanBlob posts a scan to the host account's upload endpoint and
// returns its blob id.
func uploadScanBlob(ctx context.Context, hostID string, body io.Reader, size int64, mediaType string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, scanDropUploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stalwartAdminURLFunc()+"/jmap/upload/"+hostID+"/", body)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", mediaType)
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(jmapAdminUser, token)
	resp, err := scanDropHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		BlobID string `json:"blobId"`
		Size   int64  `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", fmt.Errorf("upload response: %w", err)
	}
	if out.BlobID == "" {
		return "", errors.New("upload returned no blob id")
	}
	if out.Size != 0 && out.Size != size {
		return "", fmt.Errorf("upload stored %d bytes of %d", out.Size, size)
	}
	return out.BlobID, nil
}

// stalwartMaxUploadSize is the largest upload Stalwart's JMAP session says it
// takes, or scanDropDefaultMaxUpload when it does not say.
func stalwartMaxUploadSize(ctx context.Context) int64 {
	ctx, cancel := context.WithTimeout(ctx, stalwartHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stalwartAdminURLFunc()+"/jmap/session", nil)
	if err != nil {
		return scanDropDefaultMaxUpload
	}
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return scanDropDefaultMaxUpload
	}
	req.SetBasicAuth(jmapAdminUser, token)
	resp, err := stalwartHTTPClientFunc().Do(req)
	if err != nil {
		return scanDropDefaultMaxUpload
	}
	defer resp.Body.Close()
	var s struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s) != nil {
		return scanDropDefaultMaxUpload
	}
	var core struct {
		MaxSizeUpload int64 `json:"maxSizeUpload"`
	}
	if json.Unmarshal(s.Capabilities["urn:ietf:params:jmap:core"], &core) != nil || core.MaxSizeUpload <= 0 {
		return scanDropDefaultMaxUpload
	}
	return core.MaxSizeUpload
}

// scanDropMB formats a byte count for a message the owner reads.
func scanDropMB(n int64) string {
	return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + " MB"
}

func init() {
	Default.Register("scandrop.prepare", scanDropPrepareHandler)
	Default.Register("scandrop.ingest", scanDropIngestHandler)
}
