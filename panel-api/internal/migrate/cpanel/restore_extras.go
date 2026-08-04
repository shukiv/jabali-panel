// restore_extras.go — M35.8 P2/P5 "everything else" writers.
//
// Covered:
//   * Catch-all addresses    (cpanel: <home>/etc/<dom>/aliases line "*: target")
//   * Subdomains             (cpanel: SUB_DOMAINS=<sub.dom>,<sub.dom>,... in cp/<user>)
//
// Recorded-as-warning (no agent endpoint yet — operator follow-up):
//   * External forwarders    (M6.5 forwarders schema needs MailboxID; cpanel
//                             aliases can target external mail; not 1:1)
//   * Per-mailbox autoresponders (need post-mailbox-create id lookup)
//   * Sieve filters
//   * Per-domain custom SSL certs (LE auto-issue picks up once nginx vhost
//                                  exists; warning records source cert path)
//   * Per-domain PHP version  (needs per-domain pool create — deferred)
//   * FTP accounts            (deprecated in favour of SFTP keys)

package cpanel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ExtrasResult is the per-area counter set returned from ImportExtras.
// Counters are summed into the parent restore manifest by the caller.
type ExtrasResult struct {
	CatchallsSet           int      // domains where catchall_target was written
	SubdomainsCreated      int      // panel domain rows added from SUB_DOMAINS
	ForwardersCreated      int      // mail forwarder rows added (M6.5)
	ForwardersOrphaned     int      // alias lines whose local mailbox isn't in panel
	AutorespondersCreated  int      // vacation auto-replies restored (M6.5)
	AutorespondersOrphaned int      // .autorespond entries whose mailbox isn't in panel
	PHPVersionApplied      string   // first version applied at user-pool level (legacy)
	PHPVersionsDetected    string   // JAB-218: source spread, e.g. "7.4x3,8.1x1" ("none" if the source recorded nothing)
	PHPPoolsCreated        int      // distinct (user, version) FPM pools created (M35.8 P6)
	PHPDomainsBound        int      // domains whose php_pool_id was set to a per-version pool
	FTPAccountsObserved    int      // cpanel ftp accounts seen on source (record-only)
	DKIMKeysPreserved      int      // legacy DKIM keys copied to sidecar storage
	FiltersImported        int      // per-mailbox Sieve/cpanel inbox rules
	Skipped                []string // human-readable reasons (mirrors other restore writers)
}

// ImportExtras walks the parsed cpmove + reads cpanel meta files for
// the bits that don't have a dedicated restore_*.go writer yet.
// All operations are idempotent on rerun.
func ImportExtras(
	ctx context.Context,
	domainsRepo repository.DomainRepository,
	mailboxesRepo repository.MailboxRepository,
	forwardersRepo repository.EmailForwarderRepository,
	autoRespondersRepo repository.EmailAutoresponderRepository,
	filtersRepo repository.EmailFilterRepository,
	poolsRepo repository.PHPPoolRepository,
	agentCli agent.AgentInterface,
	parsed *ParsedTarball,
	targetUserID, targetUsername string,
	preserveMailRouting bool,
) (*ExtrasResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("ImportExtras: parsed nil")
	}
	res := &ExtrasResult{}

	// Discover the userdata file. PeekAccountMeta already searched
	// the same candidates; reuse its discovery to find the cpanel-
	// owner's primary-userdata file (cp/<user>) — that's where
	// SUB_DOMAINS lives.
	userdataPath := findUserdataFile(parsed.ExtractDir, parsed.SourceUser)

	// ---- P5: subdomains ----
	if userdataPath != "" {
		raw := extractKV(userdataPath, "SUB_DOMAINS")
		for _, name := range splitCpanelList(raw) {
			n := strings.ToLower(strings.TrimSpace(name))
			if n == "" {
				continue
			}
			if _, err := domainsRepo.FindByName(ctx, n); err == nil {
				res.Skipped = append(res.Skipped, "subdomain_skip:already_exists:"+n)
				continue
			}
			domainID := ids.NewULID()
			docRoot := filepath.Join("/home", targetUsername, "public_html", n)
			if _, err := agentCli.Call(ctx, "domain.create", map[string]any{
				"domain_id":      domainID,
				"domain":         n,
				"username":       targetUsername,
				"doc_root":       docRoot,
				"index_priority": "html_first",
			}); err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("subdomain_skip:agent_create:%s:%v", n, err))
				continue
			}
			now := time.Now()
			d := &models.Domain{
				ID:            domainID,
				UserID:        targetUserID,
				Name:          n,
				DocRoot:       docRoot,
				IsEnabled:     true,
				IndexPriority: "html_first",
				GhostState:    "unchecked",
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := domainsRepo.Create(ctx, d); err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("subdomain_skip:db_create:%s:%v", n, err))
				continue
			}
			res.SubdomainsCreated++
		}
	}

	// ---- P2: catch-all from <homedir>/etc/<dom>/aliases ----
	// Format: lines like `address: target` or `*: target` (where
	// asterisk = the catch-all). We only consume the catch-all row;
	// forwarder rows are recorded as warnings (M6.5 schema needs a
	// MailboxID which we'd have to invent here).
	if parsed.HomeDir != "" {
		etcDir := filepath.Join(parsed.HomeDir, "etc")
		if doms, derr := os.ReadDir(etcDir); derr == nil {
			for _, d := range doms {
				if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
					continue
				}
				domName := d.Name()
				aliasFile := filepath.Join(etcDir, domName, "aliases")
				ct, fwds, sk := parseAliases(aliasFile)
				if ct != "" {
					dom, err := domainsRepo.FindByName(ctx, domName)
					if err != nil {
						res.Skipped = append(res.Skipped, fmt.Sprintf("catchall_skip:domain_missing:%s", domName))
					} else if !preserveMailRouting {
						// JAB-46: don't auto-route unknown-address mail to an
						// imported catch-all (attacker-controllable if the source
						// was compromised). Quarantine — leave the catch-all unset;
						// the operator re-applies it in the Mail tab if trusted.
						res.Skipped = append(res.Skipped, fmt.Sprintf("catchall_quarantined:%s→%s", domName, ct))
					} else if uErr := domainsRepo.UpdateCatchallTarget(ctx, dom.ID, &ct); uErr != nil {
						res.Skipped = append(res.Skipped, fmt.Sprintf("catchall_skip:db_update:%s:%v", domName, uErr))
					} else {
						res.CatchallsSet++
					}
				}
				// Forwarder rows — M6.5 EmailForwarder needs both
				// MailboxID + DomainID. Look up by `<local>@<dom>` to
				// find the source mailbox; skip the line when no
				// matching panel mailbox exists (cpanel allows forwards
				// without a local mailbox, jabali doesn't).
				for _, f := range fwds {
					if mailboxesRepo == nil || forwardersRepo == nil {
						res.Skipped = append(res.Skipped, "forwarders_skip:repos_unwired")
						break
					}
					local := f.Local
					target := f.Target
					if local == "" || target == "" {
						continue
					}
					srcEmail := local + "@" + domName
					mb, mErr := mailboxesRepo.FindByEmail(ctx, srcEmail)
					if mErr != nil || mb == nil {
						res.ForwardersOrphaned++
						res.Skipped = append(res.Skipped, fmt.Sprintf("forwarder_orphan:%s→%s (no local mailbox)", srcEmail, target))
						continue
					}
					fwd := &models.EmailForwarder{
						ID:        ids.NewULID(),
						MailboxID: &mb.ID,
						DomainID:  mb.DomainID,
						Type:      "external",
						LocalPart: &local,
						Target:    target,
						Enabled:   preserveMailRouting, // JAB-46: inert unless opted in
						ManagedBy: "m35",
					}
					if cErr := forwardersRepo.Create(ctx, fwd); cErr != nil {
						res.Skipped = append(res.Skipped, fmt.Sprintf("forwarder_skip:create:%s→%s:%v", srcEmail, target, cErr))
						continue
					}
					res.ForwardersCreated++
				}
				res.Skipped = append(res.Skipped, sk...)
			}
		}
	}

	// ---- P4: per-domain PHP version ----
	// cpanel writes per-domain `<dom>.php-fpm.yaml` with `phpversion:
	// ea-php82`. Schema migration 000129 allows multiple FPM pools
	// per user (one per version) so each domain can keep its source
	// version. For each distinct version: ensure (user, version)
	// pool exists via php.pool.apply, then bind every domain in
	// that group to that pool via SetPHPPoolID.
	if agentCli != nil && poolsRepo != nil && domainsRepo != nil {
		byVersion := detectPHPVersionsPerDomain(parsed)
		// JAB-218: record the source spread BEFORE any binding, so the summary
		// distinguishes "the source ran one PHP version" from "we detected
		// nothing and every domain silently inherited the box default" — the
		// two used to look identical (`php_version=`), which is how 39 PHP-7.4
		// sites landed on 8.4 without anyone noticing until they 500'd.
		res.PHPVersionsDetected = summarisePHPVersionSpread(byVersion)
		if len(byVersion) == 0 {
			res.Skipped = append(res.Skipped,
				"php_version_detect:source recorded no per-domain PHP version — every domain will run the box default")
		}
		seenVersions := map[string]string{} // version → pool_id
		for version, doms := range byVersion {
			if version == "" {
				continue
			}
			// Look up or insert the (user, version) pool DB row first.
			// Agent's php.pool.apply only writes FPM config; the panel
			// row is the source of truth domains.SetPHPPoolID references.
			pool, lookupErr := poolsRepo.FindByUserAndVersion(ctx, targetUserID, version)
			if lookupErr != nil && !errors.Is(lookupErr, repository.ErrNotFound) {
				res.Skipped = append(res.Skipped, fmt.Sprintf("php_pool_lookup_skip:%s:%v", version, lookupErr))
				continue
			}
			if pool == nil {
				now := time.Now().UTC()
				pool = &models.PHPPool{
					ID:                        ids.NewULID(),
					UserID:                    targetUserID,
					PHPVersion:                version,
					PmMode:                    "ondemand",
					PmMaxChildren:             20,
					ProcessIdleTimeoutSeconds: 60,
					Status:                    "pending",
					CreatedAt:                 now,
					UpdatedAt:                 now,
				}
				if cErr := poolsRepo.Create(ctx, pool); cErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("php_pool_create_skip:%s:%v", version, cErr))
					continue
				}
			}
			// Ensure the source PHP version — and its required extensions,
			// notably php<v>-mysql — is actually installed before applying the
			// pool. php.pool.apply only writes FPM config and hard-fails if the
			// version dir is absent; and even a present version can lack mysqli,
			// which 500s every migrated site's mysqli_connect() (GH #531).
			// php.version.install is idempotent and backfills missing exts, so a
			// migrated Hestia/cPanel domain on e.g. 8.2 gets a complete runtime
			// even when the box shipped only the default version. Non-fatal:
			// pool.apply below still reports a clear error if the version is
			// genuinely uninstallable.
			if _, err := agentCli.Call(ctx, "php.version.install", map[string]any{
				"version": version,
			}); err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("php_version_install_skip:%s:%v", version, err))
			}
			// Ensure FPM pool exists at OS level.
			if _, err := agentCli.Call(ctx, "php.pool.apply", map[string]any{
				"username":        targetUsername,
				"php_version":     version,
				"pm_max_children": uint32(20),
				"additive":        true, // M35.8 P6: per-domain PHP — keep other-version pools
			}); err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("php_pool_apply_skip:%s:%v", version, err))
				continue
			}
			res.PHPPoolsCreated++
			seenVersions[version] = pool.ID
			if res.PHPVersionApplied == "" {
				res.PHPVersionApplied = version
			}
			// Bind every domain that runs this version.
			for _, dom := range doms {
				d, dErr := domainsRepo.FindByName(ctx, dom)
				if dErr != nil || d == nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("php_bind_skip:%s:no_domain_row", dom))
					continue
				}
				if sErr := domainsRepo.SetPHPPoolID(ctx, d.ID, &pool.ID); sErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("php_bind_skip:%s:%v", dom, sErr))
					continue
				}
				res.PHPDomainsBound++
			}
		}
		_ = seenVersions // future: clean up orphan pools
	}

	// ---- P3.5: DKIM key preserve ----
	// cpanel writes per-domain RSA DKIM private keys under
	//   cpmove-<user>/domainkeys/<domain>   (newer)
	//   <homedir>/etc/<domain>/dkim_keys.priv (legacy)
	// jabali rotates to Ed25519 + selector "jabali" on email-enable,
	// so signature continuity isn't preserved automatically. We
	// copy the source key into a sidecar path the operator can wire
	// into Stalwart for verifying pre-migration mail (DNS at the
	// source selector must stay published or be re-published).
	if agentCli != nil {
		for _, root := range []string{
			filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "domainkeys"),
			filepath.Join(parsed.ExtractDir, "domainkeys"),
		} {
			entries, derr := os.ReadDir(root)
			if derr != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				dom := e.Name()
				if !strings.Contains(dom, ".") {
					continue
				}
				keyBytes, rerr := os.ReadFile(filepath.Join(root, dom))
				if rerr != nil || len(keyBytes) == 0 {
					continue
				}
				if _, callErr := agentCli.Call(ctx, "dkim.import", map[string]any{
					"domain":    dom,
					"selector":  "default", // cpanel's per-domain selector
					"algorithm": "rsa",
					"key_pem":   string(keyBytes),
				}); callErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("dkim_skip:%s:%v", dom, callErr))
					continue
				}
				res.DKIMKeysPreserved++
			}
			if res.DKIMKeysPreserved > 0 {
				break
			}
		}
	}

	// ---- P6: FTP accounts (record-only) ----
	if parsed.HomeDir != "" {
		if files, derr := os.ReadDir(filepath.Join(parsed.HomeDir, "etc")); derr == nil {
			for _, f := range files {
				if !f.IsDir() {
					continue
				}
				ftpPasswd := filepath.Join(parsed.HomeDir, "etc", f.Name(), "passwd")
				if n := countNonCommentLines(ftpPasswd); n > 0 {
					res.FTPAccountsObserved += n
					res.Skipped = append(res.Skipped, fmt.Sprintf("ftp_observed:%s count=%d (FTP deprecated — re-issue via SFTP keys)", f.Name(), n))
				}
			}
		}
	}

	// ---- P1: per-mailbox Sieve filters ----
	// cpanel layout:
	//   <homedir>/etc/<dom>/<local>/filter.yaml   (per-mailbox)
	//   <homedir>/etc/<dom>/managefilters/*.filter (global per-domain)
	// We pick up the per-mailbox file + store as cpanel_raw (Sieve
	// conversion is post-restore operator work).
	if parsed.HomeDir != "" && filtersRepo != nil && mailboxesRepo != nil {
		etcDir := filepath.Join(parsed.HomeDir, "etc")
		if doms, derr := os.ReadDir(etcDir); derr == nil {
			for _, d := range doms {
				if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
					continue
				}
				domName := d.Name()
				users, uerr := os.ReadDir(filepath.Join(etcDir, domName))
				if uerr != nil {
					continue
				}
				for _, u := range users {
					if !u.IsDir() {
						continue
					}
					local := u.Name()
					filterPath := filepath.Join(etcDir, domName, local, "filter.yaml")
					b, rerr := os.ReadFile(filterPath)
					if rerr != nil || len(b) == 0 {
						continue
					}
					addr := local + "@" + domName
					mb, mErr := mailboxesRepo.FindByEmail(ctx, addr)
					if mErr != nil || mb == nil {
						res.Skipped = append(res.Skipped, fmt.Sprintf("filter_orphan:%s (no local mailbox)", addr))
						continue
					}
					raw := string(b)
					f := &models.EmailFilter{
						ID:        ids.NewULID(),
						MailboxID: mb.ID,
						Name:      "cpanel-import",
						CpanelRaw: &raw,
						Enabled:   preserveMailRouting, // JAB-46: inert unless opted in
						ManagedBy: "m35",
					}
					if cErr := filtersRepo.Create(ctx, f); cErr != nil {
						res.Skipped = append(res.Skipped, fmt.Sprintf("filter_skip:%s:%v", addr, cErr))
						continue
					}
					res.FiltersImported++
				}
			}
		}
	}

	// ---- P2.5: per-mailbox autoresponders ----
	// cpanel layout: <homedir>/.autorespond/<address>.{conf,yaml}
	if parsed.HomeDir != "" && autoRespondersRepo != nil && mailboxesRepo != nil {
		respDir := filepath.Join(parsed.HomeDir, ".autorespond")
		if files, derr := os.ReadDir(respDir); derr == nil {
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				name := f.Name()
				// strip .conf / .yaml — leaving the address
				addr := strings.TrimSuffix(strings.TrimSuffix(name, ".conf"), ".yaml")
				if !strings.Contains(addr, "@") {
					continue
				}
				mb, mErr := mailboxesRepo.FindByEmail(ctx, addr)
				if mErr != nil || mb == nil {
					res.AutorespondersOrphaned++
					res.Skipped = append(res.Skipped, fmt.Sprintf("autoresponder_orphan:%s (no local mailbox)", addr))
					continue
				}
				ar := parseAutoresponder(filepath.Join(respDir, name), mb.ID)
				if ar == nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("autoresponder_skip:%s parse failed", addr))
					continue
				}
				ar.Enabled = preserveMailRouting // JAB-46: inert unless opted in
				if uErr := autoRespondersRepo.Update(ctx, ar); uErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("autoresponder_skip:db:%s:%v", addr, uErr))
					continue
				}
				res.AutorespondersCreated++
			}
		}
	}

	// GH #327: HestiaCP e-mail aliases live at mail/<domain>/conf/aliases, NOT
	// cpanel's <home>/etc/<dom>/aliases — so they never imported. Scan + attach
	// each alias to its target mailbox.
	scanHestiaAliases(ctx, forwardersRepo, mailboxesRepo, domainsRepo, parsed, preserveMailRouting, res)

	return res, nil
}

// scanHestiaAliases imports HestiaCP e-mail aliases. Hestia writes them to
// mail/<domain>/conf/aliases as `alias@domain:account@domain` lines (one per
// alias), coupling an alias address to a real local account. jabali models an
// alias as a type='alias' EmailForwarder OWNED BY the target mailbox — the alias
// is an extra local_part that delivers to that mailbox. Mirroring the panel's
// create path (GH #280), Target stores the alias's OWN address so one mailbox
// can hold many aliases without colliding on uq_external_forward(mailbox_id,
// type,target). No-op for cpanel/DA (no mail/<dom>/conf/aliases). Idempotent: a
// unique-index conflict on re-run is counted as skipped, not fatal.
func scanHestiaAliases(
	ctx context.Context,
	forwardersRepo repository.EmailForwarderRepository,
	mailboxesRepo repository.MailboxRepository,
	domainsRepo repository.DomainRepository,
	parsed *ParsedTarball,
	preserveMailRouting bool,
	res *ExtrasResult,
) {
	if forwardersRepo == nil || mailboxesRepo == nil {
		return
	}
	mailRoot := filepath.Join(parsed.ExtractDir, "mail")
	doms, err := os.ReadDir(mailRoot)
	if err != nil {
		return
	}
	for _, d := range doms {
		if !d.IsDir() {
			continue
		}
		domName := d.Name()
		body, rErr := os.ReadFile(filepath.Join(mailRoot, domName, "conf", "aliases"))
		if rErr != nil {
			continue // no aliases file for this domain — not an error
		}
		dom, dErr := domainsRepo.FindByName(ctx, domName)
		if dErr != nil || dom == nil {
			res.Skipped = append(res.Skipped, "hestia_alias_skip:domain_missing:"+domName)
			continue
		}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// `alias@domain:account@domain`
			colon := strings.Index(line, ":")
			if colon < 1 {
				res.Skipped = append(res.Skipped, "hestia_alias_malformed:"+line)
				continue
			}
			aliasAddr := strings.TrimSpace(line[:colon])
			acctAddr := strings.TrimSpace(line[colon+1:])
			aliasLocal := aliasAddr
			if at := strings.LastIndex(aliasAddr, "@"); at > 0 {
				aliasLocal = aliasAddr[:at]
			}
			if aliasLocal == "" || acctAddr == "" {
				continue
			}
			// The alias delivers to the target account: attach it to that
			// mailbox. If the account isn't a panel mailbox, it's orphaned.
			mb, mErr := mailboxesRepo.FindByEmail(ctx, acctAddr)
			if mErr != nil || mb == nil {
				res.ForwardersOrphaned++
				res.Skipped = append(res.Skipped, fmt.Sprintf("hestia_alias_orphan:%s→%s (no local mailbox)", aliasAddr, acctAddr))
				continue
			}
			lp := aliasLocal
			f := &models.EmailForwarder{
				ID:        ids.NewULID(),
				MailboxID: &mb.ID,
				DomainID:  dom.ID,
				Type:      "alias",
				LocalPart: &lp,
				Target:    aliasLocal + "@" + domName, // alias's own address (GH #280 uniqueness)
				Enabled:   preserveMailRouting,        // JAB-46: inert unless opted in
				ManagedBy: "gh327-hestia",
			}
			if cErr := forwardersRepo.Create(ctx, f); cErr != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("hestia_alias_skip:%s:%v", aliasAddr, cErr))
				continue
			}
			res.ForwardersCreated++
		}
	}
}

// parseAutoresponder reads a cpanel .autorespond/<addr>.{conf,yaml}
// file + builds an EmailAutoresponder row. Both formats share the
// same "Header: value" / blank line / body shape; YAML adds quoting
// which we strip.
func parseAutoresponder(path, mailboxID string) *models.EmailAutoresponder {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	parts := strings.SplitN(string(raw), "\n\n", 2)
	headerBlock := parts[0]
	var bodyPart string
	if len(parts) == 2 {
		bodyPart = parts[1]
	}
	var subject string
	for _, line := range strings.Split(headerBlock, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		if strings.EqualFold(k, "subject") {
			subject = v
		}
	}
	body := strings.TrimSpace(bodyPart)
	if body == "" && subject == "" {
		return nil
	}
	ar := &models.EmailAutoresponder{
		MailboxID: mailboxID,
		Enabled:   true,
		ManagedBy: "m35",
	}
	if subject != "" {
		ar.Subject = &subject
	}
	if body != "" {
		ar.TextBody = &body
	}
	return ar
}

// aliasForward is one forwarder row out of an aliases file:
// `<local>@<domain>: <target>` → Local="<local>", Target="<target>".
type aliasForward struct {
	Local  string
	Target string
}

// parseAliases reads a cpanel-style aliases file. Returns
// (catchAllTarget, forwarderRows, perLineWarnings).
func parseAliases(path string) (string, []aliasForward, []string) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, nil
	}
	defer f.Close()

	var catchAll string
	var forwards []aliasForward
	var warnings []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Skip cpanel meta-lines that begin with `:` (e.g. ":fail:").
		if strings.HasPrefix(line, ":") {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
		if key == "*" {
			catchAll = val
			continue
		}
		// `key` may be `local` or `local@domain`. Normalise to local.
		if at := strings.IndexByte(key, '@'); at > 0 {
			key = key[:at]
		}
		forwards = append(forwards, aliasForward{Local: key, Target: val})
	}
	if err := sc.Err(); err != nil {
		warnings = append(warnings, fmt.Sprintf("aliases_scan_err:%s:%v", path, err))
	}
	return catchAll, forwards, warnings
}

// userdataNonDomainFiles are the fixed-name entries cpanel keeps in the
// userdata dir alongside the per-domain files.
var userdataNonDomainFiles = map[string]bool{
	"main": true, "cache": true, "scope": true, "nobody": true,
}

// userdataSidecarSuffixes are the per-domain sidecars that sit next to the
// plain userdata file. None of them carries phpversion.
var userdataSidecarSuffixes = []string{
	".php-fpm.yaml", ".php-fpm.cache", ".cache", ".json", ".db", ".yaml",
}

// isPlainDomainUserdata reports whether name is cpanel's plain per-domain
// userdata file — the one that actually carries `phpversion`.
//
// _SSL is excluded because it duplicates the http vhost's settings; counting
// both would double every domain in the version tally.
func isPlainDomainUserdata(name string) bool {
	if name == "" || userdataNonDomainFiles[name] || strings.HasSuffix(name, "_SSL") {
		return false
	}
	for _, suf := range userdataSidecarSuffixes {
		if strings.HasSuffix(name, suf) {
			return false
		}
	}
	return true
}

// scanUserdataPHPVersions walks the source userdata dir and returns
// version → [domain…] for every per-domain PHP version it can read.
//
// It reads the PLAIN `<userdata>/<domain>` file. An earlier revision scanned
// `<domain>.php-fpm.yaml` instead, which never carries the key: cpanel puts
// only pool tuning there —
//
//	_is_present: 1
//	pm_max_children: 5
//	pm_max_requests: 20
//	pm_process_idle_timeout: 10
//
// Measured on a live source box: 0 of 143 .php-fpm.yaml files contained
// `phpversion`, while 141 plain userdata files did. So detection always came
// back empty, every migrated domain silently inherited the destination's
// default PHP, and the restore summary reported the tell nobody read:
// `php_pools=0 php_domains_bound=0 php_version=` (JAB-218).
//
// The .php-fpm.yaml probe is kept as a fallback rather than deleted — it costs
// one stat per domain and covers any cpanel build that does write the key
// there — but the plain file is now the primary source.
func scanUserdataPHPVersions(parsed *ParsedTarball) map[string][]string {
	roots := []string{
		filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "userdata"),
		filepath.Join(parsed.ExtractDir, "userdata"),
		filepath.Join(parsed.ExtractDir, "cp", parsed.SourceUser, "userdata"),
	}
	out := map[string][]string{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isPlainDomainUserdata(e.Name()) {
				continue
			}
			domain := e.Name()
			ver := normalisePHPVersion(extractKV(filepath.Join(root, domain), "phpversion"))
			if ver == "" {
				// Legacy fallback: some builds may carry it in the sidecar.
				ver = normalisePHPVersion(extractKV(
					filepath.Join(root, domain+".php-fpm.yaml"), "phpversion"))
			}
			if ver == "" {
				continue
			}
			out[ver] = append(out[ver], domain)
		}
		if len(out) > 0 {
			break
		}
	}
	// Deterministic domain order per version so reruns and tests agree.
	for v := range out {
		sort.Strings(out[v])
	}
	return out
}

// summarisePHPVersionSpread renders the detected version distribution for the
// restore summary, e.g. "7.4x39,8.1x82,8.3x29". Returns "none" for an empty
// map, which is the case the operator most needs to see.
func summarisePHPVersionSpread(byVersion map[string][]string) string {
	if len(byVersion) == 0 {
		return "none"
	}
	vers := make([]string, 0, len(byVersion))
	for v := range byVersion {
		vers = append(vers, v)
	}
	sort.Strings(vers)
	parts := make([]string, 0, len(vers))
	for _, v := range vers {
		parts = append(parts, fmt.Sprintf("%sx%d", v, len(byVersion[v])))
	}
	return strings.Join(parts, ",")
}

// detectPHPVersionsPerDomain returns version → [domain1, domain2…]
// — every distinct PHP version in the source userdata dir with the
// list of domains using it. Used by the per-domain pool-bind path
// in ImportExtras.
func detectPHPVersionsPerDomain(parsed *ParsedTarball) map[string][]string {
	return scanUserdataPHPVersions(parsed)
}

// detectPHPVersion returns (primaryVersion, otherVersions) across the source's
// per-domain userdata files. "primary" = the most-frequent version; the rest
// are reported so the operator can see the spread. Empty string when the source
// records no PHP version at all.
//
// Shares scanUserdataPHPVersions with the per-domain path so both read the same
// file — see the note there for why the plain userdata file, not the
// .php-fpm.yaml sidecar, is the one that carries `phpversion` (JAB-218).
func detectPHPVersion(parsed *ParsedTarball) (string, []string) {
	byVersion := scanUserdataPHPVersions(parsed)
	if len(byVersion) == 0 {
		return "", nil
	}
	// Most-frequent wins; ties pick lexicographically lower so reruns
	// are deterministic.
	primary := ""
	bestCount := 0
	for v, doms := range byVersion {
		c := len(doms)
		if c > bestCount || (c == bestCount && (primary == "" || v < primary)) {
			primary = v
			bestCount = c
		}
	}
	var others []string
	for v := range byVersion {
		if v != primary {
			others = append(others, v)
		}
	}
	sort.Strings(others)
	return primary, others
}

// normalisePHPVersion maps cpanel's "ea-php82" / "ea-php-rpm-7.4" /
// raw "8.2" strings to jabali's "<major>.<minor>" form so
// php.pool.apply accepts them. Empty string on unparseable input.
func normalisePHPVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "ea-php")
	raw = strings.TrimPrefix(raw, "ea-")
	raw = strings.TrimPrefix(raw, "php-")
	raw = strings.TrimPrefix(raw, "php")
	raw = strings.TrimPrefix(raw, "-rpm-")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// "82" → "8.2"; already-dotted "8.2" passes through.
	if !strings.Contains(raw, ".") && len(raw) == 2 {
		return string(raw[0]) + "." + string(raw[1])
	}
	return raw
}

// countNonCommentLines tallies non-blank, non-#-prefixed lines in
// a file. Used for FTP-account counting where the source's
// /etc/<dom>/passwd is a colon-separated user list.
func countNonCommentLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
	}
	return n
}

// findUserdataFile probes the same locations PeekAccountMeta uses
// for the cp/<user> file. Returns the first hit or "".
func findUserdataFile(extractDir, sourceUser string) string {
	candidates := []string{
		filepath.Join(extractDir, "cpmove-"+sourceUser, "cp", sourceUser),
		filepath.Join(extractDir, "cp", sourceUser),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// splitCpanelList splits comma-separated lists out of cpanel's
// `KEY=v1,v2,v3` userdata lines. Tolerant of stray whitespace.
func splitCpanelList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
