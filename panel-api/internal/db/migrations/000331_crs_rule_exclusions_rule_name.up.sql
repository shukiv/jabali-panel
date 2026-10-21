-- An operator exclusion can name a CrowdSec AppSec rule as well as a CRS rule
-- id: crowdsecurity/vpatch-CVE-2025-29927 does not fit in 16 characters.
-- appseccfg.MaxRuleIDLen is this width.
ALTER TABLE crs_rule_exclusions MODIFY rule_id varchar(128) NOT NULL;
