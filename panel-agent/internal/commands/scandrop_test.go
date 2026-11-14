package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// fakeFileStalwart is the slice of Stalwart's JMAP the scanner ingest uses:
// upload, the session's maxSizeUpload, and FileNode query/get/set on one
// folder of one account.
type fakeFileStalwart struct {
	mu        sync.Mutex
	maxUpload int64
	blobs     map[string][]byte // blobId → bytes
	nodes     map[string]fakeFileNode
	nextID    int
	// onUpload / onCreate run inside the request, to change the drop
	// folder at the moment the ingest is between steps.
	onUpload func()
	onCreate func()
	created  []map[string]any
}

type fakeFileNode struct {
	ParentID string
	Name     string
	Size     int64
	BlobID   string
}

const fakeFolderID = "b"

func newFakeFileStalwart(t *testing.T) *fakeFileStalwart {
	t.Helper()
	f := &fakeFileStalwart{maxUpload: 50_000_000, blobs: map[string][]byte{}, nodes: map[string]fakeFileNode{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	prevURL, prevToken := stalwartAdminURLFunc, stalwartAdminTokenFunc
	stalwartAdminURLFunc = func() string { return srv.URL }
	stalwartAdminTokenFunc = func() (string, error) { return "test-token", nil }
	t.Cleanup(func() { stalwartAdminURLFunc, stalwartAdminTokenFunc = prevURL, prevToken })
	return f
}

func (f *fakeFileStalwart) addFile(name string, content []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.blob(content)
	f.nextID++
	f.nodes[fmt.Sprintf("n%d", f.nextID)] = fakeFileNode{ParentID: fakeFolderID, Name: name, Size: int64(len(content)), BlobID: id}
}

func (f *fakeFileStalwart) blob(content []byte) string {
	sum := sha256.Sum256(content)
	id := hex.EncodeToString(sum[:8])
	f.blobs[id] = content
	return id
}

func (f *fakeFileStalwart) names() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, n := range f.nodes {
		out[n.Name] = string(f.blobs[n.BlobID])
	}
	return out
}

func (f *fakeFileStalwart) serve(w http.ResponseWriter, r *http.Request) {
	if _, p, ok := r.BasicAuth(); !ok || p == "" {
		http.Error(w, "no auth", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/jmap/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": map[string]any{
			"urn:ietf:params:jmap:core": map[string]any{"maxSizeUpload": f.maxUpload},
		}})
		return
	case strings.HasPrefix(r.URL.Path, "/jmap/upload/rr/"):
		body, _ := io.ReadAll(r.Body)
		if f.onUpload != nil {
			f.onUpload()
		}
		f.mu.Lock()
		id := f.blob(body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"blobId": id, "size": len(body), "type": r.Header.Get("Content-Type")})
		return
	case r.URL.Path != "/jmap":
		http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		return
	}
	var req struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.MethodCalls) != 1 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var name string
	_ = json.Unmarshal(req.MethodCalls[0][0], &name)
	var args struct {
		AccountID string                    `json:"accountId"`
		Filter    map[string]string         `json:"filter"`
		Position  int                       `json:"position"`
		Limit     int                       `json:"limit"`
		IDs       []string                  `json:"ids"`
		Create    map[string]map[string]any `json:"create"`
	}
	_ = json.Unmarshal(req.MethodCalls[0][1], &args)
	if args.AccountID != "rr" {
		http.Error(w, "wrong account", http.StatusBadRequest)
		return
	}
	var result any
	f.mu.Lock()
	switch name {
	case "FileNode/query":
		var ids []string
		for id, n := range f.nodes {
			if n.ParentID == args.Filter["parentId"] {
				ids = append(ids, id)
			}
		}
		total := len(ids)
		end := min(args.Position+args.Limit, total)
		page := []string{}
		if args.Position < total {
			page = ids[args.Position:end]
		}
		result = map[string]any{"ids": page, "total": total}
	case "FileNode/get":
		list := []any{}
		for _, id := range args.IDs {
			if n, ok := f.nodes[id]; ok {
				list = append(list, map[string]any{"id": id, "name": n.Name, "size": n.Size, "blobId": n.BlobID})
			}
		}
		result = map[string]any{"list": list}
	case "FileNode/set":
		c := args.Create["s"]
		f.created = append(f.created, c)
		taken := false
		for _, n := range f.nodes {
			if n.ParentID == c["parentId"] && n.Name == c["name"] {
				taken = true
			}
		}
		switch {
		case c["@type"] != "File" || c["parentId"] != fakeFolderID:
			result = map[string]any{"notCreated": map[string]any{"s": map[string]any{"type": "invalidProperties"}}}
		case taken:
			result = map[string]any{"notCreated": map[string]any{"s": map[string]any{"type": "alreadyExists"}}}
		default:
			blobID, _ := c["blobId"].(string)
			f.nextID++
			id := fmt.Sprintf("n%d", f.nextID)
			f.nodes[id] = fakeFileNode{ParentID: fakeFolderID, Name: c["name"].(string), Size: int64(len(f.blobs[blobID])), BlobID: blobID}
			result = map[string]any{"created": map[string]any{"s": map[string]any{"id": id}}}
		}
	default:
		f.mu.Unlock()
		http.Error(w, "unexpected "+name, http.StatusBadRequest)
		return
	}
	f.mu.Unlock()
	if name == "FileNode/set" && f.onCreate != nil {
		f.onCreate()
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{name, result, "c0"}}})
}

// scanDropFixture is a drop folder with the clock moved past the settle time.
type scanDropFixture struct {
	dir    string
	owners map[uint32]bool
	params agentwire.ScanDropIngestParams
}

func newScanDropFixture(t *testing.T) *scanDropFixture {
	t.Helper()
	dir := t.TempDir()
	prevNow := scanDropNow
	scanDropNow = func() time.Time { return time.Now().Add(2 * scanDropSettle) }
	t.Cleanup(func() { scanDropNow = prevNow })
	return &scanDropFixture{
		dir:    dir,
		owners: map[uint32]bool{uint32(os.Getuid()): true},
		params: agentwire.ScanDropIngestParams{FolderID: fakeFolderID, MaxFiles: 10, MaxFolderBytes: 1 << 30},
	}
}

func (x *scanDropFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(x.dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (x *scanDropFixture) run(t *testing.T) agentwire.ScanDropIngestResult {
	t.Helper()
	fd, err := os.Open(x.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	host := func(context.Context) (string, error) { return "rr", nil }
	res, err := ingestScanDrop(context.Background(), fd, x.owners, host, x.params)
	if err != nil {
		t.Fatalf("ingestScanDrop: %v", err)
	}
	return res
}

func (x *scanDropFixture) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(x.dir, name))
	return err == nil
}

func TestScanDropIngest_MovesASettledScan(t *testing.T) {
	f := newFakeFileStalwart(t)
	x := newScanDropFixture(t)
	x.write(t, "scan-0001.pdf", "%PDF-1.7 page")

	res := x.run(t)
	if res.Moved != 1 || res.Waiting != 0 || res.Problem != "" {
		t.Fatalf("result = %+v, want 1 moved", res)
	}
	if got := f.names()["scan-0001.pdf"]; got != "%PDF-1.7 page" {
		t.Fatalf("folder holds %q, want the scan", got)
	}
	if c := f.created[0]; c["mediaType"] != "application/pdf" || c["@type"] != "File" {
		t.Fatalf("created %v, want a File of type application/pdf", c)
	}
	if x.exists("scan-0001.pdf") {
		t.Fatal("the drop copy is still there")
	}
}

func TestScanDropIngest_LeavesFilesStillArriving(t *testing.T) {
	f := newFakeFileStalwart(t)
	x := newScanDropFixture(t)
	scanDropNow = time.Now // no time has passed since the write
	x.write(t, "scan.pdf", "half a page")
	x.write(t, ".hidden.pdf", "x")
	x.write(t, "next.pdf.part", "x")
	x.write(t, "empty.pdf", "")

	res := x.run(t)
	if res.Moved != 0 || res.Waiting != 2 {
		t.Fatalf("result = %+v, want 0 moved and 2 waiting (the fresh file and the empty one)", res)
	}
	if len(f.names()) != 0 {
		t.Fatalf("folder = %v, want nothing filed", f.names())
	}
	for _, n := range []string{"scan.pdf", ".hidden.pdf", "next.pdf.part", "empty.pdf"} {
		if !x.exists(n) {
			t.Fatalf("%s was removed", n)
		}
	}
}

// The drop folder is the tenant's: a symlink, a folder or a file another uid
// owns is never read, whatever it points at.
func TestScanDropIngest_ReadsOnlyRegularFilesTheOwnersOwn(t *testing.T) {
	f := newFakeFileStalwart(t)
	x := newScanDropFixture(t)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("root only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(x.dir, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(x.dir, "sub.pdf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if res := x.run(t); res.Moved != 0 || len(f.names()) != 0 {
		t.Fatalf("result = %+v, folder = %v, want nothing taken", res, f.names())
	}

	x.write(t, "someone-elses.pdf", "not yours")
	x.owners = map[uint32]bool{uint32(os.Getuid()) + 1: true}
	if res := x.run(t); res.Moved != 0 || len(f.names()) != 0 {
		t.Fatalf("result = %+v, folder = %v, want a file of another owner left alone", res, f.names())
	}
}

func TestScanDropIngest_NameClashGetsANumber(t *testing.T) {
	f := newFakeFileStalwart(t)
	f.addFile("scan.pdf", []byte("yesterday"))
	f.addFile("scan (1).pdf", []byte("this morning"))
	x := newScanDropFixture(t)
	x.write(t, "scan.pdf", "now")

	if res := x.run(t); res.Moved != 1 {
		t.Fatalf("result = %+v, want 1 moved", res)
	}
	got := f.names()
	if got["scan.pdf"] != "yesterday" || got["scan (1).pdf"] != "this morning" || got["scan (2).pdf"] != "now" {
		t.Fatalf("folder = %v, want the new scan as scan (2).pdf", got)
	}
}

// A pass that filed a scan and stopped before removing the drop copy must not
// file it a second time.
func TestScanDropIngest_AlreadyFiledIsNotFiledTwice(t *testing.T) {
	f := newFakeFileStalwart(t)
	f.addFile("scan.pdf", []byte("other"))
	f.addFile("scan (1).pdf", []byte("same page"))
	x := newScanDropFixture(t)
	x.write(t, "scan.pdf", "same page")

	if res := x.run(t); res.Moved != 1 {
		t.Fatalf("result = %+v, want 1 moved", res)
	}
	if len(f.created) != 0 {
		t.Fatalf("created %v, want no new node", f.created)
	}
	if x.exists("scan.pdf") {
		t.Fatal("the drop copy is still there")
	}
}

func TestScanDropIngest_StopsAtTheFolderCap(t *testing.T) {
	f := newFakeFileStalwart(t)
	f.addFile("old.pdf", []byte("0123456789"))
	x := newScanDropFixture(t)
	x.params.MaxFolderBytes = 15
	x.write(t, "new.pdf", "0123456789")

	res := x.run(t)
	if res.Moved != 0 || res.Waiting != 1 || !strings.Contains(res.Problem, "full") {
		t.Fatalf("result = %+v, want the scan waiting with a full-folder problem", res)
	}
	if !x.exists("new.pdf") || len(f.names()) != 1 {
		t.Fatalf("folder = %v, want the scan left in the drop folder", f.names())
	}
}

func TestScanDropIngest_TooLargeStaysAndSaysWhy(t *testing.T) {
	f := newFakeFileStalwart(t)
	f.maxUpload = 5
	x := newScanDropFixture(t)
	x.write(t, "big.pdf", "0123456789")
	x.write(t, "small.pdf", "0123")

	res := x.run(t)
	if res.Moved != 1 || !strings.Contains(res.Problem, "big.pdf") {
		t.Fatalf("result = %+v, want small.pdf moved and big.pdf named", res)
	}
	if !x.exists("big.pdf") || x.exists("small.pdf") {
		t.Fatal("want big.pdf left and small.pdf taken")
	}
}

// A file rewritten in place while it was uploaded keeps its inode; the size
// and time give it away, and it waits for the next pass.
func TestScanDropIngest_ChangedDuringUploadWaits(t *testing.T) {
	f := newFakeFileStalwart(t)
	x := newScanDropFixture(t)
	x.write(t, "scan.pdf", "page one")
	f.onUpload = func() {
		fh, err := os.OpenFile(filepath.Join(x.dir, "scan.pdf"), os.O_WRONLY|os.O_APPEND, 0)
		if err == nil {
			_, _ = fh.WriteString(" and page two")
			_ = fh.Close()
		}
	}

	res := x.run(t)
	if res.Moved != 0 || res.Waiting != 1 || len(f.names()) != 0 {
		t.Fatalf("result = %+v, folder = %v, want nothing filed", res, f.names())
	}
	if !x.exists("scan.pdf") {
		t.Fatal("the changed file was removed")
	}
}

// A new file put in the old one's place after filing is a new scan: the
// drop copy is removed only while the name still holds the filed file.
func TestScanDropIngest_ReplacedAfterFilingIsKept(t *testing.T) {
	f := newFakeFileStalwart(t)
	x := newScanDropFixture(t)
	x.write(t, "scan.pdf", "first")
	f.onCreate = func() {
		tmp := filepath.Join(x.dir, ".incoming")
		_ = os.WriteFile(tmp, []byte("second"), 0o600)
		_ = os.Rename(tmp, filepath.Join(x.dir, "scan.pdf"))
	}

	if res := x.run(t); res.Moved != 1 {
		t.Fatalf("result = %+v, want the first scan moved", res)
	}
	b, err := os.ReadFile(filepath.Join(x.dir, "scan.pdf"))
	if err != nil || string(b) != "second" {
		t.Fatalf("drop folder scan.pdf = %q, %v; want the second scan kept", b, err)
	}
}

func TestScanDropIngest_MaxFilesPerPass(t *testing.T) {
	newFakeFileStalwart(t)
	x := newScanDropFixture(t)
	x.params.MaxFiles = 2
	for i := range 5 {
		x.write(t, fmt.Sprintf("p%d.pdf", i), fmt.Sprintf("page %d", i))
	}
	if res := x.run(t); res.Moved != 2 || res.Waiting != 3 {
		t.Fatalf("result = %+v, want 2 moved and 3 waiting", res)
	}
}

func TestScanDropNodeName(t *testing.T) {
	if got := scanDropNodeName("a\x01b\xffc.pdf"); got != "a_b_c.pdf" {
		t.Fatalf("scanDropNodeName = %q", got)
	}
}

// prepareScanDrop makes scans/<name> under the domain folder, the tenant's;
// a symlink planted anywhere on the way is refused, never followed.
func TestPrepareScanDrop(t *testing.T) {
	home := t.TempDir()
	tenant := &ftpTenant{Username: "alice", UID: os.Getuid(), GID: os.Getgid(), HomeDir: home}
	drop, err := agentwire.ScanDropDir(home, "example.com", "front-desk")
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareScanDrop(tenant, drop); err != nil {
		t.Fatalf("prepareScanDrop: %v", err)
	}
	for _, d := range []string{filepath.Dir(drop), drop} {
		st, err := os.Lstat(d)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0o750 {
			t.Fatalf("%s: %v %v, want a 0750 folder", d, st, err)
		}
	}
	if err := prepareScanDrop(tenant, drop); err != nil {
		t.Fatalf("second prepareScanDrop: %v", err)
	}

	outside := t.TempDir()
	for _, plant := range []string{
		filepath.Join(home, "domains", "planted.example"),
		filepath.Join(home, "domains", "example.org", "scans"),
	} {
		if err := os.MkdirAll(filepath.Dir(plant), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, plant); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{
		filepath.Join(home, "domains", "planted.example", "scans", "x"),
		filepath.Join(home, "domains", "example.org", "scans", "x"),
	} {
		if err := prepareScanDrop(tenant, d); err == nil {
			t.Errorf("prepareScanDrop(%s) followed a planted symlink", d)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("made %v outside the home", entries)
	}
}
