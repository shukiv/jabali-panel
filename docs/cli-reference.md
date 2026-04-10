# CLI Command Reference

Jabali provides a comprehensive command-line interface using the `jabali` command with a noun:verb pattern. All commands support `--json` output for automation and `--yes` to skip confirmation prompts.

## User Management

### Create User
```bash
jabali user:create <username> [--email=] [--password=] [--admin] [--force]
```
Creates a new hosting account. The `--admin` flag grants administrative access. `--force` skips validation.

### Delete User
```bash
jabali user:delete <username>
```
Removes a user account and all associated data. Requires confirmation.

### List Users
```bash
jabali user:list [--json]
```
Shows all hosting accounts on the system.

### Show User Details
```bash
jabali user:show <username> [--json]
```
Displays account details including quotas, domains, and suspension status.

### Change Password
```bash
jabali user:password <username> [--password=]
```
Updates user account password. If `--password` is omitted, a secure prompt appears.

### Suspend User
```bash
jabali user:suspend <username>
```
Temporarily disables a user account. The account and its domains remain, but services are inaccessible.

### Unsuspend User
```bash
jabali user:unsuspend <username>
```
Reactivates a suspended user account.

### Grant/Revoke Admin
```bash
jabali user:admin <identifier> [--revoke] [--force]
```
Grants admin access to a user or revokes it with `--revoke`. The identifier can be username or email.

## Domain Management

### Create Domain
```bash
jabali domain:create <domain> [--user=]
```
Adds a domain to the system. Assign to a user with `--user=username`, or leave unassigned.

### Delete Domain
```bash
jabali domain:delete <domain>
```
Removes a domain and all associated files, emails, and DNS records.

### Enable/Disable Domain
```bash
jabali domain:enable <domain>
jabali domain:disable <domain>
```
Toggles domain accessibility without deleting data. Disabled domains respond with HTTP 410.

### List Domains
```bash
jabali domain:list [--user=]
```
Shows all domains. Filter by user with `--user=username`.

### Show Domain Details
```bash
jabali domain:show <domain> [--json]
```
Displays configuration, SSL status, FTP users, and current resource usage.

## Database Management

### Create Database
```bash
jabali db:create <name> [--user=]
```
Creates a new MySQL database.

### Delete Database
```bash
jabali db:delete <name>
```
Permanently removes a database. Requires confirmation.

### List Databases
```bash
jabali db:list [--user=]
```
Shows all databases, optionally filtered by user.

### Create Database User
```bash
jabali db:user-create <name> [--password=] [--database=]
```
Creates a database user with optional assignment to a specific database.

### Delete Database User
```bash
jabali db:user-delete <name>
```
Removes a database user. Databases owned by this user are not affected.

### List Database Users
```bash
jabali db:users [--json]
```
Shows all database users and their privilege assignments.

### Tune Database Parameters
```bash
jabali db:tune <variable> <value> [--persist]
```
Adjusts MySQL configuration variables at runtime. Use `--persist` to write to my.cnf.

## Email Management

### Create Email Account
```bash
jabali mail:create <email> [--password=] [--quota=]
```
Creates a mailbox with optional quota (e.g., `1GB`, `500MB`). Default is unlimited.

### Delete Email Account
```bash
jabali mail:delete <email>
```
Removes a mailbox and all messages.

### Change Email Password
```bash
jabali mail:password <email> [--password=]
```
Updates mailbox password.

### Set Email Quota
```bash
jabali mail:quota <email> [--size=]
```
Adjusts storage quota for a mailbox (e.g., `2GB`). Omit to set unlimited.

### List Email Accounts
```bash
jabali mail:list [--domain=]
```
Shows all mailboxes, optionally filtered by domain.

### View Mail Log
```bash
jabali mail:log [--lines=50] [--domain=] [--type=]
```
Displays recent mail server activity. Filter by domain and log type (e.g., `smtp`, `delivery`).

### Mail Queue
```bash
jabali mail:queue [--json]
```
Shows messages pending delivery.

### Queue Retry
```bash
jabali mail:queue-retry [id] [--all]
```
Retries a specific message or all queued messages.

### Queue Delete
```bash
jabali mail:queue-delete <id>
```
Permanently removes a queued message.

## SSL/TLS Management

### Issue Certificate
```bash
jabali ssl:issue <domain> [--force]
```
Requests a new certificate from Let's Encrypt. Use `--force` to overwrite existing.

### Renew Certificate
```bash
jabali ssl:renew <domain>
```
Manually renews an existing certificate.

### Check Certificate Status
```bash
jabali ssl:check [domain] [--issue-only] [--renew-only]
```
Validates all certificates or a specific domain. `--issue-only` checks for missing certs, `--renew-only` only for expiring certs.

### List Certificates
```bash
jabali ssl:list [--json]
```
Shows all installed certificates and expiration dates.

### Check Domain SSL Status
```bash
jabali ssl:status <domain>
```
Displays certificate details, chain, and expiration for a domain.

### Panel Certificate
```bash
jabali ssl:panel [--json]
```
Shows the panel's current SSL certificate status.

### Issue Panel Certificate
```bash
jabali ssl:panel-issue [--json]
```
Issues a new certificate for the control panel itself.

## DNS Management

### List DNS Records
```bash
jabali dns:list [--json]
```
Shows all DNS records for all zones.

### Zone Records
```bash
jabali dns:records <domain> [--type=] [--json]
```
Displays records for a specific domain. Filter by type (A, MX, CNAME, etc.) with `--type=`.

### Add DNS Record
```bash
jabali dns:add <domain> <name> <type> <content> [--ttl=3600] [--priority=0]
```
Creates a new DNS record. Priority is used for MX records.

### Delete DNS Record
```bash
jabali dns:delete-record <domain> <name> <type>
```
Removes a DNS record.

### Sync Zone
```bash
jabali dns:sync <domain>
```
Synchronizes a zone from PowerDNS (useful after bulk imports).

## Cron Job Management

### Create Cron Job
```bash
jabali cron:create [--user=] [--schedule=] [--command=] [--json]
```
Adds a scheduled task. Schedule uses standard crontab format (e.g., `0 2 * * *`).

### List Cron Jobs
```bash
jabali cron:list [--user=] [--json]
```
Shows all scheduled tasks for a user.

### Run Cron Job
```bash
jabali cron:run <index> [--user=] [--json]
```
Manually executes a scheduled task (identified by index from list).

### Delete Cron Job
```bash
jabali cron:delete <index> [--user=]
```
Removes a scheduled task.

### Toggle Cron Job
```bash
jabali cron:toggle <index> [--user=] [--enable|--disable]
```
Enables or disables a cron job without deleting it.

## PHP Version Management

### List PHP Versions
```bash
jabali php:list [--json]
```
Shows all installed PHP versions.

### Install PHP Version
```bash
jabali php:install <version>
```
Compiles and installs a specific PHP version (e.g., `8.3`, `8.4`).

### Uninstall PHP Version
```bash
jabali php:uninstall <version>
```
Removes a PHP version from the system.

### Set Default PHP Version
```bash
jabali php:default <version>
```
Sets the default PHP version for new domains.

## Service Management

### List Services
```bash
jabali service:list [--json]
```
Shows all monitored services and their status.

### Start Service
```bash
jabali service:start <name>
```
Starts a stopped service.

### Stop Service
```bash
jabali service:stop <name>
```
Stops a running service.

### Restart Service
```bash
jabali service:restart <name>
```
Restarts a service.

### Enable Service
```bash
jabali service:enable <name>
```
Enables automatic startup on boot.

### Disable Service
```bash
jabali service:disable <name>
```
Disables automatic startup, service will stop on reboot.

### Service Status
```bash
jabali service:status <name>
```
Displays current status, uptime, and memory usage.

## System Information

### System Status
```bash
jabali system:status [--json]
```
Overall system health: uptime, load average, service summary.

### System Info
```bash
jabali system:info [--json]
```
Hardware and software inventory: OS, CPU, RAM, kernel version.

### Disk Usage
```bash
jabali system:disk [--json]
```
Filesystem usage by partition and mount point.

### Memory Usage
```bash
jabali system:memory [--json]
```
RAM and swap utilization with breakdown by component.

### Set Hostname
```bash
jabali system:hostname [name] [--json]
```
Displays or updates system hostname.

### Kill Process
```bash
jabali system:kill <pid> [--signal=TERM]
```
Terminates a process by PID. Use `--signal=KILL` for force termination.

## WordPress Management

### Install WordPress
```bash
jabali wp:install <domain> [--user=] [--title=] [--admin-user=] [--admin-email=] [--admin-password=] [--json]
```
Downloads and configures WordPress with optional site details.

### List WordPress Installs
```bash
jabali wp:list [--user=] [--json]
```
Shows all WordPress installations, optionally filtered by user.

### Update WordPress
```bash
jabali wp:update <domain>
```
Checks for and applies the latest WordPress core update.

### Scan WordPress
```bash
jabali wp:scan <domain>
```
Scans for security issues, malware, and plugin vulnerabilities.

### Delete WordPress
```bash
jabali wp:delete <domain>
```
Removes a WordPress installation while preserving the domain.

### Import cPanel Backup
```bash
jabali wp:import <path> [--user=]
```
Restores WordPress and database from a cPanel backup file.

## Resource Limits (cgroup v2)

### Check cgroup Availability
```bash
jabali cgroup check [--json]
```
Verifies cgroups v2 is available on the server. Reports version and available controllers.

### Apply Resource Limits
```bash
jabali cgroup apply <username> [--all] [--json]
```
Creates or updates the systemd slice for a user based on their effective resource limits (user override or hosting package defaults). Use `--all` to apply limits to all users with configured limits.

### Show Resource Usage
```bash
jabali cgroup status <username> [--json]
```
Displays live resource usage vs limits for a user: CPU time, memory used/limit, process count, and I/O bytes.

## Agent Management

### Agent Status
```bash
jabali agent:status [--json]
```
Displays agent health, version, and connectivity.

### Agent Ping
```bash
jabali agent:ping [--json]
```
Tests connectivity to the privileged agent daemon.

### Agent Log
```bash
jabali agent:log [--lines=50] [--json]
```
Shows recent agent operation log entries.

### Agent Restart
```bash
jabali agent:restart [--json]
```
Restarts the privileged agent service. Use with caution.

## Support & Diagnostics

### Generate Diagnostic Report
```bash
jabali logs:share [--ttl=86400] [--raw] [--json]
```
Creates an encrypted diagnostic package with logs for support staff. Default TTL is 24 hours. `--raw` skips encryption.

### One-Time Login Token
```bash
jabali login:token [--user=] [--ttl=15] [--panel=] [--json]
```
Generates a secure token for password-less panel login. TTL in minutes, panel is `admin` or `user` (default: user).

## cPanel Migration

### Analyze cPanel Backup
```bash
jabali cpanel:analyze <path>
```
Scans a cPanel backup file and reports importable content.

### Restore cPanel Backup
```bash
jabali cpanel:restore <path> [--user=]
```
Imports users, domains, databases, and email from a cPanel backup.

### Fix File Permissions
```bash
jabali cpanel:fix-permissions <username>
```
Repairs ownership and permissions after cPanel import.

## Help

### Show Help
```bash
jabali help
```
Displays available command categories and command syntax.

## Common Options

All commands accept these global options:

| Option | Effect |
|--------|--------|
| `--json` | Output in JSON format for scripting |
| `--yes` | Skip confirmation prompts |
| `-v` | Verbose output |
| `-q` | Quiet mode (minimal output) |

## Exit Codes

- `0` — Success
- `1` — Command failed
- `2` — Invalid arguments
- `3` — Configuration error
- `4` — Permission denied
