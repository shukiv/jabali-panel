package commands

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeCacheEntry creates a cache file at the nginx levels=1:2 location for
// key, containing the KEY: header line — the shape the walk purge parses.
func writeCacheEntry(t *testing.T, root, key string) string {
	t.Helper()
	sum := md5.Sum([]byte(key))
	h := hex.EncodeToString(sum[:])
	dir := filepath.Join(root, h[31:32], h[29:31])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, h)
	if err := os.WriteFile(fp, []byte("HEADER\nKEY: "+key+"\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	return fp
}

// GH #610 interaction: a domain with cache_query_allowlist stores separate
// entries per allowlisted query combination; the hash fast path can't
// enumerate them, so the handler must ALSO walk for such domains and evict
// the query variants of a purged path.
func TestTargetedPurgeEvictsQueryAllowlistVariants(t *testing.T) {
	root := t.TempDir()
	vhosts := t.TempDir()
	oldRoot, oldVhosts := fastcgiCacheRoot, purgeVhostDir
	fastcgiCacheRoot, purgeVhostDir = root, vhosts
	defer func() { fastcgiCacheRoot, purgeVhostDir = oldRoot, oldVhosts }()

	const dom = "shop.example.com"
	if err := os.WriteFile(filepath.Join(vhosts, dom+".conf"),
		[]byte("server {\n  fastcgi_cache_key \"$scheme$request_method$host$jabali_cache_path$jabali_qsep$jabali_qkey\";\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	clean := writeCacheEntry(t, root, "httpsGET"+dom+"/shop")
	variant1 := writeCacheEntry(t, root, "httpsGET"+dom+"/shop?paged=2&")
	variant2 := writeCacheEntry(t, root, "httpsGET"+dom+"/shop?paged=3&")
	otherPath := writeCacheEntry(t, root, "httpsGET"+dom+"/about")
	otherHost := writeCacheEntry(t, root, "httpsGETother.example.com/shop?paged=2&")

	out, err := nginxCachePurgeHandler(context.Background(),
		json.RawMessage(`{"domain":"`+dom+`","paths":["/shop"]}`))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	res := out.(map[string]any)
	if got := res["purged"].(int); got != 3 {
		t.Errorf("purged = %d, want 3 (clean + 2 query variants)", got)
	}
	for _, gone := range []string{clean, variant1, variant2} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("entry should be purged: %s", gone)
		}
	}
	for _, kept := range []string{otherPath, otherHost} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("entry should survive: %s", kept)
		}
	}
}

// A domain WITHOUT the query allowlist keeps the fast path only: exact hash
// unlinks, no walk (other entries untouched, no truncated field).
func TestTargetedPurgeFastPathWithoutAllowlist(t *testing.T) {
	root := t.TempDir()
	vhosts := t.TempDir()
	oldRoot, oldVhosts := fastcgiCacheRoot, purgeVhostDir
	fastcgiCacheRoot, purgeVhostDir = root, vhosts
	defer func() { fastcgiCacheRoot, purgeVhostDir = oldRoot, oldVhosts }()

	const dom = "plain.example.com"
	if err := os.WriteFile(filepath.Join(vhosts, dom+".conf"),
		[]byte("server {\n  fastcgi_cache_key \"$scheme$request_method$host$jabali_cache_path\";\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	clean := writeCacheEntry(t, root, "httpsGET"+dom+"/page")
	stray := writeCacheEntry(t, root, "httpsGET"+dom+"/page?paged=2&") // shouldn't exist for a non-qallow domain; must survive the fast path

	out, err := nginxCachePurgeHandler(context.Background(),
		json.RawMessage(`{"domain":"`+dom+`","paths":["/page"]}`))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	res := out.(map[string]any)
	if got := res["purged"].(int); got != 1 {
		t.Errorf("purged = %d, want 1", got)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("clean entry should be purged")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("non-walk fast path must not touch other entries")
	}
}
