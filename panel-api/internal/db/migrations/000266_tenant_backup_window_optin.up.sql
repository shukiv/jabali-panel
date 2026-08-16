-- GH #1097: the tenant backup maintenance window (GH #454 Phase 7B) silently
-- restricted EVERY tenant schedule to a few hours a day — an "Hourly" schedule
-- only ran inside the 02:00-05:00 window. Make the window an explicit OPT-IN
-- restriction, OFF by default: when tenant_backup_window_enforce = 0 the tenant
-- scheduler ignores the window and fires each schedule on its selected interval
-- (Hourly = every hour, Every 6h = every 6 hours, ...); when 1 it restricts
-- firings to the admin window as before. Defaulting to 0 makes both new AND
-- existing installs behave the way a tenant expects, without discarding the
-- window_start/end an admin may have set (they take effect again on enforce=1).
ALTER TABLE server_settings
  ADD COLUMN tenant_backup_window_enforce TINYINT(1) NOT NULL DEFAULT 0;

-- Existing window-governed schedules (cadence <> '') had their next_run_at
-- smeared into tomorrow's 02:00-05:00 window by the old gated model. With the
-- window now off by default they must fire on their chosen interval starting
-- now, not sit idle for up to ~24h waiting for the old window opening. Reset the
-- next run to now so the first firing after the upgrade is immediate; the
-- scheduler re-smears + retention-caps from there. Legacy cron schedules
-- (cadence = '') are untouched. Supersedes migration 000253's semantics, where
-- the window unconditionally gated these schedules.
UPDATE backup_schedules SET next_run_at = UTC_TIMESTAMP(6) WHERE cadence <> '';
