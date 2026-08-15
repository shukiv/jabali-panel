// Package wordpressplugin pulls a WordPress site into Jabali from the
// jabali-migrator plugin's token-authed REST API (GH #648 — the PULL model).
// Jabali drives the transfer: it fetches the manifest, the DB dump, and the
// files over an SSRF/rebind-safe HTTP client, stages dump.sql + files.tar.gz,
// then hands off to the SHARED import (migrate import-wp) — the same spine the
// wordpress_ssh path (#647) uses. No public ingress on the Jabali side.
package wordpressplugin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

const restPath = "/wp-json/jabali-migrator/v1"

// Client talks to one source site's jabali-migrator plugin.
type Client struct {
	base   string // site URL, no trailing slash
	token  string
	hc     *http.Client
	budget *hostreserve.Budget // nil = unbudgeted (JAB-240)
}

// SetBudget attaches the job's cumulative byte budget (JAB-240). Every
// transfer — DB dump, files archive, per-file fallback — draws from the
// same allowance, so a source that lies in one channel cannot recover
// headroom in another.
func (c *Client) SetBudget(b *hostreserve.Budget) { c.budget = b }

// New builds a client. siteURL is the source WordPress home (https://site);
// the plugin REST path is appended. The HTTP client is SSRF/rebind-safe.
func New(siteURL, token string, allowPrivate bool) *Client {
	return &Client{
		base:  strings.TrimRight(siteURL, "/"),
		token: token,
		hc:    migrate.SafeHTTPClient(allowPrivate, 6*time.Hour),
	}
}

func (c *Client) get(ctx context.Context, endpoint string, q url.Values) (*http.Response, error) {
	u := c.base + restPath + endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("%s -> HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Facts mirrors the plugin's /manifest payload (the subset Jabali needs).
type Facts struct {
	Home        string `json:"home"`
	SiteURL     string `json:"siteurl"`
	TablePrefix string `json:"table_prefix"`
	WPVersion   string `json:"wp_version"`
	PHPVersion  string `json:"php_version"`
	Multisite   bool   `json:"multisite"`
	DBBytes     int64  `json:"db_bytes"`
	FileCount   int64  `json:"file_count"`
	FileBytes   int64  `json:"file_bytes"`
	WPRoot      string `json:"wp_root"`
}

// Ping verifies reachability + token before any heavy transfer.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.PingInfo(ctx)
	return err
}

// PingResult is the /ping payload (used by the pre-flight handshake).
type PingResult struct {
	OK      bool   `json:"ok"`
	Plugin  string `json:"plugin"`
	Version string `json:"version"`
	SiteURL string `json:"site_url"`
}

// PingInfo returns the /ping payload — reachability + token check + the plugin
// version (so the wizard can warn when the source plugin is too old to export).
func (c *Client) PingInfo(ctx context.Context) (*PingResult, error) {
	resp, err := c.get(ctx, "/ping", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var pr PingResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&pr); err != nil {
		return nil, fmt.Errorf("ping decode: %w", err)
	}
	return &pr, nil
}

// Manifest fetches the site facts.
func (c *Client) Manifest(ctx context.Context) (*Facts, error) {
	resp, err := c.get(ctx, "/manifest", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var f Facts
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&f); err != nil {
		return nil, fmt.Errorf("manifest decode: %w", err)
	}
	if f.TablePrefix == "" {
		return nil, fmt.Errorf("manifest missing table_prefix")
	}
	return &f, nil
}

// ExportDatabase streams /db-export into dstSQL (local staging).
func (c *Client) ExportDatabase(ctx context.Context, dstSQL string) error {
	if err := os.MkdirAll(filepath.Dir(dstSQL), 0o750); err != nil {
		return err
	}
	resp, err := c.get(ctx, "/db-export", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.OpenFile(dstSQL, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer out.Close()
	// JAB-240: budgeted + reserve-guarded — the source controls this
	// stream's length, the host floor and job budget bound it.
	if _, err := io.Copy(c.budget.Writer(out, filepath.Dir(dstSQL)), resp.Body); err != nil {
		return fmt.Errorf("db-export copy: %w", err)
	}
	return nil
}

type filesManifest struct {
	Root  string `json:"root"`
	Files []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
}

// PullFilesTarball downloads the source files into dstTarball. It PREFERS the
// single-request /files-archive stream (one HTTP round-trip — orders of
// magnitude faster than file-by-file over the internet) and falls back to the
// per-file assembly for older plugins that lack that endpoint.
func (c *Client) PullFilesTarball(ctx context.Context, dstTarball string, maxTotalBytes int64) error {
	if err := c.pullFilesArchive(ctx, dstTarball); err == nil {
		return nil
	}
	return c.pullFilesByFile(ctx, dstTarball, maxTotalBytes)
}

// pullFilesArchive streams /files-archive (a single gzip tarball) to dstTarball.
func (c *Client) pullFilesArchive(ctx context.Context, dstTarball string) error {
	resp, err := c.get(ctx, "/files-archive", nil)
	if err != nil {
		return err // 404/501 on old plugins -> caller falls back
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(dstTarball), 0o750); err != nil {
		return err
	}
	tf, err := os.OpenFile(dstTarball, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer tf.Close()
	n, err := io.Copy(c.budget.Writer(tf, filepath.Dir(dstTarball)), resp.Body)
	if err != nil {
		return fmt.Errorf("files-archive copy: %w", err)
	}
	if n < 1024 { // suspiciously small -> treat as failure, fall back
		return fmt.Errorf("files-archive returned only %d bytes", n)
	}
	return nil
}

// pullFilesByFile fetches /files-manifest, then each /file, assembling a gzip
// tarball. Fallback for plugins without /files-archive.
func (c *Client) pullFilesByFile(ctx context.Context, dstTarball string, maxTotalBytes int64) error {
	resp, err := c.get(ctx, "/files-manifest", nil)
	if err != nil {
		return err
	}
	var fm filesManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&fm); err != nil {
		resp.Body.Close()
		return fmt.Errorf("files-manifest decode: %w", err)
	}
	resp.Body.Close()

	if err := os.MkdirAll(filepath.Dir(dstTarball), 0o750); err != nil {
		return err
	}
	tf, err := os.OpenFile(dstTarball, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer tf.Close()
	// JAB-240: budget the BYTES THAT LAND ON DISK (post-gzip), plus the
	// host-reserve cadence — the per-entry read caps below bound source
	// reads, but only this wrapper bounds actual staging growth.
	gz := gzip.NewWriter(c.budget.Writer(tf, filepath.Dir(dstTarball)))
	tw := tar.NewWriter(gz)

	var total int64
	for _, entry := range fm.Files {
		rel := path.Clean(entry.Path)
		if rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") ||
			strings.HasPrefix(rel, "/") || rel == ".." {
			continue // untrusted path — skip anything not strictly relative
		}
		total += entry.Size
		if maxTotalBytes > 0 && total > maxTotalBytes {
			tw.Close()
			gz.Close()
			return fmt.Errorf("file transfer exceeded manifest budget (%d bytes)", maxTotalBytes)
		}
		if err := c.tarOneFile(ctx, tw, rel, entry.Size); err != nil {
			tw.Close()
			gz.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func (c *Client) tarOneFile(ctx context.Context, tw *tar.Writer, rel string, size int64) error {
	q := url.Values{}
	q.Set("path", rel)
	resp, err := c.get(ctx, "/file", q)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rel, err)
	}
	defer resp.Body.Close()
	if err := tw.WriteHeader(&tar.Header{
		Name:     rel,
		Mode:     0o644,
		Size:     size,
		Typeflag: tar.TypeReg,
		ModTime:  time.Unix(0, 0),
	}); err != nil {
		return err
	}
	// JAB-240: `size` comes from the source's manifest. An entry the job
	// budget cannot cover fails the pull OUTRIGHT — fetching what fits and
	// zero-padding the rest would write attacker-declared bytes through
	// the tar writer unbudgeted (the padding path below trusts `size`).
	if c.budget != nil && c.budget.Remaining() < size {
		return fmt.Errorf("copy %s: manifest entry of %d bytes exceeds the remaining job budget", rel, size)
	}
	n, err := io.Copy(tw, io.LimitReader(resp.Body, size))
	if err != nil {
		return fmt.Errorf("copy %s: %w", rel, err)
	}
	if c.budget != nil {
		if err := c.budget.Consume(n); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	// Pad if the source returned fewer bytes than the manifest claimed, so the
	// tar entry stays valid (tar requires exactly Size bytes).
	if n < size {
		if _, err := tw.Write(make([]byte, size-n)); err != nil {
			return err
		}
	}
	return nil
}
