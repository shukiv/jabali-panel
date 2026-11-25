-- GH #2591: per-domain 301 from www.<domain> to <domain> (SEO canonical host).
-- Off by default: a domain keeps serving both names until its owner turns it on.
ALTER TABLE domains
  ADD COLUMN redirect_www TINYINT(1) NOT NULL DEFAULT 0;
