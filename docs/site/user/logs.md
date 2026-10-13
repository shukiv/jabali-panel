# Logs

`/jabali-panel/logs`. nginx access and error logs for your domains, plus the PHP-FPM error log for your account.

## Available logs

- **nginx access log** — one line per request: timestamp, IP, method, path, status, response size, user-agent, referer.
- **nginx error log** — server-side errors (PHP-FPM connection failures, denied request bodies, SSL handshake errors).
- **PHP-FPM error log** — uncaught PHP errors, fatal errors, warnings from your scripts.

## Filtering

- Pick a domain — defaults to "all domains".
- Pick a time range — default: last hour.
- Filter by status code (for the access log) or severity (for the error log).
- Free-text search across the log line.

## Live tail

The page supports live tail — open it and watch new lines appear as they are written. Useful while reproducing an intermittent problem on the live site.

## Per-line drill-in

Click a line in the error log to see:

- The full stack trace (if the error included one).
- The corresponding access-log line for the same request (matched on time + path).
- The PHP-FPM worker that processed the request and its current state.

## Retention

Logs default to 14 days. Configurable by the operator under Server Settings; your administrator can raise or lower it. Older log lines are pruned by a daily timer.

## What you cannot see

- Other tenants' logs.
- Server-wide logs (Stalwart, Kratos, MariaDB, panel-api, agent) — these are operator surfaces.

## When the log is empty

If you expect to see lines but the log is empty:

- Verify the time range; default is the last hour.
- Verify the domain selection; the default is "all domains" but a previous filter session may have narrowed it.
- A brand-new domain takes a few minutes after the first request before its log lines appear (reconciler delay between vhost creation and the first served request).

## Common error patterns

- **`504 Gateway Timeout`** — your PHP script took longer than `max_execution_time`. Raise the limit under [PHP Settings](./php-settings.md) or optimise the script.
- **`502 Bad Gateway`** — PHP-FPM is unable to accept connections; usually pool exhaustion. Reduce concurrency or ask the administrator to raise the pool's `pm.max_children`.
- **`413 Payload Too Large`** — the upload exceeds nginx's body limit. The administrator sets it for the whole server (512 MB by default), and **Domain options** can set a different maximum upload size for one domain. PHP has its own limits too: raise `post_max_size` and `upload_max_filesize` under [PHP Settings](./php-settings.md) to match.
