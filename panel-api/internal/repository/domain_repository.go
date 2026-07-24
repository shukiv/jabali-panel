package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// DomainRepository defines data access for hosted domains.
type DomainRepository interface {
	Create(ctx context.Context, d *models.Domain) error
	FindByID(ctx context.Context, id string) (*models.Domain, error)
	// FindByIDs batch-loads domains by id (deduped, one query) — the
	// N+1-free path for list handlers resolving many rows (JAB-147).
	FindByIDs(ctx context.Context, ids []string) ([]models.Domain, error)
	FindByName(ctx context.Context, name string) (*models.Domain, error)
	List(ctx context.Context, opts ListOptions) ([]models.Domain, int64, error)
	ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.Domain, int64, error)
	Update(ctx context.Context, d *models.Domain) error
	// BulkSetEnabledByUserID flips domains.is_enabled for every row
	// owned by the user. Returns the count of changed rows. Used by
	// the admin user-suspend handler so a single API call takes every
	// site offline. Reconciler picks up the flip on the next tick and
	// removes (or restores) the nginx sites-enabled symlinks.
	BulkSetEnabledByUserID(ctx context.Context, userID string, enabled bool) (int64, error)
	Delete(ctx context.Context, id string) error
	CountByUserID(ctx context.Context, userID string) (int64, error)
	// ListForRegistrarRefresh returns domains whose registrar expiry has never
	// been checked or was last checked before staleBefore (GH #259), oldest
	// first, capped at limit — the WHOIS-fetch ticker's work queue.
	ListForRegistrarRefresh(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error)
	// SetRegistrarExpiry caches a domain's registration expiry + stamps the
	// check time. expiresAt may be nil (whois unavailable / unparseable) — the
	// check time is still set so the ticker doesn't re-query every pass.
	SetRegistrarExpiry(ctx context.Context, id string, expiresAt *time.Time, checkedAt time.Time) error
	// CountByPHPPoolID returns the number of domains currently bound to
	// the given PHP pool. Used by the pool-delete handler to refuse with
	// 409 when any domain still references the pool (ADR-0023 decision 10).
	CountByPHPPoolID(ctx context.Context, poolID string) (int64, error)
	// SetPHPPoolID binds (or, when poolID is nil, unbinds) a domain's
	// php_pool_id column in isolation. Update()'s column allowlist does
	// NOT include php_pool_id on purpose — it's the one column whose
	// mutations must only come from the dedicated bind/unbind handlers,
	// not from generic domain PATCH.
	SetPHPPoolID(ctx context.Context, id string, poolID *string) error
	// UpdatePHPSettings atomically updates the six per-domain PHP INI override
	// columns. NULL values explicitly clear the override. This is a dedicated
	// method because the columns are not in Update()'s allowlist.
	UpdatePHPSettings(ctx context.Context, id string, settings DomainPHPSettings) error
	// UpdateEmailState writes the four M6 email columns (email_enabled,
	// dkim_selector, dkim_public_key, email_enabled_at) in one go. Dedicated
	// method because none of these are in Update()'s allowlist and because
	// all four flip together when email is enabled/disabled (ADR-0042).
	// Passing enabled=false clears the timestamp but keeps dkim_selector +
	// dkim_public_key so DNS re-publication after a later re-enable doesn't
	// re-roll the key per ADR-0043.
	UpdateEmailState(ctx context.Context, id string, state DomainEmailState) error
	// UpdateMailProvider writes the GH#181 mail-provider columns
	// (mail_provider + the optional DKIM tokens) together with the
	// derived email_enabled + skip_auto_san. Dedicated method (not the
	// Select-allowlist Update) so the columns can't be silently dropped.
	UpdateMailProvider(ctx context.Context, id string, mp DomainMailProvider) error

	// UpdateSSLMode writes ssl_mode + the derived ssl_enabled shadow together
	// (GH #246). Dedicated method, not the Select-allowlist Update, to avoid
	// silent column drops.
	UpdateSSLMode(ctx context.Context, id string, mode string) error
	// SetSharedCertificate attaches (sharedCertID non-nil, mode 'shared') or
	// detaches (nil, mode 'le') a domain from a JAB-170 shared certificate.
	SetSharedCertificate(ctx context.Context, id string, sharedCertID *string, mode string) error
	// FindPanelPrimary returns the single is_panel_primary=1 row, or
	// ErrPanelPrimaryNotFound if no such row exists. ADR-0048.
	FindPanelPrimary(ctx context.Context) (*models.Domain, error)
	// MarkPanelPrimary sets is_panel_primary=1 on the target row and
	// clears it on any other row in a single transaction, so the "at
	// most one" invariant holds without a SQL UNIQUE constraint.
	// Idempotent — re-running on an already-marked row is a no-op.
	// ADR-0048.
	MarkPanelPrimary(ctx context.Context, id string) error
	// SetListenIPs writes the M24 per-domain IP binding columns
	// (listen_ipv4_id, listen_ipv6_id). Only families flagged ChangeIPv4
	// / ChangeIPv6 are written — others are left untouched, so a PATCH
	// that only mentions one family doesn't accidentally clear the
	// other. Nil pointers map to SQL NULL ("use server default per
	// family"). Dedicated method because the columns are not in
	// Update()'s allowlist on purpose.
	SetListenIPs(ctx context.Context, id string, upd DomainListenIPs) error
	// UpdateCatchallTarget writes the catchall_target column for a domain.
	// Nil target clears the catch-all (sets to NULL). Dedicated method
	// because the column is not in Update()'s allowlist.
	UpdateCatchallTarget(ctx context.Context, id string, target *string) error
	// UpdateDisclaimer writes disclaimer_enabled + disclaimer_text.
	// M6.5 Step 6 ADR-0052; reconciler pushes to Stalwart sieve.
	UpdateDisclaimer(ctx context.Context, id string, enabled bool, text *string) error
	// UpdateDNSSECEnabled writes dnssec_enabled + dnssec_enabled_at.
	// ADR-0076. Dedicated method because neither column is in Update()'s
	// allowlist; enabling without a timestamp or disabling without clearing
	// the timestamp is a bug waiting to happen.
	UpdateDNSSECEnabled(ctx context.Context, id string, enabled bool) error
	// AttachDockerApp wires an existing (tenant-managed) domain to a
	// docker app: rewrites nginx_rules + sets docker_app_id in one
	// shot. Dedicated method because docker_app_id is not in
	// Update()'s allowlist.
	AttachDockerApp(ctx context.Context, id string, dockerAppID string, rules models.NginxRules) error
	// DetachDockerApp is the inverse: clears docker_app_id and (when
	// resetRules is true) clears nginx_rules back to an empty set.
	// Called from docker_app delete to drop the proxy_pass rule the
	// install/attach handler injected.
	DetachDockerApp(ctx context.Context, id string, resetRules bool) error
	// UpdateCacheEnabled writes domains.cache_enabled (ADR-0108).
	// Dedicated method: cache_enabled is not in Update()'s allowlist.
	UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error
	// UpdateCachePath writes domains.cache_path (Gitea #420).
	UpdateCachePath(ctx context.Context, id, path string) error
	UpdateCacheTTL(ctx context.Context, id string, seconds int) error
	// UpdateCacheQueryAllowlist writes domains.cache_query_allowlist (migration
	// 000230). csv is the normalized comma-joined param names ("" = off).
	UpdateCacheQueryAllowlist(ctx context.Context, id, csv string) error
	// UpdateSkipAutoSAN writes domains.skip_auto_san (M50 SAN opt-out).
	// Dedicated method per [[feedback_domain_update_allowlist_silent_drop]].
	UpdateSkipAutoSAN(ctx context.Context, id string, enabled bool) error
	// UpdateMTASTSEnabled writes domains.mta_sts_enabled (ADR-0109). On
	// enable, also bumps mta_sts_id to the current unix timestamp so the
	// TXT record id rotates (forces caches to refetch). On disable, the
	// id is left intact — re-enabling later picks up a fresh id anyway,
	// and keeping it lets ops correlate older TXT snapshots.
	UpdateMTASTSEnabled(ctx context.Context, id string, enabled bool) (newID uint64, err error)
	// UpdateMTASTSAppliedID records the id the agent successfully
	// applied via mail.mtasts.apply (ADR-0109 / Wave 7c). Idempotent
	// no-op when applied == 0 — the reconciler uses 0 only after a
	// disable, but that path uses UpdateMTASTSEnabled(false) which
	// doesn't touch this column on its own.
	UpdateMTASTSAppliedID(ctx context.Context, id string, appliedID uint64) error
	// UpdatePHPPoolID writes domains.php_pool_id. Dedicated method
	// because the column isn't in Update()'s allowlist (the comment
	// there explicitly notes PHP-pool binding has its own writer).
	// Pass nil to unbind. Bug: ensureDomainPHPBinding previously called
	// the generic Update which silently dropped the column, so the row
	// stayed NULL and the reconciler re-bound on every tick.
	UpdatePHPPoolID(ctx context.Context, id string, poolID *string) error
	// UpdateGhostState writes the M38 ghost-detector columns
	// (ghost_state + ghost_checked_at + ghost_detail) atomically.
	// Dedicated method because none of the three are in Update()'s
	// allowlist; the detector is the only writer and runs every few
	// hours from the eventsources package.
	UpdateGhostState(ctx context.Context, id, state string, checkedAt time.Time, detail *string) error
	// ListForGhostCheck returns up to `limit` domains whose
	// ghost_checked_at is older than `staleBefore` (NULL counts as
	// stale), ordered by checked_at NULLS first then asc. Used by
	// the ghost detector to walk the table in batches.
	ListForGhostCheck(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error)
}

// DomainListenIPs is the bundle of optional column writes for
// SetListenIPs. ChangeIPv4 / ChangeIPv6 disambiguate "absent in PATCH"
// from "explicitly set to null" — only the flagged family is touched.
type DomainListenIPs struct {
	ChangeIPv4 bool
	IPv4ID     *uint64
	ChangeIPv6 bool
	IPv6ID     *uint64
}

// DomainEmailState is the bundle of columns written together by
// UpdateEmailState. Nil DkimSelector / DkimPublicKey leave those
// columns alone (used by disable, which keeps the key material).
type DomainEmailState struct {
	Enabled        bool
	DkimSelector   *string
	DkimPublicKey  *string
	EmailEnabledAt *time.Time
}

// DomainPHPSettings holds per-domain PHP INI overrides.
// NULL means "use pool default — do not emit a fastcgi_param".
type DomainPHPSettings struct {
	MemoryLimit       *string `json:"php_memory_limit,omitempty"`
	UploadMaxFilesize *string `json:"php_upload_max_filesize,omitempty"`
	PostMaxSize       *string `json:"php_post_max_size,omitempty"`
	MaxInputVars      *int    `json:"php_max_input_vars,omitempty"`
	MaxExecutionTime  *int    `json:"php_max_execution_time,omitempty"`
	MaxInputTime      *int    `json:"php_max_input_time,omitempty"`
}

type domainRepo struct{ db *gorm.DB }

func NewDomainRepository(db *gorm.DB) DomainRepository {
	return &domainRepo{db: db}
}

func (r *domainRepo) Create(ctx context.Context, d *models.Domain) error {
	if err := r.db.WithContext(ctx).Create(d).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *domainRepo) FindByID(ctx context.Context, id string) (*models.Domain, error) {
	var d models.Domain
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// FindByIDs batch-loads domains by id (deduped, one query) — the N+1-free
// path for list handlers resolving many rows (JAB-147).
func (r *domainRepo) FindByIDs(ctx context.Context, ids []string) ([]models.Domain, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []models.Domain
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *domainRepo) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	var d models.Domain
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// domainListCols — only the domain name is free-text searchable.
// user_id is deliberately absent from Search because it's an opaque ULID
// and admins have a dedicated "this user's domains" path via ListByUserID.
//
// Sort accepts the qualified `users.username` form so the LEFT JOIN we
// apply at the SQL layer can resolve it; List() rewrites the bare
// `username` requested by the UI into the qualified form before
// passing opts down to applyListOptions.
var domainListCols = ListCols{
	Search:      []string{"domains.name"},
	Sort:        []string{"domains.name", "domains.created_at", "users.username", "domains.is_enabled"},
	DefaultSort: "domains.name",
}

// rewriteDomainSort maps the bare sort key the UI sends onto the
// qualified column the JOIN-ed query needs. Unknown keys are passed
// through verbatim and will fall back to DefaultSort via pickSort.
func rewriteDomainSort(sort string) string {
	switch sort {
	case "name":
		return "domains.name"
	case "created_at":
		return "domains.created_at"
	case "username":
		return "users.username"
	case "is_enabled":
		return "domains.is_enabled"
	}
	return sort
}

func (r *domainRepo) List(ctx context.Context, opts ListOptions) ([]models.Domain, int64, error) {
	var (
		domains []models.Domain
		total   int64
	)
	base := r.db.WithContext(ctx).
		Model(&models.Domain{}).
		Joins("LEFT JOIN users ON users.id = domains.user_id")

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, domainListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "asc"
	}
	opts.Sort = rewriteDomainSort(opts.Sort)
	q := applyListOptions(base.Session(&gorm.Session{}), opts, domainListCols)
	if err := q.Find(&domains).Error; err != nil {
		return nil, 0, err
	}
	if err := r.populateSSLStates(ctx, &domains); err != nil {
		return nil, 0, err
	}
	return domains, total, nil
}

func (r *domainRepo) ListByUserID(ctx context.Context, userID string, opts ListOptions) ([]models.Domain, int64, error) {
	var (
		domains []models.Domain
		total   int64
	)
	base := r.db.WithContext(ctx).
		Model(&models.Domain{}).
		Joins("LEFT JOIN users ON users.id = domains.user_id").
		Where("domains.user_id = ?", userID)

	countQ := applyListOptions(base.Session(&gorm.Session{}), ListOptions{Search: opts.Search}, domainListCols)
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if opts.Sort == "" && opts.Order == "" {
		opts.Order = "asc"
	}
	opts.Sort = rewriteDomainSort(opts.Sort)
	q := applyListOptions(base.Session(&gorm.Session{}), opts, domainListCols)
	if err := q.Find(&domains).Error; err != nil {
		return nil, 0, err
	}
	if err := r.populateSSLStates(ctx, &domains); err != nil {
		return nil, 0, err
	}
	return domains, total, nil
}

func (r *domainRepo) Update(ctx context.Context, d *models.Domain) error {
	// Whitelist the columns the API's update handler is allowed to write.
	// Anything not listed here is silently ignored by GORM's Select+Updates,
	// which previously dropped index_priority, redirect_all_to,
	// redirect_all_type, and page_redirects on the floor — the handler set
	// them on the in-memory struct, the row was saved without them, and the
	// reconciler never saw the new values. SSL flags, PHP pool binding, and
	// PHP per-domain settings have their own dedicated repo methods.
	if err := r.db.WithContext(ctx).Model(d).Where("id = ?", d.ID).Select(
		"name", "doc_root", "is_enabled", "nginx_custom_directives",
		"nginx_rules", "nginx_safe_options",
		"redirect_all_to", "redirect_all_type", "page_redirects",
		"index_priority", "ssl_enabled", "is_quota_suspended", "webmail_enabled", "updated_at",
	).Updates(d).Error; err != nil {
		return translate(err)
	}
	return nil
}

func (r *domainRepo) BulkSetEnabledByUserID(ctx context.Context, userID string, enabled bool) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&models.Domain{}).
		Where("user_id = ?", userID).
		Updates(map[string]any{
			"is_enabled": enabled,
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return 0, translate(res.Error)
	}
	return res.RowsAffected, nil
}

func (r *domainRepo) Delete(ctx context.Context, id string) error {
	// Guard: refuse to delete the panel-primary row. Load first so we
	// can distinguish "missing" from "protected"; this is cheap (index
	// lookup on PK) and avoids silently eating a protected row when
	// RowsAffected == 0.
	var existing models.Domain
	if err := r.db.WithContext(ctx).Select("id", "is_panel_primary").Where("id = ?", id).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if existing.IsPanelPrimary {
		return ErrCannotDeletePanelPrimary
	}

	res := r.db.WithContext(ctx).Delete(&models.Domain{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindPanelPrimary — see interface doc.
func (r *domainRepo) FindPanelPrimary(ctx context.Context) (*models.Domain, error) {
	var d models.Domain
	if err := r.db.WithContext(ctx).Where("is_panel_primary = ?", true).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPanelPrimaryNotFound
		}
		return nil, err
	}
	return &d, nil
}

// MarkPanelPrimary — see interface doc.
func (r *domainRepo) MarkPanelPrimary(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Verify target exists — fail with ErrNotFound rather than silently
		// leaving the invariant partially applied.
		var exists int64
		if err := tx.Model(&models.Domain{}).Where("id = ?", id).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		// Clear any other panel-primary marker. Scoped to id != target so
		// an idempotent re-run on the already-marked row is a no-op.
		if err := tx.Model(&models.Domain{}).
			Where("is_panel_primary = ? AND id != ?", true, id).
			Update("is_panel_primary", false).Error; err != nil {
			return err
		}
		// Set target.
		if err := tx.Model(&models.Domain{}).
			Where("id = ?", id).
			Update("is_panel_primary", true).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *domainRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Domain{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *domainRepo) SetPHPPoolID(ctx context.Context, id string, poolID *string) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("php_pool_id", poolID)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) CountByPHPPoolID(ctx context.Context, poolID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Domain{}).Where("php_pool_id = ?", poolID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *domainRepo) UpdatePHPSettings(ctx context.Context, id string, settings DomainPHPSettings) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"php_memory_limit":        settings.MemoryLimit,
			"php_upload_max_filesize": settings.UploadMaxFilesize,
			"php_post_max_size":       settings.PostMaxSize,
			"php_max_input_vars":      settings.MaxInputVars,
			"php_max_execution_time":  settings.MaxExecutionTime,
			"php_max_input_time":      settings.MaxInputTime,
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) UpdateEmailState(ctx context.Context, id string, state DomainEmailState) error {
	updates := map[string]interface{}{
		"email_enabled":    state.Enabled,
		"email_enabled_at": state.EmailEnabledAt,
	}
	// Only write DKIM material when the caller supplies it — disable
	// intentionally keeps the existing key (ADR-0043).
	if state.DkimSelector != nil {
		updates["dkim_selector"] = state.DkimSelector
	}
	if state.DkimPublicKey != nil {
		updates["dkim_public_key"] = state.DkimPublicKey
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DomainMailProvider is the input to UpdateMailProvider. EmailEnabled +
// SkipAutoSAN are the values DERIVED from Provider by models.DeriveMailFlags
// (the caller computes them so the reconciler/cert columns stay consistent
// with the enum). M365Onmicrosoft / GoogleDKIM are written as-is (nil ->
// SQL NULL); validate/normalise before calling.
type DomainMailProvider struct {
	Provider        string
	EmailEnabled    bool
	SkipAutoSAN     bool
	M365Onmicrosoft *string
	GoogleDKIM      *string
}

func (r *domainRepo) SetSharedCertificate(ctx context.Context, id string, sharedCertID *string, mode string) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"ssl_mode":              mode,
			"ssl_enabled":           models.SSLEnabledForMode(mode),
			"shared_certificate_id": sharedCertID,
			"updated_at":            time.Now().UTC(),
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) UpdateSSLMode(ctx context.Context, id string, mode string) error {
	updates := map[string]interface{}{
		"ssl_mode":    mode,
		"ssl_enabled": models.SSLEnabledForMode(mode),
		"updated_at":  time.Now().UTC(),
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) UpdateMailProvider(ctx context.Context, id string, mp DomainMailProvider) error {
	updates := map[string]interface{}{
		"mail_provider":    mp.Provider,
		"email_enabled":    mp.EmailEnabled,
		"skip_auto_san":    mp.SkipAutoSAN,
		"m365_onmicrosoft": mp.M365Onmicrosoft,
		"google_dkim":      mp.GoogleDKIM,
		"updated_at":       time.Now().UTC(),
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) SetListenIPs(ctx context.Context, id string, upd DomainListenIPs) error {
	cols := make(map[string]interface{}, 2)
	if upd.ChangeIPv4 {
		cols["listen_ipv4_id"] = upd.IPv4ID
	}
	if upd.ChangeIPv6 {
		cols["listen_ipv6_id"] = upd.IPv6ID
	}
	if len(cols) == 0 {
		return nil
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(cols)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateCatchallTarget — see interface doc.
func (r *domainRepo) UpdateCatchallTarget(ctx context.Context, id string, target *string) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("catchall_target", target)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) UpdateDisclaimer(ctx context.Context, id string, enabled bool, text *string) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"disclaimer_enabled": enabled,
			"disclaimer_text":    text,
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateDNSSECEnabled writes dnssec_enabled + dnssec_enabled_at in lockstep.
// Enabling stamps now(); disabling clears the timestamp.
func (r *domainRepo) UpdateDNSSECEnabled(ctx context.Context, id string, enabled bool) error {
	updates := map[string]any{"dnssec_enabled": enabled}
	if enabled {
		updates["dnssec_enabled_at"] = time.Now().UTC()
	} else {
		updates["dnssec_enabled_at"] = nil
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AttachDockerApp updates nginx_rules + docker_app_id in one shot
// so M48 install can attach to a tenant-owned domain without hitting
// the domains.name UNIQUE constraint via a Create.
func (r *domainRepo) DetachDockerApp(ctx context.Context, id string, resetRules bool) error {
	upd := map[string]any{
		"docker_app_id": nil,
		"updated_at":    time.Now().UTC(),
	}
	if resetRules {
		upd["nginx_rules"] = models.NginxRules{}
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(upd)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) AttachDockerApp(ctx context.Context, id string, dockerAppID string, rules models.NginxRules) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"nginx_rules":   rules,
			"docker_app_id": dockerAppID,
			"updated_at":    time.Now().UTC(),
		})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateCacheEnabled writes domains.cache_enabled (ADR-0108).
func (r *domainRepo) UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("cache_enabled", enabled)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) UpdateCachePath(ctx context.Context, id, path string) error {
	return r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).Update("cache_path", path).Error
}

// UpdateCacheTTL writes domains.cache_ttl_seconds (Gitea #596). Dedicated
// method (no Select allowlist) per the domain.Update-allowlist lesson.
func (r *domainRepo) UpdateCacheTTL(ctx context.Context, id string, seconds int) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("cache_ttl_seconds", seconds)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateCacheQueryAllowlist writes domains.cache_query_allowlist (migration
// 000230). Dedicated method (no Select allowlist) per the same lesson.
func (r *domainRepo) UpdateCacheQueryAllowlist(ctx context.Context, id, csv string) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("cache_query_allowlist", csv)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSkipAutoSAN writes domains.skip_auto_san — tenant opt-out of
// the ADR-0070 auto-added mail/autoconfig SAN entries.
func (r *domainRepo) UpdateSkipAutoSAN(ctx context.Context, id string, enabled bool) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("skip_auto_san", enabled)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMTASTSEnabled writes domains.mta_sts_enabled + mta_sts_id
// in lockstep (ADR-0109). Enabling stamps id = now-unix so the TXT
// record rotates; disabling flips the flag but leaves id untouched
// (re-enabling later rebumps it anyway).
func (r *domainRepo) UpdateMTASTSEnabled(ctx context.Context, id string, enabled bool) (uint64, error) {
	updates := map[string]any{"mta_sts_enabled": enabled}
	var newID uint64
	if enabled {
		newID = uint64(time.Now().UTC().Unix())
		updates["mta_sts_id"] = newID
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return 0, translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, ErrNotFound
	}
	return newID, nil
}

// UpdateMTASTSAppliedID stamps the applied policy id. Called by the
// reconciler after a successful mail.mtasts.apply (ADR-0109).
func (r *domainRepo) UpdateMTASTSAppliedID(ctx context.Context, id string, appliedID uint64) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("mta_sts_applied_id", appliedID)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePHPPoolID writes domains.php_pool_id (nil = unbind).
func (r *domainRepo) UpdatePHPPoolID(ctx context.Context, id string, poolID *string) error {
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Update("php_pool_id", poolID)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) UpdateGhostState(ctx context.Context, id, state string, checkedAt time.Time, detail *string) error {
	updates := map[string]any{
		"ghost_state":      state,
		"ghost_checked_at": checkedAt.UTC(),
		"ghost_detail":     detail, // GORM nil-pointer => SQL NULL
	}
	res := r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *domainRepo) ListForGhostCheck(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []models.Domain
	q := r.db.WithContext(ctx).
		Where("ghost_checked_at IS NULL OR ghost_checked_at < ?", staleBefore.UTC()).
		Order("ghost_checked_at IS NOT NULL, ghost_checked_at ASC").
		Limit(limit)
	if err := q.Find(&out).Error; err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// populateSSLStates enriches domains with their SSL certificate states
func (r *domainRepo) populateSSLStates(ctx context.Context, domains *[]models.Domain) error {
	if len(*domains) == 0 {
		return nil
	}

	// Collect domain IDs
	domainIDs := make([]string, len(*domains))
	for i, d := range *domains {
		domainIDs[i] = d.ID
	}

	// Query SSL certificates for these domains
	sslRepo := NewSSLCertificateRepository(r.db)
	certs, err := sslRepo.FindByDomainIDs(ctx, domainIDs)
	if err != nil {
		return err
	}

	// Map certs by domain_id
	certsByDomain := make(map[string]*models.SSLCertificate)
	for i := range certs {
		certsByDomain[certs[i].DomainID] = &certs[i]
	}

	// Compute SSL state for each domain
	for i := range *domains {
		(*domains)[i].SSLState = r.computeSSLState(&(*domains)[i], certsByDomain[(*domains)[i].ID])
	}

	return nil
}

// computeSSLState determines the SSL certificate state for a domain
func (r *domainRepo) computeSSLState(domain *models.Domain, cert *models.SSLCertificate) string {
	// None mode never serves TLS regardless of any lingering cert row (GH #246).
	if domain.SSLMode == models.SSLModeNone || !domain.SSLEnabled {
		return "off"
	}

	if cert == nil {
		return "pending"
	}

	// A revoked cert (e.g. left over after a mode switch) is NOT "pending".
	if cert.Status == models.SSLStatusRevoked {
		return "off"
	}

	if cert.Status == models.SSLStatusIssued {
		// Check if it's a Let's Encrypt certificate
		if cert.CertPath != nil && strings.HasPrefix(*cert.CertPath, "/etc/letsencrypt/") {
			return "active_le"
		}
		// Self-signed or other issued certs
		return "self_signed"
	}

	if cert.Status == models.SSLStatusFailed {
		return "failed"
	}

	// A DELIBERATE self-signed cert (Self mode, GH #246) is terminal — it is
	// NOT "pending/issuing". (The LE stop-gap fallback uses status
	// pending_acme_retry, handled below, so this never masks an in-flight LE.)
	if cert.Status == models.SSLStatusSelfSigned {
		return "self_signed"
	}

	// Pending, Issuing, PendingACMERetry
	return "pending"
}

func (r *domainRepo) ListForRegistrarRefresh(ctx context.Context, staleBefore time.Time, limit int) ([]models.Domain, error) {
	var rows []models.Domain
	err := r.db.WithContext(ctx).
		Where("registrar_checked_at IS NULL OR registrar_checked_at < ?", staleBefore).
		Order("registrar_checked_at IS NOT NULL, registrar_checked_at ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

func (r *domainRepo) SetRegistrarExpiry(ctx context.Context, id string, expiresAt *time.Time, checkedAt time.Time) error {
	return r.db.WithContext(ctx).Model(&models.Domain{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"registrar_expires_at": expiresAt,
			"registrar_checked_at": checkedAt,
		}).Error
}
