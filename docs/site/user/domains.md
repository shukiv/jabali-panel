# Domains (User)

`/jabali-panel/domains`. The domains hosted on the panel under your account.

## List

Columns: domain name, PHP version, SSL state, DNSSEC state, listen IP, last modified.

Filter by SSL state, DNSSEC state, or free-text on the name.

## Per-domain actions

- **Edit** — opens the domain edit page (PHP version, SSL, DNSSEC, redirects, aliases, mail toggle).
- **DNS records** — opens [DNS Records](./dns-records.md) for the domain.
- **Manage SSL** — opens [SSL](./ssl.md) scoped to the domain.

## Adding a domain

If your package allows it, **Add domain** opens a small form: domain name, PHP version, mail (on / off). On submit, the panel:

1. Creates the Domain row.
2. Creates the DNS zone with default records pointing to your account's primary IP.
3. Schedules the reconciler, which renders the nginx vhost and the FPM pool mapping within 60 seconds.

The domain count is checked against the package limit; if you are at the limit, the form refuses to submit and links you to your administrator's contact form.

## Removing a domain

Open **Edit** → **Delete**. Destructive: the vhost is torn down, the certificate is revoked, the DNS zone is removed, and any mailboxes in the domain are deleted. Asks twice.

## Subdomains

Subdomains are first-class domains in the panel. Add a subdomain by creating a new domain with the subdomain name (e.g. `blog.example.com`). The DNS zone for `blog.example.com` is created independently from `example.com` — you may need to delegate via `NS` records if both zones are hosted here.

## What you can change vs. what the admin controls

You may change PHP version (from the subset your package allows), SSL on/off, DNSSEC on/off, redirects, aliases, mail enable/disable.

The admin controls listen IP (selected from the [IP Manager](../admin/ip-addresses.md) pool), maximum domain count, and quota-related suspension. The admin may also pin SSL on or force a specific PHP version on a domain; in that case the relevant control is read-only on your side.

## Nginx options and rewrite rules

Two extra per-domain tabs — **Domain options** and **Rewrite rules** — appear only when your administrator has enabled *tenant domain options* for the server. Until then the tabs are hidden, and the domain's Overview shows a short note that your administrator can turn these controls on.

When they are enabled you can, on your own domains:

- **Domain options** — set a curated, safe set of nginx options: maximum upload size, HSTS, the common security headers (`X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`), and gzip. You supply values; each option renders to a fixed, vetted directive — never raw config.
- **Rewrite rules** — add two kinds of structured rule. A `rewrite` rule whose target must be a local path (no scheme or host, so it can never become an open redirect or a proxy to another service), and a `custom_header` rule that adds one response header. Every rule is validated before it is applied.

Raw nginx directives, reverse-proxy targets, IP access rules, and PHP settings stay admin-only whether or not tenant domain options are enabled. Your administrator turns the feature on under [Server Settings → General](../admin/server-settings.md#general).

## CLI

If you have SSH access to the panel host (operators only — tenants do not have shell access), the same operations are available via `jabali domain list / create / enable / disable / delete`.
