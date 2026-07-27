# M23 Responsive — Runbook

How to verify a new page is mobile-friendly before merge, how to run
the mobile E2E locally, and the patterns that keep M23 from regressing.

See ADR-0046 for the decisions this runbook implements.

## TL;DR

- New list page → use `<SearchableTableStringQ>` (inherits horizontal
  scroll for free). Bare `<Table>` → add `scroll={{ x: "max-content" }}`.
- New form row → use `<Row gutter={16}><Col xs={24} md={12}>` (or
  `md={8}` for 3-column). Never `<Col span={N}>` with a fixed N.
- Conditional rendering → use `Grid.useBreakpoint()`, never a custom
  `@media` query in a `.tsx` file.
- New page that lives outside the shells (like `/login`) → set
  `width: "100%"` + `maxWidth` on the root card, not a fixed `width`.
- Every page must pass the responsive E2E smoke (see below).

## Verification checklist before merging any UI change

1. `npm run build` green — `tsc -b` + vite.
2. Open the page in Chrome devtools Responsive mode at:
   - **iPhone SE** (375 × 667) — the narrowest common phone.
   - **Pixel 7** (412 × 915) — driving evidence viewport for M23.
   - **iPad Mini** (768 × 1024) — the md/lg boundary.
   - **1440 × 900** — desktop baseline.
3. At each viewport:
   - No horizontal viewport scroll.
   - Every interactive element (buttons, inputs, dropdowns) reachable.
   - Drawer opens on <lg from the hamburger; sidebar visible at ≥lg.
4. Run the responsive E2E (see next section).
5. Take a screenshot at 390×844 of the page you touched; attach to PR.

## Running the mobile E2E locally

```bash
cd panel-ui
npm run build                    # tsc + vite preview wants dist/
npx playwright test tests/e2e/responsive.spec.ts --project=mobile-chromium
npx playwright test tests/e2e/responsive.spec.ts --project=tablet-chromium
```

Both projects reuse the existing Vite preview webserver configured in
`playwright.config.ts`. Neither requires a backend — the spec uses
`mockApi()` from `fixtures.ts`.

If you see `Executable doesn't exist at .../webkit/pw_run.sh`, the
spec accidentally pulled a WebKit-based devices preset. The M23
projects are Chromium-only by design — check `playwright.config.ts`
and make sure you're extending `devices["Desktop Chrome"]` with a
viewport override, not `devices["iPhone 13"]` or `devices["iPad Mini"]`.

Both projects run in CI under `.github/workflows/ci.yml` alongside the
desktop project; nothing extra to wire if your spec matches the
`testMatch: /responsive\.spec\.ts/` filter.

## Common issues → fixes

### Table clipped at the right edge on phones

**Cause**: a `<Table>` not wrapped by `SearchableTableStringQ` that's
missing the scroll prop.

**Fix**: add `scroll={{ x: "max-content" }}` to the `<Table>` props.
Desktop layout is unchanged because `max-content` only engages when
columns would otherwise overflow.

### A two-column form squishes inputs on phones

**Cause**: `<Col span={12}>` without breakpoint overrides.

**Fix**: `<Col xs={24} md={12}>`. Fields stack on phones, go side-by-side
on tablets up.

### Drawer stays open after clicking a menu item

**Cause**: the route-change `useEffect([location.pathname])` in the
shell layout was deleted or lost its `useLocation()` dependency during
a refactor.

**Fix**: `AdminLayout.tsx` / `UserLayout.tsx` must have:

```ts
useEffect(() => {
  setDrawerOpen(false);
}, [location.pathname]);
```

### A Card extends off-screen on phones

**Cause**: a fixed `style={{ width: 420 }}` or `width="720"` on the
root element.

**Fix**: `style={{ width: "100%", maxWidth: 420 }}` — caps on desktop,
shrinks with the viewport on phones. For AntD `<Modal>`, `width={720}`
is actually fine; AntD clamps modal width to viewport automatically.

### Header squishes / overflows on phones

**Cause**: element count in the header exceeds the inline-flex budget
at 390px.

**Fix**: hide non-essential elements on xs via `Grid.useBreakpoint()`.
Don't add more elements; the current cluster (hamburger + logo +
search button + theme toggle + avatar) is already at the ceiling.

## Breakpoint reference (do not change without a new ADR)

| Token | Width    | Used for                                 |
|-------|----------|------------------------------------------|
| xs    | <576px   | phones (portrait) — drawer, modal search |
| sm    | ≥576px   | phones (landscape)                       |
| md    | ≥768px   | tablets — persistent Sider, inline tree  |
| lg    | ≥992px   | desktops — full chrome                   |
| xl    | ≥1200px  | (no special-case)                        |
| xxl   | ≥1600px  | (no special-case)                        |

The **lg (992px)** boundary is the drawer/sider toggle. Do not move it
without a follow-up ADR — user agents with 990-995px viewports flap
between modes around a moved boundary.

## Out of scope

- Offline / PWA support.
- Landscape-specific layouts (we live with whatever AntD does).
- RTL text support (Hebrew users currently use the English UI).
- Touch gesture drawer (swipe to open) — AntD default click-to-open
  is sufficient.
- Roundcube / phpMyAdmin / WordPress admin responsive — those are
  iframed upstream apps outside M23's scope.
