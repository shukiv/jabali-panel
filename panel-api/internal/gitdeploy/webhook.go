package gitdeploy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A domain's deploy-on-push webhook (ADR-0178). The Git host posts each
// push to WebhookPath(hookID), signed with a secret the host and the panel
// share. The panel believes a delivery only by its signature; the hook ID in
// the URL just finds the domain. A delivery can only make the panel deploy
// the stored branch of the stored repository, fetched by the agent itself:
// nothing in the body is deployed or trusted beyond the branch name.

// WebhookMaxBody bounds a delivery's body. A push of many commits is well
// under it.
const WebhookMaxBody = 1 << 20

// webhookMaxSkew is how far a Standard Webhooks timestamp may be from the
// panel's clock, as the specification recommends.
const webhookMaxSkew = 5 * time.Minute

// WebhookPath is a webhook's URL path on the panel.
func WebhookPath(hookID string) string { return "/api/v1/webhooks/git/" + hookID }

var hookIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidHookID reports whether s has a hook ID's shape.
func ValidHookID(s string) bool { return hookIDRE.MatchString(s) }

// NewHookID is a random hook ID.
func NewHookID() (string, error) { return randomHex(16) }

// NewWebhookSecret is a random shared secret, for the user to give the Git
// host.
func NewWebhookSecret() (string, error) { return randomHex(32) }

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var webhookSecretRE = regexp.MustCompile(`^[\x21-\x7e]{16,255}$`)

// ValidateWebhookSecret checks a secret the user brings from the Git host:
// GitLab's signing token (whsec_<base64>) and Bitbucket's generated secret
// are made by the host. 16 to 255 printable ASCII characters, no spaces.
func ValidateWebhookSecret(s string) error {
	if !webhookSecretRE.MatchString(s) {
		return errors.New("the secret must be 16 to 255 printable ASCII characters, with no spaces")
	}
	if raw, ok := strings.CutPrefix(s, "whsec_"); ok {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) < 16 {
			return errors.New("a whsec_ signing token must be base64 of at least 16 bytes after the prefix")
		}
	}
	return nil
}

// ErrWebhookSignature: no signature on the delivery matches the secret.
var ErrWebhookSignature = errors.New("no valid signature")

// VerifyDelivery checks a delivery's signature over its raw body, and
// returns the scheme that matched:
//   - hub: X-Hub-Signature-256 (GitHub, Gitea, Forgejo) or X-Hub-Signature
//     (Bitbucket Cloud and Data Center), "sha256=" and the hex HMAC-SHA256 of
//     the body. A sha1= signature is never accepted.
//   - standard: GitLab's signing token, the Standard Webhooks scheme:
//     webhook-signature "v1,<base64>" of the HMAC-SHA256 of
//     "<webhook-id>.<webhook-timestamp>.<body>", keyed with the whsec_
//     secret's bytes, the timestamp within webhookMaxSkew of now.
//   - gitlab-token: GitLab's older secret token, the secret itself in
//     X-Gitlab-Token.
//
// Every comparison takes constant time.
func VerifyDelivery(h http.Header, body []byte, secret string, now time.Time) (string, error) {
	if secret == "" {
		return "", ErrWebhookSignature
	}
	for _, name := range []string{"X-Hub-Signature-256", "X-Hub-Signature"} {
		if v := h.Get(name); v != "" && hubSignatureOK(v, body, secret) {
			return "hub", nil
		}
	}
	if v := h.Get("Webhook-Signature"); v != "" && standardSignatureOK(h, v, body, secret, now) {
		return "standard", nil
	}
	if v := h.Get("X-Gitlab-Token"); v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(secret)) == 1 {
		return "gitlab-token", nil
	}
	return "", ErrWebhookSignature
}

func hubSignatureOK(header string, body []byte, secret string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func standardSignatureOK(h http.Header, header string, body []byte, secret string, now time.Time) bool {
	raw, ok := strings.CutPrefix(secret, "whsec_")
	if !ok {
		return false
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) == 0 {
		return false
	}
	id, ts := h.Get("Webhook-Id"), h.Get("Webhook-Timestamp")
	if id == "" || ts == "" {
		return false
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := now.Sub(time.Unix(sec, 0)); d > webhookMaxSkew || d < -webhookMaxSkew {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	// Space-separated, so a sender can sign with an old and a new key while
	// rotating.
	for _, s := range strings.Fields(header) {
		v, ok := strings.CutPrefix(s, "v1,")
		if !ok {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(v)
		if err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// deliveryIDHeaders name a delivery, most specific first. Each stays the same
// when the host sends the delivery again.
var deliveryIDHeaders = []string{
	"X-GitHub-Delivery",
	"X-Gitea-Delivery", "X-Forgejo-Delivery", "X-Gogs-Delivery",
	"Webhook-Id", "Idempotency-Key", // GitLab
	"X-Request-UUID", // Bitbucket Cloud
	"X-Request-Id",   // Bitbucket Data Center
}

// DeliveryID is the host's ID for a delivery, "" when it sends none.
func DeliveryID(h http.Header) string {
	for _, name := range deliveryIDHeaders {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			if len(v) > 256 {
				v = v[:256]
			}
			return v
		}
	}
	return ""
}

// Push is what a delivery says was pushed.
type Push struct {
	// Event is the host's name for the delivery's event.
	Event string
	// Branches are the branches the delivery moved; a deleted branch is not
	// one of them.
	Branches []string
}

// Has reports whether the push moved branch.
func (p Push) Has(branch string) bool {
	for _, b := range p.Branches {
		if b == branch {
			return true
		}
	}
	return false
}

// ParsePush reads which branches a delivery moved. A delivery of another
// event (a ping, a tag, a pull request) has none. It fails only when a push
// event's body isn't the JSON its host sends.
func ParsePush(h http.Header, body []byte) (Push, error) {
	switch {
	case h.Get("X-GitHub-Event") != "":
		return parseRefPush(h.Get("X-GitHub-Event"), "push", body)
	case h.Get("X-Gitea-Event") != "":
		return parseRefPush(h.Get("X-Gitea-Event"), "push", body)
	case h.Get("X-Forgejo-Event") != "":
		return parseRefPush(h.Get("X-Forgejo-Event"), "push", body)
	case h.Get("X-Gogs-Event") != "":
		return parseRefPush(h.Get("X-Gogs-Event"), "push", body)
	case h.Get("X-Gitlab-Event") != "":
		return parseRefPush(h.Get("X-Gitlab-Event"), "Push Hook", body)
	case h.Get("X-Event-Key") == "repo:push":
		return parseBitbucketCloudPush(body)
	case h.Get("X-Event-Key") == "repo:refs_changed":
		return parseBitbucketServerPush(body)
	case h.Get("X-Event-Key") != "":
		return Push{Event: h.Get("X-Event-Key")}, nil
	default:
		// Any other sender: a JSON body with ref, signed as GitHub signs.
		return parseRefPush("push", "push", body)
	}
}

// parseRefPush reads a push as GitHub, Gitea, Forgejo, Gogs and GitLab send
// it: ref is refs/heads/<branch>, and an after of all zeros (or deleted)
// means the branch was deleted.
func parseRefPush(event, pushEvent string, body []byte) (Push, error) {
	p := Push{Event: event}
	if event != pushEvent {
		return p, nil
	}
	var b struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return p, fmt.Errorf("the %s body is not JSON: %w", event, err)
	}
	branch, ok := strings.CutPrefix(b.Ref, "refs/heads/")
	if !ok || branch == "" || b.Deleted || allZeros(b.After) {
		return p, nil
	}
	p.Branches = []string{branch}
	return p, nil
}

func allZeros(s string) bool { return s != "" && strings.Trim(s, "0") == "" }

// parseBitbucketCloudPush reads repo:push: each change's new is the ref
// after the push, null when the branch was deleted.
func parseBitbucketCloudPush(body []byte) (Push, error) {
	p := Push{Event: "repo:push"}
	var b struct {
		Push struct {
			Changes []struct {
				New *struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"new"`
			} `json:"changes"`
		} `json:"push"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return p, fmt.Errorf("the repo:push body is not JSON: %w", err)
	}
	for _, c := range b.Push.Changes {
		if c.New != nil && c.New.Type == "branch" && c.New.Name != "" {
			p.Branches = append(p.Branches, c.New.Name)
		}
	}
	return p, nil
}

// parseBitbucketServerPush reads Bitbucket Data Center's repo:refs_changed.
func parseBitbucketServerPush(body []byte) (Push, error) {
	p := Push{Event: "repo:refs_changed"}
	var b struct {
		Changes []struct {
			Ref struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"ref"`
			ToHash string `json:"toHash"`
			Type   string `json:"type"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return p, fmt.Errorf("the repo:refs_changed body is not JSON: %w", err)
	}
	for _, c := range b.Changes {
		branch, ok := strings.CutPrefix(c.Ref.ID, "refs/heads/")
		if ok && branch != "" && c.Ref.Type == "BRANCH" && c.Type != "DELETE" && !allZeros(c.ToHash) {
			p.Branches = append(p.Branches, branch)
		}
	}
	return p, nil
}
