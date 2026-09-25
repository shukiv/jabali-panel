package ftpops

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Subaccount naming and GH #1145 isolation constants. These MUST match the
// panel-agent (ftp_account_jail.go) and migration 000267 — the panel computes
// the jail path and validates the input the agent re-checks.
const (
	JailRoot       = "/var/lib/jabali-ftp-jails"
	UsernameMaxLen = 32

	homePathBadRunes = " \t\r\n\"'\\:"
)

// labelRE mirrors the agent's subaccount label rule — validated at the panel
// boundary too (defense in depth; the agent re-checks).
var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,19}$`)

// Typed create failures. Input rejections arrive as a *ValidationError whose
// Reason is one of these; ErrUIDAllocation wraps the allocator error.
var (
	ErrInvalidLabel         = errors.New("ftpops: invalid label")
	ErrLabelTooLong         = errors.New("ftpops: account name too long")
	ErrInvalidHomePath      = errors.New("ftpops: invalid home path")
	ErrIsolationUnavailable = errors.New("ftpops: isolation unavailable")
	ErrQuotaRequired        = errors.New("ftpops: isolated account requires a quota")
	ErrUIDAllocation        = errors.New("ftpops: uid allocation failed")
)

// Owner identifies the tenant a subaccount is created for.
type Owner struct {
	UserID   string
	Username string // the tenant's Linux username
}

// CreateRequest is the transport-neutral create input.
type CreateRequest struct {
	Label        string
	HomePath     string
	Password     string
	FTPAccess    bool
	SFTPAccess   *bool // nil = true
	WebDAVAccess bool
	// Isolated selects the GH #1145 separate-uid jailed model; false is the
	// legacy same-uid alias.
	Isolated bool
	// QuotaMB is the per-account disk cap, required when Isolated — an isolated
	// separate uid escapes the tenant's package quota without it.
	QuotaMB uint32
}

// ValidateHomePath enforces the panel-side half of the home_path contract:
// absolute, clean, safe charset, inside the tenant home. The agent additionally
// symlink-resolves it.
func ValidateHomePath(homePath, tenantHome string) error {
	if !filepath.IsAbs(homePath) || filepath.Clean(homePath) != homePath {
		return invalid(ErrInvalidHomePath, "home_path must be an absolute, clean path")
	}
	if strings.ContainsAny(homePath, homePathBadRunes) {
		return invalid(ErrInvalidHomePath, "home_path must not contain whitespace, quotes, backslashes, or colons")
	}
	rel, err := filepath.Rel(tenantHome, homePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return invalid(ErrInvalidHomePath, fmt.Sprintf("home_path must be inside your home directory (%s)", tenantHome))
	}
	return nil
}

// Create validates the request, reserves the row within the package cap, then
// creates the host alias and re-renders the sshd drop-in.
//
// Ordering (JAB-262 / JAB-255): the row is RESERVED first — ReserveWithinCap
// atomically enforces the account cap and the isolated quota split under a
// per-tenant lock, so concurrent creates can never exceed the cap, and the row
// always exists before any alias does. If the host create then fails, the
// reservation is compensated on a context detached from cancellation, so a
// client disconnect cannot also abort the compensating delete and strand a
// slot-consuming row. (If that delete itself fails, the surviving row is a
// valid desired-state account the reconciler provisions — never a phantom cap
// consumer.)
//
// Errors: a *ValidationError for a rejected input (no side effects);
// ErrUIDAllocation for an isolated uid allocation failure; an ErrPersist-wrapped
// repository error for a failed reservation (repository.ErrFtpCapExceeded /
// ErrFtpQuotaSplitExceeded / ErrConflict stay in the chain); else the raw agent
// error from the host create.
func Create(ctx context.Context, d Deps, owner Owner, pkg *models.HostingPackage, req CreateRequest) (*models.FtpAccount, error) {
	if !labelRE.MatchString(req.Label) {
		return nil, invalid(ErrInvalidLabel, "label must be lowercase letters, digits, or underscores (max 20 chars)")
	}
	username := owner.Username + "_" + req.Label
	if len(username) > UsernameMaxLen {
		return nil, invalid(ErrLabelTooLong, fmt.Sprintf("full account name %q exceeds %d characters", username, UsernameMaxLen))
	}
	if err := ValidatePassword(req.Password); err != nil {
		return nil, err
	}
	if err := ValidateHomePath(req.HomePath, "/home/"+owner.Username); err != nil {
		return nil, err
	}

	sftpAccess := true
	if req.SFTPAccess != nil {
		sftpAccess = *req.SFTPAccess
	}

	createParams := map[string]any{
		"tenant_username": owner.Username,
		"username":        username,
		"home_path":       req.HomePath,
		"password":        req.Password,
		"ftp_access":      req.FTPAccess,
		"webdav_access":   req.WebDAVAccess,
	}
	var allocUID uint32
	var jailPath string
	if req.Isolated {
		if d.QuotaMount == "" {
			// setquota can't run without a mount → an isolated uid would be
			// unquota'd (disk-fill). Refuse rather than ship an unenforced one.
			return nil, invalid(ErrIsolationUnavailable, "per-account disk quota is not configured on this host")
		}
		if req.QuotaMB == 0 {
			return nil, invalid(ErrQuotaRequired, "an isolated account requires a disk quota (quota_mb, in MB)")
		}
		// The split allocation (Σ existing isolated sub quotas + this one ≤
		// package quota) is enforced inside ReserveWithinCap under the same
		// per-tenant lock as the account cap (JAB-262).
		uid, err := d.Accounts.AllocateUID(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUIDAllocation, err)
		}
		allocUID = uid
		jailPath = JailRoot + "/" + owner.Username + "/" + username
		createParams["isolated"] = true
		createParams["uid"] = allocUID
		createParams["quota_mb"] = req.QuotaMB
		createParams["quota_mount"] = d.QuotaMount
		createParams["jail_path"] = jailPath
	}

	now := time.Now().UTC()
	acct := &models.FtpAccount{
		ID:           ids.NewULID(),
		UserID:       owner.UserID,
		Username:     username,
		HomePath:     req.HomePath,
		FTPAccess:    req.FTPAccess,
		SFTPAccess:   sftpAccess,
		WebDAVAccess: req.WebDAVAccess,
		IsEnabled:    true,
		Isolated:     req.Isolated,
		QuotaMB:      req.QuotaMB,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if req.Isolated {
		acct.UID = &allocUID
		acct.JailPath = jailPath
	}

	if err := d.Accounts.ReserveWithinCap(ctx, acct, int(pkg.MaxFTPAccounts), pkg.DiskQuotaMB); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPersist, err)
	}
	if err := agentCall(ctx, d.Agent, "ftpaccount.create", createParams); err != nil {
		_ = d.Accounts.Delete(context.WithoutCancel(ctx), acct.ID)
		return nil, err
	}
	syncHostAccess(ctx, d, owner.Username)
	return acct, nil
}
