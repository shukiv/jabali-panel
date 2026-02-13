---
title: Jabali CLI
description: Full command reference with options and examples.
sidebar:
  order: 6
---

The Jabali CLI is a full-featured administrative interface to the panel and the privileged agent. Use it for automation, server tasks, backups, migrations, and operational maintenance.

## Run location

Run all commands from the repo root:

~~~
cd /var/www/jabali
~~~

## Global help and options

~~~
jabali --help
jabali --help-full
jabali --version
~~~

Global options:

- `-h`, `--help` show help
- `--help-full` show full command list
- `-v`, `--version` show version
- `-y`, `--yes` auto-confirm prompts
- `-q`, `--quiet` quiet mode

## User management

Commands:

~~~
jabali user list
jabali user create <username> [--email=<email>] [--password=<password>]
jabali user show <username>
jabali user delete <username>
jabali user password <username> [--password=<password>]
jabali user suspend <username>
jabali user unsuspend <username>
~~~

Notes:

- `user create` creates a system user through the agent and a panel user in the database.
- `user password` updates both the panel password and system user password.

## Domain management

Commands:

~~~
jabali domain list [--user=<username>]
jabali domain create <domain> --user=<username>
jabali domain show <domain>
jabali domain delete <domain>
jabali domain enable <domain>
jabali domain disable <domain>
~~~

Notes:

- `domain create` triggers agent provisioning and then creates the panel record.

## Service management

Commands:

~~~
jabali service list
jabali service status <service>
jabali service start <service>
jabali service stop <service>
jabali service restart <service>
jabali service enable <service>
jabali service disable <service>
~~~

Services include common system daemons and any installed PHP-FPM versions.

## WordPress tools

Commands:

~~~
jabali wp list <username>
jabali wp install <username> <domain> [--title=<title>] [--admin=<user>] [--email=<email>] [--password=<pass>]
jabali wp scan <username>
jabali wp import <username> <path>
jabali wp delete <username> <site_id> [--files] [--database]
jabali wp update <username> <site_id>
~~~

Notes:

- `wp install` will generate a password if one is not provided.
- `wp scan` discovers existing WordPress installs under a user.

## Database tools (MariaDB)

Commands:

~~~
jabali db list [--user=<username>]
jabali db create <db_name> [--user=<username>]
jabali db delete <db_name>
jabali db users [--user=<username>]
jabali db user-create <username> [--password=<password>] [--host=<host>]
jabali db user-delete <username> [--host=<host>]
~~~

Notes:

- `db list` defaults to `admin` unless `--user` is provided.
- User creation validates password complexity if provided.

## Email (mailboxes)

Commands:

~~~
jabali mail list [--domain=<domain>]
jabali mail create <email> [--password=<password>] [--quota=<mb>]
jabali mail delete <email>
jabali mail password <email> [--password=<password>]
jabali mail quota <email> <size_mb>
jabali mail domains
~~~

Notes:

- `mail domains` lists domains with mail enabled and DKIM status.

## Backups (users + server)

### Local and user backups

~~~
jabali backup list [--user=<user>]
jabali backup user-list <user>
jabali backup create <user> [--type=full|incremental] [--output=<path>] [--incremental-base=<path>]
  [--domains=a,b] [--databases=a,b] [--mailboxes=a,b]
  [--no-files] [--no-databases] [--no-mailboxes] [--no-dns] [--no-ssl]

jabali backup restore <path> [<user>]
  [--user=<user>] [--domains=a,b] [--databases=a,b] [--mailboxes=a,b]
  [--no-files] [--no-databases] [--no-mailboxes] [--no-dns] [--no-ssl]

jabali backup info <path>
jabali backup verify <path>
jabali backup delete <file|id> [--user=<user>]
~~~

### Server backups

~~~
jabali backup server [--type=full|incremental] [--users=u1,u2] [--dest=<id>]
jabali backup server-list
~~~

### Backup history (database)

~~~
jabali backup history [--limit=<n>] [--status=<status>] [--type=<type>]
jabali backup show <id>
~~~

### Backup schedules

~~~
jabali backup schedules
jabali backup schedule-create --name=<name> [--frequency=daily|weekly] [--time=HH:MM]
  [--retention=<n>] [--dest=<id>] [--backup-type=full|incremental]
  [--no-files] [--no-databases] [--no-mailboxes] [--no-dns]

jabali backup schedule-run <id>
jabali backup schedule-enable <id>
jabali backup schedule-disable <id>
jabali backup schedule-delete <id>
~~~

### Backup destinations

~~~
jabali backup destinations
jabali backup dest-add --type=sftp --name=<name> --host=<host> --user=<user> [--password=<pass>] [--port=22] [--path=/backups]

jabali backup dest-add --type=nfs --name=<name> --host=<host> --path=<remote-path> [--mount=/mnt/backup]

jabali backup dest-add --type=s3 --name=<name> --bucket=<name> --key=<access-key> --secret=<secret-key> [--region=us-east-1] [--path=prefix]

jabali backup dest-test <id>
jabali backup dest-delete <id>
~~~

## cPanel migration

Commands:

~~~
jabali cpanel analyze <file> [--timeout=600]
jabali cpanel restore <file> <user>
  [--no-files] [--no-databases] [--no-emails] [--no-ssl]
  [--log=/path/to/log.jsonl] [--analyze] [--timeout=7200]

jabali cpanel fix-permissions <file>
~~~

Notes:

- `cpanel restore` requires the panel user and the system user to already exist.
- `--analyze` runs analysis and reuses results before restore.

## System information

Commands:

~~~
jabali system info
jabali system status
jabali system hostname [<new-hostname>]
jabali system disk
jabali system memory
~~~

## Agent control

Commands:

~~~
jabali agent status
jabali agent start
jabali agent stop
jabali agent restart
jabali agent ping
jabali agent log [--lines=<n>]
~~~

## PHP versions

Commands:

~~~
jabali php list
jabali php install <version>
jabali php uninstall <version>
jabali php default [<version>]
jabali php status
~~~

Notes:

- `php default` without a version prints the current default.

## Firewall (UFW)

Commands:

~~~
jabali firewall status
jabali firewall enable
jabali firewall disable
jabali firewall rules
jabali firewall allow <port>
jabali firewall deny <port>
jabali firewall delete <rule_number>
~~~

## SSL certificates

Commands:

~~~
jabali ssl check [<domain>] [--issue-only] [--renew-only]
jabali ssl issue <domain> [--force]
jabali ssl renew <domain>
jabali ssl status <domain>
jabali ssl list
~~~

## Example workflows

### Onboard a new user and domain

~~~
jabali user create demo --email=demo@example.com
jabali domain create example.com --user=demo
jabali ssl issue example.com
~~~

### Create and verify a user backup

~~~
jabali backup create demo --type=full
jabali backup verify /home/demo/backups/demo_2026-02-04_120000.tar.gz
~~~

### Run a cPanel migration

~~~
jabali cpanel analyze /var/backups/jabali/cpanel-migrations/site.tar.gz
jabali cpanel restore /var/backups/jabali/cpanel-migrations/site.tar.gz demo
~~~
