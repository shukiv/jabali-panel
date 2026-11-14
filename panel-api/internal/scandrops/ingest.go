package scandrops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	// Interval is how often every scanner's drop folder is checked. With
	// the agent's one-minute settle, a scan shows up in webmail within
	// about a minute and a half of the scanner finishing it.
	Interval = 30 * time.Second
	// FilesPerPass is the most files one scanner moves per pass.
	FilesPerPass = 20
	// passTimeout bounds one scanner's agent call.
	passTimeout = 5 * time.Minute
	// waitingForFolder is shown until the reconciler has made the shared
	// folder the scans go into.
	waitingForFolder = "waiting for the shared folder to be set up"
)

// Run checks every scanner each Interval until ctx ends.
func Run(ctx context.Context, d Deps) {
	if !d.ok() {
		return
	}
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			Pass(ctx, d)
		}
	}
}

// Pass runs once over every scanner: moves its finished scans, or sweeps it
// when its resource or login is gone.
func Pass(ctx context.Context, d Deps) {
	if !d.ok() {
		return
	}
	rows, err := d.Drops.ListAll(ctx)
	if err != nil {
		d.log().Warn("scanner: list", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		d.passOne(ctx, &rows[i])
	}
}

func (d Deps) passOne(ctx context.Context, row *models.ScanDrop) {
	res, err := d.Resources.FindByID(ctx, row.ResourceID)
	if errors.Is(err, repository.ErrNotFound) {
		// The resource was deleted, or its domain was: its login goes too.
		if err := d.removeLogin(ctx, row.FtpAccountID); err != nil {
			d.log().Warn("scanner: remove the login of a deleted shared folder", "resource", row.ResourceID, "err", err)
			return
		}
		if err := d.Drops.Delete(ctx, row.ResourceID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			d.log().Warn("scanner: forget a deleted shared folder's scanner", "resource", row.ResourceID, "err", err)
			return
		}
		d.log().Info("scanner: removed with its shared folder", "resource", row.ResourceID)
		return
	}
	if err != nil {
		d.log().Warn("scanner: load shared folder", "resource", row.ResourceID, "err", err)
		return
	}
	acct, err := d.Ftp.Accounts.FindByID(ctx, row.FtpAccountID)
	if errors.Is(err, repository.ErrNotFound) {
		// The login was deleted on the FTP page: the scanner is off.
		if err := d.Drops.Delete(ctx, row.ResourceID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			d.log().Warn("scanner: forget a scanner whose login is gone", "resource", row.ResourceID, "err", err)
			return
		}
		d.log().Info("scanner: off, its login was deleted", "resource", row.ResourceID)
		return
	}
	if err != nil {
		d.log().Warn("scanner: load login", "resource", row.ResourceID, "err", err)
		return
	}
	if res.CollectionID == "" || res.EmailCached == nil || *res.EmailCached == "" {
		d.record(ctx, row, 0, waitingForFolder)
		return
	}
	dom, err := d.Domains.FindByID(ctx, res.DomainID)
	if err != nil {
		d.log().Warn("scanner: load domain", "resource", row.ResourceID, "err", err)
		return
	}
	owner, err := d.Ftp.Users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil || owner.Username == nil || *owner.Username == "" {
		d.log().Warn("scanner: load owner", "resource", row.ResourceID, "err", err)
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, passTimeout)
	raw, err := d.Ftp.Agent.Call(callCtx, "scandrop.ingest", agentwire.ScanDropIngestParams{
		TenantUsername:  *owner.Username,
		ScannerUsername: acct.Username,
		DropPath:        row.DropPath,
		HostEmail:       *res.EmailCached,
		FolderID:        res.CollectionID,
		MaxFiles:        FilesPerPass,
		MaxFolderBytes:  int64(row.MaxMB) << 20,
	})
	cancel()
	if err != nil {
		var ae *agent.AgentError
		if errors.As(err, &ae) {
			d.record(ctx, row, 0, ae.Message)
		} else {
			d.record(ctx, row, 0, err.Error())
		}
		return
	}
	var out agentwire.ScanDropIngestResult
	if err := json.Unmarshal(raw, &out); err != nil {
		d.record(ctx, row, 0, "unreadable answer from the agent")
		return
	}
	d.record(ctx, row, out.Moved, out.Problem)
}

// record stores a pass's outcome, and writes nothing when nothing moved and
// the problem is the one already stored.
func (d Deps) record(ctx context.Context, row *models.ScanDrop, moved int, problem string) {
	if moved == 0 && problem == row.LastError {
		return
	}
	if err := d.Drops.RecordIngest(ctx, row.ResourceID, uint64(max(moved, 0)), problem); err != nil {
		d.log().Warn("scanner: record pass", "resource", row.ResourceID, "err", err)
	}
}
