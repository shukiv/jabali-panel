-- GH #840: daily digest email. The ticker records the last UTC day it
-- sent for (YYYY-MM-DD) so a panel restart inside the send window can't
-- double-send. Empty = never sent.
ALTER TABLE server_settings
  ADD COLUMN digest_last_sent_date varchar(10) NOT NULL DEFAULT '';
