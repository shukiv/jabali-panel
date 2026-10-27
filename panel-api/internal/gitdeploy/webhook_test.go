package gitdeploy

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Made-up secrets.
const (
	testHookSecret = "webhook-secret-abc_DEF-123456"
	// testSigningKey is the key under testSigningToken, as GitLab makes one.
	testSigningKey = "0123456789abcdef0123456789abcdef" // gitleaks:allow (test value)
)

var testSigningToken = "whsec_" + base64.StdEncoding.EncodeToString([]byte(testSigningKey))

// signHub signs body as GitHub, Gitea and Bitbucket do.
func signHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// signStandard signs body as GitLab's signing token does (Standard
// Webhooks).
func signStandard(key, id, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func headers(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestVerifyDelivery(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	old := strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10)
	sha1mac := hmac.New(sha1.New, []byte(testHookSecret))
	sha1mac.Write(body)

	cases := []struct {
		name   string
		h      http.Header
		secret string
		want   string // "" = refused
	}{
		{"github", headers("X-Hub-Signature-256", signHub(testHookSecret, body)), testHookSecret, "hub"},
		{"github, another secret", headers("X-Hub-Signature-256", signHub("another-secret-0123456789", body)), testHookSecret, ""},
		{"github, another body", headers("X-Hub-Signature-256", signHub(testHookSecret, []byte(`{"ref":"refs/heads/dev"}`))), testHookSecret, ""},
		{"bitbucket", headers("X-Hub-Signature", signHub(testHookSecret, body)), testHookSecret, "hub"},
		{"sha1 is never enough", headers("X-Hub-Signature", "sha1="+hex.EncodeToString(sha1mac.Sum(nil))), testHookSecret, ""},
		{"bare hex", headers("X-Hub-Signature-256", signHub(testHookSecret, body)[len("sha256="):]), testHookSecret, ""},
		{"gitlab signing token", headers("Webhook-Id", "msg_1", "Webhook-Timestamp", ts,
			"Webhook-Signature", signStandard(testSigningKey, "msg_1", ts, body)), testSigningToken, "standard"},
		{"gitlab signing token, one of two", headers("Webhook-Id", "msg_1", "Webhook-Timestamp", ts,
			"Webhook-Signature", "v1,bm90LXRoaXMtb25l "+signStandard(testSigningKey, "msg_1", ts, body)), testSigningToken, "standard"},
		{"gitlab signing token, too old", headers("Webhook-Id", "msg_1", "Webhook-Timestamp", old,
			"Webhook-Signature", signStandard(testSigningKey, "msg_1", old, body)), testSigningToken, ""},
		{"gitlab signing token, another id", headers("Webhook-Id", "msg_2", "Webhook-Timestamp", ts,
			"Webhook-Signature", signStandard(testSigningKey, "msg_1", ts, body)), testSigningToken, ""},
		{"standard needs a whsec_ secret", headers("Webhook-Id", "msg_1", "Webhook-Timestamp", ts,
			"Webhook-Signature", signStandard(testHookSecret, "msg_1", ts, body)), testHookSecret, ""},
		{"gitlab secret token", headers("X-Gitlab-Token", testHookSecret), testHookSecret, "gitlab-token"},
		{"gitlab secret token, wrong", headers("X-Gitlab-Token", testHookSecret+"x"), testHookSecret, ""},
		{"nothing", headers("X-GitHub-Event", "push"), testHookSecret, ""},
		{"no secret", headers("X-Gitlab-Token", ""), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VerifyDelivery(tc.h, body, tc.secret, now)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted as %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestParsePush(t *testing.T) {
	zeros := "0000000000000000000000000000000000000000"
	cases := []struct {
		name    string
		h       http.Header
		body    string
		want    []string
		wantErr bool
	}{
		{"github push", headers("X-GitHub-Event", "push"), `{"ref":"refs/heads/main","after":"2f1d"}`, []string{"main"}, false},
		{"github ping", headers("X-GitHub-Event", "ping"), `{"zen":"hi"}`, nil, false},
		{"github tag", headers("X-GitHub-Event", "push"), `{"ref":"refs/tags/v1.0","after":"2f1d"}`, nil, false},
		{"github deleted branch", headers("X-GitHub-Event", "push"), `{"ref":"refs/heads/main","deleted":true,"after":"` + zeros + `"}`, nil, false},
		{"github push not JSON", headers("X-GitHub-Event", "push"), `ref=main`, nil, true},
		{"github ping not JSON", headers("X-GitHub-Event", "ping"), `zen`, nil, false},
		{"gitlab push", headers("X-Gitlab-Event", "Push Hook"), `{"ref":"refs/heads/main","after":"2f1d"}`, []string{"main"}, false},
		{"gitlab deleted branch", headers("X-Gitlab-Event", "Push Hook"), `{"ref":"refs/heads/main","after":"` + zeros + `"}`, nil, false},
		{"gitlab tag", headers("X-Gitlab-Event", "Tag Push Hook"), `{"ref":"refs/tags/v1"}`, nil, false},
		{"gitlab system hook", headers("X-Gitlab-Event", "System Hook"), `{"object_kind":"push","ref":"refs/heads/main"}`, nil, false},
		{"gitea push", headers("X-Gitea-Event", "push"), `{"ref":"refs/heads/release/2","after":"2f1d"}`, []string{"release/2"}, false},
		{"forgejo push", headers("X-Forgejo-Event", "push"), `{"ref":"refs/heads/main"}`, []string{"main"}, false},
		{"bitbucket push", headers("X-Event-Key", "repo:push"),
			`{"push":{"changes":[{"new":null,"old":{"type":"branch","name":"old"}},{"new":{"type":"branch","name":"main"}},{"new":{"type":"tag","name":"v1"}}]}}`,
			[]string{"main"}, false},
		{"bitbucket data center", headers("X-Event-Key", "repo:refs_changed"),
			`{"changes":[{"ref":{"id":"refs/heads/main","type":"BRANCH"},"toHash":"2f1d","type":"UPDATE"},{"ref":{"id":"refs/heads/gone","type":"BRANCH"},"toHash":"` + zeros + `","type":"DELETE"},{"ref":{"id":"refs/tags/v1","type":"TAG"},"type":"ADD"}]}`,
			[]string{"main"}, false},
		{"bitbucket pull request", headers("X-Event-Key", "pullrequest:created"), `{}`, nil, false},
		{"any other sender", http.Header{}, `{"ref":"refs/heads/main"}`, []string{"main"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParsePush(tc.h, []byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("no error, got %+v", p)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Branches, tc.want) {
				t.Fatalf("branches = %q, want %q", p.Branches, tc.want)
			}
		})
	}
}

func TestDeliveryID(t *testing.T) {
	if got := DeliveryID(headers("X-GitHub-Delivery", "72d3162e", "X-Request-Id", "other")); got != "72d3162e" {
		t.Errorf("github = %q", got)
	}
	if got := DeliveryID(headers("Webhook-Id", "msg_1", "Idempotency-Key", "msg_1b")); got != "msg_1" {
		t.Errorf("gitlab = %q", got)
	}
	if got := DeliveryID(headers("X-Request-UUID", "bb-1")); got != "bb-1" {
		t.Errorf("bitbucket = %q", got)
	}
	if got := DeliveryID(http.Header{}); got != "" {
		t.Errorf("none = %q", got)
	}
}

func TestValidateWebhookSecret(t *testing.T) {
	for _, ok := range []string{testHookSecret, testSigningToken, "BitbucketGenerated0123456789"} { // gitleaks:allow (test value)
		if err := ValidateWebhookSecret(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"short", "has a space in it 0123", "whsec_not*base64*0123456789", "whsec_c2hvcnQ=", "tab\tinside-0123456789"} {
		if err := ValidateWebhookSecret(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNewWebhookSecretAndHookID(t *testing.T) {
	s1, err := NewWebhookSecret()
	if err != nil || len(s1) != 64 || ValidateWebhookSecret(s1) != nil {
		t.Fatalf("secret %q, %v", s1, err)
	}
	s2, _ := NewWebhookSecret()
	if s1 == s2 {
		t.Fatal("two secrets are the same")
	}
	id, err := NewHookID()
	if err != nil || !ValidHookID(id) {
		t.Fatalf("hook id %q, %v", id, err)
	}
	for _, bad := range []string{"", "../etc", id + "0", "ABCDEF0123456789ABCDEF0123456789"} {
		if ValidHookID(bad) {
			t.Errorf("%q passed as a hook id", bad)
		}
	}
}
