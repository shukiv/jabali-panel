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

## Verifying a new domain

A domain you add stays **Unverified** until you prove that you control the name (GH #1816). This stops anyone from claiming a name that belongs to someone else. Until the domain is verified:

- its DNS zone is not published and no mail is accepted for it;
- it has no trusted certificate;
- visitors who reach the name get no response;
- if an administrator sends a verified domain back to Unverified, its mailboxes cannot sign in until it is verified again. Their mail is kept.

To build the site while you wait, turn on **Preview URL** on the domain's Overview tab. The site then works through its preview address, even though its real name is not published yet. You can also edit its DNS records in the panel; they are published once the domain is verified.

To verify the domain:

1. Open the domain. The banner at the top shows a TXT record: a **Name** (`_jabali-challenge.<your domain>`) and a **Value** (`jabali-verify=…`).
2. Add that TXT record at the DNS provider your domain uses **today** (usually your registrar). Records added in this panel do not count while the domain is unverified.
3. Click **Verify now**, or wait: the panel checks on its own, every minute at first and less often later. The domain goes live as soon as two public DNS resolvers return the value. You can remove the record after that.

The banner shows what the last check found. If it says your nameservers already point to this server, a DNS record cannot prove the name; ask your administrator to approve it.

A domain that is never verified is removed from your account after **14 days**, with a notice 4 days before. Its site files are kept. A subdomain of a domain you already verified (for example `blog.example.com` under a verified `example.com`) is verified at once and needs no record.

## Removing a domain

Open **Edit** → **Delete**. Destructive: the vhost is torn down, the certificate is revoked, the DNS zone is removed, and any mailboxes in the domain are deleted. Asks twice.

## Subdomains

Subdomains are first-class domains in the panel. Add a subdomain by creating a new domain with the subdomain name (e.g. `blog.example.com`). The DNS zone for `blog.example.com` is created independently from `example.com` — you may need to delegate via `NS` records if both zones are hosted here.

By default only you (and the administrator) can add a subdomain of a domain you own. If another account on the server needs one — for example a client who runs `shop.example.com` from their own account — open the domain, and on its **Overview** tab turn on **Allow subdomains by other accounts** (GH #1812). Any account on the server can then add subdomains of that domain without asking you. Turning it off stops new ones; subdomains already created by other accounts stay. Each parent domain needs its own consent: allowing it on `b.example.com` does not let another account create `c.b.example.com` if `example.com` belongs to a third account that has not allowed it.

## What you can change vs. what the admin controls

You may change PHP version (from the subset your package allows), SSL on/off, DNSSEC on/off, redirects, aliases, mail enable/disable.

The admin controls listen IP (selected from the [IP Manager](../admin/ip-addresses.md) pool), maximum domain count, and quota-related suspension. The admin may also pin SSL on or force a specific PHP version on a domain; in that case the relevant control is read-only on your side.

## Nginx options and rewrite rules

Three extra per-domain tabs — **Domain options**, **Rewrite rules**, and **Advanced directives** — appear only when your administrator has enabled *tenant domain options* for the server. Until then the tabs are hidden, and the domain's Overview shows a short note that your administrator can turn these controls on.

When they are enabled you can, on your own domains:

- **Domain options** — set a curated, safe set of nginx options: maximum upload size, HSTS, the common security headers (`X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`), and gzip. You supply values; each option renders to a fixed, vetted directive — never raw config.
- **Rewrite rules** — add structured rules that the panel turns into vetted nginx directives; you never write raw config. The kinds available to you are:
    - `rewrite` — its target must be a local path (no scheme or host, so it can never become an open redirect or a proxy to another service).
    - `custom_header` — adds one response header. The headers the panel manages for security — `Strict-Transport-Security`, `X-Frame-Options`, `X-Content-Type-Options`, and `Referrer-Policy` — can't be set here; use **Domain options** for HSTS and the security headers instead.
    - **Deny paths** — block all access to files by extension (for example hide `.env`, `.sql`, `.bak`, `.log`). You supply a list of bare extensions and the panel builds the matching rule; PHP files are handled by the server and can't be blocked this way.
    - **Static cache** — set a long cache lifetime for files by extension (for example `pdf`, `mp4`). Common web assets (css, js, images, fonts) are already cached by the server, so those are declined; and PHP-family extensions are refused so a source file is never served as a static download.
    - **Front controller** — for an app with its own router. It sets where a request goes when it matches no file or folder: a PHP script (by default `/index.php`) and the query string passed to it (by default `$query_string`). The panel writes it as `try_files $uri $uri/ <script>?<query>;` in the site's `location /`. Files and folders that exist (CSS, JavaScript, images) are still served as they are, and PHP keeps running the way the panel sets it up. For example, the script `/index.php` with the query `mod=$uri&$args` sends `/users/edit/123` to `/index.php?mod=/users/edit/123`. The script must be a `.php` path from the site's root folder; the query can use letters, digits, `_ . - = & / %` and the variables `$uri`, `$args`, `$query_string`, `$request_uri`, `$document_uri` and `$is_args`. A domain has one front controller, and it applies only when the domain runs PHP.

    Every rule is validated before it is applied. **Deny paths** and **Static cache** don't apply to a domain that is served entirely by a reverse proxy — on those, the proxy handles every request before these file rules are reached.

    If you are migrating from another host, **Import from an nginx config** lets you paste a raw nginx snippet and converts the parts that map to these rules (rewrites, response headers, extension `deny` blocks, extension `expires` caches, and a `location / { try_files $uri $uri/ /index.php?...; }` block, which becomes a **Front controller**) into typed rules you can review before saving. An imported front controller replaces the one the domain already has. Anything else that routes, proxies, or reads files (`proxy_pass`, `root`, `alias`, `return`, other prefix `location` blocks) is listed as not-imported rather than applied — those stay admin-only.
- **Advanced directives** — a small raw-directive box for response tuning only. Just three directives are accepted — `add_header`, `expires`, and `etag` — one statement per line, no `{ }` blocks and no backslashes. Headers the panel manages for you (HSTS, `X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`, `Content-Length`, `Transfer-Encoding`) are rejected so you can't accidentally weaken them. If a line is refused you see exactly which one. Anything that routes, reads files, or proxies is not accepted here — those stay admin-only.

Under **Rewrite rules**, if your administrator has added their own raw nginx directives to your domain, they are shown to you read-only as *Administrator-managed directives* — so nothing that shapes your site's config is hidden from you, even though only an administrator can change it.

Reverse-proxy targets (`proxy_pass`), file paths (`root` / `alias`), `location` blocks, IP access rules, and PHP settings stay admin-only whether or not tenant domain options are enabled. Your administrator turns the feature on under [Server Settings → General](../admin/server-settings.md#general).

## CLI

If you have SSH access to the panel host (operators only — tenants do not have shell access), the same operations are available via `jabali domain list / create / enable / disable / delete`.
