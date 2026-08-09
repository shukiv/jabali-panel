-- GH #873 (round 3): per-domain mail TRAFFIC history. One row per
-- (domain, metric, sample time). Unlike mail_stats_samples (server-wide
-- Stalwart counters), these are DELTAS — the count of events attributed to
-- a local domain in the 15-minute window ending at sampled_at, derived from
-- the delivery log by the agent's mail.stats_domain_sample verb. metric is
-- one of: sent, received, delivered, failed. Summed over a range at query
-- time. Pruned after 90 days by the mail stats ticker, same as the
-- server-wide samples.
CREATE TABLE mail_stats_domain_samples (
  domain     varchar(255) NOT NULL,
  metric     varchar(16)  NOT NULL,
  sampled_at datetime(6)  NOT NULL,
  value      bigint       NOT NULL,
  PRIMARY KEY (domain, metric, sampled_at),
  KEY idx_msds_sampled_at (sampled_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
