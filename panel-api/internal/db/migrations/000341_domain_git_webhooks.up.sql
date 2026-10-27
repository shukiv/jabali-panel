-- GH #2277, ADR-0178: a domain's deploy-on-push webhook.
--
-- One row per domain, removed with the domain's Git settings (deleting them,
-- or a change of owner, deletes the domain_git_repos row). hook_id is the
-- random key in the webhook's public URL; it identifies the row and proves
-- nothing. secret_enc is the shared secret the Git host signs deliveries
-- with, sealed with the panel's sso key; no API response after the one that
-- generated it, log line or backup carries it.
--
-- queued_at: a push arrived while a deployment of the domain was running.
-- One more deployment starts once that run has ended; pushes meanwhile
-- collapse into it.
--
-- last_delivery_at and last_result: the last delivery that carried a valid
-- signature, and what the panel did with it (started, queued, ignored,
-- duplicate, or the reason it was refused).
CREATE TABLE domain_git_webhooks (
  domain_id        CHAR(26)        NOT NULL,
  hook_id          CHAR(32)        NOT NULL,
  secret_enc       VARBINARY(1024) NOT NULL,
  queued_at        DATETIME(6)     NULL,
  last_delivery_at DATETIME(6)     NULL,
  last_result      VARCHAR(64)     NOT NULL DEFAULT '',
  created_at       DATETIME(6)     NOT NULL,
  updated_at       DATETIME(6)     NOT NULL,
  PRIMARY KEY (domain_id),
  UNIQUE KEY uq_domain_git_webhooks_hook (hook_id),
  KEY idx_domain_git_webhooks_queued (queued_at),
  CONSTRAINT fk_domain_git_webhooks_repo FOREIGN KEY (domain_id) REFERENCES domain_git_repos (domain_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- source: what started a run, manual (the API) or webhook (a push).
ALTER TABLE domain_git_deployments
  ADD COLUMN source VARCHAR(16) NOT NULL DEFAULT 'manual';
