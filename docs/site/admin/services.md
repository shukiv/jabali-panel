# Services

The Services card on [Server Status](./server-status.md): the systemd units the panel shows, and what you can do to each one.

## What you can do here

Each row shows the unit's name, its state and its buttons. A unit's buttons depend on its state:

- **Start**: shown when the unit is inactive or failed.
- **Restart**: shown when the unit is running.
- **Reload**: shown when `nginx` or `pdns` is running. A reload does not drop connections in progress.
- **Stop**: shown when the unit is running. It is not shown for the units in [Units you cannot stop](#units-you-cannot-stop).
- **Enable at boot** / **Disable at boot**: change the unit's boot-time autostart. Disable is not shown for the units in [Units you cannot stop](#units-you-cannot-stop).

Stop, Disable and Restart ask you to confirm first. The agent runs each action, and the panel logs it with the admin who ran it (`event=audit kind=service_action`). Only admins can use these buttons.

An inactive unit that is not enabled at boot shows a grey **idle** tag instead of a red one. That unit is idle on purpose. For example, `jabali-webmail` starts only when a domain turns email on.

## Watched units

| Unit | Role | Shown when |
|---|---|---|
| `jabali-panel.service` | The panel API and the web UI. | Always. |
| `jabali-agent.service` | The root agent that performs every privileged host operation. | Always. |
| `jabali-kratos.service` | Identity: login, 2FA and recovery. | Always. |
| `nginx.service` | Reverse proxy and web server for every site. | Always. |
| `mariadb.service` | The panel database and tenant MariaDB databases. | Always. |
| `redis-server.service` | The notification dispatcher stream and the panel cache. | Always. |
| `ssh.service` | OpenSSH. | Always. |
| `jabali-stalwart.service` | Mail: SMTP, IMAP, POP3, JMAP and the mailbox store. | The mail module is on. |
| `jabali-webmail.service` | Bulwark webmail. | The mail module is on and the unit is running. |
| `pdns.service` | Authoritative PowerDNS. | The DNS module is on. |
| `postgresql.service` | Tenant PostgreSQL databases. | PostgreSQL is turned on and the unit is running. |
| `docker.service` | The Docker engine for the app marketplace. | The app marketplace is turned on. |

A unit that is not installed or is masked is not shown. The list is the agent's allow-list, and the agent refuses an action on any unit outside it.

PHP-FPM, `pdns-recursor`, CrowdSec, cron and the timers are not on this card. PHP-FPM runs as one `jabali-fpm@<user>.service` per panel user; the distro `php<ver>-fpm` units are masked. The scheduled jobs Jabali installs (its timers) are listed under **Admin → Cron Jobs → System jobs**, with Run now and their log. Use `systemctl` from a shell for anything else.

## Units you cannot stop

The panel refuses Stop and Disable for these units with `403 self_destruct_blocked`:

- `jabali-panel`: the panel you are using.
- `jabali-agent`: the only path from the panel to systemd.
- `jabali-kratos`: every login and session refresh goes through it.
- `mariadb`: the panel's own database.
- `nginx`: the panel is served through it.
- `redis-server`: the panel process needs it.

Stopping any of these would lock you out of the panel. Restart is still allowed. To stop one of them, use `systemctl` from a shell on the host.

## Alerts when a unit goes down

The `service.down` notification checks its own list of units once a minute:

- `jabali-panel`, `jabali-agent` and `jabali-kratos`
- `jabali-stalwart` and `jabali-webmail`
- `pdns` and `pdns-recursor`
- `nginx`, `mariadb` and `redis-server`

A `failed` unit sends the notification at once. An inactive unit sends it only when the unit is enabled at boot and stays inactive for 2 minutes; the delay skips the short restarts that `jabali update` does. A unit that is disabled, masked or not installed never sends it. See [Notification events](./notifications-events.md).

## CLI

```bash
jabali service status                                                 # the same list as JSON
jabali service action <name> <restart|start|stop|reload|enable|disable> [--force]
```

Stop, restart and disable need `--force`. `systemctl status <unit>` from a shell gives the full systemd output.
