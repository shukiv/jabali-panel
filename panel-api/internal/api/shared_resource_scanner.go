package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/scandrops"
)

// GH #2503: a shared Files resource's scanner — an FTP/SFTP login homed at a
// drop folder whose finished files the panel moves into the shared folder.
//
//   - GET    /shared-resources/:rid/scanner   state (off, or the login and the last pass)
//   - PUT    /shared-resources/:rid/scanner   turn on: make the folder and the login
//   - DELETE /shared-resources/:rid/scanner   turn off: remove the login

type scannerResponse struct {
	Enabled       bool       `json:"enabled"`
	Username      string     `json:"username,omitempty"`
	DropPath      string     `json:"drop_path,omitempty"`
	MaxMB         uint32     `json:"max_mb,omitempty"`
	IngestedTotal uint64     `json:"ingested_total"`
	LastIngestAt  *time.Time `json:"last_ingest_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Isolated      bool       `json:"isolated"`
	FTPAccess     bool       `json:"ftp_access"`
	SFTPAccess    bool       `json:"sftp_access"`
	// LoginMissing: the login was deleted; the next pass turns the scanner off.
	LoginMissing bool `json:"login_missing,omitempty"`
}

func scannerView(row *models.ScanDrop, acct *models.FtpAccount) scannerResponse {
	if row == nil {
		return scannerResponse{}
	}
	v := scannerResponse{
		Enabled:       true,
		DropPath:      row.DropPath,
		MaxMB:         row.MaxMB,
		IngestedTotal: row.IngestedTotal,
		LastIngestAt:  row.LastIngestAt,
		LastError:     row.LastError,
		LoginMissing:  acct == nil,
	}
	if acct != nil {
		v.Username = acct.Username
		v.Isolated = acct.Isolated
		v.FTPAccess = acct.FTPAccess
		v.SFTPAccess = acct.SFTPAccess
	}
	return v
}

type enableScannerRequest struct {
	Label    string `json:"label"`
	Password string `json:"password"`
	MaxMB    uint32 `json:"max_mb"`
}

func (h *sharedResourceHandler) getScanner(c *gin.Context) {
	sr, _, ok := h.loadResourceWithAuth(c, c.Param("rid"), ginctx.Claims(c))
	if !ok {
		return
	}
	if sr.Kind != "files" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_files", "detail": "only a Files resource takes a scanner"})
		return
	}
	row, acct, err := scandrops.Get(c.Request.Context(), *h.cfg.Scanners, sr.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, scannerView(row, acct))
}

func (h *sharedResourceHandler) enableScanner(c *gin.Context) {
	sr, dom, ok := h.loadResourceWithAuth(c, c.Param("rid"), ginctx.Claims(c))
	if !ok {
		return
	}
	var req enableScannerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	row, acct, err := scandrops.Enable(c.Request.Context(), *h.cfg.Scanners, scandrops.EnableInput{
		Resource: sr,
		Domain:   dom,
		Label:    req.Label,
		Password: req.Password,
		MaxMB:    req.MaxMB,
	})
	if err != nil {
		writeScannerErr(c, err)
		return
	}
	c.JSON(http.StatusOK, scannerView(row, acct))
}

func (h *sharedResourceHandler) disableScanner(c *gin.Context) {
	sr, _, ok := h.loadResourceWithAuth(c, c.Param("rid"), ginctx.Claims(c))
	if !ok {
		return
	}
	if err := scandrops.Disable(c.Request.Context(), *h.cfg.Scanners, sr.ID); err != nil {
		writeScannerErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// writeScannerErr maps a scandrops error to the response. The login's own
// failures answer as the FTP page's create does.
func writeScannerErr(c *gin.Context, err error) {
	var ve *ftpops.ValidationError
	if errors.As(err, &ve) {
		if r, ok := ftpValidationResponses[ve.Reason]; ok {
			c.JSON(r.status, gin.H{"error": r.code, "detail": ve.Detail})
			return
		}
	}
	switch {
	case errors.Is(err, scandrops.ErrNotFiles):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_files", "detail": "only a Files resource takes a scanner"})
	case errors.Is(err, scandrops.ErrAlreadyOn):
		c.JSON(http.StatusConflict, gin.H{"error": "scanner_on", "detail": "this folder already has a scanner"})
	case errors.Is(err, scandrops.ErrNotOn):
		c.JSON(http.StatusNotFound, gin.H{"error": "scanner_off", "detail": "this folder has no scanner"})
	case errors.Is(err, scandrops.ErrNoAccount):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "no_account", "detail": "the domain's owner has no hosting account"})
	case errors.Is(err, scandrops.ErrNoPackage):
		c.JSON(http.StatusForbidden, gin.H{"error": "ftp_accounts_require_package", "detail": "a scanner login needs a hosting package that includes FTP/SFTP accounts"})
	case errors.Is(err, scandrops.ErrNotInPackage):
		c.JSON(http.StatusForbidden, gin.H{"error": "ftp_accounts_not_in_package", "detail": "the hosting package does not include FTP/SFTP accounts"})
	case errors.Is(err, scandrops.ErrSpace):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_space", "detail": strings.TrimPrefix(err.Error(), scandrops.ErrSpace.Error()+": ")})
	case errors.Is(err, scandrops.ErrName):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "unusable_name", "detail": "this folder's address cannot name a drop folder; make a Files resource whose address starts with a letter or digit"})
	case errors.Is(err, ftpops.ErrUIDAllocation):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "uid_alloc_failed"})
	case errors.Is(err, repository.ErrFtpCapExceeded):
		c.JSON(http.StatusConflict, gin.H{"error": "ftp_account_quota_exceeded", "detail": "the package's FTP/SFTP account limit is reached; the scanner login counts as one"})
	case errors.Is(err, repository.ErrFtpQuotaSplitExceeded):
		c.JSON(http.StatusConflict, gin.H{"error": "quota_split_exceeded", "detail": "this space would exceed the package disk quota left for isolated logins"})
	case errors.Is(err, repository.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "account_exists", "detail": "a login with this name exists; pick another"})
	case errors.Is(err, scandrops.ErrDeps), errors.Is(err, ftpops.ErrPersist):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	default:
		status, body := mapFtpAgentError(err, "scanner_failed")
		c.JSON(status, body)
	}
}
