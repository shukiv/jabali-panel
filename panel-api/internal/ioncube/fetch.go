package ioncube

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// maxLoaderBytes bounds a single extracted .so (loaders are ~2.5 MB; 16 MB is a
// generous ceiling that still stops a malicious/oversized member from being
// buffered into memory).
const maxLoaderBytes = 16 << 20

// Fetcher downloads + extracts a verified ionCube loader. URL and Client are
// overridable so tests can serve a synthetic tarball without hitting the
// network. Zero value uses the pinned TarballURL + a 60s http client.
type Fetcher struct {
	URL    string
	Client *http.Client
}

// FetchLoader downloads the loaders tarball, extracts the .so for the given PHP
// version, verifies it against the pinned catalogue sha256, and returns the raw
// bytes. panel-api hands these to the agent's php.ioncube.install (ADR-0050: the
// agent never fetches). A sha mismatch is a hard error — the caller MUST NOT
// install unverified bytes.
func (f Fetcher) FetchLoader(ctx context.Context, version string) ([]byte, error) {
	want, err := SHA256For(version)
	if err != nil {
		return nil, err
	}
	url := f.URL
	if url == "" {
		url = TarballURL
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download loaders tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download loaders tarball: unexpected status %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gunzip tarball: %w", err)
	}
	defer gz.Close()

	target := soNameFor(version)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Match the loader by base name regardless of the archive's top dir.
		if path.Base(hdr.Name) != target {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxLoaderBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read loader %s: %w", target, err)
		}
		if len(data) > maxLoaderBytes {
			return nil, fmt.Errorf("loader %s exceeds %d bytes", target, maxLoaderBytes)
		}
		got := hex.EncodeToString(sha256Sum(data))
		if !strings.EqualFold(got, want) {
			return nil, fmt.Errorf("loader %s sha256 mismatch: got %s, want %s (ionCube may have updated the loader; the catalogue needs a panel release)", target, got, want)
		}
		return data, nil
	}
	return nil, fmt.Errorf("loader %s not found in tarball", target)
}

func sha256Sum(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}
