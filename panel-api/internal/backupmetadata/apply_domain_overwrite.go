package backupmetadata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailpolicy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: with OverwriteRows, a domain the account already has takes the
// backup's settings within the rules its own pages apply: the restore checks
// (CheckDomain) for its web settings, the package's PHP policy for its PHP
// limits, and for its catch-all a mailbox the account had before the
// restore. Its name, document root, SSL, DKIM, DNSSEC, custom nginx
// directives, mail provider and ownership stay as they are.

// overwriteDomain gives existing, the account's own domain as read here, the
// settings of dm, the backup's row for it. poolIDs maps the backup's pools to
// the account's pools here.
func overwriteDomain(ctx context.Context, d Deps, r *ApplyResult, userID, account string, existing *models.Domain, dm internalbackup.MetadataDomain, poolIDs map[string]string) {
	label := fmt.Sprintf("domain %s (%s)", dm.ID, dm.Name)
	report := func(format string, args ...any) {
		r.Errors = append(r.Errors, label+": "+fmt.Sprintf(format, args...))
	}
	if d.CheckDomain == nil {
		report("settings not updated: the restore checks are not wired")
		return
	}
	// The checks run on the domain as it is here, with the backup's settings
	// on it. Its custom nginx directives are left out: the restore doesn't
	// change them, so the checks have nothing to say about them.
	probe := *existing
	probe.NginxCustomDirectives = nil
	probe.IsEnabled = dm.IsEnabled
	probe.RedirectAllTo, probe.RedirectAllType = nonEmptyTrimmed(dm.RedirectAllTo), nonEmptyTrimmed(dm.RedirectAllType)
	probe.IndexPriority = strings.TrimSpace(dm.IndexPriority)
	backup := models.Domain{}
	setBackupPHPLimits(&backup, dm)
	setBackupPHPLimits(&probe, dm)
	warnings, err := d.CheckDomain(ctx, &probe, account)
	if err != nil {
		report("settings not updated: %v", err)
		return
	}
	for _, w := range warnings {
		report("%s", w)
	}

	changed := overwriteDomainWeb(ctx, d, report, existing, &probe, dm)
	if overwriteDomainPHPLimits(ctx, d, report, userID, existing, &probe, &backup) {
		changed = true
	}
	if dm.RateLimitRPS != existing.RateLimitRPS || dm.ConnectionLimit != existing.ConnectionLimit {
		if err := d.Domains.SetRateLimits(ctx, existing.ID, dm.RateLimitRPS, dm.ConnectionLimit); err != nil {
			report("rate limits not updated: %v", err)
		} else {
			changed = true
		}
	}
	if dm.PHPPoolID != nil && d.PHPPools != nil {
		if id, ok := poolIDs[*dm.PHPPoolID]; !ok {
			report("its PHP pool was not restored; it keeps its own")
		} else if existing.PHPPoolID == nil || *existing.PHPPoolID != id {
			if err := d.Domains.SetPHPPoolID(ctx, existing.ID, &id); err != nil {
				report("PHP pool not updated: %v", err)
			} else {
				changed = true
			}
		}
	}
	if changed && d.ScheduleDomain != nil {
		d.ScheduleDomain(existing.ID)
	}
	overwriteDomainMailPolicy(ctx, d, report, userID, existing, dm)
}

// overwriteDomainWeb gives existing the backup's enabled flag, redirect-all
// and index priority, each as the checks left it on probe. A setting the
// checks dropped leaves the domain's own. Only the row read here is written,
// so every other column keeps its value.
func overwriteDomainWeb(ctx context.Context, d Deps, report func(string, ...any), existing, probe *models.Domain, dm internalbackup.MetadataDomain) bool {
	next := *existing
	changed := false
	if probe.IsEnabled != existing.IsEnabled {
		next.IsEnabled, changed = probe.IsEnabled, true
	}
	refused := (nonEmptyTrimmed(dm.RedirectAllTo) != nil && probe.RedirectAllTo == nil) ||
		(nonEmptyTrimmed(dm.RedirectAllType) != nil && probe.RedirectAllType == nil)
	if !refused && (!sameString(probe.RedirectAllTo, existing.RedirectAllTo) || !sameString(probe.RedirectAllType, existing.RedirectAllType)) {
		next.RedirectAllTo, next.RedirectAllType, changed = probe.RedirectAllTo, probe.RedirectAllType, true
	}
	if want := strings.TrimSpace(dm.IndexPriority); want != "" && probe.IndexPriority == want && want != existing.IndexPriority {
		next.IndexPriority, changed = want, true
	}
	if !changed {
		return false
	}
	next.UpdatedAt = time.Now().UTC()
	if err := d.Domains.Update(ctx, &next); err != nil {
		report("web settings not updated: %v", err)
		return false
	}
	return true
}

// overwriteDomainPHPLimits gives existing each PHP limit the backup changes,
// as the PHP settings page would for the tenant: the change passes the
// checks (probe) and the account's package lets a tenant set it. Clearing a
// limit is a change too. When the package can't be read, the domain keeps
// its limits. Its other PHP settings are written back as they are.
func overwriteDomainPHPLimits(ctx context.Context, d Deps, report func(string, ...any), userID string, existing, probe, backup *models.Domain) bool {
	want := *existing
	var pkg *models.HostingPackage
	var pkgErr error
	read, changed := false, false
	for _, f := range domainPHPLimits {
		if f.key(backup) == f.key(existing) {
			continue
		}
		if f.key(backup) != "" && f.key(probe) == "" {
			continue // the checks dropped it, with a line of their own
		}
		if !read {
			pkg, pkgErr = accountPackage(ctx, d, userID)
			read = true
		}
		switch {
		case pkgErr != nil:
			report("PHP %s not updated: the account's package could not be read (%v); it keeps its own", f.directive, pkgErr)
		case !pkg.PHPSettingLevelFor(f.directive).TenantMaySet():
			report("PHP %s not updated: the account's package lets only an administrator set it", f.directive)
		default:
			f.copy(&want, probe)
			changed = true
		}
	}
	if !changed {
		return false
	}
	if err := d.Domains.UpdatePHPSettings(ctx, existing.ID, repository.DomainPHPSettingsOf(&want)); err != nil {
		report("PHP limits not updated: %v", err)
		return false
	}
	return true
}

// domainPHPLimit is one per-domain PHP limit, by the directive the package's
// PHP policy names.
type domainPHPLimit struct {
	directive string
	// key is the limit's value as a string, "" when unset.
	key func(*models.Domain) string
	// copy sets the limit on dst to src's.
	copy func(dst, src *models.Domain)
}

var domainPHPLimits = []domainPHPLimit{
	{"memory_limit", func(x *models.Domain) string { return stringKey(x.PHPMemoryLimit) },
		func(dst, src *models.Domain) { dst.PHPMemoryLimit = src.PHPMemoryLimit }},
	{"upload_max_filesize", func(x *models.Domain) string { return stringKey(x.PHPUploadMaxFilesize) },
		func(dst, src *models.Domain) { dst.PHPUploadMaxFilesize = src.PHPUploadMaxFilesize }},
	{"post_max_size", func(x *models.Domain) string { return stringKey(x.PHPPostMaxSize) },
		func(dst, src *models.Domain) { dst.PHPPostMaxSize = src.PHPPostMaxSize }},
	{"max_input_vars", func(x *models.Domain) string { return intKey(x.PHPMaxInputVars) },
		func(dst, src *models.Domain) { dst.PHPMaxInputVars = src.PHPMaxInputVars }},
	{"max_execution_time", func(x *models.Domain) string { return intKey(x.PHPMaxExecutionTime) },
		func(dst, src *models.Domain) { dst.PHPMaxExecutionTime = src.PHPMaxExecutionTime }},
	{"max_input_time", func(x *models.Domain) string { return intKey(x.PHPMaxInputTime) },
		func(dst, src *models.Domain) { dst.PHPMaxInputTime = src.PHPMaxInputTime }},
}

// setBackupPHPLimits sets the per-domain PHP limits on dst to the backup's.
func setBackupPHPLimits(dst *models.Domain, dm internalbackup.MetadataDomain) {
	dst.PHPMemoryLimit, dst.PHPUploadMaxFilesize, dst.PHPPostMaxSize = dm.PHPMemoryLimit, dm.PHPUploadMaxFilesize, dm.PHPPostMaxSize
	dst.PHPMaxInputVars, dst.PHPMaxExecutionTime, dst.PHPMaxInputTime = dm.PHPMaxInputVars, dm.PHPMaxExecutionTime, dm.PHPMaxInputTime
}

// overwriteDomainMailPolicy gives existing the backup's catch-all and
// outbound disclaimer, the way the domain's mail pages set them. A backup
// without them clears the domain's. The catch-all goes only to a mailbox the
// account had on this server before the restore: never an outside address,
// another account's mailbox, or a mailbox the file brings, whose password its
// author knows.
func overwriteDomainMailPolicy(ctx context.Context, d Deps, report func(string, ...any), userID string, existing *models.Domain, dm internalbackup.MetadataDomain) {
	mp := domainmailpolicy.Deps{Domains: d.Domains}
	push := mailPolicyPush(d)

	want := nonEmptyTrimmed(dm.CatchallTarget)
	switch {
	case want == nil && existing.CatchallTarget != nil:
		if w, err := domainmailpolicy.ClearCatchall(ctx, mp, existing, push); err != nil {
			report("catch-all not cleared: %v", err)
		} else if w != "" {
			report("catch-all: %s", w)
		}
	case want != nil && (existing.CatchallTarget == nil || !strings.EqualFold(*existing.CatchallTarget, *want)):
		address, ok, err := accountMailboxAddress(ctx, d, userID, *want)
		switch {
		case err != nil:
			report("catch-all not updated: %v", err)
		case !ok:
			report("catch-all to %s not restored: it isn't a mailbox this account had on this server", *want)
		default:
			if _, w, err := domainmailpolicy.SetCatchall(ctx, mp, existing, address, push); err != nil {
				report("catch-all not updated: %s", mailPolicyReason(err))
			} else if w != "" {
				report("catch-all: %s", w)
			}
		}
	}

	on, text := dm.DisclaimerEnabled, strings.TrimSpace(stringOrEmpty(dm.DisclaimerText))
	if on == existing.DisclaimerEnabled && text == strings.TrimSpace(stringOrEmpty(existing.DisclaimerText)) {
		return
	}
	var w string
	var err error
	if !on && text == "" {
		w, err = domainmailpolicy.ClearDisclaimer(ctx, mp, existing, push)
	} else {
		_, w, err = domainmailpolicy.SetDisclaimer(ctx, mp, existing, on, text, push)
	}
	switch {
	case err != nil:
		report("disclaimer not updated: %s", mailPolicyReason(err))
	case w != "":
		report("disclaimer: %s", w)
	}
}

// accountMailboxAddress is the address of the mailbox target names, when it
// is a mailbox of one of the account's domains. Mailboxes are restored after
// the domains, so during the domain pass this finds only the mailboxes the
// account had before the restore.
func accountMailboxAddress(ctx context.Context, d Deps, userID, target string) (string, bool, error) {
	if d.Mailboxes == nil || d.Domains == nil {
		return "", false, errors.New("the mailbox lookup is not wired")
	}
	address := strings.ToLower(target)
	mb, err := d.Mailboxes.FindByEmail(ctx, address)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && mb == nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up the mailbox: %v", err)
	}
	dom, err := d.Domains.FindByID(ctx, mb.DomainID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && dom == nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up the mailbox's domain: %v", err)
	}
	if dom.UserID != userID {
		return "", false, nil
	}
	if mb.EmailCached != "" {
		address = mb.EmailCached
	}
	return address, true, nil
}

// mailPolicyPush tells the mail server at once when the agent is wired; the
// reconciler applies the domain's columns on its next pass either way.
func mailPolicyPush(d Deps) domainmailpolicy.PushFunc {
	if d.Agent == nil {
		return nil
	}
	return func(ctx context.Context, cmd string, params map[string]any) error {
		_, err := d.Agent.Call(ctx, cmd, params)
		return err
	}
}

// mailPolicyReason words a domainmailpolicy refusal for the restore report.
func mailPolicyReason(err error) string {
	switch {
	case errors.Is(err, domainmailpolicy.ErrEmailNotEnabled):
		return "mail is not enabled on the domain"
	case errors.Is(err, domainmailpolicy.ErrDisclaimerTextRequired):
		return "an enabled disclaimer needs text"
	case errors.Is(err, domainmailpolicy.ErrInvalidTarget):
		return "the address is not valid"
	}
	return err.Error()
}

// nonEmptyTrimmed is *p trimmed, or nil when p is nil or blank: the pages
// store a blank setting as unset.
func nonEmptyTrimmed(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func sameString(a, b *string) bool { return stringKey(a) == stringKey(b) }

func stringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// stringKey and intKey tell an unset value ("") from a set one.
func stringKey(p *string) string {
	if p == nil {
		return ""
	}
	return "=" + *p
}

func intKey(p *int) string {
	if p == nil {
		return ""
	}
	return "=" + strconv.Itoa(*p)
}
