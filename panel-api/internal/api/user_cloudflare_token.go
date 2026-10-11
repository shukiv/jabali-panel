package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/hostedsvc"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// GH #2032 — a tenant's own Cloudflare API token for DNS-01.
//
// The reconciler and the certbot hook try it before the server's token
// (Settings → SSL) for the tenant's own domains on the tenant's own
// certificates (internal/cftoken). The secret is handled as the server's
// is (ssl_cloudflare_token.go): sealed with the sso key, written only
// here, never returned or logged. The audit middleware records each change
// with the real actor, an impersonating admin included, and never reads
// the body.
//
// Verify & Save proves the token can do the job before storing it: the
// token is valid and active, it can see a zone holding the tenant's
// domains, and in each such zone it can create and delete a TXT record
// (_jabali-dns01-check.<zone>). A read-only token, or one for someone
// else's zones, is refused now instead of failing a certificate later.

// cfProbeMaxZones bounds the zones one Verify & Save writes into.
const cfProbeMaxZones = 10

// cfProbeRecordLabel is the probe record's label under each zone.
const cfProbeRecordLabel = "_jabali-dns01-check"

// cfTokenCharset is Cloudflare's token alphabet. It also keeps the token
// out of any header or log line it could break.
var cfTokenCharset = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CloudflareProbeClient is the slice of hostedsvc.CloudflareDNS the probe
// uses.
type CloudflareProbeClient interface {
	VerifyToken(ctx context.Context) (int, error)
	ListZones(ctx context.Context) ([]hostedsvc.CloudflareZone, error)
	SetChallengeTXT(ctx context.Context, zoneID, name, value string) error
	ClearChallengeTXT(ctx context.Context, zoneID, name string) error
}

// UserDomainLister lists one user's domains.
type UserDomainLister interface {
	ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.Domain, int64, error)
}

// UserCloudflareTokenHandlerConfig wires /me/cloudflare-token.
type UserCloudflareTokenHandlerConfig struct {
	Tokens  repository.UserCloudflareTokenRepository
	Domains UserDomainLister
	SSOKey  *ssokey.Key
	Log     *slog.Logger
	// StrictRateLimit bounds PUT per user: each call makes the panel call
	// Cloudflare with a credential the caller supplied.
	StrictRateLimit gin.HandlerFunc
	// NewClient is a test seam; nil uses hostedsvc.NewCloudflareAPI.
	NewClient func(token string) CloudflareProbeClient
}

// RegisterUserCloudflareTokenRoutes mounts /me/cloudflare-token under v1.
func RegisterUserCloudflareTokenRoutes(g *gin.RouterGroup, cfg UserCloudflareTokenHandlerConfig) {
	h := &userCFTokenHandler{cfg: cfg}
	var limit []gin.HandlerFunc
	if cfg.StrictRateLimit != nil {
		limit = append(limit, cfg.StrictRateLimit)
	}
	g.GET("/me/cloudflare-token", h.status)
	g.PUT("/me/cloudflare-token", append(limit, h.set)...)
	g.DELETE("/me/cloudflare-token", h.clear)
}

type userCFTokenHandler struct {
	cfg UserCloudflareTokenHandlerConfig
}

// userCFTokenView never carries the token.
type userCFTokenView struct {
	Configured bool       `json:"configured"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// Zones are the zones Verify & Save wrote and deleted the check record in.
	Zones []string `json:"zones,omitempty"`
	// ZonesNotChecked counts zones holding the caller's domains past the cap.
	ZonesNotChecked int `json:"zones_not_checked,omitempty"`
}

func (h *userCFTokenHandler) caller(c *gin.Context) (string, bool) {
	cl := ginctx.Claims(c)
	if cl == nil || cl.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	return cl.UserID, true
}

func (h *userCFTokenHandler) status(c *gin.Context) {
	uid, ok := h.caller(c)
	if !ok {
		return
	}
	row, err := h.cfg.Tokens.Get(c.Request.Context(), uid)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusOK, userCFTokenView{})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token_read_failed", "detail": "could not read your Cloudflare token"})
		return
	}
	verified := row.VerifiedAt
	c.JSON(http.StatusOK, userCFTokenView{Configured: true, VerifiedAt: &verified})
}

func (h *userCFTokenHandler) set(c *gin.Context) {
	uid, ok := h.caller(c)
	if !ok {
		return
	}
	var req cfTokenSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": "body must be {\"token\": \"...\"}"})
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || len(token) > cfTokenMaxLen || !cfTokenCharset.MatchString(token) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_token", "detail": "a Cloudflare API token is up to 256 letters, digits, '-' and '_'"})
		return
	}
	if h.cfg.SSOKey == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured — cannot store the token securely"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	newClient := h.cfg.NewClient
	if newClient == nil {
		newClient = func(tok string) CloudflareProbeClient { return hostedsvc.NewCloudflareAPI(tok) }
	}
	cf := newClient(token)

	if _, err := cf.VerifyToken(ctx); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "token_verify_failed", "detail": err.Error()})
		return
	}
	domains, _, err := h.cfg.Domains.ListByUserID(ctx, uid, repository.ListOptions{OmitHeavyColumns: true})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "domains_read_failed", "detail": "could not read your domains"})
		return
	}
	if len(domains) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "no_domains", "detail": "you have no domains yet — add one, then save the token"})
		return
	}
	zones, err := cf.ListZones(ctx)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "zone_list_failed", "detail": err.Error()})
		return
	}
	mine := zonesHoldingDomains(zones, domains)
	if len(mine) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "no_zone_for_domains",
			"detail": "the token cannot see a Cloudflare zone holding any of your domains — give it Zone:Read and DNS:Edit on those zones, and check their nameservers are at Cloudflare"})
		return
	}
	notChecked := 0
	if len(mine) > cfProbeMaxZones {
		notChecked = len(mine) - cfProbeMaxZones
		mine = mine[:cfProbeMaxZones]
	}

	var probed []string
	for _, z := range mine {
		if code, body := probeZone(ctx, cf, z); body != nil {
			c.JSON(code, body)
			return
		}
		probed = append(probed, z.Name)
	}

	sealed, err := h.cfg.SSOKey.Seal([]byte(token))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seal_failed", "detail": "could not encrypt the token"})
		return
	}
	row := &models.UserCloudflareToken{UserID: uid, TokenEnc: sealed, VerifiedAt: time.Now().UTC()}
	if err := h.cfg.Tokens.Put(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token_write_failed", "detail": "could not store the token"})
		return
	}
	if h.cfg.Log != nil {
		h.cfg.Log.Info("tenant Cloudflare API token stored for DNS-01", "user_id", uid, "zones_checked", len(probed), "zones_not_checked", notChecked)
	}
	verified := row.VerifiedAt
	c.JSON(http.StatusOK, userCFTokenView{Configured: true, VerifiedAt: &verified, Zones: probed, ZonesNotChecked: notChecked})
}

// zonesHoldingDomains returns the zones that hold at least one of the
// domains (the domain is the zone or under it, on a label boundary),
// sorted by name. Zones holding none are never written to.
func zonesHoldingDomains(zones []hostedsvc.CloudflareZone, domains []models.Domain) []hostedsvc.CloudflareZone {
	var out []hostedsvc.CloudflareZone
	for _, z := range zones {
		zn := strings.ToLower(strings.TrimSuffix(z.Name, "."))
		if zn == "" || z.ID == "" {
			continue
		}
		for _, d := range domains {
			dn := strings.ToLower(strings.TrimSuffix(d.Name, "."))
			if dn == zn || strings.HasSuffix(dn, "."+zn) {
				out = append(out, hostedsvc.CloudflareZone{ID: z.ID, Name: zn})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// probeZone writes and deletes the check record in z. A nil body means it
// worked. The delete runs on its own context so a dropped request does not
// strand the record.
func probeZone(ctx context.Context, cf CloudflareProbeClient, z hostedsvc.CloudflareZone) (int, gin.H) {
	name := cfProbeRecordLabel + "." + z.Name
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return http.StatusInternalServerError, gin.H{"error": "probe_failed", "detail": "could not make a check value"}
	}
	setErr := cf.SetChallengeTXT(ctx, z.ID, name, hex.EncodeToString(nonce[:]))

	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	clearErr := cf.ClearChallengeTXT(clearCtx, z.ID, name)
	cancel()

	if setErr != nil {
		return http.StatusUnprocessableEntity, gin.H{"error": "token_cannot_write",
			"detail": fmt.Sprintf("the token cannot add a TXT record in %s (it needs DNS:Edit there): %v", z.Name, setErr)}
	}
	if clearErr != nil {
		return http.StatusUnprocessableEntity, gin.H{"error": "token_cannot_delete",
			"detail": fmt.Sprintf("the token added %s but could not delete it — remove that record in Cloudflare: %v", name, clearErr)}
	}
	return 0, nil
}

func (h *userCFTokenHandler) clear(c *gin.Context) {
	uid, ok := h.caller(c)
	if !ok {
		return
	}
	err := h.cfg.Tokens.Delete(c.Request.Context(), uid)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token_write_failed", "detail": "could not remove the token"})
		return
	}
	if err == nil && h.cfg.Log != nil {
		h.cfg.Log.Info("tenant Cloudflare API token removed", "user_id", uid)
	}
	c.JSON(http.StatusOK, userCFTokenView{})
}
