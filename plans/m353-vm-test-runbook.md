# M353 — Modular install VM verification runbook

How to verify the modular install (`JABALI_MODULES` gating) + the Bubble Tea TUI
on a VM **before** merging PR #81. Install is stop-the-world — use a **fresh,
throwaway Debian 13 VM**, not `.86` (already provisioned) and not anything you
care about.

Branch under test: `feat/tui-installer-t1` (T1 installer + Step 2 install.sh gating).

---

## Quick check FIRST — dry run (no VM, no root, no changes)

Before touching a VM, verify the module-gating logic on **any** machine (even
this dev box) with `--dry-run` — it prints the plan and exits before any change:

```bash
git checkout feat/tui-installer-t1

JABALI_MODULES=dns bash install.sh --dry-run        # only DNS
JABALI_MODULES=dns,mail,quota bash install.sh --dry-run
bash install.sh --dry-run                            # unset → ALL modules on
```

Expected for `JABALI_MODULES=dns`:

```
=== Jabali install plan (dry run) ===
JABALI_MODULES: dns
Optional modules:
  [x] dns        PowerDNS (DNS server)
  [ ] mail       Stalwart + Bulwark (mail) (skipped)
  [ ] security   … (skipped)
  …
server_settings flags that would be seeded:
  dns_enabled  = 1
  mail_enabled = 0
  …
No changes made.
```

The installer binary can preview too: `jabali-installer --dry-run` (headless) or
pick modules in the TUI then it runs the plan without installing.

If the dry-run plan matches your selection, proceed to the real VM install below.

---

## 0. Prep the VM

- Fresh Debian 13 (bookworm/trixie) VM, root or sudo, ≥ 2 GB RAM, a resolvable
  hostname you'll pass as `JABALI_HOSTNAME`.
- Snapshot it first if your hypervisor supports it — lets you re-run cleanly.

## 1. Get the branch onto the VM

```bash
sudo apt-get update && sudo apt-get install -y git
git clone https://github.com/shukiv/jabali-panel
cd jabali2
git checkout feat/tui-installer-t1
```

## Path A — headless module selection (fastest, tests Step 2)

Install with only DNS enabled (mail + security + python skipped):

```bash
sudo JABALI_HOSTNAME=test.example.com JABALI_MODULES=dns bash install.sh
```

Watch the log for the skip lines — you should see:

```
[i] module 'mail' disabled (JABALI_MODULES) — skipping install_stalwart
[i] module 'security' disabled (JABALI_MODULES) — skipping install_crowdsec
[i] module 'python_apps' disabled (JABALI_MODULES) — skipping install_python_apps_runtime
[✓] module flags seeded from JABALI_MODULES (dns)
```

## Path B — the TUI (tests T1 interactively)

Build + run the installer binary; it drives install.sh:

```bash
# needs Go (install.sh would fetch it, but for a standalone binary build:)
sudo apt-get install -y golang
go build -o /usr/local/bin/jabali-installer ./installer/cmd/jabali-installer
sudo JABALI_HOSTNAME=test.example.com JABALI_INSTALL_SH=$(pwd)/install.sh jabali-installer
```

- Pick a **profile** (e.g. Minimal or Web host), toggle modules with **space**,
  **enter** to continue, confirm — it prints `JABALI_MODULES=…` and runs install.sh.
- The **non-interactive fallback** (no TTY / `--unattended` / `JABALI_MODULES` preset)
  is the same as Path A. Verify: `JABALI_MODULES=dns jabali-installer </dev/null`
  should skip the TUI and run install.sh directly.

---

## 2. Verify what was (not) installed

For `JABALI_MODULES=dns` (DNS on; mail/security/python off):

```bash
# mail OFF → Stalwart not installed
systemctl status jabali-stalwart 2>&1 | head -1        # expect: not found / inactive
# security OFF → CrowdSec not installed
systemctl status crowdsec 2>&1 | head -1               # expect: not found
command -v cscli || echo "cscli absent (security off) ✓"
# dns ON → PowerDNS running
systemctl is-active pdns pdns-recursor                 # expect: active
# core always on
systemctl is-active nginx mariadb jabali-panel jabali-agent   # all active
```

## 3. Verify the panel reflects it

```bash
# module flags seeded to match the install
sudo mariadb jabali_panel -e \
  "SELECT dns_enabled, mail_enabled, security_enabled, quota_enabled, api_enabled FROM server_settings WHERE id=1;"
# expect: dns=1, mail=0, security=0, quota=0, api=0  (for JABALI_MODULES=dns)
```

In the browser (log into the panel):
- **Nav hides** the disabled modules — no **Mail**, no **Security** entries.
- **DNS** is present and works.
- Deep-link a disabled page (`/jabali-admin/mail`) → redirects to the dashboard
  with a "not enabled" toast.
- Hit a disabled endpoint directly → **409 `module_disabled`**:
  ```bash
  curl -k https://test.example.com/api/v1/domains/<id>/mailgroups   # (authed) → 409
  ```

## 4. The critical risk to watch — reconciler with a module off

The known open risk (parked "reconciler no-op when module off"). With **mail off**,
watch the agent + reconciler logs for a few minutes:

```bash
journalctl -u jabali-panel -u jabali-agent --since "5 min ago" | \
  grep -iE "stalwart|mail|dnscompile|pdns|error|refused|no such" | tail -30
```

- **Pass:** no repeating errors trying to reach the uninstalled Stalwart / an
  absent service; the panel stays up; no crash-loop.
- **Fail:** the reconciler spams "connection refused" to Stalwart / pdns, or a
  service crash-loops. If so, that's the next fix (gate those reconciler phases
  on the module flag) **before** this can merge.

## 5. Backward-compat sanity (no JABALI_MODULES)

On a second fresh VM, run the plain install (no `JABALI_MODULES`):

```bash
sudo JABALI_HOSTNAME=test2.example.com bash install.sh
```

- **Everything** installs (mail, security, dns, …) exactly as before — the gating
  is invisible when the var is unset. This proves existing `curl|bash` / CI /
  cloud-init installs are unaffected.

---

## Decision

- All of §2–§4 pass on a module-off install **and** §5 shows a full install is
  unchanged → PR #81 is safe to merge.
- §4 shows reconciler noise/crash on a disabled module → hold; fix the reconciler
  gating first.
