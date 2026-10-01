# Jabali Snuffleupagus rule bundle

The rule bundle ships in this directory and gets composed at runtime
by the panel reconciler into `/etc/jabali/snuffleupagus/active.rules`.

## File order

The renderer concatenates `*.rules` in this directory, in name order:

1. `00-base.rules` -- the loaded rules (session/cookie hardening, the
   command-execution and info-leak drops).
2. `99-jabali-overrides.rules` -- comment-only. An operator's "Disable
   rule" comments the rule's line out in `active.rules`.

`pending/` holds rules that are not loaded yet (the rest of the old base
set and the CMS overlays). See `pending/README.md` for why and for what
loading them as written breaks.

Snuffleupagus applies the FIRST matching rule. An exception (`.allow()`)
has to come before the drop it overrides, in the same or an earlier file.

Every file here and in `pending/` must be ASCII-only, comments included:
Snuffleupagus up to v0.13 drops every rule after the first non-ASCII byte.
`TestSnuffleupagusBundle_IsASCII` fails the build otherwise.

## Mode rendering

- `mode=off`   →   comment-only empty ruleset (no directive; `sp.global.enable` is invalid in v0.13 and fatals PHP-FPM, GH #718).
- `mode=simulation` → every rule above is wrapped with `.simulation()`
  so it logs without enforcing.
- `mode=enforce` → rules apply as written.

## Adding a new CMS

1. Create `10-<cms>.rules` here. The overlays in `pending/` are a
   starting point, but none of them has passed a soak yet.
2. Add CI canary: install the CMS in a fresh container, exercise the
   common admin flows with this rule active, assert exit 0.
3. Add the CMS name to the panel UI's known-app dropdown so operators
   can filter incidents by CMS.

## Adding an exception (false-positive triage)

The operator workflow is:
1. Incident appears in the UI table with a rule name.
2. Operator clicks "Disable rule" with a reason.
3. The reconciler comments that rule's line out in `active.rules` and
   lists it under "operator overrides" at the end of the file.
4. Reload propagates to every per-user PHP-FPM pool.

Never hand-edit the `.rules` files on a live system —
those are the upstream / Jabali-shipped files and `jabali update`
overwrites them.
