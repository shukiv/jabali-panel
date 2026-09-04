-- GH #1462: per-mail-domain CalDAV / CardDAV server override.
--
-- When set, the domain's _caldavs._tcp / _carddavs._tcp SRV records (RFC 6764
-- autodiscovery, used by Thunderbird 155+, Apple Mail) point clients at this
-- host instead of mail.<domain> — so calendars and contacts resolve to an
-- external server (e.g. Nextcloud) while email stays on Stalwart. Empty (the
-- default) keeps today's behaviour: DAV points at mail.<domain>.
--
-- Value is a hostname, optionally host:port (default port 443). Validated at
-- the API boundary before it reaches the SRV record content.
ALTER TABLE domains
  ADD COLUMN caldav_host VARCHAR(255) NOT NULL DEFAULT '',
  ADD COLUMN carddav_host VARCHAR(255) NOT NULL DEFAULT '';
