-- Rows naming an AppSec rule do not fit the old width, and the old binary
-- cannot render them. Drop them before narrowing the column.
DELETE FROM crs_rule_exclusions WHERE CHAR_LENGTH(rule_id) > 16;
ALTER TABLE crs_rule_exclusions MODIFY rule_id varchar(16) NOT NULL;
