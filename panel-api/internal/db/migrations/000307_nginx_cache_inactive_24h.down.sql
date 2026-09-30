UPDATE server_settings SET nginx_cache_inactive_min = 60 WHERE nginx_cache_inactive_min = 1440;
ALTER TABLE server_settings ALTER COLUMN nginx_cache_inactive_min SET DEFAULT 60;
