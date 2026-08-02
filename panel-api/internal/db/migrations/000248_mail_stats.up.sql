-- GH #873: mail statistics history. One row per (metric, sample time),
-- fed every 15 minutes from Stalwart's Prometheus exporter by the mail
-- stats ticker. Counters are stored cumulative (deltas are computed at
-- query time, clamped >= 0 across Stalwart restarts); gauges are stored
-- as-is. Pruned after 90 days by the same ticker.
CREATE TABLE mail_stats_samples (
  metric     varchar(64) NOT NULL,
  sampled_at datetime(6) NOT NULL,
  value      double      NOT NULL,
  PRIMARY KEY (metric, sampled_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
