package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailsso"
)

// --- fakes (M6.6 SSO landing) ---

type ssoFakeTokenRepo struct {
	mu     sync.Mutex
	tokens map[string]*models.MailboxSSOToken
}

func (f *ssoFakeTokenRepo) Create(ctx context.Context, t *models.MailboxSSOToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokens == nil {
		f.tokens = map[string]*models.MailboxSSOToken{}
	}
	f.tokens[t.TokenHash] = t
	return nil
}
func (f *ssoFakeTokenRepo) PeekByHash(ctx context.Context, hash string) (*models.MailboxSSOToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[hash]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return t, nil
}
func (f *ssoFakeTokenRepo) ConsumeByHash(ctx context.Context, hash string) (*models.MailboxSSOToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[hash]
	if !ok {
		return nil, repository.ErrNotFound
	}
	delete(f.tokens, hash)
	return t, nil
}
func (f *ssoFakeTokenRepo) DeleteByHash(ctx context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tokens, hash)
	return nil
}
func (f *ssoFakeTokenRepo) PurgeExpired(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := int64(0)
	now := time.Now()
	for h, t := range f.tokens {
		if t.ExpiresAt.Before(now) {
			delete(f.tokens, h)
			n++
		}
	}
	return n, nil
}

type ssoFakeMailboxRepo struct {
	mbs map[string]*models.Mailbox
}

func (r *ssoFakeMailboxRepo) CountAll(context.Context) (int64, error) { return 0, nil }
func (r *ssoFakeMailboxRepo) FindByID(ctx context.Context, id string) (*models.Mailbox, error) {
	if mb, ok := r.mbs[id]; ok {
		return mb, nil
	}
	return nil, repository.ErrNotFound
}

func (r *ssoFakeMailboxRepo) FindByIDs(_ context.Context, ids []string) ([]models.Mailbox, error) {
	var out []models.Mailbox
	for _, id := range ids {
		if mb, ok := r.mbs[id]; ok {
			out = append(out, *mb)
		}
	}
	return out, nil
}
func (r *ssoFakeMailboxRepo) Create(ctx context.Context, mb *models.Mailbox) error { return nil }
func (r *ssoFakeMailboxRepo) FindByEmail(ctx context.Context, email string) (*models.Mailbox, error) {
	return nil, repository.ErrNotFound
}
func (r *ssoFakeMailboxRepo) ListByDomainID(ctx context.Context, domainID string, opts repository.ListOptions) ([]models.Mailbox, int64, error) {
	return nil, 0, nil
}
func (r *ssoFakeMailboxRepo) CountByDomainID(ctx context.Context, domainID string) (int64, error) {
	return 0, nil
}
func (r *ssoFakeMailboxRepo) ExistsByDomainAndLocalPart(ctx context.Context, domainID, localPart string) (bool, error) {
	return false, nil
}
func (r *ssoFakeMailboxRepo) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	return nil
}
func (r *ssoFakeMailboxRepo) UpdatePasswordHashAndEnc(ctx context.Context, id, hash string, enc []byte) error {
	return nil
}
func (r *ssoFakeMailboxRepo) UpdateQuota(ctx context.Context, id string, quotaBytes uint64) error {
	return nil
}

func (r *ssoFakeMailboxRepo) UpdateDisplayName(_ context.Context, _ string, _ string) error {
	return nil
}
func (r *ssoFakeMailboxRepo) SetDisabled(_ context.Context, _ string, _ bool) error { return nil }
func (r *ssoFakeMailboxRepo) SetSendOnly(_ context.Context, _ string, _ bool) error { return nil }
func (r *ssoFakeMailboxRepo) ListAllWithDomain(_ context.Context) ([]repository.MailboxWithDomain, error) {
	return nil, nil
}
func (r *ssoFakeMailboxRepo) ListByOwnerWithDomain(_ context.Context, _ string) ([]repository.MailboxWithDomain, error) {
	return nil, nil
}
func (r *ssoFakeMailboxRepo) UpdateDisabled(ctx context.Context, id string, disabled bool) error {
	return nil
}
func (r *ssoFakeMailboxRepo) UpdateUsage(ctx context.Context, id string, usageBytes uint64, at time.Time) error {
	return nil
}
func (r *ssoFakeMailboxRepo) Delete(ctx context.Context, id string) error { return nil }
func (r *ssoFakeMailboxRepo) ListByDomainIDs(ctx context.Context, domainIDs []string) ([]models.Mailbox, error) {
	return nil, nil
}

type ssoFakeUserRepo struct {
	repository.UserRepository
	users map[string]*models.User
}

func (r *ssoFakeUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := r.users[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

// ssoOKUsers returns a user repo with one active, webmail-enabled owner.
func ssoOKUsers(userID string) *ssoFakeUserRepo {
	return &ssoFakeUserRepo{users: map[string]*models.User{
		userID: {ID: userID, WebmailEnabled: true, Suspended: false},
	}}
}

// --- helpers ---

func ssoNewTestMinter(t *testing.T) *webmailsso.Minter {
	t.Helper()
	secret := []byte(strings.Repeat("k", webmailsso.MinSecretLength))
	m, err := webmailsso.New(secret, "test-issuer")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ssoMintTestToken(t *testing.T, repo *ssoFakeTokenRepo, mailboxID string, exp time.Time) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	hash := sha256.Sum256(raw)
	hashHex := hex.EncodeToString(hash[:])
	if err := repo.Create(context.Background(), &models.MailboxSSOToken{
		ID:        "tok-1",
		MailboxID: mailboxID,
		TokenHash: hashHex,
		ExpiresAt: exp,
	}); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func ssoHashHexOfToken(token string) string {
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func ssoFakeSSOKey(t *testing.T) *ssokey.Key {
	t.Helper()
	var k ssokey.Key // zero-valued; new handler only nil-checks the pointer
	return &k
}

// --- tests ---

func TestWebmailSSO_RedirectsToImpersonate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mb := &models.Mailbox{
		ID:          "mb-1",
		DomainID:    "dom-1",
		EmailCached: "alice@example.com",
	}
	dom := &models.Domain{ID: "dom-1", Name: "example.com", UserID: "usr-1", WebmailEnabled: true}
	mboxRepo := &ssoFakeMailboxRepo{mbs: map[string]*models.Mailbox{mb.ID: mb}}
	domRepo := newMockDomainRepo()
	domRepo.domains[dom.ID] = dom
	tokRepo := &ssoFakeTokenRepo{}
	token := ssoMintTestToken(t, tokRepo, mb.ID, time.Now().Add(time.Minute))

	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: mboxRepo,
		Domains:   domRepo,
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: tokRepo,
		Users:     ssoOKUsers("usr-1"),
		Minter:    ssoNewTestMinter(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token="+token, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("code = %d; want 303 (body: %s)", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("no Location header")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("invalid Location URL %q: %v", loc, err)
	}
	if u.Scheme != "https" {
		t.Errorf("Location scheme = %q; want https", u.Scheme)
	}
	if u.Host != "mail.example.com" {
		t.Errorf("Location host = %q; want mail.example.com", u.Host)
	}
	if u.Path != "/api/auth/impersonate" {
		t.Errorf("Location path = %q; want /api/auth/impersonate", u.Path)
	}
	jwt := u.Query().Get("token")
	if jwt == "" {
		t.Fatal("Location missing token query param")
	}
	if parts := strings.Split(jwt, "."); len(parts) != 3 {
		t.Errorf("jwt segments = %d; want 3 (%q)", len(parts), jwt)
	}
	if _, ok := tokRepo.tokens[ssoHashHexOfToken(token)]; ok {
		t.Error("SSO token not deleted after redirect (single-use violated)")
	}
}

func TestWebmailSSO_MissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: &ssoFakeMailboxRepo{},
		Domains:   newMockDomainRepo(),
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: &ssoFakeTokenRepo{},
		Users:     ssoOKUsers("x"),
		Minter:    ssoNewTestMinter(t),
	})
	req := httptest.NewRequest(http.MethodGet, "/sso/webmail", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d; want 400", w.Code)
	}
}

func TestWebmailSSO_InvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: &ssoFakeMailboxRepo{},
		Domains:   newMockDomainRepo(),
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: &ssoFakeTokenRepo{},
		Users:     ssoOKUsers("x"),
		Minter:    ssoNewTestMinter(t),
	})
	req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token=!!!not-base64!!!", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d; want 400", w.Code)
	}
}

func TestWebmailSSO_UnknownToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: &ssoFakeMailboxRepo{},
		Domains:   newMockDomainRepo(),
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: &ssoFakeTokenRepo{},
		Users:     ssoOKUsers("x"),
		Minter:    ssoNewTestMinter(t),
	})
	bogus := base64.RawURLEncoding.EncodeToString([]byte("garbage-but-base64-decodes-fine"))
	req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token="+bogus, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("code = %d; want 403", w.Code)
	}
}

func TestWebmailSSO_NoMinterReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: &ssoFakeMailboxRepo{},
		Domains:   newMockDomainRepo(),
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: &ssoFakeTokenRepo{},
		// Minter intentionally nil.
	})
	bogus := base64.RawURLEncoding.EncodeToString([]byte("x"))
	req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token="+bogus, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d; want 503 (body: %s)", w.Code, w.Body.String())
	}
}

func TestWebmailSSO_PrefetchRequestDoesNotConsumeToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tokRepo := &ssoFakeTokenRepo{}
	mb := &models.Mailbox{ID: "mb-1", DomainID: "dom-1", EmailCached: "alice@example.com"}
	dom := &models.Domain{ID: "dom-1", Name: "example.com"}
	domRepo := newMockDomainRepo()
	domRepo.domains[dom.ID] = dom
	token := ssoMintTestToken(t, tokRepo, mb.ID, time.Now().Add(time.Minute))

	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: &ssoFakeMailboxRepo{mbs: map[string]*models.Mailbox{mb.ID: mb}},
		Domains:   domRepo,
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: tokRepo,
		Minter:    ssoNewTestMinter(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token="+token, nil)
	req.Header.Set("Sec-Purpose", "prefetch")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("code = %d; want 204 for prefetch", w.Code)
	}
	if _, ok := tokRepo.tokens[ssoHashHexOfToken(token)]; !ok {
		t.Error("token was consumed on prefetch — should be preserved")
	}
}

// buildWebmailSSO wires a handler + a freshly-minted token for the given
// mailbox/domain/user state. Returns the router and the token string.
func buildWebmailSSO(t *testing.T, mb *models.Mailbox, dom *models.Domain, usr *models.User) (*gin.Engine, string, *ssoFakeTokenRepo) {
	t.Helper()
	tokRepo := &ssoFakeTokenRepo{}
	domRepo := newMockDomainRepo()
	domRepo.domains[dom.ID] = dom
	token := ssoMintTestToken(t, tokRepo, mb.ID, time.Now().Add(time.Minute))
	r := gin.New()
	RegisterWebmailSSORoutes(r, WebmailSSOHandlerConfig{
		Mailboxes: &ssoFakeMailboxRepo{mbs: map[string]*models.Mailbox{mb.ID: mb}},
		Domains:   domRepo,
		SSOKey:    ssoFakeSSOKey(t),
		SSOTokens: tokRepo,
		Users:     &ssoFakeUserRepo{users: map[string]*models.User{usr.ID: usr}},
		Minter:    ssoNewTestMinter(t),
	})
	return r, token, tokRepo
}

// JAB-9: concurrent redemption of one token must mint at most one JWT.
func TestWebmailSSO_ConcurrentRedemptionMintsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mb := &models.Mailbox{ID: "mb-1", DomainID: "dom-1", EmailCached: "alice@example.com"}
	dom := &models.Domain{ID: "dom-1", Name: "example.com", UserID: "usr-1", WebmailEnabled: true}
	usr := &models.User{ID: "usr-1", WebmailEnabled: true}
	r, token, _ := buildWebmailSSO(t, mb, dom, usr)

	const n = 12
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token="+token, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i)
	}
	close(start)
	wg.Wait()

	redirects := 0
	for _, c := range codes {
		if c == http.StatusSeeOther {
			redirects++
		}
	}
	if redirects != 1 {
		t.Errorf("concurrent redemption minted %d JWTs; want exactly 1 (codes=%v)", redirects, codes)
	}
}

// JAB-9: current policy blocks redemption even for a valid, unexpired token —
// with a generic 403 (no oracle) and the token stays burned.
func TestWebmailSSO_PolicyFreshnessBlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		mb   *models.Mailbox
		dom  *models.Domain
		usr  *models.User
	}{
		{"mailbox disabled",
			&models.Mailbox{ID: "mb-1", DomainID: "dom-1", EmailCached: "a@example.com", IsDisabled: true},
			&models.Domain{ID: "dom-1", Name: "example.com", UserID: "usr-1", WebmailEnabled: true},
			&models.User{ID: "usr-1", WebmailEnabled: true}},
		{"domain webmail disabled",
			&models.Mailbox{ID: "mb-1", DomainID: "dom-1", EmailCached: "a@example.com"},
			&models.Domain{ID: "dom-1", Name: "example.com", UserID: "usr-1", WebmailEnabled: false},
			&models.User{ID: "usr-1", WebmailEnabled: true}},
		{"user webmail disabled",
			&models.Mailbox{ID: "mb-1", DomainID: "dom-1", EmailCached: "a@example.com"},
			&models.Domain{ID: "dom-1", Name: "example.com", UserID: "usr-1", WebmailEnabled: true},
			&models.User{ID: "usr-1", WebmailEnabled: false}},
		{"user suspended",
			&models.Mailbox{ID: "mb-1", DomainID: "dom-1", EmailCached: "a@example.com"},
			&models.Domain{ID: "dom-1", Name: "example.com", UserID: "usr-1", WebmailEnabled: true},
			&models.User{ID: "usr-1", WebmailEnabled: true, Suspended: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, token, tokRepo := buildWebmailSSO(t, tc.mb, tc.dom, tc.usr)
			req := httptest.NewRequest(http.MethodGet, "/sso/webmail?token="+token, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("code = %d; want 403 (generic expired page)", w.Code)
			}
			// Token must stay burned (consumed before the policy check).
			if _, ok := tokRepo.tokens[ssoHashHexOfToken(token)]; ok {
				t.Error("token not burned on policy refusal — enables retry/oracle")
			}
		})
	}
}

// webmailBlockReason maps each disabling condition to a distinct audit reason.
func TestWebmailBlockReason(t *testing.T) {
	if webmailBlockReason(false, true, true, false) != "" {
		t.Error("all-clear should return empty reason")
	}
	if webmailBlockReason(true, true, true, false) != "mailbox_disabled" {
		t.Error("mailbox_disabled")
	}
	if webmailBlockReason(false, false, true, false) != "domain_webmail_disabled" {
		t.Error("domain_webmail_disabled")
	}
	if webmailBlockReason(false, true, false, false) != "user_webmail_disabled" {
		t.Error("user_webmail_disabled")
	}
	if webmailBlockReason(false, true, true, true) != "user_suspended" {
		t.Error("user_suspended")
	}
}
