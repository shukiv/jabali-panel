package models

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// JAB-228. nginx's client_max_body_size and the PHP-FPM pool's
// upload_max_filesize/post_max_size are set in different files, in different
// languages, by different components — and they must agree or uploads break in
// a way nobody can diagnose.
//
// They drifted: the pool template allowed 512M while nginx capped bodies at
// 50m, so nginx returned 413 before PHP ever saw the request. WordPress reads
// the PHP limits and advertised 200MB, so the user was told a size the stack
// would never accept. Uploads failed mid-transfer and retries left duplicate
// attachments (name.jpg, name-1.jpg, name-2.jpg).
//
// This is the contract test that would have caught it: nginx must allow at
// least what PHP will accept.

var (
	phpIniSizeRe   = regexp.MustCompile(`(?m)^php_value\[(upload_max_filesize|post_max_size)\]\s*=\s*(\d+)([MG])`)
	nginxDefaultRe = regexp.MustCompile(`default:'(\d+)([mg])'`)
)

func parseMB(n string, unit string) int {
	v, _ := strconv.Atoi(n)
	switch strings.ToLower(unit) {
	case "g":
		return v * 1024
	default:
		return v
	}
}

func TestNginxBodySizeCoversPHPUploadLimit(t *testing.T) {
	root := repoRootFrom(t)

	tmpl, err := os.ReadFile(filepath.Join(root, "install", "php", "jabali-php-pool.conf.tmpl"))
	if err != nil {
		t.Skipf("pool template not readable from this checkout: %v", err)
	}
	matches := phpIniSizeRe.FindAllStringSubmatch(string(tmpl), -1)
	if len(matches) == 0 {
		t.Fatal("no upload_max_filesize/post_max_size found in the pool template — regex or template changed")
	}
	phpMaxMB := 0
	for _, m := range matches {
		if mb := parseMB(m[2], m[3]); mb > phpMaxMB {
			phpMaxMB = mb
		}
	}

	src, err := os.ReadFile(filepath.Join(root, "panel-api", "internal", "models", "server_settings.go"))
	if err != nil {
		t.Fatalf("read server_settings.go: %v", err)
	}
	idx := strings.Index(string(src), "nginx_client_max_body_size")
	if idx < 0 {
		t.Fatal("nginx_client_max_body_size field not found")
	}
	line := string(src)[idx:]
	if e := strings.Index(line, "\n"); e > 0 {
		line = line[:e]
	}
	nm := nginxDefaultRe.FindStringSubmatch(line)
	if nm == nil {
		t.Fatalf("could not parse the nginx default from: %s", line)
	}
	nginxMB := parseMB(nm[1], nm[2])

	if nginxMB < phpMaxMB {
		t.Errorf("nginx client_max_body_size default is %dM but the PHP pool accepts %dM — "+
			"nginx will 413 uploads PHP would have taken, and WordPress advertises the PHP "+
			"number so users are told a size that cannot work (JAB-228)", nginxMB, phpMaxMB)
	}
}

// repoRootFrom walks up until it finds go.mod, so the test works from any
// checkout layout.
func repoRootFrom(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("repo root not found")
	return ""
}
