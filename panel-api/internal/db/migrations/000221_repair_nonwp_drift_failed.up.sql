-- GH #378: the WordPress drift probe ran a wp-includes/version.php existence
-- check against every ready install regardless of app_type, so non-WordPress
-- apps (itflow, dokuwiki, …) — which have no such file — were flipped to
-- 'failed' with the drift message. The reconciler no longer probes non-WordPress
-- apps; repair the rows it already mislabeled, matched by that exact message so
-- genuinely-failed installs are left untouched.
UPDATE application_installs
SET status = 'ready', last_error = ''
WHERE app_type <> 'wordpress'
  AND status = 'failed'
  AND last_error = 'wp-includes/version.php missing — install drifted';
