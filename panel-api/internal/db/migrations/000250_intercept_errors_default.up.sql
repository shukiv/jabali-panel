-- GH #879 follow-up: server-wide default for the branded application-error
-- page. Per-domain nginx_safe_options.intercept_errors becomes a tri-state
-- override (absent = inherit this default).
ALTER TABLE server_settings
  ADD COLUMN intercept_app_errors_default tinyint(1) NOT NULL DEFAULT 0;
