-- Keep the shared FastCGI page cache's entries for 24 hours instead of 60
-- minutes (inactive= in fastcgi_cache_path). With 60 minutes, any page not
-- requested for an hour was evicted, so low-traffic sites served a cold,
-- full PHP render to most visitors. An expired entry is still served stale
-- while it refreshes, and WordPress purges a page when it is edited, so a
-- longer window does not serve outdated content. max_size still bounds disk.
-- Boxes still on the old default move to the new one; a value an admin
-- changed is kept. The panel re-applies the value to nginx at startup.
ALTER TABLE server_settings ALTER COLUMN nginx_cache_inactive_min SET DEFAULT 1440;
UPDATE server_settings SET nginx_cache_inactive_min = 1440 WHERE nginx_cache_inactive_min = 60;
