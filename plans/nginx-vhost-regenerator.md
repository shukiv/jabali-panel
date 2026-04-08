# Nginx Vhost Regeneration System

## Problem

Vhosts are written once and never updated. Template fixes (e.g., phpMyAdmin config bug) require a full reinstall because `jabali update` doesn't regenerate nginx configs.

## Solution

Extract vhost templates into reusable agent functions, add custom nginx directives support for the panel vhost, and enable regeneration via agent commands + CLI + UI.

---

## Phase 1: Core Infrastructure (P0)

### 1. Database Migration

- New `DnsSetting` key: `panel_nginx_directives` (nullable text)
- No schema migration needed — `dns_settings` is key-value

### 2. Agent Functions (`bin/jabali-agent`)

**`generatePanelVhost()`** — builds panel nginx config from template
- Input: hostname, panel port, PHP socket, custom directives, http2 settings
- Output: complete nginx config string
- Extracted from install.sh `configure_nginx()` (~lines 1767-1851)

**`getNginxHttp2Settings()`** — detect nginx version once, cache
- Returns: `['listen_suffix' => ' http2'|'', 'directive' => 'http2 on;'|'']`

**`determineDomainSslPaths(string $domain)`** — parse existing vhost
- Regex extract `ssl_certificate` and `ssl_certificate_key` paths
- Fallback to snakeoil certs if not found

### 3. Agent RPC Commands

**`nginx.regenerate_panel_vhost`**
1. Read hostname from `.env` / system hostname
2. Read `panel_nginx_directives` from params (passed by Laravel)
3. Call `generatePanelVhost()`
4. Backup old config to `{hostname}.bak.{timestamp}`
5. Write new config
6. `nginx -t` → if fail, restore backup, return error
7. `systemctl reload nginx`

**`nginx.regenerate_domain_vhosts`**
- Input: optional `domain` filter
1. Query all domains (or single domain)
2. For each: extract SSL paths from existing config, generate new vhost, inject SSL paths + custom_nginx_directives
3. Backup each old config before overwrite
4. `nginx -t` → if fail, restore all backups
5. `systemctl reload nginx`
6. Return: count regenerated + errors

### 4. Artisan Command

```
php artisan jabali:nginx-regenerate [--domain=example.com] [--panel-only]
```

- Calls agent RPC commands
- Displays progress and errors
- Exit code 0/1

---

## Phase 2: User Interface (P1)

### Server Settings — Nginx tab or section in General

- **Textarea**: Panel custom nginx directives
  - Validated by `NginxDirectiveValidator::validateForAdmin()` before saving
  - Saved to `DnsSetting::set('panel_nginx_directives', $value)`
  - Helper text: "Injected into the panel server block. Dangerous directives are blocked."

- **Action button**: "Regenerate Panel Vhost"
  - Requires confirmation ("This will reload nginx")
  - Calls agent `nginx.regenerate_panel_vhost`
  - Shows success/error notification

- **Action button**: "Regenerate All Domain Vhosts"
  - Requires confirmation
  - Calls agent `nginx.regenerate_domain_vhosts`
  - Shows count + errors

---

## Phase 3: Integration & Testing (P2)

### jabali update integration

In `upgrade_infra()` in install.sh, after other infra updates:
```bash
php artisan jabali:nginx-regenerate --panel-only 2>&1 || warn "Panel vhost regeneration failed"
```

Non-blocking — logs warning on failure, doesn't abort update.

### Safety guarantees

- **Backup**: Every config backed up before overwrite (`*.bak.{timestamp}`)
- **Validate**: `nginx -t` before reload, always
- **Rollback**: Restore backup if `nginx -t` fails
- **Directive validation**: Blocked: `load_module`, `lua_`, `perl_`, `ssl_certificate`, `ssl_certificate_key`
- **SSL preservation**: Parse old vhost → extract cert paths → inject into new config

### Tests

| Test | File | Coverage |
|------|------|----------|
| Panel vhost generation | `tests/Unit/NginxPanelVhostTest.php` | Template, placeholders, http2 |
| SSL path extraction | `tests/Unit/NginxSslPathParserTest.php` | Parse existing configs |
| Directive validation | Already covered by `NginxDirectiveValidatorTest` | Reuse |
| Artisan command | `tests/Feature/NginxRegenerateCommandTest.php` | CLI flow |
| Server Settings UI | `tests/Feature/ServerSettingsNginxTest.php` | Form + save |

---

## File Changes

| File | Action | LOC | Risk |
|------|--------|-----|------|
| `bin/jabali-agent` | Add 4 functions + 2 RPC routes | ~400 | High |
| `app/Console/Commands/RegenerateNginxVhosts.php` | New | ~80 | Low |
| `app/Filament/Admin/Pages/ServerSettings.php` | Add nginx section | ~60 | Medium |
| `install.sh` | Add regeneration to upgrade_infra | ~5 | Low |
| Tests (3 files) | New | ~400 | Low |

## Implementation Order

1. Agent functions (generatePanelVhost, helpers, RPC commands)
2. Artisan command (jabali:nginx-regenerate)
3. Test on server manually
4. Filament UI (textarea + buttons)
5. install.sh integration (upgrade_infra)
6. Tests
