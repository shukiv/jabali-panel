-- GH #1798 rollback.
ALTER TABLE hosting_packages
    DROP COLUMN egress_icmp,
    DROP COLUMN egress_ssh_out_cidrs,
    DROP COLUMN egress_ssh_out;
