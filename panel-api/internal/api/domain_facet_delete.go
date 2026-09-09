// Facet-preserving Web Domain delete (GH #1603, johnnyq).
//
// A jabali "domain" is one row carrying web + mail + DNS facets. Deleting the
// row (userops.DeleteDomain) tears down all three. This path lets the operator
// delete the WEB facet while KEEPING the Mail Domain and/or the DNS Zone: the
// row survives (web-off), so mail keeps delivering and/or the zone keeps
// answering. The delete handler routes here whenever at least one facet is kept;
// when both are also being deleted it falls through to the full row delete.
package api

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// facetPreservingWebDelete removes the WEB facet of dom while keeping the row,
// then conditionally tears down the mail and/or DNS facets. Ordering is chosen
// so a mid-run failure is safe and re-runnable:
//
//  1. flip web_disabled=true FIRST so the reconciler stops rendering the web
//     vhost (otherwise it would re-create what step 2 removes);
//  2. agent domain.web_teardown removes the existing web vhost + web cert while
//     leaving the mail vhost / mail cert intact;
//  3. if deleteMail, run the shared mail teardown (GH #1387 core) — its
//     purge_accounts hard gate is the only step that returns a hard error;
//  4. if deleteDNS, flip dns_disabled=true and delete the PowerDNS zone;
//  5. if deleteFiles, remove the docroot as the tenant uid (async, best-effort).
//
// hardErr is set only for a failure that leaves the operation genuinely
// incomplete and worth surfacing (500); every idempotent step is re-runnable, so
// retrying the same delete converges. Non-fatal residue comes back in warnings.
func (h *domainHandler) facetPreservingWebDelete(
	ctx context.Context,
	dom *models.Domain,
	deleteMail, deleteDNS, deleteFiles bool,
) (warnings []string, hardErr error) {
	// The teardown must complete regardless of the client hanging up.
	ctx = context.WithoutCancel(ctx)

	// 1. web_disabled=true BEFORE the host teardown so the reconciler does not
	//    re-render the vhost we are about to remove.
	if err := h.cfg.Domains.UpdateWebDisabled(ctx, dom.ID, true); err != nil {
		return nil, err
	}

	// 2. Remove the web vhost + web cert (keeps mail). Idempotent; on failure the
	//    flag is already set, so the reconciler won't fight a retry.
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, werr := h.cfg.Agent.Call(wctx, "domain.web_teardown", map[string]string{"domain": dom.Name})
	cancel()
	if werr != nil {
		return nil, werr
	}

	// 3. Delete the Mail Domain (optional) — the shared GH #1387 mail teardown.
	if deleteMail {
		_, mailWarnings, mErr := purgeDomainMailService(ctx, DomainMailPurgeHandlerConfig{
			Domains:        h.cfg.Domains,
			Mailboxes:      h.cfg.Mailboxes,
			MailCerts:      h.cfg.MailCerts,
			Agent:          h.cfg.Agent,
			DNSZones:       h.cfg.DNSZones,
			DNSRecords:     h.cfg.DNSRecords,
			ServerSettings: h.cfg.ServerSettings,
		}, dom)
		if mErr != nil {
			return warnings, mErr
		}
		warnings = append(warnings, mailWarnings...)
	}

	// 4. Delete the DNS Zone (optional) — flip dns_disabled so the reconciler
	//    stops pushing the zone, then delete the PowerDNS zone. A box without the
	//    DNS module has no PowerDNS backend; that is permanent, not a failure.
	if deleteDNS {
		if err := h.cfg.Domains.UpdateDNSDisabled(ctx, dom.ID, true); err != nil {
			warnings = append(warnings, "DNS management flag not cleared in the database")
		}
		zctx, zcancel := context.WithTimeout(ctx, 30*time.Second)
		_, zerr := h.cfg.Agent.Call(zctx, "dns.zone.delete", map[string]string{"zone": dom.Name})
		zcancel()
		if zerr != nil && !strings.Contains(zerr.Error(), "powerdns backend not available") {
			warnings = append(warnings, "DNS zone not removed; remove it manually if it lingers")
		}
	}

	// 5. Delete the files (optional) — same async, tenant-uid removal the full
	//    delete uses (GH #1382). Best-effort; on failure the files simply remain.
	if deleteFiles && dom.DocRoot != "" {
		if owner, uerr := h.cfg.Users.FindByID(ctx, dom.UserID); uerr == nil &&
			owner != nil && owner.Username != nil && *owner.Username != "" {
			username, dname := *owner.Username, dom.Name
			target := domainDeleteFilesTarget(username, dname, dom.DocRoot)
			ag := h.cfg.Agent
			go func() {
				dctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if _, aerr := ag.Call(dctx, "domain.docroot.delete", map[string]string{
					"username": username,
					"docroot":  target,
				}); aerr != nil {
					slog.Warn("web-domain facet delete: docroot cleanup failed; files left in place",
						"domain", dname, "error", aerr)
				}
			}()
		}
	}

	// The row survives web-off; nudge the reconciler so the now mail-only / DNS-
	// only shape converges promptly.
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(dom.ID)
	}
	return warnings, nil
}
