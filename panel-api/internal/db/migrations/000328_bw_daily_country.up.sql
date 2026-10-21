-- Per-domain daily web traffic by country, for the domain Statistics tab.
--
-- Written by the same daily bandwidth scan as bw_daily, from goaccess's
-- geolocation panel, when the GeoLite2 City database CrowdSec downloads is
-- on the host. country_code is ISO 3166-1 alpha-2; ZZ holds addresses with
-- no country plus the countries past the scan's per-day cap. requests_total
-- counts the requests goaccess calls valid, so a day's rows can sum to less
-- than that day's bw_daily.requests_total.
--
-- A re-scan of the same day replaces the day's rows. Pruned with bw_daily
-- (retention category "bandwidth").

CREATE TABLE bw_daily_country (
    domain_id      VARCHAR(26)     NOT NULL,
    day            DATE            NOT NULL,
    country_code   CHAR(2)         NOT NULL,
    bytes_total    BIGINT UNSIGNED NOT NULL DEFAULT 0,
    requests_total BIGINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at     DATETIME(6)     NOT NULL,
    PRIMARY KEY (domain_id, day, country_code),
    CONSTRAINT fk_bw_daily_country_domain
        FOREIGN KEY (domain_id) REFERENCES domains (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE INDEX idx_bw_daily_country_day ON bw_daily_country (day);
