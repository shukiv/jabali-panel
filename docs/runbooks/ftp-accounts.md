# FTP / SFTP accounts (GH #1053)

Tenant-created file-transfer subaccounts. Each account is a second passwd
entry sharing the owning tenant's uid (`useradd --non-unique`): its own
username (`<tenant>_<label>`), its own password, its own start directory —
but every file it writes is owned by the tenant, so quotas, per-user
PHP-FPM, AppArmor, and backups behave exactly as if the tenant wrote it.

- **SFTP** always works for these accounts (port 22, per-account
  `Match User` blocks in `/etc/ssh/sshd_config.d/jabali-xfer.conf`,
  chrooted to the tenant home, password auth only).
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

## Troubleshooting

| Symptom | Check |
|---|---|
| FTP login fails, SFTP works | Is the account's FTPS toggle on (`id <name>` shows `jabali-ftp`)? Is `ftp_enabled` on? `systemctl status vsftpd` |
| "530 Login incorrect" for a valid password | Account disabled in the panel (`usermod -L` lock)? `passwd -S <name>` shows `L` |
| SFTP connection resets after auth | `sshd -t`; is `/home/<tenant>` root-owned 0751? Never add a subaccount to `jabali-sftp` — its `/home/%u` chroot cannot fit an alias username |
| Passive transfers hang | NAT without `pasv_address` set, or 40000:40100/tcp closed upstream |
| Deleting an account fails | Should never block on running processes — the agent uses `userdel -f` (the tenant's own FPM holds the shared uid). Check the panel log for the agent error |
| Brute-force noise | `cscli decisions list --scenario crowdsecurity/vsftpd-bf` |
