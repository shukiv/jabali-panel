package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type limitCountRepo struct {
	sendOnlyFlipRepo
	count int64
}

func (r *limitCountRepo) CountByOwner(context.Context, string) (int64, error) { return r.count, nil }

type mbLimitUsers struct{ repository.UserRepository }

func (mbLimitUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	pkg := "p1"
	return &models.User{ID: id, PackageID: &pkg}, nil
}

type mbLimitPackages struct {
	repository.PackageRepository
	max uint32
	err error
}

func (p mbLimitPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &models.HostingPackage{ID: id, MaxEmailAccounts: p.max}, nil
}

func limitRouter(repo *limitCountRepo, pkgs mbLimitPackages) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterMailboxRoutes(r.Group(""), MailboxHandlerConfig{
		Mailboxes: repo,
		Domains:   &saDomainRepo{byID: map[string]*models.Domain{"dom1": sendOnlyDomain(true, models.MailProviderJabali)}},
		Addresses: &recAddressReleaser{},
		Users:     mbLimitUsers{},
		Packages:  pkgs,
	})
	return r
}

// The package's email-account limit was stored and shown as "0 = unlimited"
// but a create never checked it.
func TestMailboxCreate_PackageEmailAccountLimit(t *testing.T) {
	repo := &limitCountRepo{count: 2}
	w := sendOnlyRequest(t, limitRouter(repo, mbLimitPackages{max: 2}), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"third"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "quota_exceeded" || body["resource"] != "email_accounts" {
		t.Errorf("body = %v", body)
	}
	if repo.created {
		t.Fatal("a row was written past the limit")
	}
}

func TestMailboxCreate_PackageLimitUnavailable(t *testing.T) {
	repo := &limitCountRepo{}
	w := sendOnlyRequest(t, limitRouter(repo, mbLimitPackages{err: errors.New("db down")}), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"alice"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", w.Code, w.Body.String())
	}
	if repo.created {
		t.Fatal("a row was written without the limit")
	}
}
