# Admin Dashboard

`/jabali-admin/dashboard`. First page after admin login. The layout is the one voted for in [discussion #1249](https://github.com/shukiv/jabali-panel/discussions/1249) (Option 7, "community mix").

## Banners

The update banners sit above everything else: the panel is behind its release channel, or OS security updates are waiting. See [Server updates](./server-updates.md).

## Host card

- Hostname, with a health tag: **Healthy**, or the number of warnings or critical alerts. Click the tag to open the **Alerts** tab.
- The server's public IPv4 and IPv6, each with a copy button.
- The OS, the panel build (commit) and the uptime.
- CPU, Memory and Disk meters. Disk is the root filesystem's `df` Use%. The colours follow the same thresholds as [Server Status](./server-status.md): disk turns orange at 80% and red at 95%.
- **View server status** opens the full [Server Status](./server-status.md) page.

## Count cards

Users, Domains, Mailboxes and Applications, each a link to its list. The four numbers come from one request, `GET /api/v1/admin/counts`. The Mailboxes card is hidden when the mail module is off.

## Tabs

| Tab | Shows |
|---|---|
| Activity | The newest audit-log rows: who did what, to what, and when. **Open the Audit Log** for the full [Audit log](./audit-log.md). |
| Domains / Users / Databases | The five newest of each, with **View all** links. |
| Hosting Packages | The packages and whether they include SSH. |
| Backups | The newest backup runs and manual backup jobs, with their status. |
| Alerts | The host alerts Server Status raises, critical first. The tab's badge shows how many there are. |

Only the open tab loads its list, so the Dashboard makes no extra requests for tabs nobody opens. The open tab is kept in the address (`?tab=alerts`), so a link or a reload lands on the same tab.

## Services

The right-hand card lists the watched services and how many of those meant to run are running. A service that is off by design (for example webmail before any domain enables mail) shows as **idle** and does not count as missing. On a phone the card moves below the tabs.

The card is read-only. Start, stop and restart a service on [Server Status](./server-status.md).

## Source

`panel-ui/src/shells/admin/Dashboard.tsx` and `panel-ui/src/shells/admin/dashboard/`. The host card, the services card and the Alerts tab read the same `GET /api/v1/admin/server-status` response the Server Status page uses.
