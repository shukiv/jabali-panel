package ioncube

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidVersion(t *testing.T) {
	for _, v := range []string{"7.4", "8.1", "8.4", "8.5"} {
		if !ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"8.0", "9.9", "8", "8.4.1", "../8.4", ""} {
		if ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = true, want false", v)
		}
	}
}

func TestSupportedVersionsSorted(t *testing.T) {
	got := SupportedVersions()
	if len(got) != 6 {
		t.Fatalf("SupportedVersions len = %d, want 6", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("SupportedVersions not sorted: %v", got)
		}
	}
}

// makeTarball builds a gzip'd tar containing one member named for the loader
// plus a decoy, and returns (bytes, sha256-of-loader-hex).
func makeTarball(t *testing.T, version string, loader []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	write("ioncube/README.txt", []byte("decoy"))
	write("ioncube/"+soNameFor(version), loader)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serveBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withCatalogueEntry temporarily pins a sha for a test version, restoring after.
func withCatalogueEntry(t *testing.T, version, sha string) {
	t.Helper()
	orig, had := sha256ByVersion[version]
	sha256ByVersion[version] = sha
	t.Cleanup(func() {
		if had {
			sha256ByVersion[version] = orig
		} else {
			delete(sha256ByVersion, version)
		}
	})
}

func TestFetchLoader_VerifiesAndExtracts(t *testing.T) {
	loader := []byte("\x7fELF fake ioncube loader body")
	sum := sha256.Sum256(loader)
	withCatalogueEntry(t, "8.4", hex.EncodeToString(sum[:]))

	tarball := makeTarball(t, "8.4", loader)
	srv := serveBytes(t, tarball)

	got, err := Fetcher{URL: srv.URL, Client: srv.Client()}.FetchLoader(context.Background(), "8.4")
	if err != nil {
		t.Fatalf("FetchLoader: %v", err)
	}
	if !bytes.Equal(got, loader) {
		t.Errorf("extracted bytes mismatch")
	}
}

func TestFetchLoader_RejectsShaMismatch(t *testing.T) {
	loader := []byte("real loader")
	// Pin a wrong-but-valid-length sha for 8.4 so extraction succeeds but the
	// integrity check fails (the "ionCube updated the loader" case).
	withCatalogueEntry(t, "8.4", "deadbeef00000000000000000000000000000000000000000000000000000000")

	srv := serveBytes(t, makeTarball(t, "8.4", loader))
	_, err := Fetcher{URL: srv.URL, Client: srv.Client()}.FetchLoader(context.Background(), "8.4")
	if err == nil {
		t.Fatal("expected sha256 mismatch error, got nil")
	}
}

func TestFetchLoader_MissingVersion(t *testing.T) {
	srv := serveBytes(t, makeTarball(t, "8.4", []byte("x")))
	if _, err := (Fetcher{URL: srv.URL, Client: srv.Client()}).FetchLoader(context.Background(), "9.9"); err == nil {
		t.Fatal("expected error for uncataloged version")
	}
}
