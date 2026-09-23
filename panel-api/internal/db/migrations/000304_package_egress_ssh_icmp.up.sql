-- GH #1798: per-package egress allowances for the M34 per-user firewall.
-- Both flags default 0 (DENY) so existing packages are unchanged — outbound
-- SSH and ICMP stay blocked unless an admin opts a package in. NULL-package
-- (admin) accounts get neither (the reconciler LEFT JOIN COALESCEs to 0).
--
-- egress_ssh_out       — when 1, users on this package whose egress is
--                        enforced/learning also get outbound TCP :22 allowed.
-- egress_ssh_out_cidrs — JSON array of CIDRs to scope the :22 allowance to
--                        (e.g. GitHub ranges). Empty '' means anywhere
--                        (["0.0.0.0/0","::/0"]) — the reconciler's fallback.
-- egress_icmp          — when 1, those users may send ICMP echo-request
--                        (ping) out over IPv4 + IPv6.
ALTER TABLE hosting_packages
    ADD COLUMN egress_ssh_out       tinyint(1)     NOT NULL DEFAULT 0,
    ADD COLUMN egress_ssh_out_cidrs varchar(1000)  NOT NULL DEFAULT '',
    ADD COLUMN egress_icmp          tinyint(1)     NOT NULL DEFAULT 0;
