-- GH #2262: the site whose script a PHP Defense rule stopped. The agent
-- fills it only when the journal's _UID owns that /home/<user>; older
-- incidents stay NULL.
ALTER TABLE snuffleupagus_incidents
  ADD COLUMN domain VARCHAR(253) NULL AFTER domain_id;
