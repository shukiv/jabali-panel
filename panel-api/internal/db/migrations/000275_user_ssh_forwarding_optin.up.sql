-- GH #1229: per-user opt-in for SSH TCP forwarding (VS Code Remote-SSH).
-- Default 0 keeps the JAB-352 forwarding lockdown for everyone; an admin opts a
-- user in, and the SSH reconciler converges jabali-ssh-forward group membership
-- from this flag so it is durable across user reprovision (the v1 group-only
-- opt-in reverted to OFF on reprovision). TINYINT on users — no row-ceiling
-- concern (that ceiling is server_settings' VARCHAR width, not this table).
ALTER TABLE users
    ADD COLUMN ssh_forwarding_enabled TINYINT(1) NOT NULL DEFAULT 0;
