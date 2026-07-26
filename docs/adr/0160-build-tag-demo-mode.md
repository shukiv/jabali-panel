# ADR-0160 — Build-tag-gated demo mode on main (JAB-159)

**Status:** Accepted — JAB-159. **Supersedes** the "never merge `feat/demo-mode`"
decision that lived in the README "Demo and Website" section. Demo mode now lives on
`main`, gated out of production artifacts at compile time. Blueprint at
`plans/jab159-demo-build-tag.md`. Shipped in serial, individually-reviewed slices:
phase 1 Go build-tag split + CI anti-leak guard (#684), phase 2 SPA `VITE_DEMO` gate
(#705), phase 3 demo deploy profile wired into `install.sh` + `jabali update` (#707).

## Context

The public demo at `https://demo.jabali-panel.com` runs a read-only "demo mode": a
write-block that turns every non-idempotent `/api/v1/*` request into
`403 {"error":"demo_mode"}`, an `/info` endpoint that exposes the *seeded* demo
credentials, and a fixed banner + "Enter as admin / user" buttons.

That code used to live on a long-lived `feat/demo-mode` branch, **intentionally never
merged** — the reasoning was that merging would ship the credential-exposing `/info`
endpoint and the write-block into every production binary + SPA, "one mis-set
`[demo] enabled` toggle away from leaking seeded creds on a real host." The cost: the
branch drifted ~1,800 commits behind `main`, so refreshing the demo meant a heavy,
conflict-prone rebase across heavily-refactored files (`App.tsx`, `Login.tsx`,
`config.go`, `app.go`). The demo was chronically stale.

## Decision

Keep demo mode on `main`, but gate it so it is **physically absent from production
artifacts** — a strictly stronger guarantee than a runtime flag.

- **Go:** demo-only files carry `//go:build demo` (`middleware/demo.go` write-block,
  `api/index_demo_on.go` `/info` handler, `app/demo_on.go` mount). `//go:build !demo`
  stub files (`*_demo_off.go`) provide no-op equivalents so `app.go`/`index.go` compile
  both ways. A production (untagged) build contains none of the demo code.
- **SPA:** the demo UI (`DemoBanner`, `useServiceInfo`) is a dynamic `import()` behind
  `import.meta.env.VITE_DEMO === "1"`. Vite statically replaces `VITE_DEMO`, so a
  non-demo build dead-code-eliminates the import and tree-shakes the demo UI out of the
  bundle.
- **Deploy profile:** `/etc/jabali/deploy-profile` containing `demo` makes both build
  paths (`install.sh` and `jabali update`) build the panel with `-tags demo` and the SPA
  with `VITE_DEMO=1`. Absent / anything else = production. The profile is folded into the
  per-artifact rebuild hashes so flipping it forces a rebuild of the affected artifacts.
- **CI anti-leak guard** (`make demo-guard` + the `demo-guard` job, plus a step in the
  `ui-unit` job): every PR builds both variants and asserts a production binary contains
  no `demo_mode` marker and a production SPA bundle contains no `jabali-demo-banner`
  marker, while the demo build contains both. This is what makes demo-on-main safe — the
  boundary is enforced, not merely intended.

## Consequences

- Demo mode tracks `main` automatically: a demo host refreshes with a plain
  `jabali update` on the demo profile — no rebasing, ever. The `feat/demo-mode` branch is
  retired.
- The security property is now machine-checked. The old "hope the toggle is off" model is
  replaced by "the code is not in the binary/bundle at all," verified in CI on every PR.
- The runtime `[demo] enabled` flag still exists **inside** the demo build as a second
  gate (belt + suspenders), but the build tag is the guarantee.
- Threat model shift: the risk is no longer "a production host mis-sets a flag" (that code
  path does not exist in production) but "a build is mistakenly produced with `-tags demo`
  / `VITE_DEMO=1`." The deploy profile marker + CI guard bound that to an explicit,
  auditable host-level choice.
