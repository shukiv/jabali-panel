package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestUploadPathAction(t *testing.T) {
	const home, dir = "/home/alice", "/home/alice/domains/shop.example/public_html"
	dirs := map[string]bool{dir + "/wp-content/uploads": true}
	files := map[string]bool{"/home/alice/public_html/busy.example/wp-content/uploads": true}
	isDir := func(p string) bool { return dirs[p] }
	holds := func(p string) bool { return files[p] }
	for _, c := range []struct{ v, want string }{
		{"", "unchanged"},
		{"wp-content/uploads", "unchanged"}, // relative: WordPress resolves it under the install
		{dir + "/wp-content/uploads", "unchanged"},
		{dir + "/media", "unchanged"},
		{"/home/alice/public_html/shop.example/wp-content/uploads", "cleared"}, // cPanel addon layout
		{"/home/olduser/public_html/wp-content/uploads/", "cleared"},          // source account name differs
		{"/var/www/vhosts/shop.example/httpdocs/wp-content/uploads", "cleared"},
		{"/home/alice/domains/blog.example/public_html/wp-content/uploads", "kept"}, // another live site's folder
		{"/home/alice/public_html/busy.example/wp-content/uploads", "kept"},         // stale folder holds files
		{"/home/alice/public_html/shop.example/media", "kept"},                      // not the uploads shape
	} {
		if got, _ := uploadPathAction(c.v, dir, home, isDir, holds); got != c.want {
			t.Errorf("uploadPathAction(%q) = %s, want %s", c.v, got, c.want)
		}
	}
	// No wp-content/uploads of its own: clearing would point WordPress at an empty place.
	if got, _ := uploadPathAction("/home/alice/public_html/shop.example/wp-content/uploads", dir, home,
		func(string) bool { return false }, holds); got != "kept" {
		t.Errorf("without the site's own uploads folder = %s, want kept", got)
	}
}

func stubUploadPathWP(t *testing.T, getOut string, getErr error) *[][]string {
	t.Helper()
	var calls [][]string
	saved, savedDir, savedHolds := uploadPathWP, uploadPathIsDir, uploadPathHoldsFiles
	t.Cleanup(func() { uploadPathWP, uploadPathIsDir, uploadPathHoldsFiles = saved, savedDir, savedHolds })
	uploadPathIsDir = func(p string) bool { return strings.HasSuffix(p, "/blog/wp-content/uploads") }
	uploadPathHoldsFiles = func(string) bool { return false }
	uploadPathWP = func(_ context.Context, osUser string, args ...string) (string, error) {
		calls = append(calls, append([]string{osUser}, args...))
		if args[1] == "get" {
			return getOut, getErr
		}
		return "Success: Updated 'upload_path' option.\n", nil
	}
	return &calls
}

func callUploadPathFix(t *testing.T, params string) (wordpressUploadPathFixResult, error) {
	t.Helper()
	res, err := wordpressUploadPathFixHandler(context.Background(), json.RawMessage(params))
	if err != nil {
		return wordpressUploadPathFixResult{}, err
	}
	return res.(wordpressUploadPathFixResult), nil
}

func TestUploadPathFix_ClearsTheSourcePathAsTheSiteUser(t *testing.T) {
	calls := stubUploadPathWP(t, "PHP Deprecated:  something old in wp-config.php\n\"\\/home\\/alice\\/public_html\\/shop.example\\/blog\\/wp-content\\/uploads\"\n", nil)
	raw, _ := os.ReadFile("../../../panel-api/internal/agent/testdata/wordpress_upload_path_fix_request.json")
	res, err := callUploadPathFix(t, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "cleared" || res.Before != "/home/alice/public_html/shop.example/blog/wp-content/uploads" {
		t.Fatalf("result = %+v", res)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %q", *calls)
	}
	upd := strings.Join((*calls)[1], " ")
	if (*calls)[1][0] != "alice" || !strings.HasPrefix(upd, "alice option update upload_path  --path=/home/alice/domains/shop.example/public_html/blog") {
		t.Errorf("update call = %q", (*calls)[1])
	}
}

func TestUploadPathFix_LeavesOtherValuesAlone(t *testing.T) {
	for _, out := range []string{`""`, `"wp-content/uploads"`, `"\/home\/alice\/domains\/blog.example\/public_html\/wp-content\/uploads"`} {
		calls := stubUploadPathWP(t, out+"\n", nil)
		res, err := callUploadPathFix(t, `{"os_user":"alice","path":"/home/alice/domains/shop.example/public_html/blog"}`)
		if err != nil || res.Action == "cleared" || len(*calls) != 1 {
			t.Errorf("%s: res=%+v err=%v calls=%q", out, res, err, *calls)
		}
	}
}

func TestUploadPathFix_UnreadableOutputIsAnErrorNotAClear(t *testing.T) {
	for _, c := range []struct {
		out string
		err error
	}{
		{"Warning: something\n", nil},
		{"", errors.New("exit status 1: Error: Error establishing a database connection.")},
	} {
		calls := stubUploadPathWP(t, c.out, c.err)
		_, err := callUploadPathFix(t, `{"os_user":"alice","path":"/home/alice/domains/shop.example/public_html"}`)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInternal || len(*calls) != 1 {
			t.Errorf("%q: err=%v calls=%q", c.out, err, *calls)
		}
	}
}

func TestUploadPathFix_RejectsUnsafeParams(t *testing.T) {
	calls := stubUploadPathWP(t, `""`, nil)
	for _, p := range []string{
		`{"os_user":"root;x","path":"/home/root/x"}`,
		`{"os_user":"alice","path":"/home/bob/domains/x/public_html"}`,
		`{"os_user":"alice","path":"/home/alice/../bob/domains/x/public_html"}`,
		`{"os_user":"alice","path":"relative/dir"}`,
		`{"os_user":"alice","path":"/home/alice"}`,
	} {
		var ae *agentwire.AgentError
		if _, err := callUploadPathFix(t, p); !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want invalid_argument", p, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("ran wp for unsafe params: %q", *calls)
	}
}

// The panel's side of the contract decodes into the handler's params and result.
func TestUploadPathFix_PanelContract(t *testing.T) {
	for name, into := range map[string]any{
		"wordpress_upload_path_fix_request.json":  &wordpressUploadPathFixParams{},
		"wordpress_upload_path_fix_response.json": &wordpressUploadPathFixResult{},
	} {
		raw, err := os.ReadFile("../../../panel-api/internal/agent/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(into); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
