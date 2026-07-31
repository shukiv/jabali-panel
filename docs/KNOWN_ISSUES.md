# Known Issues

Tracking file for non-blocking bugs that have been investigated but deferred. New entries go at the top; closed entries move to the bottom with a resolution SHA.

---

## Open

### KI-2 — GH #429: Plesk migration pull-source dials port 22 despite a custom SSH port

**Opened:** 2026-07-31
**Severity:** MEDIUM (blocks Plesk migration on non-22 sources; workaround exists).
**Status:** believed to be build/data staleness, not a live code bug — awaiting reporter's `jabali version`.

Reporter (lxsdevcode) reports the wizard connects + discovers on a custom port (`840x`) but `jabali migrate pull-source` then dials `22` (`plesk.Connect: tcp dial <ip>:22 i/o timeout`).

Code traced end-to-end and is correct on `main`/`stable` (7085d502): the wizard persists `source_port` on the job row at create (`admin_migrations.go` create handler) AND via the Connection-step PATCH (`PatchDraft`, gated on `state='draft'`); `srcSSHPort(job)` / `pullPlesk` read it back, falling back to `22` only when unset; there is **no code path that resets `source_port`**. The dedicated-port fix landed in #444 (2026-07-15) + #463 (2026-07-18).

So a `pull-source` dialing `22` implies `job.SourcePort==0` — i.e. a build that predates #463, or a job row created before updating (updating the binary does not rewrite existing job rows). Reply asked the reporter to confirm `jabali version` and to create a **fresh** job.

Related: **GH #787** — the reporter opened their own PR (`patch-2`) adding `net.SplitHostPort(host)` to `plesk/discover.go` so a `host:port` string overrides `d.Port`. This is a symptom-level workaround for `d.Port==0`; recommend not merging (redundant vs the `source_port` field, breaks IPv6). Decision pending.

**Next step:** if a freshly-created job on a confirmed-updated box still dials 22, this becomes a real bug — reproduce the wizard create→PATCH→pull flow and inspect `source_port` in the DB at each step. A `source_port` persistence regression test would guard the path.

---

---

## Closed

### KI-1 — Login.test.tsx: 4 failing tests (`useThemeMode must be used inside ThemeModeProvider`)

Closed 2026-05-16 in `3db12b7e` (`fix(test): wrap LoginPage test in ThemeModeProvider`, committed 2026-04-26 — already on `origin/main`; this file just wasn't updated). Verified: `Login.test.tsx` 4/4 pass and the full panel-ui vitest suite is 14 files / 83 tests green, no regression.

**Opened:** 2026-04-23
**Severity:** LOW (pre-existing on `origin/main` before M24 shipped, no production impact — tests are wrong, app works).
**Scope:** `panel-ui/src/pages/Login.test.tsx` — 4 of 4 tests fail.
**Discovered by:** wt-a's M24 ship-day smoke (2026-04-22) — noted as pre-existing and non-blocking; M24 merged on that basis.

**Failure signature (all 4 tests):**

```
Error: useThemeMode must be used inside ThemeModeProvider
 ❯ useThemeMode src/theme/ThemeModeContext.tsx:107
 ❯ LoginPage src/pages/Login.tsx:56
```

Tests:
- `LoginPage > renders the fields from the password-group flow`
- `LoginPage > shows an error when flow initialisation fails`
- `LoginPage > switches to TOTP input when the flow continues to AAL2`
- `LoginPage > surfaces top-level flow errors into an alert`

**Root cause:** Commit `f68e022 style(ui): center logo + title on login card` introduced a `useThemeMode()` call into `Login.tsx` (for light/dark logo selection on the login card). The test harness in `Login.test.tsx` renders `<LoginPage />` with `<BrowserRouter>` + `<QueryClientProvider>` but does NOT wrap in `<ThemeModeProvider>`, so the hook throws on mount. All 4 tests fail identically, before any assertions run.

**Why it hasn't broken production:** `App.tsx` wraps the entire tree in `<ThemeModeProvider>` — the real app is fine. Only the unit-test renderer is missing the wrapper.

**Fix sketch (cheap, not yet scheduled):**

Wrap the render helper in `Login.test.tsx`:

```tsx
import { ThemeModeProvider } from "@/theme/ThemeModeContext";

function renderLogin(/* args */) {
  return render(
    <ThemeModeProvider>
      <BrowserRouter>
        <QueryClientProvider client={queryClient}>
          <LoginPage />
        </QueryClientProvider>
      </BrowserRouter>
    </ThemeModeProvider>,
  );
}
```

If multiple test files need the same wrapper (likely), factor to `panel-ui/src/test/renderWithProviders.tsx` and update all callers.

**Reproduction:**

```bash
cd panel-ui && npx vitest run src/pages/Login.test.tsx --reporter=basic
```

**Blocks:** Nothing currently. CI job `panel-ui vitest` tolerates these failures via whatever test-tolerance config is in place (or they're being ignored — worth auditing as part of the fix).

**Close when:** All 4 tests pass on `origin/main` without the rest of the suite regressing.

---

## How to add an entry

1. Next `KI-N` number (sequential, no reuse).
2. Short title.
3. Required fields: Opened, Severity (CRITICAL / HIGH / MEDIUM / LOW), Scope, Discovered by, Failure signature / symptoms, Root cause, Why-not-production-impact (if LOW/MEDIUM), Fix sketch, Reproduction, Blocks, Close-when criteria.
4. Keep to one screen per entry — if an issue needs more context, link to a plans/ or docs/adr/ file.

## How to close an entry

Move the entry to the `## Closed` section with a one-line note: `Closed 2026-NN-NN in <SHA>.` Keep the body intact for future reference.
