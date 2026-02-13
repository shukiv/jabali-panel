# Jabali Web Hosting Panel

A modern web hosting control panel built with Laravel 12, Filament v5, and Livewire 4.

## Installation Requirements

**Critical for mail server functionality:**
- Fresh Debian 12/13 installation (no existing web/mail software)
- Domain with glue records pointing to server IP (`ns1.domain.com` → IP)
- PTR record (reverse DNS) pointing to mail hostname (`IP` → `mail.domain.com`)
- Port 25 open (check with VPS provider)

See README.md "Prerequisites" section for detailed DNS setup instructions.

### Subdomain Installation

The panel can be installed on a subdomain (e.g., `panel.example.com`). The installer automatically:

- Extracts the root domain (`example.com`) for DNS zone creation
- Sets nameservers as `ns1.example.com` and `ns2.example.com`
- Creates an A record for the subdomain (`panel` → server IP)
- Configures email at the root domain (`webmaster@example.com`)

**Example:** Installing on `panel.example.com`
```
DNS Zone: example.com
NS Records: ns1.example.com, ns2.example.com
A Records: @, www, panel, mail, ns1, ns2 → server IP
Email: webmaster@example.com
```

## Database

The panel uses **SQLite** by default (not MySQL). The database file is located at:
```
/var/www/jabali/database/database.sqlite
```

## Quick Reference

```bash
# IMPORTANT: All artisan commands must be run from /var/www/jabali/
cd /var/www/jabali

# Development
composer dev              # Start all dev servers (artisan, queue, pail, vite)
composer test             # Run PHPUnit tests
./vendor/bin/pint         # Format PHP code
php artisan serve         # Web server only
php artisan tinker        # Interactive REPL

# Production
php artisan migrate       # Run migrations
php artisan config:cache  # Cache configuration
php artisan route:cache   # Cache routes
```

## Git Workflow

**Important:** Only push to git when explicitly requested by the user. Do not auto-push after commits.
**Important:** Push to GitHub from the test server `root@192.168.100.50` (where the GitHub deploy key is configured).

### Version Numbers

**IMPORTANT:** Before every push, bump the `VERSION` in the `VERSION` file:

```bash
# VERSION file format:
VERSION=0.9-rc

# Bump before pushing:
# 0.9-rc → 0.9-rc1 → 0.9-rc2 → 0.9-rc3 → ...
```

| Field | When to Bump | Format |
|-------|--------------|--------|
| `VERSION` | Every push | `0.9-rc`, `0.9-rc1`, `0.9-rc2`, ... |

## Project Structure

```
/var/www/jabali/
├── app/
│   ├── Filament/
│   │   ├── Admin/        # Admin panel (route: /admin)
│   │   │   ├── Pages/    # Admin pages (Dashboard, Services, etc.)
│   │   │   ├── Resources/# Admin resources (Users)
│   │   │   └── Widgets/  # Admin widgets (Stats, Disk, Network)
│   │   └── Jabali/       # User panel (route: /panel)
│   │       ├── Pages/    # User pages (Domains, Email, WordPress, etc.)
│   │       └── Widgets/  # User widgets (Stats, Disk, Domains)
│   ├── Models/           # Eloquent models (22 models)
│   ├── Services/         # Business logic services
│   └── Console/Commands/ # Artisan commands
├── bin/
│   ├── jabali-agent      # Privileged operations daemon (runs as root)
│   └── screenshot        # Chromium screenshot capture script
├── config/                    # Laravel config files
│   └── languages.php          # Supported languages configuration
├── database/migrations/       # Database migrations
├── lang/                      # Translation files (JSON)
│   ├── en.json                # English (base)
│   ├── es.json                # Spanish
│   ├── fr.json                # French
│   ├── ru.json                # Russian
│   ├── pt.json                # Portuguese
│   ├── ar.json                # Arabic (RTL)
│   └── he.json                # Hebrew (RTL)
└── resources/views/filament/  # Blade templates for Filament pages
```

## Architecture

### Two Panels
- **Admin Panel** (`/admin`): Server-wide management, user administration
- **User Panel** (`/panel`): Per-user domain, email, database management

### Privileged Agent
The `bin/jabali-agent` daemon runs as root and handles operations requiring elevated privileges:
- System user creation/deletion
- Domain/vhost configuration
- Email (Postfix/Dovecot) management
- SSL certificate operations
- Database operations
- Backup operations

Communication via Unix socket at `/var/run/jabali/agent.sock`.

### Key Services
- **AgentClient**: PHP client for communicating with jabali-agent
- **AdminNotificationService**: System notifications (SSL, backups, quotas)

### Self-Healing Services
The `bin/jabali-health-monitor` daemon automatically monitors and restarts critical services when they fail.

**Monitored Services:**
- nginx, mariadb, jabali-agent, php-fpm
- postfix, dovecot, named (if installed)
- redis-server, fail2ban (if installed)

**Features:**
- Checks services every 30 seconds
- Automatic restart on failure (up to 3 attempts)
- Email notifications via `AdminNotificationService`
- Systemd restart policies as backup protection

**Files:**
| File | Purpose |
|------|---------|
| `bin/jabali-health-monitor` | Health monitoring daemon |
| `/etc/systemd/system/jabali-health-monitor.service` | Systemd service unit |
| `/var/log/jabali/health-monitor.log` | Event log |
| `/var/run/jabali/health-monitor.state` | Service state tracking |

**Commands:**
```bash
# Check status
systemctl status jabali-health-monitor

# View logs
journalctl -u jabali-health-monitor -f

# Manual notification test
php artisan notify:service-health down nginx --description="Web Server"
```

**Notification Setting:** `notify_service_health` in dns_settings table (Admin > Server Settings)

### Admin Notifications & Monitoring

The system sends email notifications to configured admin recipients for various events.

**Configuration:** Admin > Server Settings > Notifications tab

**Notification Types:**
| Type | Setting | Description |
|------|---------|-------------|
| SSL Errors | `notify_ssl_errors` | Certificate errors and expiration warnings |
| Backup Failures | `notify_backup_failures` | Failed scheduled backups |
| Disk Quota | `notify_disk_quota` | Users reaching 90% quota |
| Login Failures | `notify_login_failures` | Brute force and Fail2ban alerts |
| SSH Logins | `notify_ssh_logins` | Successful SSH login alerts |
| System Updates | `notify_system_updates` | Panel update availability |
| Service Health | `notify_service_health` | Service failures and auto-restarts |
| High Load | `notify_high_load` | Server load exceeds threshold |

**Notification Log:**
All sent notifications are logged in `notification_logs` table and viewable in Admin > Server Settings > Notifications tab.

**High Load Monitoring:**
- Monitors server load average every minute via `jabali-health-monitor`
- Configurable threshold (default: 5.0) and duration (default: 5 minutes)
- Sends alert when load exceeds threshold for configured duration
- Settings: `load_threshold`, `load_alert_minutes` in dns_settings

**Commands:**
```bash
# Manual high load notification test
php artisan notify:high-load

# Test email
# Use "Send Test Email" button in Server Settings > Notifications
```

### Backup System

The panel provides comprehensive backup functionality for both users and administrators.

**Backup Types:**
| Type | Description | Storage |
|------|-------------|---------|
| User Backup | Single user's domains, databases, mailboxes | `/home/{user}/backups/` |
| Server Backup (Full) | All users as tar.gz archive | `/var/backups/jabali/` |
| Server Backup (Incremental) | Rsync to remote destination | Remote SFTP/NFS |

**Admin Backup Features:**
- **Create Server Backup**: Backup all users or selected users
- **Restore Backup**: Selective restore with modal UI
- **Download Backup**: Download local backups (creates zip for directories)

**Restore Options:**
| Option | Description |
|--------|-------------|
| Website Files | Restore domain files to `/home/{user}/domains/` |
| Databases | Restore MySQL databases with security sanitization |
| MySQL Users | Restore MySQL users and their permissions |
| Mailboxes | Restore email mailboxes and messages |
| SSL Certificates | Restore SSL certificates for domains |
| DNS Zones | Restore DNS zone files |

**Backup Contents:**
```
backup_folder/
├── manifest.json           # Backup metadata
├── {username}.tar.gz       # Per-user archive containing:
│   ├── files/              # Domain files
│   │   └── {domain}/
│   ├── mysql/              # Database dumps
│   │   ├── {database}.sql.gz
│   │   └── users.sql       # MySQL users and grants
│   ├── mail/               # Mailbox data
│   │   └── {domain}/{user}/
│   ├── ssl/                # SSL certificates
│   │   └── {domain}/
│   └── dns/                # DNS zone files
│       └── {domain}.zone
```

**Security Validations (Restore):**
The backup restore process includes comprehensive security checks to prevent privilege escalation:

| Check | Description |
|-------|-------------|
| Database Prefix | Only restore databases matching user's prefix (`{username}_*`) |
| MySQL Users | Only restore users with correct prefix, block global grants |
| SQL Sanitization | Remove DEFINER, GRANT, SET GLOBAL from dumps |
| Domain Ownership | Verify user owns domains before restoring files/SSL/DNS |
| Symlink Prevention | Remove dangerous symlinks pointing outside backup |
| Path Traversal | Block `..` sequences in paths |
| DNS Validation | Validate zone files with `named-checkzone` |

**Agent Actions:**
```
backup.create           - Create user backup
backup.restore          - Restore backup with selective options
backup.get_info         - Get backup manifest/contents
backup.create_server    - Create server-wide backup
backup.delete           - Delete backup file
backup.upload_remote    - Upload backup to remote destination
backup.download_remote  - Download backup from remote
backup.incremental      - Create incremental backup via rsync
backup.test_destination - Test remote backup destination
```

**Download Route:**
```
GET /jabali-admin/backup-download?id={backup_id}  # Admin (requires is_admin)
GET /jabali-panel/backup-download?path={base64}   # User (validates ownership)
```

**Files:**
| File | Purpose |
|------|---------|
| `app/Filament/Admin/Pages/Backups.php` | Admin backup management UI |
| `app/Filament/Jabali/Pages/Backups.php` | User backup management UI |
| `app/Http/Controllers/BackupDownloadController.php` | Download handlers |
| `bin/jabali-agent` | Backup/restore implementation |
| `/var/log/jabali/agent.log` | Security audit log for blocked operations |

### DNSSEC Support

DNSSEC (Domain Name System Security Extensions) adds cryptographic signatures to DNS records.

**Management:** Admin > Server Settings > DNS tab > DNSSEC section

**Features:**
- Enable/disable DNSSEC per domain
- Automatic KSK (Key Signing Key) and ZSK (Zone Signing Key) generation
- Uses ECDSAP256SHA256 algorithm
- Auto-generates DS records for registrar configuration
- Inline zone signing with auto-dnssec maintain

**Agent Actions:**
```
dns.enable_dnssec   - Generate keys and sign zone
dns.disable_dnssec  - Remove keys and unsign zone
dns.get_dnssec_status - Check DNSSEC status and keys
dns.get_ds_records  - Get DS records for registrar
```

**Files:**
| Path | Description |
|------|-------------|
| `/etc/bind/keys/{domain}/` | DNSSEC keys directory |
| `/etc/bind/zones/db.{domain}` | Unsigned zone file |
| `/etc/bind/zones/db.{domain}.signed` | Signed zone file |

**Setup Process:**
1. Enable DNSSEC for domain in Server Settings
2. Copy DS record from modal
3. Add DS record to domain registrar
4. Wait for DNS propagation (up to 48 hours)

### Test Credentials
| Panel | URL | Email | Password |
|-------|-----|-------|----------|
| Admin | `https://jabali.lan/jabali-admin` | `admin@jabali.lan` | `q1w2E#R$` |
| User | `https://jabali.lan/jabali-panel` | `user@jabali.lan` | `wjqr9t6Z#%r&@C$4` |

### Demo Credentials
| Panel | URL | Email | Password |
|-------|-----|-------|----------|
| Admin | `https://demo.jabali-panel.com/jabali-admin` | `admin@jabali-panel.com` | `demo1234` |
| User | `https://demo.jabali-panel.com/jabali-panel` | `demo@jabali-panel.com` | `demo1234` |

**Demo mode behavior**
- `JABALI_DEMO=1` enables read-only mode via `App\Http\Middleware\DemoReadOnly`.
- Livewire `authenticate` calls are allowed for unauthenticated users.
- Some pages use static demo data to avoid agent socket calls.
- Reverse proxy must set `X-Forwarded-Proto` and the app must trust proxies for HTTPS Livewire updates.

## Models

| Model | Table | Description |
|-------|-------|-------------|
| User | users | Panel users (system users) |
| Domain | domains | Hosted domains |
| EmailDomain | email_domains | Email-enabled domains |
| Mailbox | mailboxes | Email mailboxes |
| EmailForwarder | email_forwarders | Email forwarding rules |
| DnsRecord | dns_records | DNS zone records |
| DnsSetting | dns_settings | Key-value settings store |
| SslCertificate | ssl_certificates | SSL/TLS certificates |
| MysqlCredential | mysql_credentials | Database credentials |
| Backup | backups | User backups |
| BackupSchedule | backup_schedules | Scheduled backups |
| BackupDestination | backup_destinations | Remote backup targets |
| CronJob | cron_jobs | User cron jobs |
| AuditLog | audit_logs | Admin audit trail |
| NotificationLog | notification_logs | Admin notification history |

## Filament Pages

### Admin Panel
- Dashboard, Services, ServerStatus, ServerSettings
- SslManager, PhpManager, EmailSettings, DnsZones
- Backups, AuditLogs, Fail2ban, ClamAV, Security, ServerImports

### User Panel
- Dashboard, Domains, DnsRecords, Files
- Email, WordPress, Databases, Ssl
- Backups, CronJobs, SshKeys, PhpSettings, Logs

## Dashboard Configurations

### Admin Dashboard (`/jabali-admin`)

**Location:** `App\Filament\Admin\Pages\Dashboard`

**Header Widgets:**
- `DashboardStatsWidget` - Stats cards showing Users, Domains, Mailboxes, Databases, SSL Certificates
  - Uses `<x-filament::section>` components with icon, value (bold), label
  - Responsive: 1 col mobile, 2 cols tablet, 5 cols desktop

**Schema Components:**
- `RecentActivityTable` - Embedded audit log table via `EmbeddedTable::make()`

**Header Actions:**
- Refresh - Reloads the page
- Setup Wizard - Onboarding modal (visible until completed)
- Take Tour - Starts the admin panel tour

**Files:**
| File | Purpose |
|------|---------|
| `app/Filament/Admin/Pages/Dashboard.php` | Dashboard page class |
| `app/Filament/Admin/Widgets/DashboardStatsWidget.php` | Stats widget |
| `resources/views/filament/admin/widgets/dashboard-stats.blade.php` | Stats template |
| `app/Filament/Admin/Widgets/Dashboard/RecentActivityTable.php` | Activity table widget |

### User Dashboard (`/jabali-panel`)

**Location:** `App\Filament\Jabali\Pages\Dashboard`

**Widgets (in order):**
1. `StatsOverview` - Stats cards showing Domains, Mailboxes, Databases, SSL Certificates
   - Responsive: 1 col mobile, 2 cols tablet, 4 cols desktop
2. `DiskUsageWidget` - Disk quota visualization with progress bar
3. `DomainsWidget` - Recent domains table with SSL status
4. `MailboxesWidget` - Recent mailboxes table
5. `RecentBackupsWidget` - Latest backups table

**Layout:** 2-column responsive grid (1 col mobile, 2 cols desktop)

**Subheading:** Personalized welcome message ("Welcome back, {name}!")

**Files:**
| File | Purpose |
|------|---------|
| `app/Filament/Jabali/Pages/Dashboard.php` | Dashboard page class |
| `app/Filament/Jabali/Widgets/StatsOverview.php` | Stats widget |
| `resources/views/filament/jabali/widgets/stats-overview.blade.php` | Stats template |
| `app/Filament/Jabali/Widgets/DiskUsageWidget.php` | Disk usage widget |
| `app/Filament/Jabali/Widgets/DomainsWidget.php` | Domains table widget |
| `app/Filament/Jabali/Widgets/MailboxesWidget.php` | Mailboxes table widget |
| `app/Filament/Jabali/Widgets/RecentBackupsWidget.php` | Backups table widget |

## Agent Actions

The jabali-agent supports these action categories:

```
user.*        - System user management
domain.*      - Domain/vhost operations
wp.*          - WordPress installation/management
email.*       - Email domain/mailbox operations
mysql.*       - Database operations
dns.*         - DNS zone management (includes DNSSEC)
php.*         - PHP version management
ssl.*         - SSL certificate operations
backup.*      - Backup/restore operations (see Backup System section)
service.*     - System service control
ufw.*         - Firewall management
file.*        - File operations
ssh.*         - SSH key management
cron.*        - Cron job management
quota.*       - Disk quota management
clamav.*      - Antivirus scanning
metrics.*     - System metrics
scanner.*     - Security scanning (Lynis, Nikto)
logs.*        - Log access
redis.*       - Redis user management
```

**Detailed Action Reference:**

| Action | Parameters | Description |
|--------|------------|-------------|
| `dns.enable_dnssec` | `domain` | Generate keys and sign zone |
| `dns.disable_dnssec` | `domain` | Remove keys and unsign zone |
| `dns.get_dnssec_status` | `domain` | Check DNSSEC status and keys |
| `dns.get_ds_records` | `domain` | Get DS records for registrar |
| `backup.create` | `username`, `options` | Create user backup |
| `backup.restore` | `username`, `backup_path`, `restore_*` | Restore with selective options |
| `backup.get_info` | `backup_path` | Get backup manifest |
| `backup.create_server` | `path`, `options` | Create server backup |
| `backup.delete` | `path` | Delete backup |
| `backup.upload_remote` | `local_path`, `config` | Upload to remote |
| `backup.download_remote` | `remote_path`, `local_path`, `config` | Download from remote |
| `backup.incremental` | `config`, `options` | Incremental rsync backup |
| `backup.test_destination` | `config` | Test remote destination |
| `backup.delete_remote` | `remote_path`, `config` | Delete backup from remote |

## Backup System

The backup system supports both user-level and server-wide backups with local and remote storage.

### Backup Types

| Type | Description | Storage |
|------|-------------|---------|
| **Full (tar.gz)** | Complete archive of all data | Local or remote |
| **Incremental (rsync)** | Space-efficient with hard links | Remote only (SFTP/NFS) |

### Remote Destinations

Supported destination types:
- **SFTP** - SSH-based file transfer to remote servers
- **NFS** - Network File System mounts
- **S3** - S3-compatible object storage (AWS, MinIO, etc.)

### Backup Schedules & Retention

Scheduled backups run via the `backups:run-schedules` artisan command (called by cron). Each schedule has:
- **Frequency**: Hourly, daily, weekly, or monthly
- **Retention Count**: Number of backups to keep (default: 7)
- **Destination**: Local or remote storage

**Retention Policy:**
- Applied automatically after each backup completes
- Deletes oldest backups beyond the retention count
- Works for both local files and remote destinations
- Implemented in `App\Jobs\RunServerBackup::applyRetention()`

**Important:** The queue worker must be running for scheduled backups and retention:
```bash
systemctl status jabali-queue    # Check status
systemctl restart jabali-queue   # Restart to pick up code changes
```

### Backup Files

| Path | Description |
|------|-------------|
| `/var/backups/jabali/` | Default server backup location |
| `/home/{user}/backups/` | User backup location |
| `metadata/panel_data.json` | Panel database records (domains, mailboxes, etc.) |
| `mysql/` | Database dumps (.sql.gz) |
| `zones/` | DNS zone files |
| `ssl/` | SSL certificates |
| `mail/` | Mailbox data |

### Related Models

- `Backup` - Individual backup records
- `BackupSchedule` - Schedule configuration with retention settings
- `BackupDestination` - Remote storage configuration

### Related Jobs

- `RunServerBackup` - Executes backup and applies retention
- `IndexRemoteBackups` - Indexes backups on remote destinations

## Security Features

### Security Page (Admin)

Located at `/jabali-admin/security`, provides:
- **Overview** - System security status and recent audit logs
- **Firewall** - UFW rules management
- **Fail2ban** - Intrusion prevention with jail management
- **Antivirus** - ClamAV scanning
- **SSH** - SSH hardening settings
- **Vulnerability Scanner** - Security scanning tools

### Security Scanning Tools

| Tool | Purpose | Installation |
|------|---------|--------------|
| **Lynis** | System security auditing | Pre-installed |
| **Nikto** | Web server vulnerability scanner | `/opt/nikto` (GitHub clone) |
| **WPScan** | WordPress vulnerability scanner | Ruby gem |

**Nikto Configuration:**
- Installed from GitHub at `/opt/nikto`
- Symlinked to `/usr/local/bin/nikto`
- Called with full path in timeout commands (PATH restriction)

**WPScan Configuration:**
- Cache directory: `/var/www/.wpscan` (owned by www-data)
- Run with `HOME=/var/www` environment variable
- Version displayed without ASCII banner using `grep -i 'version'`

### Audit Logs

All administrative actions are logged to the `audit_logs` table.

**Logged Events:**
- Authentication (login, logout, login_failed)
- CRUD operations on domains, mailboxes, users
- System configuration changes
- Security-related actions

**Audit Log Retention:**
- Configured via `audit_log_retention_days` setting (default: 90 days)
- Pruned daily at 2:00 AM via scheduled task
- Method: `AuditLog::prune()`

**Viewing Logs:**
- Admin Panel: Security page → Overview tab (paginated table)
- Direct page: `/jabali-admin/audit-logs` (hidden from sidebar)

### Related Files

| File | Description |
|------|-------------|
| `app/Models/AuditLog.php` | Audit log model with prune method |
| `app/Filament/Admin/Pages/Security.php` | Security dashboard |
| `app/Filament/Admin/Widgets/Security/AuditLogsTable.php` | Audit logs table widget |
| `routes/console.php` | Scheduled audit log pruning |

## Code Style

- PHP 8.2+ with strict types
- Follow Laravel conventions
- Use `DnsSetting::get()` / `DnsSetting::set()` for key-value storage
- All privileged operations go through jabali-agent

## Filament v4 UI Guidelines

**CRITICAL: Build using ONLY Filament v4 native components and styles with proper spacing. Always use pure Filament native components with proper icons and colors.**

### Architecture Guidelines

1. **Use Filament Resources for Eloquent Models**: When data is backed by an Eloquent model, prefer creating a Filament Resource (`php artisan make:filament-resource`) over custom pages.

2. **Use Tables for List Data**: When displaying list data (arrays or collections), ALWAYS use proper Filament tables via `EmbeddedTable::make()` or the `HasTable` trait.

3. **Use Schema-based Pages for Complex UIs**: For pages with multiple sections, tabs, and mixed content (forms + tables + stats), use Schema-based pages with embedded table widgets.

4. **Prefer Existing Patterns**: Look at existing pages in the codebase (e.g., `Security.php`, `DnsRecords.php`, `SshKeys.php`) for examples of how to implement similar features.

### Absolute Rules (NO EXCEPTIONS)
- **NO custom HTML** - Never use raw `<div>`, `<span>`, `<p>`, `<ul>` etc. in Filament pages
- **NO inline styles** - Never use `style="..."` attributes
- **NO custom CSS** - Never create CSS files for Filament components
- **NO custom blade templates** - Don't create View components with custom HTML for UI elements
- **USE Filament components** - Sections, badges, buttons, tables, infolists, stats widgets, grids
- **USE proper icons** - Always use `->icon('heroicon-o-*')` on sections and actions
- **USE proper colors** - Always use `->iconColor('success'|'danger'|'warning'|'gray')` for status indication
- **USE Tailwind classes** - Only when absolutely necessary for minor adjustments
- **MUST be responsive** - All pages must work on mobile, tablet, and desktop

### Warning Banners

- Use Filament `Section::make()` for warning banners (no raw HTML).
- Always set `->icon('heroicon-o-exclamation-triangle')` and `->iconColor('warning')`.
- Keep banners non-collapsible: `->collapsed(false)->collapsible(false)`.
- Put the full message in `->description()` and keep the heading short.

### Allowed Components

Use these Filament native components exclusively:

| Category | Components |
|----------|------------|
| Layout | `Section::make()`, `Grid::make()`, `Group::make()`, `Tabs::make()` |
| Display | `Text::make()`, `<x-filament::badge>`, `<x-filament::icon>` |
| Actions | `Actions::make()`, `Action::make()`, `<x-filament::button>` |
| Forms | TextInput, Select, Toggle, FileUpload, Checkbox, etc. |
| Data | Tables, Infolists, Stats Widgets, `EmbeddedTable::make()` |
| Feedback | `<x-filament::modal>`, Notifications |

### Tables with Array Data (IMPORTANT)

When displaying list data, **ALWAYS use proper Filament tables**, not Sections or custom HTML. Use `EmbeddedTable::make()` to embed table widgets within Schema-based pages.

**Creating a Self-Refreshing Table Widget:**

For tables with actions that modify data (enable/disable, delete, etc.), the widget should reload its own data directly rather than dispatching events to the parent (which causes full page re-renders).

```php
<?php
namespace App\Filament\Admin\Widgets\Security;

use App\Services\Agent\AgentClient;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class MyDataTable extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    public array $items = [];  // Data passed from parent

    // Reload data directly from source (agent, API, database)
    protected function reloadData(): void
    {
        try {
            $agent = new AgentClient();
            $result = $agent->send('some.action', []);
            if ($result['success'] ?? false) {
                $this->items = $result['items'] ?? [];
                $this->resetTable();  // Force table to re-render with new data
            }
        } catch (\Exception $e) {
            // Keep existing data on error
        }
    }

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->items)
            ->columns([
                TextColumn::make('name')->label(__('Name'))->searchable(),
                IconColumn::make('enabled')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('gray'),
            ])
            ->actions([
                Action::make('toggle')
                    ->label(fn (array $record): string => ($record['enabled'] ?? false) ? __('Disable') : __('Enable'))
                    ->icon(fn (array $record): string => ($record['enabled'] ?? false) ? 'heroicon-o-x-circle' : 'heroicon-o-check-circle')
                    ->color(fn (array $record): string => ($record['enabled'] ?? false) ? 'danger' : 'success')
                    ->action(function (array $record): void {
                        try {
                            $agent = new AgentClient();
                            $result = $agent->send('item.toggle', ['id' => $record['id']]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('Status updated'))
                                    ->success()
                                    ->send();

                                // Reload data directly - don't dispatch events to parent
                                $this->reloadData();
                            } else {
                                throw new \Exception($result['error'] ?? __('Operation failed'));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Error'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->striped()
            ->emptyStateHeading(__('No items'))
            ->emptyStateIcon('heroicon-o-inbox');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
```

**Embedding Table in Schema:**
```php
use Filament\Schemas\Components\EmbeddedTable;
use App\Filament\Admin\Widgets\Security\MyDataTable;

// In your page's schema
Section::make(__('Data List'))
    ->icon('heroicon-o-list-bullet')
    ->schema([
        EmbeddedTable::make(MyDataTable::class, ['items' => $this->items]),
    ])
```

**Key Points:**
- Implement `HasTable`, `HasSchemas`, and `HasActions` interfaces
- Use `InteractsWithTable`, `InteractsWithSchemas`, and `InteractsWithActions` traits
- Use `->records(fn () => $this->arrayData)` for array/collection data
- Use `->query(Model::query())` for Eloquent models
- Use `->actions([])` for row actions on array-based tables
- Always implement `makeFilamentTranslatableContentDriver()` returning null
- Return `$this->getTable()->render()` in render method (NOT `view('filament-tables::index')`)
- Import actions from `Filament\Actions\Action` (NOT `Filament\Tables\Actions\Action`)
- **After modifying data, call `$this->resetTable()` to force the table to re-render**
- **Reload data directly in the widget rather than dispatching events to parent** (avoids full page re-renders)

### Section with Icons and Colors
Always use Section's native icon and color support:
```php
use Filament\Schemas\Components\Section;

// Status card with icon and color
Section::make(__('Active'))
    ->description(__('Firewall'))
    ->icon('heroicon-o-shield-check')
    ->iconColor('success')  // success, danger, warning, gray

// Section with header actions
Section::make(__('Settings'))
    ->icon('heroicon-o-cog')
    ->headerActions([
        Action::make('save')->label(__('Save')),
    ])
    ->schema([...])
```

### Responsive Grid Layout
Use Grid with responsive column configuration:
```php
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;

// 3-column responsive grid for stat cards
Grid::make(['default' => 1, 'sm' => 3])
    ->schema([
        Section::make(__('Active'))
            ->description(__('Firewall'))
            ->icon('heroicon-o-shield-check')
            ->iconColor('success'),
        Section::make('0')
            ->description(__('IPs Banned'))
            ->icon('heroicon-o-lock-closed')
            ->iconColor('success'),
        Section::make('0')
            ->description(__('Threats'))
            ->icon('heroicon-o-bug-ant')
            ->iconColor('gray'),
    ])
```

### Stats Overview Widget Pattern

For dashboard stats (domains count, mailboxes count, etc.), use a custom Widget with `<x-filament::section>` components in a blade template.

**Widget class:**
```php
<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Widgets\Widget;

class DashboardStatsWidget extends Widget
{
    protected static ?int $sort = 1;
    protected int|string|array $columnSpan = 'full';
    protected string $view = 'filament.admin.widgets.dashboard-stats';

    public function getStats(): array
    {
        return [
            [
                'value' => $userCount,
                'label' => __('Users'),
                'icon' => 'heroicon-o-users',
                'color' => 'primary',
            ],
            [
                'value' => $domainCount,
                'label' => __('Domains'),
                'icon' => 'heroicon-o-globe-alt',
                'color' => 'success',
            ],
            // ... more stats
        ];
    }
}
```

**Blade template** (`resources/views/filament/{panel}/widgets/dashboard-stats.blade.php`):
```blade
<x-filament-widgets::widget>
    <style>
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(1, minmax(0, 1fr));
            gap: 16px;
        }
        @media (min-width: 640px) {
            .stats-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
        }
        @media (min-width: 1024px) {
            .stats-grid { grid-template-columns: repeat(5, minmax(0, 1fr)); }
        }
    </style>
    <div class="stats-grid">
        @foreach($this->getStats() as $stat)
            <x-filament::section :icon="$stat['icon']" :icon-color="$stat['color']">
                <x-slot name="heading">
                    <span class="text-2xl font-bold">{{ $stat['value'] }}</span>
                </x-slot>
                <x-slot name="description">{{ $stat['label'] }}</x-slot>
            </x-filament::section>
        @endforeach
    </div>
</x-filament-widgets::widget>
```

**Key points:**
- Use custom Widget extending `Filament\Widgets\Widget` with a blade view
- Use `<x-filament::section>` for each stat card with icon and icon-color
- Value goes in `heading` slot with bold styling, label goes in `description` slot
- Use CSS media queries in `<style>` tag for responsive grid (Tailwind dynamic classes may not work)
- Adjust grid columns based on number of stats (e.g., 5 cols for admin, 4 cols for user panel)

### Responsive Design Verification

**CRITICAL: Always test responsive design on three viewport sizes:**

1. **Mobile** (375px width) - iPhone size
2. **Tablet** (768px width) - iPad size
3. **Desktop** (1400px width) - Standard desktop

**Puppeteer viewport examples:**
```javascript
// Mobile
await page.setViewport({ width: 375, height: 812 });

// Tablet
await page.setViewport({ width: 768, height: 1024 });

// Desktop
await page.setViewport({ width: 1400, height: 900 });
```

**Common responsive issues to check:**
- Stats/cards should stack on mobile, grid on tablet/desktop
- Tables should be scrollable on mobile
- Navigation should collapse to hamburger menu on mobile
- Buttons and inputs should be touch-friendly (min 44px tap target)
- Text should not overflow or be cut off

### Tabs with Dynamic Table Content (IMPORTANT)

**Problem:** When using Filament's `Tabs::make()` with tables inside `View::make()`, table row actions (modals, confirmations) don't work because the action mounting breaks when nested inside the Tabs schema.

**Solution:** Render the table OUTSIDE the form schema, and use a custom tabs navigation component with `wire:click` for tab switching.

**1. Create a custom tabs nav component** (`resources/views/filament/{panel}/components/my-tabs-nav.blade.php`):
```blade
@php
    $tabs = [
        'first' => ['label' => __('First Tab'), 'icon' => 'heroicon-o-home'],
        'second' => ['label' => __('Second Tab'), 'icon' => 'heroicon-o-cog'],
        'third' => ['label' => __('Third Tab'), 'icon' => 'heroicon-o-list-bullet'],
    ];
@endphp

<nav class="fi-tabs flex max-w-full gap-x-1 overflow-x-auto mx-auto rounded-xl bg-white p-2 shadow-sm ring-1 ring-gray-950/5 dark:bg-white/5 dark:ring-white/10" role="tablist">
    @foreach($tabs as $key => $tab)
        <button
            type="button"
            role="tab"
            aria-selected="{{ $this->activeTab === $key ? 'true' : 'false' }}"
            wire:click="setTab('{{ $key }}')"
            @class([
                'fi-tabs-item group flex items-center gap-x-2 rounded-lg px-3 py-2 text-sm font-medium outline-none transition duration-75',
                'fi-active bg-gray-50 dark:bg-white/5' => $this->activeTab === $key,
                'hover:bg-gray-50 focus-visible:bg-gray-50 dark:hover:bg-white/5 dark:focus-visible:bg-white/5' => $this->activeTab !== $key,
            ])
        >
            <x-filament::icon
                :icon="$tab['icon']"
                @class([
                    'fi-tabs-item-icon h-5 w-5 shrink-0 transition duration-75',
                    'text-primary-600 dark:text-primary-400' => $this->activeTab === $key,
                    'text-gray-400 group-hover:text-gray-500 group-focus-visible:text-gray-500 dark:text-gray-500 dark:group-hover:text-gray-400 dark:group-focus-visible:text-gray-400' => $this->activeTab !== $key,
                ])
            />
            <span @class([
                'fi-tabs-item-label transition duration-75',
                'text-primary-600 dark:text-primary-400' => $this->activeTab === $key,
                'text-gray-500 group-hover:text-gray-700 group-focus-visible:text-gray-700 dark:text-gray-400 dark:group-hover:text-gray-200 dark:group-focus-visible:text-gray-200' => $this->activeTab !== $key,
            ])>
                {{ $tab['label'] }}
            </span>
        </button>
    @endforeach
</nav>
```

**2. Page class:**
```php
<?php
namespace App\Filament\Jabali\Pages;

use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\View;
use Filament\Schemas\Schema;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Attributes\Url;

class MyTabbedPage extends Page implements HasTable
{
    use InteractsWithTable;

    #[Url(as: 'tab')]
    public ?string $activeTab = 'first';

    public function mount(): void
    {
        $this->activeTab = $this->normalizeTabName($this->activeTab);
    }

    protected function normalizeTabName(?string $tab): string
    {
        return match ($tab) {
            'first', 'second', 'third' => $tab,
            default => 'first',
        };
    }

    protected function getForms(): array
    {
        return ['myForm'];
    }

    // Form renders ONLY the tabs nav (and optional info section)
    public function myForm(Schema $schema): Schema
    {
        return $schema->schema([
            Section::make(__('Page Title'))
                ->description(__('Description of this page'))
                ->icon('heroicon-o-information-circle')
                ->iconColor('info'),
            View::make('filament.jabali.components.my-tabs-nav'),
        ]);
    }

    public function setTab(string $tab): void
    {
        $this->activeTab = $this->normalizeTabName($tab);
        $this->resetTable();
    }

    // Table rendered separately - NOT inside the form schema
    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'first' => $this->firstTable($table),
            'second' => $this->secondTable($table),
            'third' => $this->thirdTable($table),
            default => $this->firstTable($table),
        };
    }

    protected function firstTable(Table $table): Table
    {
        return $table
            ->query(MyModel::query())
            ->columns([...])
            ->recordActions([
                // These actions will work because table is outside the form schema
                Action::make('edit')->modalHeading('Edit Item')->form([...])->action(...),
                Action::make('delete')->requiresConfirmation()->action(...),
            ]);
    }
}
```

**3. Blade template** - render table OUTSIDE the form with negative margin:
```blade
<x-filament-panels::page>
    {{ $this->myForm }}

    <div class="-mt-4">
        {{ $this->table }}
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
```

**Key points:**
- **DO NOT** put `View::make('...table')` inside `Tabs\Tab::make()->schema([])` - table actions won't work
- Render the table DIRECTLY in the blade template with `{{ $this->table }}`
- Use `<div class="-mt-4">` to compensate for the parent's row-gap CSS
- Custom tabs nav uses `wire:click="setTab('tabname')"` for proper Livewire updates
- `setTab()` normalizes the tab name and calls `$this->resetTable()` to refresh the table
- `#[Url(as: 'tab')]` syncs the active tab with the URL query string

**Examples in codebase:**
- `app/Filament/Jabali/Pages/Email.php` - Email page with mailboxes/forwarders/catchall/logs tabs
- `app/Filament/Jabali/Pages/Backups.php` - Backups page with local/remote/history tabs

### Examples

**Stats Widget (correct):**
```php
use Filament\Widgets\StatsOverviewWidget;
use Filament\Widgets\StatsOverviewWidget\Stat;

class MyStatsWidget extends StatsOverviewWidget
{
    protected function getStats(): array
    {
        return [
            Stat::make('Total', 100)->icon('heroicon-o-users'),
            Stat::make('Active', 80)->color('success'),
        ];
    }
}
```

**Section with content (correct):**
```blade
<x-filament::section icon="heroicon-o-cog" collapsible>
    <x-slot name="heading">Settings</x-slot>
    <x-slot name="description">Configure options</x-slot>

    {{-- Use Filament form components here --}}
</x-filament::section>
```

**Table columns (correct):**
```php
TextColumn::make('status')
    ->badge()
    ->color(fn (string $state): string => match ($state) {
        'active' => 'success',
        'pending' => 'warning',
        default => 'gray',
    })
```

### Progress Bar Widget Pattern

For displaying progress (disk usage, quotas, etc.), use a Widget with Filament Section and Tailwind-based progress bar. Use Tailwind's dynamic color classes with `@class` directive:

**Widget class:**
```php
<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use Filament\Widgets\Widget;

class DiskUsageWidget extends Widget
{
    protected static ?int $sort = 2;
    protected int|string|array $columnSpan = 1;
    protected string $view = 'filament.jabali.widgets.disk-usage';

    public function getData(): array
    {
        $percent = 25.0;
        return [
            'used' => '2.5 GB',
            'quota' => '10 GB',
            'free' => '7.5 GB',
            'percent' => $percent,
            'has_quota' => true,
            'home' => '/home/user',
            'color' => $this->getColor($percent),
        ];
    }

    protected function getColor(float $percent): string
    {
        if ($percent >= 90) return 'danger';
        if ($percent >= 70) return 'warning';
        return 'success';
    }
}
```

**Blade template (resources/views/filament/{panel}/widgets/disk-usage.blade.php):**
```blade
<x-filament-widgets::widget>
    <x-filament::section icon="heroicon-o-circle-stack">
        <x-slot name="heading">{{ __('Disk Usage') }}</x-slot>
        <x-slot name="description">{{ $this->getData()['home'] }}</x-slot>

        @php
            $data = $this->getData();
            $percent = min(100, max(0, $data['percent']));
        @endphp

        <div class="space-y-4">
            <div>
                <div class="flex items-center justify-between mb-2">
                    <span class="text-sm font-medium text-gray-700 dark:text-gray-200">
                        {{ $data['used'] }} {{ __('used') }}
                    </span>
                    <x-filament::badge :color="$data['color']">
                        {{ number_format($percent, 1) }}%
                    </x-filament::badge>
                </div>
                <div class="w-full h-3 bg-gray-200 rounded-full dark:bg-gray-700 overflow-hidden">
                    <div
                        @class([
                            'h-full rounded-full transition-all duration-500',
                            'bg-success-500' => $data['color'] === 'success',
                            'bg-warning-500' => $data['color'] === 'warning',
                            'bg-danger-500' => $data['color'] === 'danger',
                        ])
                        style="width: {{ $percent }}%"
                    ></div>
                </div>
            </div>
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
```

**Key points:**
- Use `@class` directive with conditional Tailwind classes for colors
- Only use inline `style` for truly dynamic values like percentage width (Tailwind can't generate dynamic classes)
- Use Filament's native `<x-filament::badge>` and `<x-filament::section>` components
- Use Filament color names (`success`, `warning`, `danger`) for consistency

### Anti-patterns (avoid)
```blade
{{-- BAD: Custom HTML --}}
<div class="custom-card">
    <h3>Title</h3>
    <p>Content</p>
</div>

{{-- GOOD: Use Filament section --}}
<x-filament::section>
    <x-slot name="heading">Title</x-slot>
    Content
</x-filament::section>
```

```blade
{{-- BAD: Inline styles --}}
<div style="display: flex; gap: 1rem;">

{{-- GOOD: Tailwind classes if needed --}}
<div class="flex gap-4">
```

### Filament v4 Namespaces (IMPORTANT)

Filament v4 uses `Schema` instead of `Form` for page layouts, and layout components are in different namespaces:

```php
// CORRECT Filament v4 imports
use Filament\Schemas\Schema;                    // NOT Filament\Forms\Form
use Filament\Schemas\Components\Tabs;           // NOT Filament\Forms\Components\Tabs
use Filament\Schemas\Components\Grid;           // Layout components
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Group;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\View;
use Filament\Schemas\Components\Actions as FormActions;

// Form INPUT components stay in Forms namespace
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Toggle;
```

**Method signatures:**
```php
// CORRECT - Filament v4
public function myForm(Schema $schema): Schema

// WRONG - Old Filament v3 style
public function myForm(Form $form): Form
```

### Schema-based Tabs (For Form-Only Content)

Use `Filament\Schemas\Components\Tabs` within a form schema **ONLY when tabs contain form fields or static content** - NOT when tabs contain tables with row actions.

**When to use Schema-based Tabs:**
- Tabs with form inputs (TextInput, Select, Toggle, etc.)
- Tabs with static content (Text, View with no interactive elements)
- Settings pages where each tab is a different category of settings

**When NOT to use Schema-based Tabs:**
- Tabs with tables that have row actions (edit, delete, view modals)
- Use the "Tabs with Dynamic Table Content" pattern above instead

**Example - Settings page with form tabs:**
```php
public function settingsForm(Schema $schema): Schema
{
    return $schema->schema([
        Tabs::make('Settings')
            ->tabs([
                Tabs\Tab::make(__('General'))
                    ->icon('heroicon-o-cog')
                    ->schema([
                        TextInput::make('site_name')->label(__('Site Name')),
                        Toggle::make('maintenance_mode')->label(__('Maintenance Mode')),
                    ]),
                Tabs\Tab::make(__('Email'))
                    ->icon('heroicon-o-envelope')
                    ->schema([
                        TextInput::make('smtp_host')->label(__('SMTP Host')),
                        TextInput::make('smtp_port')->label(__('SMTP Port')),
                    ]),
            ])
            ->persistTabInQueryString(),
    ]);
}
```

**Key Points:**
- `persistTabInQueryString()` syncs tab state with URL (client-side only)
- For tables with actions, see "Tabs with Dynamic Table Content" pattern above

**Text component (no label method):**
```php
// CORRECT - Text takes content directly
Text::make(__('Your message here'))
Text::make($this->someVariable)

// WRONG - Text doesn't have label() or content() methods
Text::make('name')->label('Label')->content('Content')
```

## Debugging & Verification

### IMPORTANT: Always Clear Laravel Cache After Tasks
After completing any task that modifies PHP files, views, or configuration, **ALWAYS clear Laravel cache**:

```bash
cd /var/www/jabali && php artisan optimize:clear
```

This is equivalent to running:
```bash
php artisan config:clear
php artisan view:clear
php artisan cache:clear
php artisan route:clear
php artisan event:clear
```

**When to clear cache:**
- After modifying any PHP file in `app/`
- After modifying any Blade template in `resources/views/`
- After modifying `.env` or `config/` files
- After adding or removing files
- Before taking screenshots to verify changes

### Always Diagnose with Artisan
When encountering errors or unexpected behavior, use `php artisan` commands to diagnose:

```bash
# Check PHP syntax errors
php -l app/Filament/Admin/Pages/YourPage.php

# Clear all caches (do this after code changes)
php artisan config:clear
php artisan view:clear
php artisan cache:clear
php artisan route:clear

# Rebuild caches for production
php artisan config:cache
php artisan view:cache
php artisan route:cache

# Check routes are registered
php artisan route:list --name=filament

# Debug with tinker
php artisan tinker

# Check for missing migrations
php artisan migrate:status

# View logs in real-time
php artisan pail
```

### Common Error Patterns

| Error | Cause | Fix |
|-------|-------|-----|
| `TypeError: must be of type X, Y given` | Wrong type hint (e.g., `Form` vs `Schema` in Filament v4) | Check method signatures match framework version |
| `Unable to locate component` | Blade component doesn't exist | Check component name spelling and namespace |
| `Class not found` | Missing import or autoload | Run `composer dump-autoload`, check `use` statements |
| `View not found` | Missing blade template | Create the view file in correct location |

### Screenshots for UI Verification
Use Puppeteer scripts in `/tmp/` to take screenshots of authenticated pages. Always save screenshots to `/tmp/`:

```bash
# For authenticated admin panel pages, use Puppeteer scripts in /tmp/
# Example: /tmp/security-overview.js
node /tmp/security-overview.js

# Then use the Read tool to view the screenshot
# Read /tmp/security-overview.png to see the result
```

**Example Puppeteer script** (`/tmp/screenshot-page.js`):
```javascript
const puppeteer = require('puppeteer');

(async () => {
  const browser = await puppeteer.launch({
    headless: 'new',
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--ignore-certificate-errors']
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1600, height: 1200 });

  // Login to admin panel
  await page.goto('https://jabali.lan/jabali-admin/login', { waitUntil: 'networkidle0' });
  await new Promise(r => setTimeout(r, 1000));
  await page.type('input[type="email"]', 'admin@jabali.lan');
  await page.type('input[type="password"]', 'q1w2E#R$');
  await page.click('button[type="submit"]');
  await new Promise(r => setTimeout(r, 5000));

  // Navigate to target page
  await page.goto('https://jabali.lan/jabali-admin/security', { waitUntil: 'networkidle0', timeout: 30000 });
  await new Promise(r => setTimeout(r, 3000));

  await page.screenshot({ path: '/tmp/security.png', fullPage: true });
  await browser.close();
})();
```

**Important:** Always save screenshots to `/tmp/` directory (e.g., `/tmp/page.png`).

**When to use screenshots:**
- After making UI/layout changes to verify they look correct
- When debugging visual issues that are hard to diagnose from code
- To check responsive design on different viewport sizes
- Before committing changes to ensure nothing is visually broken

**Workflow:**
1. Make code changes
2. Run `php -l` to check syntax
3. Clear caches with `php artisan view:clear`
4. Create/update Puppeteer script in `/tmp/` and run with `node`
5. Use Read tool to view the screenshot

## Testing

```bash
composer test             # Run all tests
php artisan test --filter=ClassName  # Run specific test
```

## Common Tasks

### Adding a New Filament Page

1. Create page class in `app/Filament/{Panel}/Pages/`
2. Create blade view in `resources/views/filament/{panel}/pages/`
3. Register in panel provider if needed

### Adding Agent Actions

1. Add action handler in `bin/jabali-agent` (match statement)
2. Implement function with proper validation
3. Call from PHP:
```php
use App\Services\Agent\AgentClient;

$client = new AgentClient();
$response = $client->send('category.action', ['param1' => 'value1']);
```

### Agent Best Practices

**Service Restart Operations:**
When agent actions restart services (fail2ban, nginx, postfix, etc.), add a wait period before returning success. This ensures the UI gets accurate status when it reloads:

```php
function myServiceToggle(array $params): array
{
    // ... modify config ...

    // Restart the service
    exec('systemctl restart myservice 2>&1', $output, $code);

    // Wait for service to be fully ready before returning
    if ($code === 0) {
        sleep(2);  // Give service time to fully start
        exec('systemctl is-active myservice 2>/dev/null', $statusOutput);
    }

    return ['success' => $code === 0, 'error' => $code !== 0 ? implode("\n", $output) : null];
}
```

**Fallback Data:**
For data that may not be available from the source (e.g., descriptions from filter files), provide fallback mappings:

```php
$descriptions = [
    'postfix-sasl' => 'Postfix SASL authentication protection',
    'dovecot' => 'Dovecot IMAP/POP3 server protection',
    // ... more fallbacks
];

$description = getFromSource() ?: ($descriptions[$name] ?? '');
```

### Working with Settings

```php
use App\Models\DnsSetting;

// Get setting with default
$value = DnsSetting::get('setting_key', 'default');

// Set setting
DnsSetting::set('setting_key', $value);
```

## File Paths

| Path | Description |
|------|-------------|
| `/home/{user}/domains/{domain}/public_html` | Domain web root |
| `/home/{user}/backups/` | User backups (tar.gz files) |
| `/var/backups/jabali/` | Server backups (admin-created) |
| `/var/vmail/{domain}/{user}` | Email mailbox storage |
| `/var/run/jabali/agent.sock` | Agent socket |
| `/var/log/jabali/agent.log` | Agent log (includes security audit) |
| `/etc/nginx/sites-available/` | Nginx vhosts |
| `/etc/letsencrypt/` | SSL certificates |
| `/etc/bind/zones/` | DNS zone files |
| `/etc/bind/keys/{domain}/` | DNSSEC keys |

## Environment Variables

Key `.env` settings:
- `APP_URL` - Panel URL
- `PANEL_DOMAIN` - Panel hostname
- `DB_*` - Database connection
- `MAIL_*` - Mail configuration

## Dependencies

- PHP 8.2+, Laravel 12, Filament 4, Livewire 3
- Nginx, PHP-FPM, MariaDB/MySQL
- Postfix, Dovecot, OpenDKIM
- BIND9 (named), Certbot
- Redis (optional caching)

## Internationalization (i18n) & Translations

The panel supports multiple languages with full RTL (right-to-left) support for Arabic and Hebrew.

### Supported Languages

| Code | Language | Native Name | Direction |
|------|----------|-------------|-----------|
| `en` | English | English | LTR |
| `es` | Spanish | Español | LTR |
| `fr` | French | Français | LTR |
| `ru` | Russian | Русский | LTR |
| `pt` | Portuguese | Português | LTR |
| `ar` | Arabic | العربية | RTL |
| `he` | Hebrew | עברית | RTL |

### Configuration

**Language settings:** `config/languages.php`

```php
// Get supported languages
$languages = config('languages.supported');

// Check if language is RTL
$isRtl = config('languages.supported.ar.direction') === 'rtl';
```

### Translation Files

| File | Description | Entries |
|------|-------------|---------|
| `lang/en.json` | English (base) | 602 |
| `lang/es.json` | Spanish | 1171 |
| `lang/fr.json` | French | 887 |
| `lang/ru.json` | Russian | 887 |
| `lang/pt.json` | Portuguese | 880 |
| `lang/ar.json` | Arabic | 880 |
| `lang/he.json` | Hebrew | 880 |

### Using Translations

**In PHP/Blade:**
```php
// Simple translation
__('Dashboard')

// With parameters
__('Welcome, :name', ['name' => $user->name])

// In Filament components
TextColumn::make('status')->label(__('Status'))
Section::make(__('Settings'))->description(__('Configure your options'))
```

**Best Practices:**
- Always wrap user-facing strings with `__()`
- Use English as the key: `__('Create Domain')` not `__('domain.create')`
- Parameters use `:name` syntax for substitution
- Keep translations concise and contextually appropriate

### Adding New Translations

1. **Add the English string** in your code using `__('Your string')`

2. **Add translations** to each language file in `lang/`:

```bash
# Example: Adding "New Feature" translation
# Edit each file: lang/es.json, lang/fr.json, etc.
```

```json
{
    "New Feature": "Nueva Función"
}
```

3. **For bulk additions**, create entries in all language files:

```php
// lang/es.json
{
    "New Feature": "Nueva Función",
    "Another String": "Otra Cadena"
}

// lang/fr.json
{
    "New Feature": "Nouvelle Fonctionnalité",
    "Another String": "Une Autre Chaîne"
}
```

### RTL Language Support

Arabic and Hebrew use right-to-left text direction. Filament handles RTL automatically when the locale is set. For custom components:

```blade
{{-- Use Tailwind RTL utilities --}}
<div class="rtl:text-right ltr:text-left">Content</div>
<div class="rtl:mr-4 ltr:ml-4">Indented content</div>
```

### Login Page Word Clouds

Both admin and user panels feature typographic word cloud backgrounds on login pages:

- **Admin Panel:** "Administrator" in all supported languages (red, diagonal pattern)
- **User Panel:** "Client Dashboard" in all supported languages (blue, diagonal pattern)

The word clouds use fonts that support all scripts: `"Segoe UI", Arial, "Noto Sans", "Noto Sans Arabic", "Noto Sans Hebrew", sans-serif`

### Extracting Translatable Strings

To find all translatable strings in the codebase:

```bash
# Find all __() calls in PHP files
grep -roh "__('[^']*')" app/ resources/ --include="*.php" | sort | uniq

# Find all __() calls in Blade files
grep -roh "__('[^']*')" resources/ --include="*.blade.php" | sort | uniq
```

### Language Switcher

Users can change their language preference through their profile settings. The selected language is stored in the session and persists across requests.

===

<laravel-boost-guidelines>
=== foundation rules ===

# Laravel Boost Guidelines

The Laravel Boost guidelines are specifically curated by Laravel maintainers for this application. These guidelines should be followed closely to enhance the user's satisfaction building Laravel applications.

## Foundational Context
This application is a Laravel application and its main Laravel ecosystems package & versions are below. You are an expert with them all. Ensure you abide by these specific packages & versions.

- php - 8.4.16
- filament/filament (FILAMENT) - v5
- laravel/fortify (FORTIFY) - v1
- laravel/framework (LARAVEL) - v12
- laravel/prompts (PROMPTS) - v0
- laravel/sanctum (SANCTUM) - v4
- livewire/livewire (LIVEWIRE) - v4
- laravel/mcp (MCP) - v0
- laravel/pint (PINT) - v1
- laravel/sail (SAIL) - v1
- phpunit/phpunit (PHPUNIT) - v11
- tailwindcss (TAILWINDCSS) - v4

## Conventions
- You must follow all existing code conventions used in this application. When creating or editing a file, check sibling files for the correct structure, approach, and naming.
- Use descriptive names for variables and methods. For example, `isRegisteredForDiscounts`, not `discount()`.
- Check for existing components to reuse before writing a new one.

## Verification Scripts
- Do not create verification scripts or tinker when tests cover that functionality and prove it works. Unit and feature tests are more important.

## Application Structure & Architecture
- Stick to existing directory structure; don't create new base folders without approval.
- Do not change the application's dependencies without approval.

## Frontend Bundling
- If the user doesn't see a frontend change reflected in the UI, it could mean they need to run `npm run build`, `npm run dev`, or `composer run dev`. Ask them.

## Replies
- Be concise in your explanations - focus on what's important rather than explaining obvious details.

## Documentation Files
- You must only create documentation files if explicitly requested by the user.

=== boost rules ===

## Laravel Boost
- Laravel Boost is an MCP server that comes with powerful tools designed specifically for this application. Use them.

## Artisan
- Use the `list-artisan-commands` tool when you need to call an Artisan command to double-check the available parameters.

## URLs
- Whenever you share a project URL with the user, you should use the `get-absolute-url` tool to ensure you're using the correct scheme, domain/IP, and port.

## Tinker / Debugging
- You should use the `tinker` tool when you need to execute PHP to debug code or query Eloquent models directly.
- Use the `database-query` tool when you only need to read from the database.

## Reading Browser Logs With the `browser-logs` Tool
- You can read browser logs, errors, and exceptions using the `browser-logs` tool from Boost.
- Only recent browser logs will be useful - ignore old logs.

## Searching Documentation (Critically Important)
- Boost comes with a powerful `search-docs` tool you should use before any other approaches when dealing with Laravel or Laravel ecosystem packages. This tool automatically passes a list of installed packages and their versions to the remote Boost API, so it returns only version-specific documentation for the user's circumstance. You should pass an array of packages to filter on if you know you need docs for particular packages.
- The `search-docs` tool is perfect for all Laravel-related packages, including Laravel, Inertia, Livewire, Filament, Tailwind, Pest, Nova, Nightwatch, etc.
- You must use this tool to search for Laravel ecosystem documentation before falling back to other approaches.
- Search the documentation before making code changes to ensure we are taking the correct approach.
- Use multiple, broad, simple, topic-based queries to start. For example: `['rate limiting', 'routing rate limiting', 'routing']`.
- Do not add package names to queries; package information is already shared. For example, use `test resource table`, not `filament 4 test resource table`.

### Available Search Syntax
- You can and should pass multiple queries at once. The most relevant results will be returned first.

1. Simple Word Searches with auto-stemming - query=authentication - finds 'authenticate' and 'auth'.
2. Multiple Words (AND Logic) - query=rate limit - finds knowledge containing both "rate" AND "limit".
3. Quoted Phrases (Exact Position) - query="infinite scroll" - words must be adjacent and in that order.
4. Mixed Queries - query=middleware "rate limit" - "middleware" AND exact phrase "rate limit".
5. Multiple Queries - queries=["authentication", "middleware"] - ANY of these terms.

=== php rules ===

## PHP

- Always use strict typing at the head of a `.php` file: `declare(strict_types=1);`.
- Always use curly braces for control structures, even if it has one line.

### Constructors
- Use PHP 8 constructor property promotion in `__construct()`.
    - <code-snippet>public function __construct(public GitHub $github) { }</code-snippet>
- Do not allow empty `__construct()` methods with zero parameters unless the constructor is private.

### Type Declarations
- Always use explicit return type declarations for methods and functions.
- Use appropriate PHP type hints for method parameters.

<code-snippet name="Explicit Return Types and Method Params" lang="php">
protected function isAccessible(User $user, ?string $path = null): bool
{
    ...
}
</code-snippet>

## Comments
- Prefer PHPDoc blocks over inline comments. Never use comments within the code itself unless there is something very complex going on.

## PHPDoc Blocks
- Add useful array shape type definitions for arrays when appropriate.

## Enums
- Typically, keys in an Enum should be TitleCase. For example: `FavoritePerson`, `BestLake`, `Monthly`.

=== tests rules ===

## Test Enforcement

- Every change must be programmatically tested. Write a new test or update an existing test, then run the affected tests to make sure they pass.
- Run the minimum number of tests needed to ensure code quality and speed. Use `php artisan test --compact` with a specific filename or filter.

=== laravel/core rules ===

## Do Things the Laravel Way

- Use `php artisan make:` commands to create new files (i.e. migrations, controllers, models, etc.). You can list available Artisan commands using the `list-artisan-commands` tool.
- If you're creating a generic PHP class, use `php artisan make:class`.
- Pass `--no-interaction` to all Artisan commands to ensure they work without user input. You should also pass the correct `--options` to ensure correct behavior.

### Database
- Always use proper Eloquent relationship methods with return type hints. Prefer relationship methods over raw queries or manual joins.
- Use Eloquent models and relationships before suggesting raw database queries.
- Avoid `DB::`; prefer `Model::query()`. Generate code that leverages Laravel's ORM capabilities rather than bypassing them.
- Generate code that prevents N+1 query problems by using eager loading.
- Use Laravel's query builder for very complex database operations.

### Model Creation
- When creating new models, create useful factories and seeders for them too. Ask the user if they need any other things, using `list-artisan-commands` to check the available options to `php artisan make:model`.

### APIs & Eloquent Resources
- For APIs, default to using Eloquent API Resources and API versioning unless existing API routes do not, then you should follow existing application convention.

### Controllers & Validation
- Always create Form Request classes for validation rather than inline validation in controllers. Include both validation rules and custom error messages.
- Check sibling Form Requests to see if the application uses array or string based validation rules.

### Queues
- Use queued jobs for time-consuming operations with the `ShouldQueue` interface.

### Authentication & Authorization
- Use Laravel's built-in authentication and authorization features (gates, policies, Sanctum, etc.).

### URL Generation
- When generating links to other pages, prefer named routes and the `route()` function.

### Configuration
- Use environment variables only in configuration files - never use the `env()` function directly outside of config files. Always use `config('app.name')`, not `env('APP_NAME')`.

### Testing
- When creating models for tests, use the factories for the models. Check if the factory has custom states that can be used before manually setting up the model.
- Faker: Use methods such as `$this->faker->word()` or `fake()->randomDigit()`. Follow existing conventions whether to use `$this->faker` or `fake()`.
- When creating tests, make use of `php artisan make:test [options] {name}` to create a feature test, and pass `--unit` to create a unit test. Most tests should be feature tests.

### Vite Error
- If you receive an "Illuminate\Foundation\ViteException: Unable to locate file in Vite manifest" error, you can run `npm run build` or ask the user to run `npm run dev` or `composer run dev`.

=== laravel/v12 rules ===

## Laravel 12

- Use the `search-docs` tool to get version-specific documentation.
- Since Laravel 11, Laravel has a new streamlined file structure which this project uses.

### Laravel 12 Structure
- In Laravel 12, middleware are no longer registered in `app/Http/Kernel.php`.
- Middleware are configured declaratively in `bootstrap/app.php` using `Application::configure()->withMiddleware()`.
- `bootstrap/app.php` is the file to register middleware, exceptions, and routing files.
- `bootstrap/providers.php` contains application specific service providers.
- The `app\Console\Kernel.php` file no longer exists; use `bootstrap/app.php` or `routes/console.php` for console configuration.
- Console commands in `app/Console/Commands/` are automatically available and do not require manual registration.

### Database
- When modifying a column, the migration must include all of the attributes that were previously defined on the column. Otherwise, they will be dropped and lost.
- Laravel 12 allows limiting eagerly loaded records natively, without external packages: `$query->latest()->limit(10);`.

### Models
- Casts can and likely should be set in a `casts()` method on a model rather than the `$casts` property. Follow existing conventions from other models.

=== livewire/core rules ===

## Livewire

- Use the `search-docs` tool to find exact version-specific documentation for how to write Livewire and Livewire tests.
- Use the `php artisan make:livewire [Posts\CreatePost]` Artisan command to create new components.
- State should live on the server, with the UI reflecting it.
- All Livewire requests hit the Laravel backend; they're like regular HTTP requests. Always validate form data and run authorization checks in Livewire actions.

## Livewire Best Practices
- Livewire components require a single root element.
- Use `wire:loading` and `wire:dirty` for delightful loading states.
- Add `wire:key` in loops:

    ```blade
    @foreach ($items as $item)
        <div wire:key="item-{{ $item->id }}">
            {{ $item->name }}
        </div>
    @endforeach
    ```

- Prefer lifecycle hooks like `mount()`, `updatedFoo()` for initialization and reactive side effects:

<code-snippet name="Lifecycle Hook Examples" lang="php">
    public function mount(User $user) { $this->user = $user; }
    public function updatedSearch() { $this->resetPage(); }
</code-snippet>

## Testing Livewire

<code-snippet name="Example Livewire Component Test" lang="php">
    Livewire::test(Counter::class)
        ->assertSet('count', 0)
        ->call('increment')
        ->assertSet('count', 1)
        ->assertSee(1)
        ->assertStatus(200);
</code-snippet>

<code-snippet name="Testing Livewire Component Exists on Page" lang="php">
    $this->get('/posts/create')
    ->assertSeeLivewire(CreatePost::class);
</code-snippet>

=== pint/core rules ===

## Laravel Pint Code Formatter

- You must run `vendor/bin/pint --dirty` before finalizing changes to ensure your code matches the project's expected style.
- Do not run `vendor/bin/pint --test`, simply run `vendor/bin/pint` to fix any formatting issues.

=== phpunit/core rules ===

## PHPUnit

- This application uses PHPUnit for testing. All tests must be written as PHPUnit classes. Use `php artisan make:test --phpunit {name}` to create a new test.
- If you see a test using "Pest", convert it to PHPUnit.
- Every time a test has been updated, run that singular test.
- When the tests relating to your feature are passing, ask the user if they would like to also run the entire test suite to make sure everything is still passing.
- Tests should test all of the happy paths, failure paths, and weird paths.
- You must not remove any tests or test files from the tests directory without approval. These are not temporary or helper files; these are core to the application.

### Running Tests
- Run the minimal number of tests, using an appropriate filter, before finalizing.
- To run all tests: `php artisan test --compact`.
- To run all tests in a file: `php artisan test --compact tests/Feature/ExampleTest.php`.
- To filter on a particular test name: `php artisan test --compact --filter=testName` (recommended after making a change to a related file).

=== tailwindcss/core rules ===

## Tailwind CSS

- Use Tailwind CSS classes to style HTML; check and use existing Tailwind conventions within the project before writing your own.
- Offer to extract repeated patterns into components that match the project's conventions (i.e. Blade, JSX, Vue, etc.).
- Think through class placement, order, priority, and defaults. Remove redundant classes, add classes to parent or child carefully to limit repetition, and group elements logically.
- You can use the `search-docs` tool to get exact examples from the official documentation when needed.

### Spacing
- When listing items, use gap utilities for spacing; don't use margins.

<code-snippet name="Valid Flex Gap Spacing Example" lang="html">
    <div class="flex gap-8">
        <div>Superior</div>
        <div>Michigan</div>
        <div>Erie</div>
    </div>
</code-snippet>

### Dark Mode
- If existing pages and components support dark mode, new pages and components must support dark mode in a similar way, typically using `dark:`.

=== tailwindcss/v4 rules ===

## Tailwind CSS 4

- Always use Tailwind CSS v4; do not use the deprecated utilities.
- `corePlugins` is not supported in Tailwind v4.
- In Tailwind v4, configuration is CSS-first using the `@theme` directive — no separate `tailwind.config.js` file is needed.

<code-snippet name="Extending Theme in CSS" lang="css">
@theme {
  --color-brand: oklch(0.72 0.11 178);
}
</code-snippet>

- In Tailwind v4, you import Tailwind using a regular CSS `@import` statement, not using the `@tailwind` directives used in v3:

<code-snippet name="Tailwind v4 Import Tailwind Diff" lang="diff">
   - @tailwind base;
   - @tailwind components;
   - @tailwind utilities;
   + @import "tailwindcss";
</code-snippet>

### Replaced Utilities
- Tailwind v4 removed deprecated utilities. Do not use the deprecated option; use the replacement.
- Opacity values are still numeric.

| Deprecated |	Replacement |
|------------+--------------|
| bg-opacity-* | bg-black/* |
| text-opacity-* | text-black/* |
| border-opacity-* | border-black/* |
| divide-opacity-* | divide-black/* |
| ring-opacity-* | ring-black/* |
| placeholder-opacity-* | placeholder-black/* |
| flex-shrink-* | shrink-* |
| flex-grow-* | grow-* |
| overflow-ellipsis | text-ellipsis |
| decoration-slice | box-decoration-slice |
| decoration-clone | box-decoration-clone |

=== filament/blueprint rules ===

## Filament Blueprint

You are writing Filament v5 implementation plans. Plans must be specific enough
that an implementing agent can write code without making decisions.

**Start here**: Read
`/vendor/filament/blueprint/resources/markdown/planning/overview.md` for plan format,
required sections, and what to clarify with the user before planning.

### Filament v5 Key Namespaces

| Element        | Namespace Pattern                         |
| -------------- | ----------------------------------------- |
| Form fields    | `Filament\Forms\Components\{Component}`   |
| Table columns  | `Filament\Tables\Columns\{Column}`        |
| Table filters  | `Filament\Tables\Filters\{Filter}`        |
| Actions        | `Filament\Actions\{Action}`               |
| Infolist       | `Filament\Infolists\Components\{Entry}`   |
| Layout         | `Filament\Schemas\Components\{Component}` |
| Reactive utils | `Filament\Schemas\Components\Utilities\*` |

### Blueprint Planning Files

When creating Filament implementation plans, reference these files:

| File                 | Purpose                                |
| -------------------- | -------------------------------------- |
| overview.md          | Plan structure and required sections   |
| models.md            | Model attributes, relationships, enums |
| resources.md         | Resource specification format          |
| custom-pages.md      | Standalone pages and resource pages    |
| forms.md             | Form field components and validation   |
| tables.md            | Table columns, filters, summarizers    |
| infolists.md         | Infolist entries for View pages        |
| schema-layouts.md    | Layout components (Section, Tabs, etc) |
| relationships.md     | Relationship handling decisions        |
| actions.md           | Custom actions with modals             |
| reactive-fields.md   | Reactive fields (Get/Set)              |
| authorization.md     | Policies and permissions               |
| widgets.md           | Dashboard widgets                      |
| testing.md           | Test specifications                    |
| checklist.md         | Pre-submission checklist               |

Files located at: `/vendor/filament/blueprint/resources/markdown/planning/`

</laravel-boost-guidelines>
