-- GH #454 Phase 7B: tenant multi-schedule backups, maintenance-window model.
--
-- hosting_packages.max_backup_schedules: how many backup schedules a tenant on
--   this plan may own. Default 1 = the existing single-schedule behaviour, so no
--   plan changes until an admin raises it. The tenant multi-schedule UI + the
--   window-governed scheduler path activate for a tenant only once this is > 1.
--
-- server_settings.tenant_backup_window_{start,end}: the admin-owned UTC
--   maintenance window (HH:MM) that tenant window-governed schedules fire inside.
--   The scheduler enqueues a due tenant schedule only when now is within the
--   window, and smears multiple due schedules across it so N tenants don't all
--   fire at window open. Default 02:00-05:00. tenant_backup_cron (000232) stays
--   for legacy single-schedule installs; the window supersedes it for schedules
--   created through the multi-schedule surface (backup_schedules.cadence != '').
--
-- backup_schedules.cadence: a tenant's chosen firing cadence for a
--   window-governed schedule (hourly / every_6h / every_12h / daily / weekly),
--   from a fixed allow-list mapped to an interval — NOT free cron, so a tenant
--   can never pick a stampede minute. Empty '' = a legacy cron-governed schedule
--   (admin fan-out, system, or the pre-7B single tenant schedule); those keep
--   using cron_expr + next_run_at unchanged. Non-empty routes the schedule
--   through the window-guarded dispatch path.
ALTER TABLE hosting_packages
  ADD COLUMN max_backup_schedules INT UNSIGNED NOT NULL DEFAULT 1;
ALTER TABLE server_settings
  ADD COLUMN tenant_backup_window_start VARCHAR(5) NOT NULL DEFAULT '02:00',
  ADD COLUMN tenant_backup_window_end   VARCHAR(5) NOT NULL DEFAULT '05:00';
ALTER TABLE backup_schedules
  ADD COLUMN cadence VARCHAR(16) NOT NULL DEFAULT '';
