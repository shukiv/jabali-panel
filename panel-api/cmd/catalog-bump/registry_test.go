package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeRegistry serves the OCI distribution API surface the client uses:
// token challenge on /v2/, paginated tags/list, manifest HEAD.
type fakeRegistry struct {
	mux       *http.ServeMux
	srv       *httptest.Server
	anonymous bool
	tags      []string
	digests   map[string]string
	pageSize  int
}

func newFakeRegistry(t *testing.T, anonymous bool, tags []string, digests map[string]string, pageSize int) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{anonymous: anonymous, tags: tags, digests: digests, pageSize: pageSize}
	f.mux = http.NewServeMux()
	f.srv = httptest.NewServer(f.mux)
	t.Cleanup(f.srv.Close)

	host := strings.TrimPrefix(f.srv.URL, "http://")

	f.mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		if f.anonymous {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Www-Authenticate",
			fmt.Sprintf(`Bearer realm="http://%s/token",service="fake.io"`, host))
		w.WriteHeader(http.StatusUnauthorized)
	})
	f.mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "repository:acme/app:pull" {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "fake-token"})
	})
	f.mux.HandleFunc("/v2/acme/app/tags/list", func(w http.ResponseWriter, r *http.Request) {
		if !f.anonymous && r.Header.Get("Authorization") != "Bearer fake-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		last, _ := url.QueryUnescape(r.URL.Query().Get("last"))
		start := 0
		if last != "" {
			for i, tg := range f.tags {
				if tg == last {
					start = i + 1
				}
			}
		}
		end := len(f.tags)
		if f.pageSize > 0 && start+f.pageSize < end {
			end = start + f.pageSize
			w.Header().Set("Link",
				fmt.Sprintf(`</v2/acme/app/tags/list?last=%s&n=%d>; rel="next"`,
					url.QueryEscape(f.tags[end-1]), f.pageSize))
		}
		json.NewEncoder(w).Encode(map[string][]string{"tags": f.tags[start:end]})
	})
	f.mux.HandleFunc("/v2/acme/app/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "want HEAD", http.StatusMethodNotAllowed)
			return
		}
		tag := strings.TrimPrefix(r.URL.Path, "/v2/acme/app/manifests/")
		d, ok := f.digests[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
		w.WriteHeader(http.StatusOK)
	})
	return f
}

func (f *fakeRegistry) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func testClient() *regClient {
	c := newRegClient()
	c.scheme = "http"
	return c
}

func TestRegistryTokenFlowAndDigest(t *testing.T) {
	d := "sha256:" + strings.Repeat("cd", 32)
	f := newFakeRegistry(t, false, []string{"1.0.0", "1.1.0"}, map[string]string{"1.1.0": d}, 0)
	c := testClient()

	tags, truncated, err := c.tags(f.host(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(tags) != 2 {
		t.Fatalf("tags = %v truncated=%v", tags, truncated)
	}
	got, err := c.digest(f.host(), "acme/app", "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != d {
		t.Fatalf("digest = %q, want %q", got, d)
	}
}

func TestRegistryAnonymous(t *testing.T) {
	f := newFakeRegistry(t, true, []string{"2.0"}, nil, 0)
	tags, _, err := testClient().tags(f.host(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != "2.0" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestRegistryPagination(t *testing.T) {
	var all []string
	for i := 0; i < 25; i++ {
		all = append(all, fmt.Sprintf("1.0.%d", i))
	}
	f := newFakeRegistry(t, false, all, nil, 10)
	tags, truncated, err := testClient().tags(f.host(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(tags) != 25 {
		t.Fatalf("got %d tags across pages, want 25", len(tags))
	}
}

func TestRunEndToEnd(t *testing.T) {
	newDigest := "sha256:" + strings.Repeat("ef", 32)
	f := newFakeRegistry(t, false,
		[]string{"1.0.0", "1.2.0", "1.2.1", "2.0.0-rc1", "latest"},
		map[string]string{"1.2.1": newDigest}, 0)

	dir := t.TempDir()
	appDir := dir + "/acmeapp"
	if err := createApp(appDir, "version: \"1.0.0\"\nimage_channel: "+f.host()+"/acme/app:1.0.0@"+testDigest+"\n"); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	changed, hadErr, err := run(dir, false, testClient(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if hadErr || changed != 1 {
		t.Fatalf("changed=%d hadErr=%v\n%s", changed, hadErr, out.String())
	}
	got, err := readFile(appDir + "/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := "version: \"1.2.1\"\nimage_channel: " + f.host() + "/acme/app:1.2.1@" + newDigest + "\n"
	if got != want {
		t.Fatalf("rewritten app.yaml:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(out.String(), "⬆ bumped") {
		t.Fatalf("summary missing bump row:\n%s", out.String())
	}
}
