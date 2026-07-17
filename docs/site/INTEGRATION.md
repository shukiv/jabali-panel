# Integration Guide — for the jabali-panel.com Docs Team

This directory (`/home/shuki/projects/jabali2/docs/site/`) is the **source-of-truth English markdown** for the public docs at https://jabali-panel.com/docs/. Use this guide to land the content on the site cleanly.

Audience: anyone maintaining the `jabali-panel.com` Astro site (`/home/shuki/projects/jabali-panel.com/`).

---

## 1. How the site stores docs

The site is **Astro** (static), one route per doc page under `src/pages/docs/`, content rendered server-side from HTML strings in `src/i18n/ui.ts`.

```
~/projects/jabali-panel.com/
├── src/
│   ├── pages/docs/
│   │   ├── index.astro          ← reads ui.ts: t('docsHtml.index')
│   │   ├── ssl.astro            ← reads ui.ts: t('docsHtml.ssl')
│   │   ├── platform/
│   │   │   ├── stack.astro
│   │   │   └── cli.astro
│   │   └── …
│   ├── i18n/ui.ts               ← 25k lines, 7 langs × ~3500 each
│   └── layouts/DocsLayout.astro ← sidebar + search shell
├── scripts/
│   ├── sync-docs.mjs            ← MD → HTML → ui.ts updater
│   └── generate-docs-search.mjs ← rebuilds search JSON
└── public/docs-search*.json     ← search index per lang
```

Key facts:

- **`ui.ts` is the source of truth for what renders**, not the `*.astro` file. The `.astro` file is a thin wrapper that calls `t('docsHtml.<key>')`.
- **One `docsHtml` block per language**, in this order: `en`, `he`, `ar`, `es`, `fr`, `de`, `pt`. The site falls back to `en` when a key is missing in another language.
- **HTML in `ui.ts` is wrapped in backtick template literals**, so backticks (`` ` ``) and `${` inside the HTML must be escaped (`\\\`` and `\\${`). `sync-docs.mjs` handles this automatically.

---

## 2. The two integration paths

Pick one per page. Use **A** as the default; use **B** for one-off pasting.

### Path A — extend the sync pipeline (recommended)

Single command updates every English `docsHtml.*` key from the markdown source.

1. **Make this directory the markdown source** (or symlink each file into the existing `~/projects/jabali/docs/` tree).

2. **Edit `MAPPINGS` in `~/projects/jabali-panel.com/scripts/sync-docs.mjs`** — add one entry per markdown file. Pattern:

   ```js
   { src: '/home/shuki/projects/jabali2/docs/site/ssl.md',          key: 'ssl' },
   { src: '/home/shuki/projects/jabali2/docs/site/dns.md',          key: 'dns' },
   { src: '/home/shuki/projects/jabali2/docs/site/mail.md',         key: 'email' },
   { src: '/home/shuki/projects/jabali2/docs/site/security.md',     key: 'security' },
   { src: '/home/shuki/projects/jabali2/docs/site/firewall.md',     key: 'firewall' },
   { src: '/home/shuki/projects/jabali2/docs/site/quickstart.md',   key: 'quickstart' },
   { src: '/home/shuki/projects/jabali2/docs/site/installation.md', key: 'installation' },
   { src: '/home/shuki/projects/jabali2/docs/site/admin.md',        key: 'admin' },
   { src: '/home/shuki/projects/jabali2/docs/site/user.md',         key: 'user' },
   { src: '/home/shuki/projects/jabali2/docs/site/domains.md',      key: 'domains' },
   { src: '/home/shuki/projects/jabali2/docs/site/wordpress.md',    key: 'wordpress' },
   { src: '/home/shuki/projects/jabali2/docs/site/applications.md', key: 'applications' },
   { src: '/home/shuki/projects/jabali2/docs/site/files.md',        key: 'files' },
   { src: '/home/shuki/projects/jabali2/docs/site/sftp.md',         key: 'sftp' },
   { src: '/home/shuki/projects/jabali2/docs/site/cron.md',         key: 'cron' },
   { src: '/home/shuki/projects/jabali2/docs/site/databases.md',    key: 'databases' },
   { src: '/home/shuki/projects/jabali2/docs/site/backups.md',      key: 'backups' },
   { src: '/home/shuki/projects/jabali2/docs/site/php.md',          key: 'php' },
   { src: '/home/shuki/projects/jabali2/docs/site/notifications.md',     key: 'notifications' },
   { src: '/home/shuki/projects/jabali2/docs/site/server-status.md',     key: 'serverStatus' },
   { src: '/home/shuki/projects/jabali2/docs/site/updates.md',           key: 'updates' },
   { src: '/home/shuki/projects/jabali2/docs/site/support.md',           key: 'support' },
   { src: '/home/shuki/projects/jabali2/docs/site/ip-manager.md',        key: 'ipManager' },
   { src: '/home/shuki/projects/jabali2/docs/site/migrations.md',        key: 'migrations' },
   { src: '/home/shuki/projects/jabali2/docs/site/operations.md',        key: 'operations' },
   { src: '/home/shuki/projects/jabali2/docs/site/troubleshooting.md',   key: 'troubleshooting' },
   { src: '/home/shuki/projects/jabali2/docs/site/resource-limits.md',   key: 'resourceLimits' },
   { src: '/home/shuki/projects/jabali2/docs/site/removed-features.md',  key: 'removedFeatures' },
   { src: '/home/shuki/projects/jabali2/docs/site/index.md',             key: 'index' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/stack.md',          key: 'platformStack' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/agent.md',          key: 'platformAgent' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/cli.md',            key: 'platformCli' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/dnssec.md',         key: 'platformDnssec' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/mail-autoconfig.md', key: 'platformMailAutoconfig' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/health-monitor.md', key: 'platformHealthMonitor' },
   { src: '/home/shuki/projects/jabali2/docs/site/platform/monitoring.md',     key: 'platformMonitoring' },
   ```

   **Remove old PHP-era mappings** that no longer apply:
   ```js
   // remove (or comment out):
   { src: '/home/shuki/projects/jabali/docs/cli-reference.md',     key: 'platformCli' },   // superseded by site/platform/cli.md
   { src: '/home/shuki/projects/jabali/docs/architecture.md',      key: 'platformStack' }, // superseded by site/platform/stack.md
   { src: '/home/shuki/projects/jabali/docs/architecture.md',      key: 'platformAgent' }, // superseded
   { src: '/home/shuki/projects/jabali/docs/diagnostic-logs.md',   key: 'support' },       // superseded
   { src: '/home/shuki/projects/jabali/docs/ssl.md',               key: 'ssl' },
   { src: '/home/shuki/projects/jabali/docs/security.md',          key: 'security' },
   { src: '/home/shuki/projects/jabali/docs/mail.md',              key: 'email' },
   { src: '/home/shuki/projects/jabali/docs/dns.md',               key: 'DnsMail' },       // dns.md and mail.md are now separate pages
   { src: '/home/shuki/projects/jabali/docs/backups.md',           key: 'backups' },
   { src: '/home/shuki/projects/jabali-security/docs/CLI.md',      key: 'firewall' },      // superseded by site/firewall.md
   ```

3. **Add the new keys to every language's `docsHtml` block in `ui.ts`** — `sync-docs.mjs` only updates *existing* keys (the regex is `\\s+${key}: \\\`…\\\``). Bootstrap each new key once with an empty value:

   ```ts
   docsHtml: {
     // existing keys …
     index: ``,
     quickstart: ``,
     installation: ``,
     admin: ``,
     user: ``,
     domains: ``,
     // … etc
     platformStack: ``,
     platformAgent: ``,
     platformCli: ``,
     platformDnssec: ``,
     platformMailAutoconfig: ``,
     platformHealthMonitor: ``,
     platformMonitoring: ``,
     ipManager: ``,
     resourceLimits: ``,
     serverStatus: ``,
     updates: ``,
     support: ``,
     notifications: ``,
     migrations: ``,
     operations: ``,
     troubleshooting: ``,
     removedFeatures: ``,
     files: ``,
     sftp: ``,
     cron: ``,
     databases: ``,
     applications: ``,
     wordpress: ``,
     php: ``,
     firewall: ``,
   },
   ```

   Repeat for `he`, `ar`, `es`, `fr`, `de`, `pt`. Easiest: copy the `en` block, paste over the others, then commit a "translate placeholder" todo (see §4).

4. **Run the sync**:

   ```bash
   cd ~/projects/jabali-panel.com
   node scripts/sync-docs.mjs
   ```

   Output looks like:
   ```
   Reading ui.ts...
   Processing: /home/shuki/projects/jabali2/docs/site/ssl.md → ssl
   Processing: /home/shuki/projects/jabali2/docs/site/dns.md → dns
   …
   Writing 26 changed keys to ui.ts (was 1,234,567 bytes, now 1,289,012 bytes)
   ```

   If you see `WARNING: Key "X" not found in ui.ts` — step 3 wasn't done for that key. Add the placeholder and re-run.

5. **Regenerate the search index**:

   ```bash
   npm run docs-search
   ```

   Touches `public/docs-search-en.json` etc.

6. **Add the `.astro` pages** (see §3).

### Path B — direct paste (for hotfixes)

When you need a one-off correction to a single page and don't want to round-trip through the markdown:

```bash
pandoc -f markdown -t html5 --no-highlight --wrap=none \
  ~/projects/jabali2/docs/site/<page>.md \
  | sed -E 's/<h1[^>]*>.*?<\/h1>//g' \
  | sed -E 's/`/\\`/g; s/\$\{/\\${/g'
```

Paste the output into the matching `docsHtml.<key>` literal in `ui.ts`. Strip the `<h1>` (the layout supplies the page title from a sibling `t(...)` call).

Don't make a habit of B; A is canonical, B drifts.

---

## 3. Astro page wiring

Every doc page is a thin `.astro` wrapper. Template:

```astro
---
// src/pages/docs/<key>.astro
import DocsLayout from '@layouts/DocsLayout.astro';
import { useTranslations } from '@i18n/ui';

const t = useTranslations('en');
---
<DocsLayout title={t('docs.<key>.title')} description={t('docs.<key>.description')}>
  <Fragment set:html={t('docsHtml.<key>')} />
</DocsLayout>
```

Localised mirror at `src/pages/[lang]/docs/<key>.astro` for `he`, `ar`, etc. — same template, but `useTranslations(Astro.params.lang)`.

### New pages to create

For each new key from §2 step 3 that doesn't already have an `.astro` file, create:

```
src/pages/docs/quickstart.astro
src/pages/docs/admin.astro
src/pages/docs/user.astro
src/pages/docs/applications.astro
src/pages/docs/notifications.astro
src/pages/docs/server-status.astro
src/pages/docs/updates.astro
src/pages/docs/ip-manager.astro
src/pages/docs/migrations.astro
src/pages/docs/operations.astro
src/pages/docs/resource-limits.astro
src/pages/docs/removed-features.astro
src/pages/docs/cron.astro             # may already exist under user/
src/pages/docs/sftp.astro
src/pages/docs/platform/agent.astro
src/pages/docs/platform/mail-autoconfig.astro
src/pages/docs/platform/health-monitor.astro
src/pages/docs/platform/monitoring.astro
```

And the matching `src/pages/[lang]/docs/...` mirrors for all 6 non-EN languages (6 × 18 = 108 files). Glob-copy with `cp -r src/pages/docs src/pages/he/docs` then `sed -i "s/useTranslations('en')/useTranslations('he')/"` per dir.

### Sidebar / navigation

Open `src/layouts/DocsLayout.astro` and add each new key to the sidebar tree under the right group. The current grouping mirrors `~/projects/jabali2/docs/site/admin.md` "Sidebar groups" section — use that as the canonical IA:

- **Overview** — index, quickstart, installation
- **Hosting** — admin, user, domains, applications, wordpress, ip-manager
- **Mail & DNS** — mail, dns, ssl, platform/mail-autoconfig, platform/dnssec
- **Files & Code** — files, sftp, cron, php
- **Data** — databases, backups
- **Security & Ops** — security, firewall, resource-limits, notifications, server-status, updates, support, troubleshooting, operations
- **Platform** — platform/stack, platform/agent, platform/cli, platform/health-monitor, platform/monitoring
- **Migration** — migrations, removed-features

---

## 4. Translation flow

The new pages land in EN first. For other languages:

1. After §2 sync, the non-EN `docsHtml.<new-key>` entries are empty literals. The site falls back to EN for unmatched keys (current behaviour in `useTranslations`).
2. Translate each new page in turn — DeepL / Google Translate the markdown, hand-tune for technical terms, then paste into the matching language block in `ui.ts`.
3. Re-run `npm run docs-search` after each language pass — the search index is per-language.

Glossary terms that **must not be translated** (keep English exact):

- `Agent`, `Reconciler`, `Privileged DB Admin Action`, `Cron Job`, `Cron Job Intake`, `SSO Token`, `Shadow Account`, `SSO Token Resolution` (CONTEXT.md domain language).
- Product names: `CrowdSec`, `AppSec`, `Stalwart`, `Bulwark`, `Kratos`, `Roundcube`, `PowerDNS`, `pdns-recursor`, `MariaDB`, `PostgreSQL`, `Snuffleupagus`, `AppArmor`, `AIDE`, `Tetragon`, `ClamAV`, `LMD`, `YARA`, `restic`.
- CLI verbs (everything starting with `jabali …`).
- File paths (`/etc/...`, `/var/...`, `/run/...`).
- Service / unit names (`jabali-panel.service`, `nginx`, etc.).

Translate prose, headings, button labels.

---

## 5. Screenshots

You said you'd handle screenshots. The markdown contains **no image references** on purpose — drop them in where you want, using:

```markdown
![Server Status dashboard](/docs-assets/server-status-dashboard.png)
```

Suggested asset path: `~/projects/jabali-panel.com/public/docs-assets/<page-slug>/<screenshot>.png`. The DocsLayout already serves `/public/*` at the root.

A few hot spots where a screenshot pays off:

| Page | Suggested shots |
|---|---|
| `quickstart.md` | The admin one-time-login URL appearing at install end; the first dashboard. |
| `admin.md` | Each top-level page (one per route). |
| `user.md` | The tenant dashboard, Mail tabs, Applications wizard. |
| `domains.md` | The Domain Edit page with PHP / SSL / DNSSEC / Listen IP toggles. |
| `mail.md` | Mailboxes tab, Forwarders tab, Disclaimer editor. |
| `security.md` | CrowdSec page, Malware page, Egress page, AppSec status card. |
| `server-status.md` | Full dashboard with at least one degraded service. |
| `notifications.md` | The bell dropdown, channel config form. |
| `wordpress.md` | The install wizard, the "Open Admin" button. |
| `applications.md` | The app picker grid. |
| `files.md` | The file manager with text editor open. |
| `backups.md` | Destinations list, restore wizard. |

For multi-step flows (WP install, backup restore, CrowdSec test-IP), an animated GIF or a numbered series of screenshots reads better than a single static shot.

---

## 6. Deployment

After §2 + §3:

```bash
cd ~/projects/jabali-panel.com
npm run build                      # generate search indexes, sitemap, then build static site
npm run preview                    # local preview at http://localhost:4321
# inspect, then:
npm run deploy                     # rsync dist/ to hostsclick:/var/www/jabali-panel.com/
```

`scripts/deploy.sh` also syncs the contact backend (`server/contact/`) and restarts `jabali-contact.service`. To skip:

```bash
npm run deploy -- --skip-contact
```

CSP is deployed from `security-headers.conf` to `/etc/nginx/snippets/security-headers.conf` and nginx reloaded. If you add a new third-party (analytics, CDN, etc.) edit `security-headers.conf` first.

---

## 7. Continuous update workflow

When jabali2 ships a feature that changes a doc:

1. Update the markdown in `/home/shuki/projects/jabali2/docs/site/<page>.md` (this is the canonical source).
2. Commit to the jabali2 repo (the markdown lives with the code, so the docs version with the panel).
3. In `~/projects/jabali-panel.com/`:
   ```bash
   node scripts/sync-docs.mjs        # picks up the new EN HTML
   npm run docs-search               # rebuild search
   git add -p && git commit -m "docs: sync from jabali2"
   npm run deploy
   ```
4. Translate the changed sections into the other 6 languages and repeat.

For breaking-change ADRs (e.g. another feature removal like M27 ModSec), update `removed-features.md` in jabali2 first, then sync.

---

## 8. Conventions the markdown follows (so your tooling can rely on them)

- **One H1 per file** — sync-docs.mjs strips it; the Astro layout supplies the page title from `t('docs.<key>.title')`.
- **No image references** — you add screenshots after.
- **No language-specific text** in the EN source (no "tradução" / "翻译" placeholders).
- **Code blocks are triple-backtick fenced** with a language hint where useful (`bash`, `ts`, `nginx`, `sql`).
- **Tables** use GFM pipe syntax — pandoc handles them.
- **Internal links** use relative `.md` paths (`[ssl.md](./ssl.md)`); on the site these need to be rewritten to `/docs/ssl/` (or the `[lang]/docs/ssl/` variant). Add a sync-time transform if it isn't already there — current sync-docs.mjs doesn't, so add:

  ```js
  // inside mdToHtml(), after the existing replacements:
  html = html.replace(/href="\.\/([\w-]+)\.md(?:#([\w-]+))?"/g, (_m, slug, hash) =>
    `href="/docs/${slug}/${hash ? '#' + hash : ''}"`);
  html = html.replace(/href="\.\.\/([\w-]+)\.md(?:#([\w-]+))?"/g, (_m, slug, hash) =>
    `href="/docs/${slug}/${hash ? '#' + hash : ''}"`);
  html = html.replace(/href="\.\/platform\/([\w-]+)\.md(?:#([\w-]+))?"/g, (_m, slug, hash) =>
    `href="/docs/platform/${slug}/${hash ? '#' + hash : ''}"`);
  ```

  (Test against `dns.md` which links to `platform/dnssec.md`.)
- **Domain terminology** matches `~/projects/jabali2/CONTEXT.md`. If a doc page introduces a new term, add it to CONTEXT.md in the jabali2 repo first.

---

## 9. Quick reference — file → key → URL

| Markdown source | `docsHtml.<key>` | URL |
|---|---|---|
| `site/index.md` | `index` | `/docs/` |
| `site/quickstart.md` | `quickstart` | `/docs/quickstart/` |
| `site/installation.md` | `installation` | `/docs/installation/` |
| `site/admin.md` | `admin` | `/docs/admin/` |
| `site/user.md` | `user` | `/docs/user/` |
| `site/domains.md` | `domains` | `/docs/domains/` |
| `site/ssl.md` | `ssl` | `/docs/ssl/` |
| `site/dns.md` | `dns` | `/docs/dns/` |
| `site/mail.md` | `email` | `/docs/email/` |
| `site/php.md` | `php` | `/docs/php/` |
| `site/wordpress.md` | `wordpress` | `/docs/wordpress/` |
| `site/applications.md` | `applications` | `/docs/applications/` |
| `site/files.md` | `files` | `/docs/files/` |
| `site/sftp.md` | `sftp` | `/docs/sftp/` |
| `site/cron.md` | `cron` | `/docs/cron/` |
| `site/databases.md` | `databases` | `/docs/databases/` |
| `site/backups.md` | `backups` | `/docs/backups/` |
| `site/security.md` | `security` | `/docs/security/` |
| `site/firewall.md` | `firewall` | `/docs/firewall/` |
| `site/resource-limits.md` | `resourceLimits` | `/docs/resource-limits/` |
| `site/notifications.md` | `notifications` | `/docs/notifications/` |
| `site/server-status.md` | `serverStatus` | `/docs/server-status/` |
| `site/updates.md` | `updates` | `/docs/updates/` |
| `site/support.md` | `support` | `/docs/support/` |
| `site/ip-manager.md` | `ipManager` | `/docs/ip-manager/` |
| `site/migrations.md` | `migrations` | `/docs/migrations/` |
| `site/operations.md` | `operations` | `/docs/operations/` |
| `site/troubleshooting.md` | `troubleshooting` | `/docs/troubleshooting/` |
| `site/removed-features.md` | `removedFeatures` | `/docs/removed-features/` |
| `site/platform/stack.md` | `platformStack` | `/docs/platform/stack/` |
| `site/platform/agent.md` | `platformAgent` | `/docs/platform/agent/` |
| `site/platform/cli.md` | `platformCli` | `/docs/platform/cli/` |
| `site/platform/dnssec.md` | `platformDnssec` | `/docs/platform/dnssec/` |
| `site/platform/mail-autoconfig.md` | `platformMailAutoconfig` | `/docs/platform/mail-autoconfig/` |
| `site/platform/health-monitor.md` | `platformHealthMonitor` | `/docs/platform/health-monitor/` |
| `site/platform/monitoring.md` | `platformMonitoring` | `/docs/platform/monitoring/` |

---

## 9b. Admin and user subpages

The current site exposes ~68 subpages under `/docs/admin/*` and `/docs/user/*`. The new docs set ships those subpages under `site/admin/` and `site/user/` so the same URL structure can be preserved.

### Naming convention

Keys follow camelCase with the leading prefix `admin` / `user`:

| Source file | `docsHtml.<key>` | URL |
|---|---|---|
| `site/admin/dashboard.md` | `adminDashboard` | `/docs/admin/dashboard/` |
| `site/admin/users.md` | `adminUsers` | `/docs/admin/users/` |
| `site/admin/users-create.md` | `adminUsersCreate` | `/docs/admin/users/create/` |
| `site/admin/users-edit.md` | `adminUsersEdit` | `/docs/admin/users/edit/` |
| `site/admin/hosting-packages.md` | `adminHostingPackages` | `/docs/admin/hosting-packages/` |
| `site/admin/hosting-packages-create.md` | `adminHostingPackagesCreate` | `/docs/admin/hosting-packages/create/` |
| `site/admin/hosting-packages-edit.md` | `adminHostingPackagesEdit` | `/docs/admin/hosting-packages/edit/` |
| `site/admin/domains.md` | `adminDomains` | `/docs/admin/domains/` |
| `site/admin/dns-zones.md` | `adminDnsZones` | `/docs/admin/dns-zones/` |
| `site/admin/ssl-manager.md` | `adminSslManager` | `/docs/admin/ssl-manager/` |
| `site/admin/panel-certificate.md` | `adminPanelCertificate` | `/docs/admin/panel-certificate/` |
| `site/admin/panel-hostname.md` | `adminPanelHostname` | `/docs/admin/panel-hostname/` |
| `site/admin/mail-deliverability.md` | `adminMailDeliverability` | `/docs/admin/mail-deliverability/` |
| `site/admin/mail-throttles.md` | `adminMailThrottles` | `/docs/admin/mail-throttles/` |
| `site/admin/email-logs.md` | `adminEmailLogs` | `/docs/admin/email-logs/` |
| `site/admin/email-queue.md` | `adminEmailQueue` | `/docs/admin/email-queue/` |
| `site/admin/php-manager.md` | `adminPhpManager` | `/docs/admin/php-manager/` |
| `site/admin/ip-addresses.md` | `adminIpAddresses` | `/docs/admin/ip-addresses/` |
| `site/admin/server-settings.md` | `adminServerSettings` | `/docs/admin/server-settings/` |
| `site/admin/server-status.md` | `adminServerStatus` | `/docs/admin/server-status/` |
| `site/admin/services.md` | `adminServices` | `/docs/admin/services/` |
| `site/admin/server-updates.md` | `adminServerUpdates` | `/docs/admin/server-updates/` |
| `site/admin/audit-log.md` | `adminAuditLog` | `/docs/admin/audit-log/` |
| `site/admin/automation-api.md` | `adminAutomationApi` | `/docs/admin/automation-api/` |
| `site/admin/applications.md` | `adminApplications` | `/docs/admin/applications/` |
| `site/admin/backups.md` | `adminBackups` | `/docs/admin/backups/` |
| `site/admin/backup-destinations.md` | `adminBackupDestinations` | `/docs/admin/backup-destinations/` |
| `site/admin/backup-schedules.md` | `adminBackupSchedules` | `/docs/admin/backup-schedules/` |
| `site/admin/backup-restore.md` | `adminBackupRestore` | `/docs/admin/backup-restore/` |
| `site/admin/backup-download.md` | `adminBackupDownload` | `/docs/admin/backup-download/` |
| `site/admin/migration.md` | `adminMigration` | `/docs/admin/migration/` |
| `site/admin/cpanel-migration.md` | `adminCpanelMigration` | `/docs/admin/cpanel-migration/` |
| `site/admin/directadmin-migration.md` | `adminDirectadminMigration` | `/docs/admin/directadmin-migration/` |
| `site/admin/hestiacp-migration.md` | `adminHestiacpMigration` | `/docs/admin/hestiacp-migration/` |
| `site/admin/whm-migration.md` | `adminWhmMigration` | `/docs/admin/whm-migration/` |
| `site/admin/security.md` | `adminSecurity` | `/docs/admin/security/` |
| `site/admin/crowdsec-decisions.md` | `adminCrowdsecDecisions` | `/docs/admin/crowdsec-decisions/` |
| `site/admin/crowdsec-allowlists.md` | `adminCrowdsecAllowlists` | `/docs/admin/crowdsec-allowlists/` |
| `site/admin/crowdsec-test-ip.md` | `adminCrowdsecTestIp` | `/docs/admin/crowdsec-test-ip/` |
| `site/admin/appsec.md` | `adminAppsec` | `/docs/admin/appsec/` |
| `site/admin/apparmor.md` | `adminApparmor` | `/docs/admin/apparmor/` |
| `site/admin/snuffleupagus.md` | `adminSnuffleupagus` | `/docs/admin/snuffleupagus/` |
| `site/admin/aide.md` | `adminAide` | `/docs/admin/aide/` |
| `site/admin/malware.md` | `adminMalware` | `/docs/admin/malware/` |
| `site/admin/ufw-baseline.md` | `adminUfwBaseline` | `/docs/admin/ufw-baseline/` |
| `site/admin/egress.md` | `adminEgress` | `/docs/admin/egress/` |
| `site/admin/database-tuning.md` | `adminDatabaseTuning` | `/docs/admin/database-tuning/` |
| `site/admin/notifications-channels.md` | `adminNotificationsChannels` | `/docs/admin/notifications/channels/` |
| `site/admin/notifications-events.md` | `adminNotificationsEvents` | `/docs/admin/notifications/events/` |
| `site/admin/notifications-routing.md` | `adminNotificationsRouting` | `/docs/admin/notifications/routing/` |
| `site/admin/notifications-test.md` | `adminNotificationsTest` | `/docs/admin/notifications/test/` |
| `site/admin/support.md` | `adminSupport` | `/docs/admin/support/` |
| `site/admin/terminal.md` | `adminTerminal` | `/docs/admin/terminal/` |
| `site/admin/login.md` | `adminLogin` | `/docs/admin/login/` |
| `site/admin/two-factor-challenge.md` | `adminTwoFactorChallenge` | `/docs/admin/two-factor-challenge/` |
| `site/admin/password-reset-request.md` | `adminPasswordResetRequest` | `/docs/admin/password-reset-request/` |
| `site/admin/password-reset-reset.md` | `adminPasswordResetReset` | `/docs/admin/password-reset-reset/` |
| `site/admin/home.md` | `adminHome` | `/docs/admin/home/` (alias) |

| Source file | `docsHtml.<key>` | URL |
|---|---|---|
| `site/user/index.md` | `userIndex` | `/docs/user/` |
| `site/user/dashboard.md` | `userDashboard` | `/docs/user/dashboard/` |
| `site/user/profile.md` | `userProfile` | `/docs/user/profile/` |
| `site/user/login.md` | `userLogin` | `/docs/user/login/` |
| `site/user/two-factor-challenge.md` | `userTwoFactorChallenge` | `/docs/user/two-factor-challenge/` |
| `site/user/password-reset-request.md` | `userPasswordResetRequest` | `/docs/user/password-reset-request/` |
| `site/user/password-reset-reset.md` | `userPasswordResetReset` | `/docs/user/password-reset-reset/` |
| `site/user/domains.md` | `userDomains` | `/docs/user/domains/` |
| `site/user/dns-records.md` | `userDnsRecords` | `/docs/user/dns-records/` |
| `site/user/dnssec.md` | `userDnssec` | `/docs/user/dnssec/` |
| `site/user/ssl.md` | `userSsl` | `/docs/user/ssl/` |
| `site/user/email.md` | `userEmail` | `/docs/user/email/` |
| `site/user/mailboxes.md` | `userMailboxes` | `/docs/user/mailboxes/` |
| `site/user/forwarders.md` | `userForwarders` | `/docs/user/forwarders/` |
| `site/user/autoresponders.md` | `userAutoresponders` | `/docs/user/autoresponders/` |
| `site/user/catch-all.md` | `userCatchAll` | `/docs/user/catch-all/` |
| `site/user/disclaimer.md` | `userDisclaimer` | `/docs/user/disclaimer/` |
| `site/user/shared-folders.md` | `userSharedFolders` | `/docs/user/shared-folders/` |
| `site/user/email-logs.md` | `userEmailLogs` | `/docs/user/email-logs/` |
| `site/user/databases.md` | `userDatabases` | `/docs/user/databases/` |
| `site/user/db-users.md` | `userDbUsers` | `/docs/user/db-users/` |
| `site/user/postgresql.md` | `userPostgresql` | `/docs/user/postgresql/` |
| `site/user/php-settings.md` | `userPhpSettings` | `/docs/user/php-settings/` |
| `site/user/files.md` | `userFiles` | `/docs/user/files/` |
| `site/user/ssh-keys.md` | `userSshKeys` | `/docs/user/ssh-keys/` |
| `site/user/cron-jobs.md` | `userCronJobs` | `/docs/user/cron-jobs/` |
| `site/user/applications.md` | `userApplications` | `/docs/user/applications/` |
| `site/user/wordpress.md` | `userWordpress` | `/docs/user/wordpress/` |
| `site/user/backups.md` | `userBackups` | `/docs/user/backups/` |
| `site/user/backup-download.md` | `userBackupDownload` | `/docs/user/backup-download/` |
| `site/user/logs.md` | `userLogs` | `/docs/user/logs/` |
| `site/user/activity.md` | `userActivity` | `/docs/user/activity/` |
| `site/user/cpanel-migration.md` | `userCpanelMigration` | `/docs/user/cpanel-migration/` |
| `site/user/directadmin-migration.md` | `userDirectadminMigration` | `/docs/user/directadmin-migration/` |
| `site/user/home.md` | `userHome` | `/docs/user/home/` (alias) |

### Removed subpages from the legacy site

Do **not** port these existing legacy `.astro` pages into the new build:

- `admin/geo-block-rules.astro`, `admin/geo-block-rules-create.astro`, `admin/geo-block-rules-edit.astro` — the model is replaced by [CrowdSec scenarios + allowlists](./admin/crowdsec-decisions.md) plus [AppSec](./admin/appsec.md).
- `admin/waf.astro` — superseded by [AppSec](./admin/appsec.md).
- `admin/webhook-endpoints*.astro` — admin webhooks are not currently shipped; use [Notifications](./admin/notifications-channels.md) sender channels instead.
- `user/cdn-integration.astro` — not shipped.
- `user/git-deployment.astro` — not shipped.
- `user/image-optimization.astro` — not shipped.
- `user/imap-sync.astro` — Stalwart handles IMAP natively; users move mail via standard IMAP clients (no panel-side sync UI).
- `user/mailing-lists.astro` — Mailman is not part of the panel.
- `user/protected-directories.astro` — not shipped (nginx `auth_basic` per-domain is roadmap).

Either delete the corresponding pages from the site or redirect them to the closest equivalent (`waf` → `appsec`, `geo-block-rules` → `crowdsec-decisions`, etc.).

### sync-docs.mjs MAPPINGS additions

Add one entry per subpage to the existing MAPPINGS array. Example pattern (truncated; replicate for every subpage):

```js
// Admin subpages
{ src: '/home/shuki/projects/jabali2/docs/site/admin/dashboard.md',          key: 'adminDashboard' },
{ src: '/home/shuki/projects/jabali2/docs/site/admin/users.md',              key: 'adminUsers' },
{ src: '/home/shuki/projects/jabali2/docs/site/admin/users-create.md',       key: 'adminUsersCreate' },
{ src: '/home/shuki/projects/jabali2/docs/site/admin/users-edit.md',         key: 'adminUsersEdit' },
{ src: '/home/shuki/projects/jabali2/docs/site/admin/hosting-packages.md',   key: 'adminHostingPackages' },
// … repeat for every row in the file→key tables above

// User subpages
{ src: '/home/shuki/projects/jabali2/docs/site/user/dashboard.md',           key: 'userDashboard' },
{ src: '/home/shuki/projects/jabali2/docs/site/user/profile.md',             key: 'userProfile' },
// … repeat for every row in the user table above
```

A copy-paste-ready full MAPPINGS list is in `site/MAPPINGS-snippet.txt` — drop straight into `sync-docs.mjs`.

### Placeholder bootstrap

`sync-docs.mjs` only overwrites *existing* keys (the regex requires the key to already exist in `ui.ts`). Before the first sync, add an empty literal for every new key in each `docsHtml` block. The placeholders must exist in **all 7 languages** (`en`, `he`, `ar`, `es`, `fr`, `de`, `pt`), even if the non-EN values stay empty initially — the site's `useTranslations` falls back to EN for any key whose target-language value is the empty string.

### Astro page wiring for the subpages

For every key, create a matching `.astro` file:

```
src/pages/docs/admin/dashboard.astro          → t('docsHtml.adminDashboard')
src/pages/docs/admin/users/index.astro        → t('docsHtml.adminUsers')
src/pages/docs/admin/users/create.astro       → t('docsHtml.adminUsersCreate')
src/pages/docs/admin/users/edit.astro         → t('docsHtml.adminUsersEdit')
…
src/pages/docs/user/dashboard.astro           → t('docsHtml.userDashboard')
src/pages/docs/user/mailboxes.astro           → t('docsHtml.userMailboxes')
…
```

And the localised mirrors at `src/pages/[lang]/docs/admin/...` and `src/pages/[lang]/docs/user/...`. For the 6 non-EN languages with 91 subpages each, generate with:

```bash
cd ~/projects/jabali-panel.com/src/pages
for lang in he ar es fr de pt; do
  mkdir -p "$lang/docs/admin/users" "$lang/docs/admin/hosting-packages" "$lang/docs/admin/notifications" "$lang/docs/user"
  cp -r docs/admin/*.astro "$lang/docs/admin/" 2>/dev/null
  cp -r docs/user/*.astro  "$lang/docs/user/" 2>/dev/null
  sed -i "s/useTranslations('en')/useTranslations('$lang')/" "$lang/docs/admin/"*.astro "$lang/docs/user/"*.astro
done
```

### Sidebar grouping for the subpages

In `src/layouts/DocsLayout.astro`, the sidebar should mirror the page-level IA from [admin.md](./admin.md) and [user.md](./user.md). Suggested structure:

**Admin sidebar group**

- Overview — `adminDashboard`, `adminAuditLog`, `adminServerStatus`
- Users + Packages — `adminUsers` (+create/edit), `adminHostingPackages` (+create/edit)
- Hosting — `adminDomains`, `adminDnsZones`, `adminSslManager`, `adminPanelCertificate`, `adminPanelHostname`, `adminIpAddresses`, `adminApplications`
- Mail — `adminMailDeliverability`, `adminMailThrottles`, `adminEmailLogs`, `adminEmailQueue`
- Database — `adminDatabaseTuning`
- PHP — `adminPhpManager`
- Security — `adminSecurity`, `adminCrowdsecDecisions`, `adminCrowdsecAllowlists`, `adminCrowdsecTestIp`, `adminAppsec`, `adminApparmor`, `adminSnuffleupagus`, `adminAide`, `adminMalware`, `adminUfwBaseline`, `adminEgress`
- System — `adminServerSettings`, `adminServices`, `adminServerUpdates`, `adminTerminal`
- Backups — `adminBackups`, `adminBackupDestinations`, `adminBackupSchedules`, `adminBackupRestore`, `adminBackupDownload`
- Notifications — `adminNotificationsChannels`, `adminNotificationsEvents`, `adminNotificationsRouting`, `adminNotificationsTest`
- Migration — `adminMigration`, `adminCpanelMigration`, `adminDirectadminMigration`, `adminHestiacpMigration`, `adminWhmMigration`
- Support — `adminAutomationApi`, `adminSupport`
- Auth — `adminLogin`, `adminTwoFactorChallenge`, `adminPasswordResetRequest`, `adminPasswordResetReset`

**User sidebar group**

- Getting started — `userIndex`, `userDashboard`, `userProfile`
- Auth — `userLogin`, `userTwoFactorChallenge`, `userPasswordResetRequest`, `userPasswordResetReset`
- Domains & DNS — `userDomains`, `userDnsRecords`, `userDnssec`, `userSsl`
- Mail — `userEmail`, `userMailboxes`, `userForwarders`, `userAutoresponders`, `userCatchAll`, `userDisclaimer`, `userSharedFolders`, `userEmailLogs`
- Data — `userDatabases`, `userDbUsers`, `userPostgresql`
- Files & Code — `userFiles`, `userSshKeys`, `userPhpSettings`, `userCronJobs`
- Apps — `userApplications`, `userWordpress`
- Operations — `userBackups`, `userBackupDownload`, `userLogs`, `userActivity`
- Migration — `userCpanelMigration`, `userDirectadminMigration`

The exact sidebar markup is up to your Astro layout conventions; the order above mirrors the natural mental flow.

---

## 10. Questions / coordination

The markdown source lives in the jabali2 repo and versions with the panel. If you spot:

- A factual error (wrong CLI verb, wrong route path, wrong service name) — open an issue against `jabali2`, label `docs`. The fix should land in this directory first, then sync.
- A typo / phrasing issue in EN — fix in this directory, sync.
- A missing translation — fix in `ui.ts` directly under the appropriate language block (no need to round-trip through markdown).
- An out-of-date screenshot — replace under `public/docs-assets/`; no markdown change needed.

For new features that need a brand-new doc page: contact the jabali2 maintainer (`shukivaknin@gmail.com`); the new page should be drafted here in `docs/site/` before you add the corresponding `docsHtml.<key>` literal.
