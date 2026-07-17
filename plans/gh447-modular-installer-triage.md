# GH #447 triage — pdns false-failure + modular installer feedback

Source: issue #447 (netsol-dev) + comment by mrlp57 (2026-07-16). Two layers:
a concrete pdns bug (fixed) and a batch of modular/TUI installer feedback that
belongs to the WIP M353 workstream (`plans/m353-*`).

## 1. pdns.service reported "failed" on a no-DNS install — FIXED

**Symptom:** operator chose no PowerDNS (all sites on Cloudflare); dashboard +
Server Status show `pdns.service is failed`.

**Root cause:** the base apt batch installs the pdns + pdns-recursor units on
*every* host, but their post-install config only runs when the DNS module is
enabled (`run_if_module dns install_powerdns`). On a no-DNS host the units are
left unconfigured → systemd `failed`. The Server Status module-gate only hides
pdns when `server_settings.dns_enabled=0`, and that flag is seeded to 0 only for
modular installs (`JABALI_MODULES` set) — it defaults to 1 — so a host that kept
`dns_enabled=1` still shows the failed unit. The dashboard/monitor health path
(`normalizeServiceHealth`) never gated on the flag at all; it only omits
`masked`/`not-found` units, so a *present-but-failed* pdns always showed.

**Fix (this change):**
- `install.sh` `converge_pdns_masking`: mask pdns + pdns-recursor when DNS is off
  (DB `dns_enabled` is truth on installed hosts; `JABALI_MODULES` fallback on
  fresh installs), unmask when on. Called in `main()` before the DNS config step
  AND in the `provision_new_software` update prelude, so existing hosts converge
  on `jabali update`. A masked unit reports `masked`, not `failed`, and the
  capability-aware health paths omit it.
- `server_status.go` `filterModuleServices`: also omit `masked`/`not-found`
  units, matching the dashboard's `normalizeServiceHealth`. So a masked pdns
  disappears from Server Status too, independent of the flag.

**Operator remediation for an already-broken host:** toggle DNS off in
Server Settings → Modules (sets `dns_enabled=0`), then `jabali update` masks pdns
and the warning clears. (Before the update reaches them, `systemctl mask
pdns.service pdns-recursor.service` does it immediately.)

**Validation:** `converge_pdns_masking` branch logic verified 4 ways (DB on/off ×
module on/off, DB wins); DB-read path confirmed on a live host; server_status
change unit-tested. NOT yet end-to-end on a live no-DNS host (none available
safely) — worth a box run when one exists.

## 2. Modular / TUI installer feedback — M353 workstream (NOT fixed here)

mrlp57 tested the new module installer. Actionable items for `plans/m353-*`:

- **Selections not honored.** "Whatever I didn't select to install is still
  listed in menu" + "installs unselected modules." Strong signal the TUI
  selection isn't threaded into `JABALI_MODULES` / `is_module_enabled`, so
  `run_if_module` runs everything and `seed_module_flags` no-ops (or seeds
  wrong). This is *why* `dns_enabled` stayed 1 above. **Highest priority** — the
  module gating is the whole point of M353.
- **Let's Encrypt didn't work** in the module install. Likely `setup_certbot`
  ordering/skip under the modular path, or the DNS-off ordering (certbot HTTP-01
  needs the panel hostname to resolve). Repro + fix under M353.
- **Graph/TUI installer hard to read** over SSH. Readability pass on the
  bubbletea TUI (`plans/m353-tui-installer-bubbletea.md`) — contrast, plain
  fallback for dumb terminals.
- **Positive (keep):** installing modules from the panel (Server Settings →
  Modules, runtime `install_module <key>`) "worked very well." The runtime path
  is solid; the install-time selection path is the gap.

See `plans/m353-modular-panel-proposal.md`, `plans/m353-phase1-modular-install.md`,
`plans/m353-tui-installer-bubbletea.md`.
