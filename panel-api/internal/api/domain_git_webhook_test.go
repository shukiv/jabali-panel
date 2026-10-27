package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type gitTestWebhooks struct {
	rows map[string]*models.DomainGitWebhook
}

func (f *gitTestWebhooks) Get(_ context.Context, domainID string) (*models.DomainGitWebhook, error) {
	if r, ok := f.rows[domainID]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *gitTestWebhooks) FindByHookID(_ context.Context, hookID string) (*models.DomainGitWebhook, error) {
	for _, r := range f.rows {
		if r.HookID == hookID {
			cp := *r
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *gitTestWebhooks) Put(_ context.Context, row *models.DomainGitWebhook) error {
	if old, ok := f.rows[row.DomainID]; ok {
		old.HookID, old.SecretEnc, old.UpdatedAt = row.HookID, row.SecretEnc, row.UpdatedAt
		return nil
	}
	cp := *row
	f.rows[row.DomainID] = &cp
	return nil
}

func (f *gitTestWebhooks) Delete(_ context.Context, domainID string) error {
	if _, ok := f.rows[domainID]; !ok {
		return repository.ErrNotFound
	}
	delete(f.rows, domainID)
	return nil
}

func (f *gitTestWebhooks) Queue(_ context.Context, domainID string, at time.Time) error {
	if r, ok := f.rows[domainID]; ok && r.QueuedAt == nil {
		r.QueuedAt = &at
	}
	return nil
}

func (f *gitTestWebhooks) ListQueued(_ context.Context, _ int) ([]models.DomainGitWebhook, error) {
	var out []models.DomainGitWebhook
	for _, r := range f.rows {
		if r.QueuedAt != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *gitTestWebhooks) Unqueue(_ context.Context, domainID string) (bool, error) {
	r, ok := f.rows[domainID]
	if !ok || r.QueuedAt == nil {
		return false, nil
	}
	r.QueuedAt = nil
	return true, nil
}

func (f *gitTestWebhooks) RecordDelivery(_ context.Context, domainID string, at time.Time, result string) error {
	if r, ok := f.rows[domainID]; ok {
		if !at.IsZero() {
			r.LastDeliveryAt = &at
		}
		r.LastResult = result
	}
	return nil
}

// gitTestSign signs body as GitHub does.
func gitTestSign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// hookUp stores settings for example.com (branch main) and creates its
// webhook. It returns the hook ID and the secret.
func (e *gitTestEnv) hookUp(t *testing.T) (string, string) {
	t.Helper()
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git"}`)
	e.agent.answers["git.deploy_start"] = gitdeploy.DeployStartResponse{Unit: "u", PHPVersion: "8.3"}
	code, out, raw := e.do(t, http.MethodPost, "/domains/d1/git/webhook", "")
	if code != http.StatusCreated {
		t.Fatalf("POST webhook = %d %s", code, raw)
	}
	url, _ := out["url"].(string)
	return url[strings.LastIndex(url, "/")+1:], out["secret"].(string)
}

// post delivers body to the webhook with the given headers.
func (e *gitTestEnv) post(t *testing.T, hookID, body string, h map[string]string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/git/"+hookID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// push delivers a GitHub push of branch, signed with secret. Each delivery
// is another push: its body names the delivery.
func (e *gitTestEnv) push(t *testing.T, hookID, secret, branch, delivery string) (int, map[string]any, string) {
	t.Helper()
	body := `{"ref":"refs/heads/` + branch + `","after":"2f1d6a2c3b4e5f60718293a4b5c6d7e8f9a0b1c2","delivery":"` + delivery + `"}`
	return e.post(t, hookID, body, map[string]string{
		"X-GitHub-Event": "push", "X-GitHub-Delivery": delivery, "X-Hub-Signature-256": gitTestSign(secret, body),
	})
}

// webhookRuns are the runs pushes started.
func (e *gitTestEnv) webhookRuns() []*models.DomainGitDeployment {
	var out []*models.DomainGitDeployment
	for _, r := range e.deploys.runs {
		if r.Source == models.GitDeploySourceWebhook {
			out = append(out, r)
		}
	}
	return out
}

// finishRuns marks every run as succeeded, as if each had ended.
func (e *gitTestEnv) finishRuns() {
	for _, r := range e.deploys.runs {
		r.State = models.GitDeploySucceeded
	}
}

// The secret is in the answer that makes it, and never again. A new secret
// keeps the URL, and the old secret stops working.
func TestGitWebhook_TheSecretIsShownOnceAndCanBeReplaced(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	if !gitdeploy.ValidHookID(hookID) || len(secret) != 64 {
		t.Fatalf("hook %q, secret %q", hookID, secret)
	}
	plain, err := e.key.Open(e.hooks.rows["d1"].SecretEnc)
	if err != nil || string(plain) != secret {
		t.Fatalf("the stored secret = %q, %v", plain, err)
	}
	code, out, raw := e.do(t, http.MethodGet, "/domains/d1/git/webhook", "")
	if code != http.StatusOK || out["configured"] != true || strings.Contains(raw, secret) ||
		out["url"] != "https://example.com/api/v1/webhooks/git/"+hookID {
		t.Fatalf("GET = %d %s", code, raw)
	}

	code, out, raw = e.do(t, http.MethodPost, "/domains/d1/git/webhook", "")
	if code != http.StatusOK || out["secret"] == secret || !strings.HasSuffix(out["url"].(string), hookID) {
		t.Fatalf("a new secret = %d %s", code, raw)
	}
	if code, _, raw := e.push(t, hookID, secret, "main", "d-1"); code != http.StatusUnauthorized {
		t.Fatalf("the old secret = %d %s", code, raw)
	}
	if code, _, raw := e.push(t, hookID, out["secret"].(string), "main", "d-2"); code != http.StatusAccepted {
		t.Fatalf("the new secret = %d %s", code, raw)
	}
}

// GitLab makes its signing token itself: the user gives it to the panel. A
// GitLab without signing tokens sends the secret as X-Gitlab-Token.
func TestGitWebhook_TakesTheHostsOwnSecret(t *testing.T) {
	e := newGitTestEnv(t)
	e.hookUp(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	token := "whsec_" + base64.StdEncoding.EncodeToString(key)

	if code, _, raw := e.do(t, http.MethodPost, "/domains/d1/git/webhook", `{"secret":"short"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a short secret = %d %s", code, raw)
	}
	code, out, raw := e.do(t, http.MethodPost, "/domains/d1/git/webhook", `{"secret":"`+token+`"}`)
	if code != http.StatusOK || out["secret"] != nil || strings.Contains(raw, token) {
		t.Fatalf("POST own secret = %d %s", code, raw)
	}
	hookID := e.hooks.rows["d1"].HookID

	body := `{"ref":"refs/heads/main","after":"2f1d"}`
	ts := strconv.FormatInt(e.now.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("msg_1." + ts + "." + body))
	code, _, raw = e.post(t, hookID, body, map[string]string{
		"X-Gitlab-Event": "Push Hook", "Webhook-Id": "msg_1", "Webhook-Timestamp": ts,
		"Webhook-Signature": "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	})
	if code != http.StatusAccepted {
		t.Fatalf("signing token = %d %s", code, raw)
	}
	e.finishRuns()
	body = `{"ref":"refs/heads/main","after":"8e2a"}`
	code, _, raw = e.post(t, hookID, body, map[string]string{"X-Gitlab-Event": "Push Hook", "Webhook-Id": "msg_2", "X-Gitlab-Token": token})
	if code != http.StatusAccepted {
		t.Fatalf("secret token = %d %s", code, raw)
	}
	if n := len(e.webhookRuns()); n != 2 {
		t.Fatalf("runs = %d", n)
	}
}

func TestGitWebhook_NeedsSettingsAndGitDeploy(t *testing.T) {
	e := newGitTestEnv(t)
	if code, out, _ := e.do(t, http.MethodPost, "/domains/d1/git/webhook", ""); code != http.StatusNotFound || out["error"] != "git_not_configured" {
		t.Fatalf("before the settings = %d %v", code, out)
	}
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/webhook", ""); code != http.StatusOK || out["configured"] != false {
		t.Fatalf("GET before = %d %v", code, out)
	}
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git"}`)
	e.pkg.GitDeployEnabled = false
	if code, out, _ := e.do(t, http.MethodPost, "/domains/d1/git/webhook", ""); code != http.StatusForbidden || out["error"] != "git_deploy_not_in_package" {
		t.Fatalf("without Git deploy = %d %v", code, out)
	}
	if len(e.hooks.rows) != 0 {
		t.Fatal("a webhook was stored")
	}
}

// A signed push to the branch deploys it, as the domain's owner, just as the
// Deploy button does.
func TestGitWebhook_PushDeploysTheBranch(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	code, out, raw := e.push(t, hookID, secret, "main", "d-1")
	if code != http.StatusAccepted || out["result"] != "started" {
		t.Fatalf("push = %d %s", code, raw)
	}
	runs := e.webhookRuns()
	if len(runs) != 1 || runs[0].ID != out["deployment_id"] || runs[0].Branch != "main" || runs[0].UserID != "u-alice" ||
		runs[0].Kind != models.GitDeployKindDeploy || runs[0].State != models.GitDeployRunning {
		t.Fatalf("runs = %+v", runs)
	}
	var start gitdeploy.DeployStartRequest
	for i, c := range e.agent.calls {
		if c == "git.deploy_start" {
			start = e.agent.params[i].(gitdeploy.DeployStartRequest)
		}
	}
	if start.OSUser != "alice" || start.Branch != "main" || start.RepoURL != "https://gitlab.com/acme/shop.git" {
		t.Fatalf("git.deploy_start = %+v", start)
	}
	if h := e.hooks.rows["d1"]; h.LastResult != "started" || h.LastDeliveryAt == nil {
		t.Fatalf("recorded = %+v", h)
	}
}

// Without the secret's signature nothing happens and nothing is written.
func TestGitWebhook_UnsignedOrUnknownChangesNothing(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, _ := e.hookUp(t)
	body := `{"ref":"refs/heads/main"}`
	for name, h := range map[string]map[string]string{
		"unsigned":           {"X-GitHub-Event": "push"},
		"another secret":     {"X-GitHub-Event": "push", "X-Hub-Signature-256": gitTestSign("another-secret-0123456789", body)},
		"a wrong gitlab key": {"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": "another-secret-0123456789"},
	} {
		if code, _, raw := e.post(t, hookID, body, h); code != http.StatusUnauthorized {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
	if code, _, _ := e.post(t, strings.Repeat("0", 32), body, nil); code != http.StatusNotFound {
		t.Errorf("unknown hook = %d", code)
	}
	if code, _, _ := e.post(t, "..%2fd1", body, nil); code != http.StatusNotFound {
		t.Errorf("malformed hook = %d", code)
	}
	if len(e.webhookRuns()) != 0 || e.hooks.rows["d1"].LastDeliveryAt != nil {
		t.Fatalf("runs %d, recorded %+v", len(e.webhookRuns()), e.hooks.rows["d1"])
	}
}

// Everything signed that isn't a push to the branch is answered 2xx, so
// GitLab doesn't turn the webhook off, and deploys nothing.
func TestGitWebhook_OtherDeliveriesAreAnsweredOK(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	for name, d := range map[string]struct{ event, body string }{
		"another branch": {"push", `{"ref":"refs/heads/dev","after":"2f1d"}`},
		"a tag":          {"push", `{"ref":"refs/tags/v1","after":"2f1d"}`},
		"a deletion":     {"push", `{"ref":"refs/heads/main","deleted":true,"after":"0000000000000000000000000000000000000000"}`},
		"a ping":         {"ping", `{"zen":"Keep it logically awesome."}`},
	} {
		code, out, raw := e.post(t, hookID, d.body, map[string]string{"X-GitHub-Event": d.event, "X-Hub-Signature-256": gitTestSign(secret, d.body)})
		if code != http.StatusOK || out["result"] != "ignored" {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
	if len(e.webhookRuns()) != 0 || e.hooks.rows["d1"].LastResult != "ignored" {
		t.Fatalf("runs %d, recorded %+v", len(e.webhookRuns()), e.hooks.rows["d1"])
	}
}

func TestGitWebhook_ADeliverySentAgainDeploysOnce(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	if code, _, raw := e.push(t, hookID, secret, "main", "72d3162e"); code != http.StatusAccepted {
		t.Fatalf("first = %d %s", code, raw)
	}
	e.finishRuns()
	code, out, raw := e.push(t, hookID, secret, "main", "72d3162e")
	if code != http.StatusOK || out["result"] != "duplicate" {
		t.Fatalf("again = %d %s", code, raw)
	}
	if n := len(e.webhookRuns()); n != 1 {
		t.Fatalf("runs = %d", n)
	}
	if code, _, raw := e.push(t, hookID, secret, "main", "8e2a1f00"); code != http.StatusAccepted {
		t.Fatalf("another delivery = %d %s", code, raw)
	}
}

// A GitHub signature covers the body alone, so a captured delivery replayed
// under a new ID is the same delivery.
func TestGitWebhook_AReplayedBodyDeploysOnce(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	body := `{"ref":"refs/heads/main","after":"2f1d6a2c3b4e5f60718293a4b5c6d7e8f9a0b1c2"}`
	sig := gitTestSign(secret, body)
	if code, _, raw := e.post(t, hookID, body, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "72d3162e", "X-Hub-Signature-256": sig}); code != http.StatusAccepted {
		t.Fatalf("first = %d %s", code, raw)
	}
	e.finishRuns()
	for _, id := range []string{"replayed-0001", ""} {
		code, out, raw := e.post(t, hookID, body, map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": id, "X-Hub-Signature-256": sig})
		if code != http.StatusOK || out["result"] != "duplicate" {
			t.Fatalf("replay with id %q = %d %s", id, code, raw)
		}
	}
	if n := len(e.webhookRuns()); n != 1 {
		t.Fatalf("runs = %d", n)
	}
}

// A push during a run waits for it; more pushes meanwhile wait with it.
func TestGitWebhook_PushDuringARunWaits(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	e.seed("01JCXMPSRRVN00000000000AB9", "u-alice", "d1", models.GitDeployKindDeploy, models.GitDeployRunning)
	e.agent.answers["git.deploy_status"] = gitdeploy.DeployStatusResponse{State: "running"}

	code, out, raw := e.push(t, hookID, secret, "main", "d-1")
	if code != http.StatusAccepted || out["result"] != "queued" {
		t.Fatalf("push = %d %s", code, raw)
	}
	first := e.hooks.rows["d1"].QueuedAt
	if first == nil || len(e.webhookRuns()) != 0 {
		t.Fatalf("queued %v, runs %d", first, len(e.webhookRuns()))
	}
	e.now = e.now.Add(time.Minute)
	if code, out, _ := e.push(t, hookID, secret, "main", "d-2"); code != http.StatusAccepted || out["result"] != "queued" {
		t.Fatalf("second push = %d %v", code, out)
	}
	if got := e.hooks.rows["d1"].QueuedAt; !got.Equal(*first) {
		t.Fatalf("the waiting push's time moved: %v -> %v", first, got)
	}
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/webhook", ""); code != http.StatusOK || out["queued"] != true || out["last_result"] != "queued" {
		t.Fatalf("GET = %d %v", code, out)
	}
}

// A run that has ended, though nobody has read its result yet, doesn't make
// a push wait.
func TestGitWebhook_AnEndedRunDoesntMakeThePushWait(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	e.seed("01JCXMPSRRVN00000000000AB9", "u-alice", "d1", models.GitDeployKindDeploy, models.GitDeployRunning)
	e.agent.answers["git.deploy_status"] = gitdeploy.DeployStatusResponse{State: "succeeded", Result: "success"}
	code, out, raw := e.push(t, hookID, secret, "main", "d-1")
	if code != http.StatusAccepted || out["result"] != "started" {
		t.Fatalf("push = %d %s", code, raw)
	}
	if e.deploys.runs["01JCXMPSRRVN00000000000AB9"].State != models.GitDeploySucceeded {
		t.Fatal("the ended run was not stored")
	}
}

// A start that failed on the panel's side lets the host's next try of the
// same delivery through.
func TestGitWebhook_AFailedStartLetsTheRetryThrough(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	e.agent.errs["git.deploy_start"] = errors.New("agent socket: connection refused")
	if code, _, raw := e.push(t, hookID, secret, "main", "d-1"); code != http.StatusInternalServerError {
		t.Fatalf("push = %d %s", code, raw)
	}
	if e.hooks.rows["d1"].LastResult != "failed" {
		t.Fatalf("recorded %+v", e.hooks.rows["d1"])
	}
	delete(e.agent.errs, "git.deploy_start")
	if code, out, raw := e.push(t, hookID, secret, "main", "d-1"); code != http.StatusAccepted || out["result"] != "started" {
		t.Fatalf("retry = %d %s", code, raw)
	}
}

func TestGitWebhook_APackageWithoutGitDeployIsRefused(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	e.pkg.GitDeployEnabled = false
	code, out, raw := e.push(t, hookID, secret, "main", "d-1")
	if code != http.StatusForbidden || out["error"] != "git_deploy_not_in_package" {
		t.Fatalf("push = %d %s", code, raw)
	}
	if len(e.webhookRuns()) != 0 || e.hooks.rows["d1"].LastResult != "refused:git_deploy_not_in_package" {
		t.Fatalf("runs %d, recorded %+v", len(e.webhookRuns()), e.hooks.rows["d1"])
	}
}

func TestGitWebhook_ARemovedWebhookIsUnknown(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	if code, _, _ := e.do(t, http.MethodDelete, "/domains/d1/git/webhook", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	if code, _, _ := e.push(t, hookID, secret, "main", "d-1"); code != http.StatusNotFound {
		t.Fatalf("push = %d", code)
	}
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/webhook", ""); code != http.StatusOK || out["configured"] != false {
		t.Fatalf("GET = %d %v", code, out)
	}
}

// Past a few starts a minute, a push waits as it does during a run.
func TestGitWebhook_ABurstOfPushesWaits(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	for i := 0; i < gitWebhookStartBurst; i++ {
		if code, out, raw := e.push(t, hookID, secret, "main", "d-"+strconv.Itoa(i)); code != http.StatusAccepted || out["result"] != "started" {
			t.Fatalf("push %d = %d %s", i, code, raw)
		}
		e.finishRuns()
	}
	code, out, raw := e.push(t, hookID, secret, "main", "d-last")
	if code != http.StatusAccepted || out["result"] != "queued" || e.hooks.rows["d1"].QueuedAt == nil {
		t.Fatalf("one more = %d %s", code, raw)
	}
	if n := len(e.webhookRuns()); n != gitWebhookStartBurst {
		t.Fatalf("runs = %d", n)
	}
}

// Settings left by a domain's previous owner deploy nothing (GH #1238): a
// change of owner removes them, and the webhook with them.
func TestGitWebhook_APreviousOwnersSettingsDeployNothing(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	e.repos.rows["d1"].UserID = "u-bob"
	if code, out, raw := e.push(t, hookID, secret, "main", "d-1"); code != http.StatusNotFound || out["error"] != "git_not_configured" {
		t.Fatalf("push = %d %s", code, raw)
	}
	if len(e.webhookRuns()) != 0 {
		t.Fatal("a run started")
	}
}

func TestGitWebhook_ABodyOver1MiBIsRefused(t *testing.T) {
	e := newGitTestEnv(t)
	hookID, secret := e.hookUp(t)
	body := `{"ref":"refs/heads/main","pad":"` + strings.Repeat("x", gitdeploy.WebhookMaxBody) + `"}`
	code, _, _ := e.post(t, hookID, body, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": gitTestSign(secret, body)})
	if code != http.StatusRequestEntityTooLarge || len(e.webhookRuns()) != 0 {
		t.Fatalf("push = %d, runs %d", code, len(e.webhookRuns()))
	}
}
