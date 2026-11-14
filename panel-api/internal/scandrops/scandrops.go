// Package scandrops is the scanner lifecycle for shared Files resources
// (GH #2503): turning a resource's scanner on and off, and the loop that moves
// finished scans into the resource's Stalwart folder. The REST handler and the
// operator CLI both route through it.
//
// A scanner is three things: a drop folder in the domain's directory
// (/home/<user>/domains/<domain>/scans/<name>, outside public_html), an
// FTP/SFTP login homed there (an ordinary ftp_accounts row, counted against
// the package's FTP logins), and a scan_drops row tying them to the resource.
// Every pass, the agent moves the files the scanner finished writing into the
// shared folder; members granted the folder see them in webmail.
//
// The scan_drops row has no foreign keys: this package sweeps by it. A row
// whose resource is gone (deleted, or its domain deleted) gets its login
// removed and is then deleted; a row whose login was deleted on the FTP page
// is deleted.
package scandrops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var (
	ErrDeps         = errors.New("scandrops: dependencies are not wired")
	ErrNotFiles     = errors.New("scandrops: only a files resource takes a scanner")
	ErrAlreadyOn    = errors.New("scandrops: the scanner is already on")
	ErrNotOn        = errors.New("scandrops: the scanner is off")
	ErrNoAccount    = errors.New("scandrops: the domain's owner has no hosting account")
	ErrNoPackage    = errors.New("scandrops: a scanner login needs a hosting package")
	ErrNotInPackage = errors.New("scandrops: the hosting package includes no FTP/SFTP logins")
	ErrSpace        = errors.New("scandrops: space out of range")
	ErrName         = errors.New("scandrops: the resource's address cannot name a folder")
	// ErrUnconfined: the login could be neither isolated nor kept to FTP. A
	// login that is not isolated reaches the owner's whole home over SFTP
	// (its chroot is the home), and a scanner's password lives on a printer.
	ErrUnconfined = errors.New("scandrops: a scanner login needs isolated logins or the FTP server")
)

// MaxSpaceMB is the most space a scanner may keep when the package has no
// disk quota to bound it.
const MaxSpaceMB = 1 << 20

// prepareTimeout bounds the agent's drop folder create.
const prepareTimeout = 30 * time.Second

// ResourceLookup and DomainLookup are the slices of the shared-resource and
// domain repositories this package reads.
type ResourceLookup interface {
	FindByID(ctx context.Context, id string) (*models.SharedResource, error)
}
type DomainLookup interface {
	FindByID(ctx context.Context, id string) (*models.Domain, error)
}

// SettingsGetter reads the server settings (whether the FTP server is on).
type SettingsGetter interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
}

// Deps wires the operations. Ftp carries the agent, the FTP account and user
// repositories, the package lookup and the quota mount the scanner login
// is made with.
type Deps struct {
	Drops     repository.ScanDropRepository
	Resources ResourceLookup
	Domains   DomainLookup
	Settings  SettingsGetter
	Ftp       ftpops.Deps
	Log       *slog.Logger
}

func (d Deps) ok() bool {
	return d.Drops != nil && d.Resources != nil && d.Domains != nil && d.Settings != nil &&
		d.Ftp.Agent != nil && d.Ftp.Accounts != nil && d.Ftp.Users != nil && d.Ftp.Packages != nil
}

func (d Deps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// EnableInput turns a resource's scanner on. Label names the login
// (<owner>_<label>); MaxMB is the space its scans may keep.
type EnableInput struct {
	Resource *models.SharedResource
	Domain   *models.Domain
	Label    string
	Password string
	MaxMB    uint32
}

// SpaceLimit is the largest MaxMB the package allows: its disk quota, or
// MaxSpaceMB when it has none.
func SpaceLimit(pkg *models.HostingPackage) uint32 {
	if pkg != nil && pkg.DiskQuotaMB > 0 && pkg.DiskQuotaMB < MaxSpaceMB {
		return pkg.DiskQuotaMB
	}
	return MaxSpaceMB
}

// Enable makes the drop folder, then the login homed there, then the row. A
// rejected input (a *ftpops.ValidationError or one of this package's errors)
// leaves nothing behind but, at most, the empty drop folder; a failed row
// write removes the login again.
func Enable(ctx context.Context, d Deps, in EnableInput) (*models.ScanDrop, *models.FtpAccount, error) {
	if !d.ok() || in.Resource == nil || in.Domain == nil {
		return nil, nil, ErrDeps
	}
	res, dom := in.Resource, in.Domain
	if res.Kind != "files" {
		return nil, nil, ErrNotFiles
	}
	if _, err := d.Drops.FindByResourceID(ctx, res.ID); err == nil {
		return nil, nil, ErrAlreadyOn
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, nil, err
	}
	owner, err := d.Ftp.Users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil || owner.Username == nil || *owner.Username == "" {
		return nil, nil, ErrNoAccount
	}
	if owner.PackageID == nil {
		return nil, nil, ErrNoPackage
	}
	pkg, err := d.Ftp.Packages.FindByID(ctx, *owner.PackageID)
	if err != nil || pkg == nil {
		return nil, nil, ErrNoPackage
	}
	if pkg.MaxFTPAccounts == 0 {
		return nil, nil, ErrNotInPackage
	}
	if limit := SpaceLimit(pkg); in.MaxMB == 0 || in.MaxMB > limit {
		return nil, nil, fmt.Errorf("%w: space must be 1 to %d MB", ErrSpace, limit)
	}
	if err := ftpops.ValidatePassword(in.Password); err != nil {
		return nil, nil, err
	}
	username := *owner.Username
	drop, err := agentwire.ScanDropDir("/home/"+username, dom.Name, localPart(res))
	if err != nil {
		return nil, nil, ErrName
	}

	// Isolated: its own uid, jailed in the drop folder, FTP and SFTP alike.
	// Not isolated: FTP only, which chroots to the drop folder; its SFTP
	// would reach the whole home.
	isolated := isolationAvailable(ctx, d)
	if !isolated {
		set, err := d.Settings.Get(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("read server settings: %w", err)
		}
		if set == nil || !set.FTPEnabled {
			return nil, nil, ErrUnconfined
		}
	}

	prepCtx, cancel := context.WithTimeout(ctx, prepareTimeout)
	_, err = d.Ftp.Agent.Call(prepCtx, "scandrop.prepare", agentwire.ScanDropPrepareParams{TenantUsername: username, DropPath: drop})
	cancel()
	if err != nil {
		return nil, nil, err
	}

	sftp := isolated
	req := ftpops.CreateRequest{
		Label:      in.Label,
		HomePath:   drop,
		Password:   in.Password,
		FTPAccess:  true,
		SFTPAccess: &sftp,
		Isolated:   isolated,
	}
	if isolated {
		req.QuotaMB = in.MaxMB
	}
	acct, err := ftpops.Create(ctx, d.Ftp, ftpops.Owner{UserID: owner.ID, Username: username}, pkg, req)
	if err != nil {
		return nil, nil, err
	}

	row := &models.ScanDrop{ResourceID: res.ID, FtpAccountID: acct.ID, DropPath: drop, MaxMB: in.MaxMB}
	if err := d.Drops.Create(ctx, row); err != nil {
		if derr := ftpops.Delete(context.WithoutCancel(ctx), d.Ftp, acct, username); derr != nil {
			d.log().Warn("scanner: remove the login after a failed save", "login", acct.Username, "err", derr)
		}
		if errors.Is(err, repository.ErrConflict) {
			return nil, nil, ErrAlreadyOn
		}
		return nil, nil, fmt.Errorf("save scanner: %w", err)
	}
	return row, acct, nil
}

// Disable removes the resource's scanner login, then its row. Scans still in
// the drop folder stay there.
func Disable(ctx context.Context, d Deps, resourceID string) error {
	if !d.ok() {
		return ErrDeps
	}
	row, err := d.Drops.FindByResourceID(ctx, resourceID)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotOn
	}
	if err != nil {
		return err
	}
	if err := d.removeLogin(ctx, row.FtpAccountID); err != nil {
		return err
	}
	if err := d.Drops.Delete(ctx, resourceID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	return nil
}

// Get returns the resource's scanner and its login; both nil when it is off.
// The login is nil, too, when it was deleted and the sweep has not caught up.
func Get(ctx context.Context, d Deps, resourceID string) (*models.ScanDrop, *models.FtpAccount, error) {
	if !d.ok() {
		return nil, nil, ErrDeps
	}
	row, err := d.Drops.FindByResourceID(ctx, resourceID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	acct, err := d.Ftp.Accounts.FindByID(ctx, row.FtpAccountID)
	if errors.Is(err, repository.ErrNotFound) {
		return row, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return row, acct, nil
}

// removeLogin deletes a scanner login through the FTP lifecycle. A login
// already gone is done.
func (d Deps) removeLogin(ctx context.Context, ftpAccountID string) error {
	acct, err := d.Ftp.Accounts.FindByID(ctx, ftpAccountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, err := d.Ftp.Users.FindByID(ctx, acct.UserID)
	if err != nil {
		return fmt.Errorf("scanner login %s: load its owner: %w", acct.Username, err)
	}
	if owner == nil || owner.Username == nil || *owner.Username == "" {
		return fmt.Errorf("scanner login %s: its owner has no Linux user", acct.Username)
	}
	return ftpops.Delete(ctx, d.Ftp, acct, *owner.Username)
}

// isolationAvailable asks the agent whether a login can be isolated (its own
// uid, jailed, with a disk quota), as the FTP page does: the login is then
// confined to the drop folder by the kernel, not just by its chroot.
func isolationAvailable(ctx context.Context, d Deps) bool {
	if d.Ftp.QuotaMount == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := d.Ftp.Agent.Call(cctx, "ftpaccount.isolation_status", map[string]any{"quota_mount": d.Ftp.QuotaMount})
	if err != nil {
		return false
	}
	var resp struct {
		Available bool `json:"available"`
	}
	return json.Unmarshal(raw, &resp) == nil && resp.Available
}

// localPart is the resource's address local part, the drop folder's name.
func localPart(res *models.SharedResource) string {
	if res.LocalPart != nil && *res.LocalPart != "" {
		return *res.LocalPart
	}
	if res.EmailCached != nil {
		if at := strings.LastIndex(*res.EmailCached, "@"); at > 0 {
			return (*res.EmailCached)[:at]
		}
	}
	return ""
}
