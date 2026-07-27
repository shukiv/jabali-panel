# Jabali Snuffleupagus rule bundle

The rule bundle ships in this directory and gets composed at runtime
by the panel reconciler into `/etc/jabali/snuffleupagus/active.rules`.

## File order

1. `00-base.rules` — universal safety (always-on)
2. `10-wordpress.rules`, `10-drupal.rules`, `10-joomla.rules`,
   `10-prestashop.rules`, `10-magento.rules` — per-CMS overlays
3. `99-jabali-overrides.rules` — operator per-rule kill list
   (rendered by reconciler, never hand-edit on a live system)

## Mode rendering

- `mode=off`   →   comment-only empty ruleset (no directive; `sp.global.enable` is invalid in v0.13 and fatals PHP-FPM, GH #718).
- `mode=simulation` → every rule above is wrapped with `.simulation()`
  so it logs without enforcing.
- `mode=enforce` → rules apply as written.

## Adding a new CMS

1. Create `10-<cms>.rules` here. Use the existing files as templates.
2. Add CI canary: install the CMS in a fresh container, exercise the
   common admin flows with this rule active, assert exit 0.
3. Add the CMS name to the panel UI's known-app dropdown so operators
   can filter incidents by CMS.

## Adding an exception (false-positive triage)

The operator workflow is:
1. Incident appears in the UI table with a rule name.
2. Operator clicks "Disable rule" with a reason.
3. The reconciler appends a kill directive to `99-jabali-overrides.rules`.
4. Reload propagates to every per-user PHP-FPM pool.

Never hand-edit `00-base.rules` or `10-*.rules` on a live system —
those are the upstream / Jabali-shipped files and `jabali update`
overwrites them.
