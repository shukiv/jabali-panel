# Changelog

Each entry is one move of the `stable` tag. Servers on the stable channel
install the commit `stable` points at, at their next automatic update (daily,
04:30). `stable` moves once a month, on the 1st, and in between only for a
hotfix ([runbook](docs/runbooks/hotfix-release.md)). Servers on the
development channel follow main and get each change as it merges.

An entry lists every change merged since the previous entry, oldest first,
and the migrations it adds. "Changing rows" names the migrations that update,
insert or delete existing data, not only the schema. The newest entry is
first.

Moves of `stable` before October 2026 did not follow the monthly schedule and
are not listed here. The commit history and the GitHub releases
(`release-<commit>`) have them.

## Adding an entry

Right after `stable` moves, generate the entry from the commit the previous
entry names, put it above the newest entry, and merge it to main through a
pull request:

```
git fetch origin --force main '+refs/tags/stable:refs/tags/stable'
tools/changelog-entry.sh <previous entry's commit> stable [--hotfix]
```

Add a line by hand when servers do something on their first update with it:
a data migration, a service restart, a new alert. The entry reaches stable
servers with the next move.

## 2026-10-21 — hotfix: `51fd7fc25`

`stable` moved from `7715b0dc5` to `51fd7fc25`: 2 changes.

No migrations.

- `21cb1f638` PHP security releases reach the box, and FPM then runs the patched PHP
- `51fd7fc25` AppArmor: every profile includes its /etc/apparmor.d/local file (copied from `e88a3a5ee`)

Both changes are copies of fixes merged to main as [#2210](https://github.com/shukiv/jabali-panel/pull/2210) and [#2211](https://github.com/shukiv/jabali-panel/pull/2211).

## 2026-10-01 — monthly cut: `7715b0dc5`

`stable` moved from `075419db9` to `7715b0dc5`: 184 changes.

Migrations 000299–000308 (10). Changing rows: 000300, 000307.

- `28816ca1e` docs(nginx): correct the redosNestedQuantRE comment — PCRE match_limit is the backstop — GH #1624 ([#1742](https://github.com/shukiv/jabali-panel/pull/1742))
- `46cf5a3cd` harden(nginx): close proxy_pass target + location-path directive injection — GH #1624 ([#1743](https://github.com/shukiv/jabali-panel/pull/1743))
- `19af74644` refactor(dbops): route account + application db-user/role teardown through a shared DropDatabaseUserHost (JAB-275 AC6) ([#1744](https://github.com/shukiv/jabali-panel/pull/1744))
- `8abf204c1` fix(nginx-rules): scroll the freshly added rule's field into view — GH #1624 ([#1745](https://github.com/shukiv/jabali-panel/pull/1745))
- `81e58b282` feat(ssl): make CLI `ssl enable` idempotent on a valid issued certificate (JAB-356 AC3) ([#1746](https://github.com/shukiv/jabali-panel/pull/1746))
- `381fcd638` refactor(ssl): guard the HTTP TLS doors — protected-domain disable refusal + operator-lineage enable no-op (JAB-356 AC1+AC2) ([#1741](https://github.com/shukiv/jabali-panel/pull/1741))
- `f97afea01` fix(nav): align tenant sidebar collapsible section headers with group headers — GH #1626 ([#1747](https://github.com/shukiv/jabali-panel/pull/1747))
- `fc739c453` feat(packages): add a webmail entitlement to Hosting Packages (default ON) — GH #1628 ([#1748](https://github.com/shukiv/jabali-panel/pull/1748))
- `72bbbb998` feat(ssl): make CLI `ssl disable` idempotent when TLS is already off (JAB-356 AC4 disable-side) ([#1749](https://github.com/shukiv/jabali-panel/pull/1749))
- `1e1a74448` feat(ssl): make HTTP disableSSL idempotent when TLS is already off (JAB-356 AC4 disable-side, meaningful half) ([#1750](https://github.com/shukiv/jabali-panel/pull/1750))
- `8e6100f6a` fix(nav): pull tenant sidebar Tools/Account child items left to match group children — GH #1626 ([#1751](https://github.com/shukiv/jabali-panel/pull/1751))
- `e2eca1319` fix(backups): default the admin backup Compression select to Auto — GH #1646 ([#1753](https://github.com/shukiv/jabali-panel/pull/1753))
- `ceae18d51` feat(server-status): stale-serve last-good slices + per-slice cache metrics (JAB-373 AC5+AC7) ([#1755](https://github.com/shukiv/jabali-panel/pull/1755))
- `95867b873` feat(webmail): AND the hosting-package webmail entitlement into the mail-vhost reconciler — GH #1628 (slice 2) ([#1757](https://github.com/shukiv/jabali-panel/pull/1757))
- `b60c78be4` feat(dashboard): load recent mailboxes via one /me/mailboxes request, not a per-domain fan-out (JAB-370 Recent projection) ([#1756](https://github.com/shukiv/jabali-panel/pull/1756))
- `e886006ac` feat(mail): load mailbox group memberships via one owner-scoped request, not a per-domain fan-out (JAB-370 Selection projection) ([#1758](https://github.com/shukiv/jabali-panel/pull/1758))
- `a97d3f1bc` feat(mail): load mailbox autoresponders via one owner-scoped request, not a per-domain fan-out (JAB-370 Selection projection) ([#1759](https://github.com/shukiv/jabali-panel/pull/1759))
- `379e0a60d` feat(webmail): retire the per-user webmail toggle; gate webmail on the hosting package + per-domain flag — GH #1628 (slice 3) ([#1760](https://github.com/shukiv/jabali-panel/pull/1760))
- `bc12ea529` feat(webmail): tenant per-domain webmail Settings tab — GH #1628 (slice 4) ([#1761](https://github.com/shukiv/jabali-panel/pull/1761))
- `98933726e` JAB-279 back-half: CLI `domain create` shared-cert auto-attach + one shared SAN matcher ([#1752](https://github.com/shukiv/jabali-panel/pull/1752))
- `fbf2369c1` feat(domains): apply domain-config writes atomically on both doors — JAB-318 AC4 ([#1762](https://github.com/shukiv/jabali-panel/pull/1762))
- `d4eca2dec` feat(mail): add optional mail_hostname setting + one shared resolver (JAB-390 foundation) ([#1754](https://github.com/shukiv/jabali-panel/pull/1754))
- `1ee70b5e1` refactor(webmail): stop writing the retired users.webmail_enabled column — GH #1628 follow-up ([#1763](https://github.com/shukiv/jabali-panel/pull/1763))
- `cd0cb8e57` docs(adr): revise ADR-0168 to adopt Variant B — central authoritative DNS + API push — GH #1632
- `3ec146c0e` fix(cron): populate tenant cron Edit form on open — GH #1686 ([#1767](https://github.com/shukiv/jabali-panel/pull/1767))
- `0381126b4` feat(ftp): one shared effective-access projection for both FTP sync sites — JAB-276 ([#1768](https://github.com/shukiv/jabali-panel/pull/1768))
- `e2ff562f0` fix(cron): make the admin cron drawer target-aware and show command restrictions — GH #1686 ([#1769](https://github.com/shukiv/jabali-panel/pull/1769))
- `32e5c6785` fix(ftp): land isolated FTP subaccounts in /data, not the empty jail root — GH #1720 ([#1770](https://github.com/shukiv/jabali-panel/pull/1770))
- `9460e31fe` refactor(db-console): extract engine dispatch into shared dbconsoleops leaf — JAB-348 AC3 ([#1771](https://github.com/shukiv/jabali-panel/pull/1771))
- `bd3716b5c` feat(ftp): immediate sshd sync on suspend/unsuspend/teardown — JAB-276 AC4/AC5 ([#1772](https://github.com/shukiv/jabali-panel/pull/1772))
- `05c89f9e5` fix(db): relieve the server_settings row-size ceiling in migration 301 + self-heal stuck hosts — GH #1766 ([#1775](https://github.com/shukiv/jabali-panel/pull/1775))
- `5cd511344` fix(docker): let a tenant reinstall a docker app under a name they deleted — GH #1776 ([#1777](https://github.com/shukiv/jabali-panel/pull/1777))
- `a7b13be3e` fix(security): converge jabali-webmail out of the broad `jabali` group on every update — JAB-357 AC2/AC5 ([#1774](https://github.com/shukiv/jabali-panel/pull/1774))
- `7d464881f` ci(db): gate PRs and releases on applying the full migration chain to a real MariaDB — GH #1766 / #1773 ([#1778](https://github.com/shukiv/jabali-panel/pull/1778))
- `bbcc5d39c` fix(update): keep /usr/sbin on PATH so `jabali update` can't abort before the binary swap — GH #1773 ([#1779](https://github.com/shukiv/jabali-panel/pull/1779))
- `ad27b43c1` fix(php): show the real inherited default for error_reporting and timezone — GH #1705 ([#1783](https://github.com/shukiv/jabali-panel/pull/1783))
- `c49140d8d` fix(backups): count admin Backups badge by runs + manual, not per-account jobs — GH #1784 ([#1786](https://github.com/shukiv/jabali-panel/pull/1786))
- `3deadda9f` refactor(db-console): extract SSO redirect construction into the shared dbconsoleops leaf — JAB-348 AC1/AC3 ([#1787](https://github.com/shukiv/jabali-panel/pull/1787))
- `d8109a7dd` fix(domains): block cross-tenant DNS subdomain hijack on domain claim — GH #1789 ([#1791](https://github.com/shukiv/jabali-panel/pull/1791))
- `d0796b05a` fix(docker): run the GH #1625 alias-collision guard on the tenant docker-install door — GH #1789 follow-up ([#1792](https://github.com/shukiv/jabali-panel/pull/1792))
- `2e1264d8b` feat(db-console): audit CLI SSO issuance via the shared token-hash-prefix leaf — JAB-348 AC5 ([#1788](https://github.com/shukiv/jabali-panel/pull/1788))
- `02ef84de5` fix(docker): reject non-FQDN domains before they reach the compose renderer — GH #1790 ([#1793](https://github.com/shukiv/jabali-panel/pull/1793))
- `88355e04b` test(db-console): door-level regression for the unified SSO redirect + guards — JAB-348 AC4 seed ([#1794](https://github.com/shukiv/jabali-panel/pull/1794))
- `4dc0aa20d` fix(mail): self-heal the Stalwart Principal in forwarder.apply — GH #1795 ([#1799](https://github.com/shukiv/jabali-panel/pull/1799))
- `bfa6a044a` feat(appsec): per-host detection-only mode for AppSec — GH #1641 ([#1797](https://github.com/shukiv/jabali-panel/pull/1797))
- `da662bdf4` refactor(ftp): route both sshd_sync dispatch sites through one shared renderer — JAB-276 AC3 ([#1800](https://github.com/shukiv/jabali-panel/pull/1800))
- `ab524d908` fix(ftp): make the admin FTP access-update database-first + cancellation-safe — JAB-276 AC2 ([#1803](https://github.com/shukiv/jabali-panel/pull/1803))
- `e728db367` fix(appsec): make `exclusion rm` fail loudly on an unknown id — GH #1641 follow-up ([#1801](https://github.com/shukiv/jabali-panel/pull/1801))
- `de26c9652` feat(mail): one shared forwarder-convergence path; wire cPanel/backup restore — GH #1795 follow-up ([#1802](https://github.com/shukiv/jabali-panel/pull/1802))
- `2cb391848` fix(mail): cPanel external-forwarder import — NULL local_part + split multi-target lines — GH #1795 follow-up ([#1805](https://github.com/shukiv/jabali-panel/pull/1805))
- `a8c4e5d1e` fix(api): clamp an over-max page_size to the endpoint max, not the default — GH #1796 ([#1809](https://github.com/shukiv/jabali-panel/pull/1809))
- `4196ba1cd` feat(agent): bind admin_root to the connecting peer (SO_PEERCRED) — JAB-357 AC4 ([#1806](https://github.com/shukiv/jabali-panel/pull/1806))
- `943af2a02` fix(ssl): confirm a DNS-01 cert after a panel→agent read timeout instead of backing off — JAB-407 ([#1807](https://github.com/shukiv/jabali-panel/pull/1807))
- `cf4ba5f1d` fix(nav): remove the gray band behind expanded tenant sidebar sections — GH #1626 ([#1811](https://github.com/shukiv/jabali-panel/pull/1811))
- `618259799` feat(domains): per-domain subdomain delegation — owner-consented cross-tenant subdomains — GH #1812 ([#1814](https://github.com/shukiv/jabali-panel/pull/1814))
- `14715b9e2` fix(mail): surface a failed forwarder.apply instead of reporting success — GH #1795 ([#1819](https://github.com/shukiv/jabali-panel/pull/1819))
- `f423ed0ab` fix(ssl): confirm a shared wildcard cert after a panel→agent read timeout instead of backing off — JAB-407 follow-up ([#1813](https://github.com/shukiv/jabali-panel/pull/1813))
- `405d86196` feat(reconciler): coalescing typed dirty set for out-of-band scheduling (JAB-369) ([#1764](https://github.com/shukiv/jabali-panel/pull/1764))
- `868742d61` refactor(db-console): one issuance leaf owns mint→prefix→redirect for all four SSO doors — JAB-348 AC1/AC2 ([#1821](https://github.com/shukiv/jabali-panel/pull/1821))
- `e5f4bc44f` fix(mail): resolve group@domain recipients — add mail-group expansion to the queryRecipient converger — GH #1818 ([#1822](https://github.com/shukiv/jabali-panel/pull/1822))
- `0813c9ae9` fix(db-console): audit phpMyAdmin issuance failures with the canonical outcome taxonomy — JAB-348 AC5 ([#1823](https://github.com/shukiv/jabali-panel/pull/1823))
- `a405ac088` feat(server-status): surface stale-serve slices on the Server Status page + document the cache in ADR-0065 — JAB-373 ([#1825](https://github.com/shukiv/jabali-panel/pull/1825))
- `bcc3ac7e3` test(ftp): lock admin/tenant access-update transcript parity — JAB-276 AC1 ([#1824](https://github.com/shukiv/jabali-panel/pull/1824))
- `8a07d19b1` fix(domains): enforce the protected-TLS invariant on the CLI ssl-mode door + pin the HTTP/CLI config parity matrix — JAB-318 AC3/AC5 ([#1826](https://github.com/shukiv/jabali-panel/pull/1826))
- `051905e03` test(db-console): pin the DB-Console SSO adapter contract matrix — JAB-348 AC4 ([#1827](https://github.com/shukiv/jabali-panel/pull/1827))
- `bc69f90d0` feat(egress): per-package outbound SSH + ICMP allowances for the M34 firewall — GH #1798 ([#1828](https://github.com/shukiv/jabali-panel/pull/1828))
- `2df9d9ef2` follow-ups: unify the privileged Adminer SSO door onto the issuance leaf + correct the CLI --ssl-mode error message — JAB-348 / JAB-318 ([#1829](https://github.com/shukiv/jabali-panel/pull/1829))
- `172faac2a` refactor(domains): one shared domainops FQDN gate for every create door — JAB-279 AC2/AC6 ([#1830](https://github.com/shukiv/jabali-panel/pull/1830))
- `07460b29b` GH #1795 deliver external forwards + autoresponders via the standard Sieve store ([#1831](https://github.com/shukiv/jabali-panel/pull/1831))
- `66e619e43` fix(cron): surface structured validation codes + unify the error-message map — GH #1686 item 5 ([#1832](https://github.com/shukiv/jabali-panel/pull/1832))
- `43c17724a` refactor(db-console): drive the tenant SSO doors through the AC4 contract matrix — JAB-348 ([#1837](https://github.com/shukiv/jabali-panel/pull/1837))
- `930ddda77` GH #1795 adopt an externally-set (webmail) vacation reply into the composite
- `2c0adacb1` refactor(db-console): collapse the privileged door's duplicate mint interfaces onto the dbconsoleops leaf — JAB-348 ([#1839](https://github.com/shukiv/jabali-panel/pull/1839))
- `c2037d524` feat(cron): admins can edit a tenant's cron job — GH #1686 item 2 ([#1840](https://github.com/shukiv/jabali-panel/pull/1840))
- `117974997` refactor(db-console): give `jabali db sso` an injection seam and drive it through the AC4 contract matrix — JAB-348 ([#1841](https://github.com/shukiv/jabali-panel/pull/1841))
- `2cecde6a8` fix(cron): a root cron job must be owned by an admin — found in GH #1686 ([#1842](https://github.com/shukiv/jabali-panel/pull/1842))
- `f32ad3f38` refactor(ftp): route admin and tenant access-update + delete through one FTP Account Lifecycle Module — JAB-276 ([#1843](https://github.com/shukiv/jabali-panel/pull/1843))
- `c419e0948` fix(ftp): a deleted FTP account could reappear after a disconnect; finish the FTP Account Lifecycle Module — JAB-276 ([#1844](https://github.com/shukiv/jabali-panel/pull/1844))
- `d66c01656` Deliver distribution mail groups to their members as Stalwart mailing lists — GH #1818 / GH #1834 ([#1845](https://github.com/shukiv/jabali-panel/pull/1845))
- `64902c3e9` refactor(domains): one reverse-proxy port reservation + persist step for every create door; fix a port leak on client disconnect — JAB-279 ([#1846](https://github.com/shukiv/jabali-panel/pull/1846))
- `6bde4c06b` fix(mail): the per-domain mailbox list no longer selects password hashes or SSO ciphertext — JAB-370 AC4 ([#1847](https://github.com/shukiv/jabali-panel/pull/1847))
- `b8616d23b` fix(mail): deleting a domain's mail or a mailbox refreshes every mail inventory view — JAB-370 AC7 ([#1848](https://github.com/shukiv/jabali-panel/pull/1848))
- `52fe8c4fa` refactor(domains): one shared-certificate auto-attach step for every create door — JAB-279 AC1/AC6 ([#1849](https://github.com/shukiv/jabali-panel/pull/1849))
- `b742191b5` test(mail): characterize both Mail Inventory adapters — JAB-370 AC8 ([#1850](https://github.com/shukiv/jabali-panel/pull/1850))
- `859eeb5ea` refactor(domains): one service-matrix and mail-posture step for every create door — JAB-279 AC1/AC2 ([#1851](https://github.com/shukiv/jabali-panel/pull/1851))
- `128fadf3c` fix(domains): a domain delete never strands its reverse-proxy port — JAB-279 AC4 ([#1852](https://github.com/shukiv/jabali-panel/pull/1852))
- `c668fa0dd` refactor(domains): one package domain-quota check for every create door — JAB-279 AC1/AC2 ([#1853](https://github.com/shukiv/jabali-panel/pull/1853))
- `66b51580c` fix(domains): the preview-slug collision check sees every preview-enabled domain — JAB-279 ([#1854](https://github.com/shukiv/jabali-panel/pull/1854))
- `92956bab6` fix(domains): an explicit ssl_mode self or none wins over the create-time shared-cert attach — JAB-279 ([#1855](https://github.com/shukiv/jabali-panel/pull/1855))
- `4c86b0d60` fix(domains): the REST domain delete logs a stranded reverse-proxy port — JAB-279 AC4 ([#1856](https://github.com/shukiv/jabali-panel/pull/1856))
- `8a58e3272` refactor(domains): the cross-tenant name guards live in the domain module — JAB-279 AC1 ([#1857](https://github.com/shukiv/jabali-panel/pull/1857))
- `c1841db49` refactor(domains): one domainops.Create entrypoint behind the REST create door — JAB-279 AC1 ([#1858](https://github.com/shukiv/jabali-panel/pull/1858))
- `028b84161` refactor(domains): jabali domain create runs the domainops.Create entrypoint — JAB-279 AC1/AC2 ([#1859](https://github.com/shukiv/jabali-panel/pull/1859))
- `7c002d05d` refactor(dbconsole): one dbconsoleops.Issue entrypoint for every DB-console SSO door — JAB-348 AC1/AC2 ([#1862](https://github.com/shukiv/jabali-panel/pull/1862))
- `f4181ba62` refactor(files): one Upload Intake module behind the File Manager upload routes — JAB-365 AC1 ([#1863](https://github.com/shukiv/jabali-panel/pull/1863))
- `be9182b19` feat(cli): jabali files upload stages through the Upload Intake module — JAB-365 AC2/AC3 ([#1864](https://github.com/shukiv/jabali-panel/pull/1864))
- `6eab408e0` Package egress toggles and tenant subdomain-delegation switch — GH #1798, GH #1812 ([#1860](https://github.com/shukiv/jabali-panel/pull/1860))
- `962c9b845` Scope the phpMyAdmin and Adminer shadow users to the tenant's own databases (security) ([#1861](https://github.com/shukiv/jabali-panel/pull/1861))
- `d3307b934` refactor(domains): domain delete moves into the domain lifecycle module — JAB-279 ([#1865](https://github.com/shukiv/jabali-panel/pull/1865))
- `6f85425ec` fix(security): a system restore never adds members to the panel's privileged groups — JAB-357 ([#1866](https://github.com/shukiv/jabali-panel/pull/1866))
- `455d0d906` test(security): socket identity matrix on a live host, and a group guard for every identity — JAB-357 ([#1867](https://github.com/shukiv/jabali-panel/pull/1867))
- `eca054616` feat(reconciler): run modes, one apply ledger and a per-run report — JAB-369 ([#1868](https://github.com/shukiv/jabali-panel/pull/1868))
- `4c251fd36` feat(reconciler): a steady tick sends no projection the Agent already has — JAB-369 ([#1869](https://github.com/shukiv/jabali-panel/pull/1869))
- `a4f8ece4c` test(reconciler): the planner's phase dependency graph, enforced — JAB-369 ([#1870](https://github.com/shukiv/jabali-panel/pull/1870))
- `dc88f4c84` perf(reconciler): a tick reads each global row once, however many domains — JAB-369 ([#1872](https://github.com/shukiv/jabali-panel/pull/1872))
- `c0d0da5bc` fix(mail): mailbox sharing sends the mail capability; domain deletes leave no Stalwart leftovers ([#1871](https://github.com/shukiv/jabali-panel/pull/1871))
- `7dcabf3a6` feat(mail): webmail URL and Panel SSL follow the applied mail hostname — JAB-390 ([#1876](https://github.com/shukiv/jabali-panel/pull/1876))
- `a03f7d68f` fix(mail): a renewed panel mail cert reaches IMAPS/SMTPS without a manual restart ([#1877](https://github.com/shukiv/jabali-panel/pull/1877))
- `bdb8f0257` fix(migrations): the daily reaper removes orphaned source credentials and can clean staging ([#1878](https://github.com/shukiv/jabali-panel/pull/1878))
- `27099d6b1` feat(security): `jabali secrets rotate kratos` replaces the exposed Kratos secrets — JAB-357 ([#1879](https://github.com/shukiv/jabali-panel/pull/1879))
- `a14ce8072` fix(mail): panel mail cert renewals deploy whatever the lineage is named, and replace the old Stalwart entry — JAB-389/390 ([#1880](https://github.com/shukiv/jabali-panel/pull/1880))
- `3b7e4325e` feat(mail): install.sh renders the applied panel mail hostname, not a derived one — JAB-390 ([#1881](https://github.com/shukiv/jabali-panel/pull/1881))
- `1b58312d1` feat(mail): webmail vhosts follow the panel mail hostname, not a derived one — JAB-390 ([#1882](https://github.com/shukiv/jabali-panel/pull/1882))
- `0dece69fc` feat(mail): agent verb points Bulwark's JMAP URL at the panel mail hostname — JAB-390 ([#1883](https://github.com/shukiv/jabali-panel/pull/1883))
- `5a2589e7b` fix(security): a tenant forwarder warning no longer carries the agent error ([#1884](https://github.com/shukiv/jabali-panel/pull/1884))
- `7680705d3` fix(agent): user.slice.remove reports a unit file it could not remove ([#1885](https://github.com/shukiv/jabali-panel/pull/1885))
- `51d85fcba` fix(security): `jabali user delete` removes the Linux account before exiting ([#1875](https://github.com/shukiv/jabali-panel/pull/1875))
- `b1b895456` fix(mail): mailbox shares reach Stalwart, same account only ([#1873](https://github.com/shukiv/jabali-panel/pull/1873))
- `b12f3bfaa` fix(egress): ping works from the SSH shell on packages that allow it — GH #1798 ([#1874](https://github.com/shukiv/jabali-panel/pull/1874))
- `bed072a4e` feat(mail): switch the shared panel mail hostname — JAB-390 ([#1887](https://github.com/shukiv/jabali-panel/pull/1887))
- `da38685b1` fix(mail): a renamed panel keeps mail on the name its certificate serves — JAB-389/390 ([#1892](https://github.com/shukiv/jabali-panel/pull/1892))
- `364dc058d` fix(secrets): every rotate command records its audit event — JAB-357 ([#1893](https://github.com/shukiv/jabali-panel/pull/1893))
- `2817122d6` fix(agent): the nspawn pin accepts usernames with `_`; bot-challenge opt-in rule pinned ([#1889](https://github.com/shukiv/jabali-panel/pull/1889))
- `d1d69d980` fix(mail): share and forwarder toasts show the API's reason, not its error code ([#1890](https://github.com/shukiv/jabali-panel/pull/1890))
- `271a9919d` fix(egress): a tenant's SSH shell goes through its egress firewall ([#1891](https://github.com/shukiv/jabali-panel/pull/1891))
- `dd7fcfe99` fix(security): a MariaDB database grant no longer covers a sibling tenant's database ([#1886](https://github.com/shukiv/jabali-panel/pull/1886))
- `dc593a590` fix(security): a tenant's Postgres role can no longer connect to other tenants' databases ([#1888](https://github.com/shukiv/jabali-panel/pull/1888))
- `05ee52ee1` fix(domains): the panel hostname and mail.<hostname> are never a tenant's — JAB-390 ([#1894](https://github.com/shukiv/jabali-panel/pull/1894))
- `257323f39` fix(migrations): an auto-detected destination domain runs the tenant create rules ([#1895](https://github.com/shukiv/jabali-panel/pull/1895))
- `7a19fc029` docs(adr): record the shipped mail hostname lifecycle and its decisions — JAB-390 ([#1896](https://github.com/shukiv/jabali-panel/pull/1896))
- `75a87bc15` fix(migrations): cancelling or destroying a job wipes its source credentials — JAB-357 ([#1900](https://github.com/shukiv/jabali-panel/pull/1900))
- `9e6bdabc9` fix(security): every hosting user is behind the egress firewall; the soak ends ([#1897](https://github.com/shukiv/jabali-panel/pull/1897))
- `1510f09c5` fix(cli): jabali db backup/restore work on Postgres databases and on any dump path ([#1899](https://github.com/shukiv/jabali-panel/pull/1899))
- `015da2381` fix(php-settings): the domain PHP settings show the real default, not "Use pool default" ([#1901](https://github.com/shukiv/jabali-panel/pull/1901))
- `cd5770284` fix(docker-apps): every env value reaches the container exactly as typed ([#1902](https://github.com/shukiv/jabali-panel/pull/1902))
- `8d83fd51c` fix(mail): /webmail follows a mail hostname switchover at once — JAB-390 ([#1904](https://github.com/shukiv/jabali-panel/pull/1904))
- `962c39b5c` feat(mail): Settings → Email lists the DNS records a new mail hostname needs — JAB-390 ([#1905](https://github.com/shukiv/jabali-panel/pull/1905))
- `8931777bb` docs(security): the admin password and 2FA reset steps after rotating Kratos — JAB-357 ([#1907](https://github.com/shukiv/jabali-panel/pull/1907))
- `e06368c5b` fix(mail): a switchover checks the served certificate and cleans up the old custom lineage — JAB-408 ([#1909](https://github.com/shukiv/jabali-panel/pull/1909))
- `e462aec14` fix(apps): Flarum forums stop being blocked by the WAF — GH #1650 ([#1908](https://github.com/shukiv/jabali-panel/pull/1908))
- `149ed8f02` chore(docker-apps): immich 3.2.2, with the ML image matched to the server ([#1906](https://github.com/shukiv/jabali-panel/pull/1906))
- `d165d3335` fix(docker-apps): pin a tenant compose's project, networks and service set — GH #1903 ([#1911](https://github.com/shukiv/jabali-panel/pull/1911))
- `6ccc01366` fix(docker-apps): every tenant bring-up runs the tenant compose gate — GH #1903 ([#1913](https://github.com/shukiv/jabali-panel/pull/1913))
- `c60eccbed` fix(backups): a restored domain passes the checks its create door runs — GH #1898 ([#1912](https://github.com/shukiv/jabali-panel/pull/1912))
- `03036b6bc` feat(security): WAF exclusions tab — AppSec FP triage and live CRS exclusions — GH #1649 ([#1921](https://github.com/shukiv/jabali-panel/pull/1921))
- `a8eae06c8` docs: the agent's socket checks and verbs, and the panel mail certificate runbook ([#1922](https://github.com/shukiv/jabali-panel/pull/1922))
- `4e6101359` feat(mail): each mail domain gets a read-only directory of its mailboxes — GH #1637 ([#1926](https://github.com/shukiv/jabali-panel/pull/1926))
- `43d313b9c` fix(domains): deleting a domain removes the box's recursor forward for it ([#1924](https://github.com/shukiv/jabali-panel/pull/1924))
- `411b3c234` fix(mail): an old password or a disabled mailbox no longer keeps webmail access ([#1925](https://github.com/shukiv/jabali-panel/pull/1925))
- `21c9984bd` fix(mail): a suspended user's mailboxes stop signing in and keep their mail ([#1927](https://github.com/shukiv/jabali-panel/pull/1927))
- `36e0f3dbe` fix(cron): a cron job can run WP-CLI on a chosen PHP version ([#1928](https://github.com/shukiv/jabali-panel/pull/1928))
- `bfe1daf86` fix(api): the OpenAPI spec documents only routes that exist ([#1929](https://github.com/shukiv/jabali-panel/pull/1929))
- `3af8702c0` docs(api): the OpenAPI spec documents every route and passes an OpenAPI validator ([#1930](https://github.com/shukiv/jabali-panel/pull/1930))
- `5f4af50cf` docs: platform and admin pages name the units, sockets and endpoints a box really has ([#1932](https://github.com/shukiv/jabali-panel/pull/1932))
- `091503109` fix(cli): jabali service action refuses stop and disable on the same six units as the admin API ([#1934](https://github.com/shukiv/jabali-panel/pull/1934))
- `09848e487` docs: every jabali command the docs name exists, with a test that keeps it so ([#1933](https://github.com/shukiv/jabali-panel/pull/1933))
- `76ac59757` fix(mail): a mailbox password set through the API needs 8+ characters ([#1931](https://github.com/shukiv/jabali-panel/pull/1931))
- `bb737c2c6` docs(notifications): the event catalog, routing and channel pages describe the real dispatcher ([#1935](https://github.com/shukiv/jabali-panel/pull/1935))
- `9505924fc` fix(mail): outbound throttles reach Stalwart, written by the panel with its own admin token ([#1936](https://github.com/shukiv/jabali-panel/pull/1936))
- `3b4e6f85c` fix(mail): DMARC, TLS and abuse reports reach the deliverability page ([#1937](https://github.com/shukiv/jabali-panel/pull/1937))
- `fbbdf874e` fix(mail): a new mailbox no longer signs in to the account that once had its address as an alias ([#1941](https://github.com/shukiv/jabali-panel/pull/1941))
- `e46e8a434` fix(mail): a refused mailbox create leaves the address's alias on its account ([#1942](https://github.com/shukiv/jabali-panel/pull/1942))
- `6d5ac6633` fix(users): creating an admin works again — GH #1938 ([#1943](https://github.com/shukiv/jabali-panel/pull/1943))
- `2b42db801` fix(mail): the API and CLI no longer create a shared mailbox that takes no mail — GH #1914 ([#1944](https://github.com/shukiv/jabali-panel/pull/1944))
- `42d890af2` feat(mail): the disclaimer is set on the mail domain's Settings tab — GH #1915 ([#1945](https://github.com/shukiv/jabali-panel/pull/1945))
- `c903a302d` fix(dns): a box with the DNS module off stops pushing zones and recursor forwards every minute — GH #1820 ([#1946](https://github.com/shukiv/jabali-panel/pull/1946))
- `cf800e8dc` feat(cache): low-traffic WordPress pages stay cached, and an edited page is cached again right away ([#1947](https://github.com/shukiv/jabali-panel/pull/1947))
- `86b6b2e56` feat(php): each hosting package decides which PHP settings its tenants may change — GH #1701 ([#1948](https://github.com/shukiv/jabali-panel/pull/1948))
- `fef45cc80` fix(cache): the panel can read Redis INFO, so the eviction warning and admin cache stats show ([#1949](https://github.com/shukiv/jabali-panel/pull/1949))
- `96a42b021` feat(redis): size Redis maxmemory to host RAM instead of a fixed 128 MB ([#1950](https://github.com/shukiv/jabali-panel/pull/1950))
- `c56e55009` chore(mcp): remove the jabali-db MCP server ([#1952](https://github.com/shukiv/jabali-panel/pull/1952))
- `6c5b80a8a` test(ssl): match "port 465", not the bare number, so a random certificate hash cannot fail the mail-served test ([#1953](https://github.com/shukiv/jabali-panel/pull/1953))
- `f2f5c5ba8` fix(php): refuse control characters in pool ini override values, so a value cannot add a line to the pool file ([#1954](https://github.com/shukiv/jabali-panel/pull/1954))
- `7b497e9fa` chore(docker-apps): October catalog bumps, with gitea, nextcloud and odoo held on their current major ([#1957](https://github.com/shukiv/jabali-panel/pull/1957))
- `a64ce0391` chore(deps): safe monthly bumps for October — Go modules, signature-base, yara-x, patch npm (GH #1955) ([#1958](https://github.com/shukiv/jabali-panel/pull/1958))
- `cee2de3ba` deps: bump Stalwart to 0.16.24 and stalwart-cli to 1.0.13 (GH #1955) ([#1960](https://github.com/shukiv/jabali-panel/pull/1960))
- `c711001ee` deps: bump Bulwark webmail to 1.12.0 (GH #1955) ([#1961](https://github.com/shukiv/jabali-panel/pull/1961))
- `1008930ca` fix(php-defense): Snuffleupagus 0.14 on every PHP version with the rules boxes enforce today; Adminer 6.1.1 (GH #1955) ([#1959](https://github.com/shukiv/jabali-panel/pull/1959))
- `81df6e20e` deps: Go toolchain 1.26.8; jabali update installs the pinned Go (GH #1955) ([#1962](https://github.com/shukiv/jabali-panel/pull/1962))
- `971bf2bac` feat(php): Reset OPcache on a domain's PHP Settings; saves and version switches apply now — GH #1701 ([#1963](https://github.com/shukiv/jabali-panel/pull/1963))
- `0d93809a1` docs(ftp): runbook covers isolated (separate-uid) subaccounts ([#1964](https://github.com/shukiv/jabali-panel/pull/1964))
- `79592676f` fix(update): stay on the current build when the release channel can't be read ([#1966](https://github.com/shukiv/jabali-panel/pull/1966))
- `caec273ed` feat(php): "View error log" on PHP Settings opens the domain's error log in place — GH #1701 ([#1967](https://github.com/shukiv/jabali-panel/pull/1967))
- `7715b0dc5` feat(cron): System jobs under Admin → Cron Jobs — the scheduled jobs Jabali installs, with status, Run now and log — GH #1686 ([#1968](https://github.com/shukiv/jabali-panel/pull/1968))
