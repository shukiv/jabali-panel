package api

import (
	"context"
	"os"
	"sync"
	"time"
)

// GoAccess rendering is expensive and the caller is a poller: the log
// modal's iframe re-requests every 10s for as long as it is open, and each
// render is a full goaccess pass over the whole current-day access log
// (hundreds of MB on a busy domain) plus a multi-MB HTML response. Two
// admins with the modal open used to mean two full re-parses every 10s,
// forever — sustained double-digit CPU from idle browser tabs.
//
// The cache collapses all of that into at most one render per log per TTL,
// no matter how many viewers or how fast they poll. A render is reused when
// it is younger than the TTL, and also when it is older but the log has not
// changed (same mtime+size) — an idle domain never re-parses at all.
//
// Renders for one path are single-flighted: concurrent viewers that arrive
// on a cold entry wait for the one in-flight goaccess run instead of each
// spawning their own.

// goaccessCacheTTL bounds re-render frequency. Must be >= the modal's poll
// interval (10s) for the cache to do its job; 30s keeps the dashboard
// usefully fresh while cutting a 3-viewer host from 18 renders/min to 2.
const goaccessCacheTTL = 30 * time.Second

type goaccessCacheEntry struct {
	html     []byte
	mtime    time.Time
	size     int64
	rendered time.Time
}

type goaccessRenderCache struct {
	mu      sync.Mutex
	entries map[string]*goaccessCacheEntry
	// inflight serialises renders per path so N concurrent viewers cause
	// one goaccess process, not N.
	inflight map[string]*sync.Mutex
}

var goaccessCache = &goaccessRenderCache{
	entries:  map[string]*goaccessCacheEntry{},
	inflight: map[string]*sync.Mutex{},
}

// fresh reports a usable cached render for (path, st), or nil.
func (c *goaccessRenderCache) fresh(path string, st os.FileInfo, now time.Time) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok {
		return nil
	}
	if now.Sub(e.rendered) < goaccessCacheTTL {
		return e.html
	}
	// Past the TTL, but an untouched log renders identically — reuse it
	// rather than re-parsing the same bytes.
	if e.mtime.Equal(st.ModTime()) && e.size == st.Size() {
		return e.html
	}
	return nil
}

func (c *goaccessRenderCache) store(path string, st os.FileInfo, html []byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[path] = &goaccessCacheEntry{
		html:     html,
		mtime:    st.ModTime(),
		size:     st.Size(),
		rendered: now,
	}
}

// pathLock returns the per-path render mutex, creating it on first use.
func (c *goaccessRenderCache) pathLock(path string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.inflight[path]
	if !ok {
		m = &sync.Mutex{}
		c.inflight[path] = m
	}
	return m
}

// renderGoAccessCached returns the GoAccess HTML for accessLogPath, running
// goaccess only when no reusable render exists. render is injected so tests
// can count invocations without a goaccess binary.
func renderGoAccessCached(
	ctx context.Context,
	accessLogPath string,
	st os.FileInfo,
	now func() time.Time,
	render func(context.Context, string) ([]byte, error),
) ([]byte, error) {
	if html := goaccessCache.fresh(accessLogPath, st, now()); html != nil {
		return html, nil
	}

	lock := goaccessCache.pathLock(accessLogPath)
	lock.Lock()
	defer lock.Unlock()
	// Re-check after acquiring: a concurrent viewer may have just rendered
	// this path while we waited, which is the whole point of the lock.
	if html := goaccessCache.fresh(accessLogPath, st, now()); html != nil {
		return html, nil
	}

	html, err := render(ctx, accessLogPath)
	if err != nil {
		return nil, err
	}
	goaccessCache.store(accessLogPath, st, html, now())
	return html, nil
}
