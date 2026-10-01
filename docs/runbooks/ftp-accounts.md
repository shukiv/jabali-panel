# FTP / SFTP accounts (GH #1053)

Tenant-created file-transfer subaccounts. Each account has its own
username (`<tenant>_<label>`), its own password and its own directory.
There are two models, chosen per account (`ftp_accounts.isolated`):

- **Isolated (separate uid, GH #1145).** The default where filesystem
  quota is enabled. The account is its own system user: its own uid
  (from 1000000000 up, allocated by the panel and never reused) and its own
  primary group. It is chrooted to a root-owned jail,
  `/var/lib/jabali-ftp-jails/<tenant>/<label>` (root:root 0755), where the
  selected directory is bind-mounted at `/data`. `..` from `/data` reaches only
  the empty jail root. Files it writes are owned by **the account's own
  uid**. POSIX ACLs (access and default) give both the account and the tenant
  `rwX` on the selected tree, so the tenant can still manage those files.
  Disk use counts against the account's own per-uid quota (`quota_mb`).
- **Same-uid alias (legacy).** The fallback where quota is not available,
  or when the Isolated toggle is turned off at create time. A second
  passwd entry sharing the tenant's uid (`useradd --non-unique`), chrooted
  to the tenant home. Every file it writes is owned by the tenant, so quotas,
  per-user PHP-FPM, AppArmor and backups behave exactly as if the tenant
  wrote it. An SFTP session can `..` out of its start directory to the whole
  tenant home (JAB-252), which is why isolated is the default.

- **SFTP** always works for these accounts (port 22, per-account
  `Match User` blocks in `/etc/ssh/sshd_config.d/jabali-xfer.conf`,
  password auth only). The chroot is the jail for an isolated account and
  the tenant home for an alias.
- **FTPS** works only after the server-level opt-in below.

## Enabling FTP (server level, default OFF)

Server Settings → SSH & FTP → **Enable FTP server**. This flips
`server_settings.ftp_enabled`, which:

1. installs + starts vsftpd (module install-on-enable; also converged by
   the reconciler if the install is interrupted),
2. renders `/etc/vsftpd.conf` from the DB (TLS from the panel-hostname
   cert; `force_local_logins_ssl=YES` unless the plaintext toggle is on),
3. opens `21/tcp` and the passive range `40000:40100/tcp` in UFW,
4. installs the CrowdSec `crowdsecurity/vsftpd` collection + acquisition.

Turning it OFF stops + masks vsftpd and closes both firewall rules on the
next `jabali update` sweep (`converge_ftp_masking`). SFTP is unaffected.

Auth is PAM service `vsftpd-jabali`: only members of the `jabali-ftp`
group (= the per-account FTPS toggle) can log in. The PAM file
deliberately omits `pam_shells.so` — subaccounts use `/usr/sbin/nologin`.
Do not "fix" a failing FTP login by adding nologin to `/etc/shells`; that
widens every other pam_shells consumer.

## NAT / passive mode

If the box sits behind NAT, set **Passive-mode address** to the public IP
(vsftpd's `pasv_address`). The passive port range is fixed at 40000–40100;
if you change it in `/etc/vsftpd.conf` by hand it will be overwritten on
the next module re-render, and the UFW rule + the
`install/tests/test_ftp_module_optin.sh` guard both assume the shipped
range.

## Connection details (what to tell users)

- SFTP: `sftp <tenant>_<label>@<panel-hostname>` port 22 (password).
- FTPS: host `<panel-hostname>`, port 21, explicit TLS, passive mode. The
  TLS certificate is the PANEL hostname's — clients connecting to their
  own domain name will see a name mismatch; point them at the panel host.

## Resource limits (what bounds FTPS load)

FTPS sessions are **daemon-bounded, not per-tenant cgroup-bounded** (JAB-263,
ADR-0167). The M18 CPU/memory/task limits on `jabali-user-<tenant>.slice` bind
only panel services (php-fpm, python, docker); a vsftpd worker runs under
`vsftpd.service` and is never migrated into the tenant slice — the same is true
of SSH/SFTP interactive logins. What DOES bound FTPS, from `server_settings`
into `/etc/vsftpd.conf`:

- `max_clients` — total concurrent FTPS connections on the host (always on,
  default 50).
- `max_per_ip` — concurrent connections per source IP (always on, default 8).
- `local_max_rate` — per-session transfer-rate ceiling. **Opt-in**: default 0 =
  unlimited; set `ftp_local_max_rate_kbs` to throttle per session.

There is no per-*tenant* connection cap: a tenant behind one IP is held by
`max_per_ip`, but across many IPs only the global `max_clients` applies, and at
the default rate each session is unthrottled. Disk is still bounded: an alias
writes into the tenant's own quota, and an isolated account into its per-uid
quota. Per-tenant cgroup placement of interactive sessions is
deferred to the session-placement epic (JAB-259/260).

## Tenant home permission flip

The FIRST SFTP-enabled subaccount a tenant creates flips
`/home/<tenant>` to `root:<tenant> 0751` (the M12 chroot layout — sshd
refuses to chroot into a non-root-owned directory). This also happens for
tenants who never enabled SSH themselves. Consequence: the tenant cannot
create files directly in the TOP level of their home anymore (subdirs are
untouched). This is the established M12 trade-off, applied to a new
population.

## Disaster recovery / drift

The `ftp_accounts` DB table is the truth; the reconciler re-creates
missing passwd aliases on its tick. A recreated alias gets an UNKNOWABLE
random password — the tenant must reset it in the panel before the
account can log in again (deliberate: the real password only ever lived
in `/etc/shadow`). Stray aliases with no DB row are removed.

An isolated account's bind mount does not survive a reboot or a manual
`umount`. The reconciler re-mounts it on its next tick (`ftp.ensure_jail`,
called for every isolated row each pass), so expect up to a minute after
boot before those accounts can log in.

## Troubleshooting

| Symptom | Check |
|---|---|
| FTP login fails, SFTP works | Is the account's FTPS toggle on (`id <name>` shows `jabali-ftp`)? Is `ftp_enabled` on? `systemctl status vsftpd` |
| "530 Login incorrect" for a valid password | Account disabled in the panel (`usermod -L` lock)? `passwd -S <name>` shows `L` |
| SFTP connection resets after auth | `sshd -t`; is `/home/<tenant>` root-owned 0751? Never add a subaccount to `jabali-sftp` — its `/home/%u` chroot cannot fit an alias username |
| Passive transfers hang | NAT without `pasv_address` set, or 40000:40100/tcp closed upstream |
| Isolated account: SFTP resets after auth, or lands in an empty directory | `findmnt /var/lib/jabali-ftp-jails/<tenant>/<label>/data` shows the bind mount? If not, the next reconcile tick re-mounts it. The jail and its parents must stay root-owned 0755 |
| Isolated account's uploads return 404 in the browser | nginx (www-data) must be able to read the folder the account is jailed to. Folders under a docroot are normally group www-data with setgid; one that lost that group blocks every file inside it, and the nginx error log shows `Permission denied`. `jabali domain fix-perms <tenant>` restores group www-data and setgid across the tenant's docroots |
| Deleting an account fails | Should never block on running processes — the agent uses `userdel -f` (for an alias, the tenant's own FPM holds the shared uid). Deleting an isolated account also unmounts and removes its jail, never the bind-mount source. Check the panel log for the agent error |
| Brute-force noise | `cscli decisions list --scenario crowdsecurity/vsftpd-bf` |
