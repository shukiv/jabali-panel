-- GH #2262: the site whose script a PHP Defense rule stopped, filled only
-- when the account that logged the line (the journal's _UID) owns that
-- domain on the panel. Older incidents stay NULL.
ALTER TABLE snuffleupagus_incidents
  ADD COLUMN domain VARCHAR(253) NULL AFTER domain_id;
