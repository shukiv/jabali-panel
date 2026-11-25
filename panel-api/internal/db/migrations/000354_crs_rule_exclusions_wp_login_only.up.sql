-- Operator CRS exclusions can be limited to WordPress editor saves: when
-- wp_login_only is set, the rule is dropped only for POSTs that carry a
-- wordpress_logged_in_ cookie, on the entry's host and path prefix. Existing
-- rows keep applying to every request, as before.
ALTER TABLE crs_rule_exclusions
  ADD COLUMN wp_login_only TINYINT(1) NOT NULL DEFAULT 0;
