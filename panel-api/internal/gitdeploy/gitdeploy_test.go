package gitdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseRepoURL(t *testing.T) {
	for raw, host := range map[string]string{
		"https://github.com/acme/shop.git":          "github.com",
		"https://GitLab.com/acme/sub/shop":          "gitlab.com",
		"https://git.example.com:8443/team/app.git": "git.example.com:8443",
		"https://bitbucket.org/acme/shop.git/":      "bitbucket.org",
	} {
		u, err := ParseRepoURL(raw)
		if err != nil || u.Host != host || u.Raw != raw {
			t.Errorf("ParseRepoURL(%q) = %+v, %v; want host %q", raw, u, err, host)
		}
	}
	if u, _ := ParseRepoURL("https://git.example.com:8443/a/b.git"); u.Hostname != "git.example.com" {
		t.Errorf("Hostname = %q", u.Hostname)
	}
	for _, raw := range []string{
		"", "http://github.com/a/b.git", "ssh://git@github.com/a/b.git", "git@github.com:a/b.git",
		"file:///srv/a.git", "ext::sh -c id", "https://tok@github.com/a/b.git",
		"https://u:p@github.com/a/b.git", "https://github.com/a/b.git?x=1", "https://github.com/a/b.git#x",
		"https://github.com/a/%62.git", "https://github.com/a/$x.git", "https://github.com/a b.git",
		"https://github.com", "https://github.com/", "https://github.com/a/../b.git",
		"https://[::1]/a/b.git", "https://github.com:0/a/b.git", "https://github.com:70000/a/b.git", "https://github.com:/a/b.git",
		"https://github.com/" + strings.Repeat("a", 512),
	} {
		if u, err := ParseRepoURL(raw); err == nil {
			t.Errorf("ParseRepoURL(%q) = %+v, want a refusal", raw, u)
		}
	}
}

func TestValidateBranch(t *testing.T) {
	for _, ok := range []string{"main", "release/1.x", "feature/new-ui", "v2.0", "user+fix"} {
		if err := ValidateBranch(ok); err != nil {
			t.Errorf("ValidateBranch(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "/x", ".x", "x/", "x.", "x.lock", "a..b", "a//b", "a/.b", "a b", "a~1", "a^", "a:b", "a@{1}", strings.Repeat("a", 256)} {
		if err := ValidateBranch(bad); err == nil {
			t.Errorf("ValidateBranch(%q) accepted", bad)
		}
	}
}

func TestValidateUsernameAndToken(t *testing.T) {
	for _, ok := range []string{"", "x-token-auth", "gitlab+deploy-token-7", "me@example.com"} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("ValidateUsername(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"a b", "a:b", "a/b", strings.Repeat("a", 129)} {
		if ValidateUsername(bad) == nil {
			t.Errorf("ValidateUsername(%q) accepted", bad)
		}
	}
	if ValidateToken("ghp_abc/+=:@x") != nil {
		t.Error("ValidateToken refused a token of printable characters")
	}
	for _, bad := range []string{"", "a b", "a\nb", strings.Repeat("a", 513)} {
		if ValidateToken(bad) == nil {
			t.Errorf("ValidateToken(%q) accepted", bad)
		}
	}
}

// The server never connects to its own loopback for a user.
func TestCheckHost_RefusesLoopback(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "localhost"} {
		if err := CheckHost(context.Background(), h); err == nil {
			t.Errorf("CheckHost(%q) allowed it", h)
		}
	}
}

type fakeAgent struct{ answer string }

func (f fakeAgent) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(f.answer), nil
}

func TestLsRemote_NoBranchesIsAnEmptyList(t *testing.T) {
	resp, err := LsRemote(context.Background(), fakeAgent{`{"default_branch":""}`}, LsRemoteRequest{})
	if err != nil || resp.Branches == nil || len(resp.Branches) != 0 {
		t.Fatalf("LsRemote = %+v, %v", resp, err)
	}
	if resp.HasBranch("main") {
		t.Error("HasBranch on no branches")
	}
}

// The panel's side of the git.* contract: the agent's handler test decodes
// the same fixtures (git_remote_test.go), so a field renamed on either side
// fails one of them.
func TestContract(t *testing.T) {
	const dir = "../agent/testdata/"
	roundTrip[LsRemoteRequest](t, dir+"git_ls_remote_request.json")
	roundTrip[LsRemoteResponse](t, dir+"git_ls_remote_response.json")
}

func roundTrip[T any](t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var typed T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&typed); err != nil {
		t.Fatalf("decode %s into %T: %v", path, typed, err)
	}
	again, _ := json.Marshal(typed)
	var got, want any
	_ = json.Unmarshal(again, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%T drops or renames fields:\nwant %s\ngot  %s", typed, raw, again)
	}
}
