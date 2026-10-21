-- User-agent blocking in nginx (/etc/nginx/conf.d/00-jabali-bots.conf).
--
-- domains.block_ai_crawlers: the domain refuses AI crawlers and AI
-- assistants that identify themselves (the agent's curated list). Owner or
-- admin; off by default, which renders the vhost as before.
--
-- server_settings.blocked_user_agents: the admin's server-wide list, one
-- entry per line, matched as a case-insensitive substring of the User-Agent
-- header on every site. Empty by default. TEXT: the server_settings row is
-- at InnoDB's inline-size ceiling, so a new string column cannot be VARCHAR.

ALTER TABLE domains
    ADD COLUMN block_ai_crawlers TINYINT(1) NOT NULL DEFAULT 0;

ALTER TABLE server_settings
    ADD COLUMN blocked_user_agents TEXT NOT NULL DEFAULT '';
