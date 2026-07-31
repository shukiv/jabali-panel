# Jabali Conventions

Repo-wide patterns every worktree (main, wt-a, wt-b, wt-c) and agent follows. These are descriptive: they codify what the codebase already does so new features slot in without bikeshedding. Deviations need an ADR.

If this file and a matching ADR disagree, the ADR wins — but open a PR to fix this file.

---

## Security over functionality — non-negotiable

Security hardening is never sacrificed to make a feature work. If a sandbox, drop-privilege, or namespace-isolation directive is blocking a workflow, **change the workflow**, do not loosen the hardening.

Concrete rules every contributor follows:

- **Do not remove `PrivateTmp=`, `ProtectSystem=`, `ProtectKernelTunables=`, `NoNewPrivileges=`, `CapabilityBoundingSet=`, `AmbientCapabilities=`, or any other systemd hardening already present on a unit** to unblock a feature. If a command inside the service can't reach a path that's sandboxed (most often `/tmp` via `PrivateTmp=yes`), move the staging location outside the sandbox — e.g., `/var/lib/jabali-agent-staging/` for agent-side tarballs — rather than dropping the isolation.
- **Do not widen a file's mode (0755 on a dir that should be 0700, `chmod a+r` on root-owned state) to let a handler work.** Re-evaluate who should own it and use the privilege-drop path (e.g. `systemd-run --uid=<user>`) with appropriately-scoped paths.
- **Do not add `-k`/`--insecure` to curl, `InsecureSkipVerify: true` to Go HTTP clients, or disable a firewall rule** "just to test". Production code ships with the production defaults.
- **Do not bypass install.sh's validator hard-fails** (`recursor.conf` loopback-only check, `pdns` bind addresses, UFW post-install SSH rule, etc.). If a validator fails legitimately, fix the *config* — the validator exists because we've been burnt by that configuration drifting.

When a hardening setting legitimately has to go, it requires:
1. An ADR explaining the threat model delta + mitigation,
2. A matching comment at the edit site pointing at that ADR,
3. Reviewer sign-off.

Scar story behind this rule: M11/agent-install workflow first "fixed" Drupal/Joomla tarball-extract failures by dropping `PrivateTmp=yes` from `jabali-agent.service`. That trivially opens the agent to `/tmp` symlink races from tenant users. The correct fix was to stage tarballs outside `/tmp` (see `panel-agent/internal/commands/staging_tmp.go`) and keep `PrivateTmp=yes`. Always pick the longer path over the unsafe shortcut.

---

## Repository layout

```
panel-api/            Go + Gin HTTP server, reads/writes MariaDB via GORM
  cmd/server/         cobra-based entry (serve.go wires Deps into app.NewWithDeps)
  internal/api/       Gin route families, one file per resource
  internal/app/       app.NewWithDeps (Deps{} + route mount)
  internal/config/    Viper + toml, defaults in config.Defaults()
  internal/db/        migrations/ + GORM open helper
  internal/eventsources/  in-process goroutines producing M14 envelopes
  internal/ginctx/    request-scoped Claims + RequestID helpers
  internal/ids/       NewULID()
  internal/middleware/  RequireAdmin/RequireKratosSession/RequireLocalhost/ratelimit
  internal/models/    GORM structs; one struct per table
  internal/notifications/  M14 dispatcher + senders
  internal/reconciler/  domain convergence loop
  internal/repository/  one file per aggregate; interface + gorm impl
panel-agent/          Go NDJSON unix-socket daemon (root-privileged)
  cmd/jabali-agent/   entry; flag-based config
  internal/commands/  one file per command; init() { Default.Register(...) }
  internal/server/    UDS listener
panel-ui/             React + Vite + AntD SPA
  src/apiClient.ts    axios instance + error envelope
  src/App.tsx         react-router route tree
  src/components/     cross-shell components (JabaliHeader, SearchableTable, NotificationBell)
  src/hooks/          useQueries (list/create/update/delete), useTableURL
  src/icons/          @icons shim over lucide-react (AntD-compatible Outlined names)
  src/nav.ts          adminNav + userNav arrays — single source for sidebar items
  src/shells/         AdminLayout.tsx + UserLayout.tsx
  src/shells/admin/<resource>/   one folder per admin feature
agentwire/            shared JSON types for panel↔agent wire contract
internal/             repo-root packages reused across panel-api + panel-agent
plans/                multi-step blueprints + runbooks (plans/<milestone>.md)
docs/
  adr/                Architecture Decision Records (0001 onward)
  runbooks/           operator-facing procedures
  BLUEPRINT.md        milestone catalogue (status table at bottom)
  CONTRIBUTING.md     dev setup + commit rules
  CONVENTIONS.md      this file
```

---

## Go — panel-api

### Route family pattern

One file per resource under `panel-api/internal/api/<resource>.go` exporting `RegisterXxxRoutes(g *gin.RouterGroup, cfg XxxHandlerConfig)`.

```go
type XxxHandlerConfig struct {
    Repo            repository.XxxRepository
    Agent           agent.AgentInterface  // optional — handler checks nil
    Log             *slog.Logger
    StrictRateLimit gin.HandlerFunc       // optional — wire from rl.Strict()
}

func RegisterXxxRoutes(g *gin.RouterGroup, cfg XxxHandlerConfig) {
    if cfg.Repo == nil {
        panic("api.RegisterXxxRoutes: cfg.Repo is nil")
    }
    h := &xxxHandler{cfg: cfg}
    admin := g.Group("/admin/xxx", middleware.RequireAdmin())
    admin.GET("", h.list)
    admin.POST("", h.create)
    admin.PATCH("/:id", h.update)
    admin.DELETE("/:id", h.delete)
}
```

- Required deps: panic at boot (programmer error, not a 500).
- Optional deps: check `nil` at handler time, degrade gracefully.
- Mount from `app.go` — add the call inside `NewWithDeps` guarded by the specific dep being present.
- Admin-only routes: `middleware.RequireAdmin()` on the sub-group.
- Localhost-only (agent → panel-api): `middleware.RequireLocalhost()` on the sub-group, register **off `r` (root)** not `v1` so `RequireKratosSession` doesn't block the agent.

### List response envelope

Every paginated list returns:

```json
{
  "data":      [...],
  "total":     123,
  "page":      1,
  "page_size": 50
}
```

**Not** `{items,total}`. See `feedback_verify_wire_contract` — this was a real drift in M21. Panel-ui's `useListQuery` reads `.data`.

### Repository pattern

One file per aggregate at `panel-api/internal/repository/<aggregate>_repository.go`.

```go
type XxxRepository interface {
    Create(ctx context.Context, x *models.Xxx) error
    Update(ctx context.Context, x *models.Xxx) error
    Delete(ctx context.Context, id string) error
    FindByID(ctx context.Context, id string) (*models.Xxx, error)
    ListAll(ctx context.Context, opts ListOptions) ([]models.Xxx, int64, error)
}

type xxxRepo struct{ db *gorm.DB }

func NewXxxRepository(db *gorm.DB) XxxRepository { return &xxxRepo{db: db} }
```

- `ListOptions` (shared: `repository/list_options.go`) carries Offset/Limit/Search/Sort/Order. `applyListOptions` whitelists sort columns.
- Aggregate-specific filters → new method on the repo, not another field on `ListOptions` (M18's narrow-iface approach).
- GORM `ErrRecordNotFound` must be wrapped to `repository.ErrNotFound`.
- Every repo has a `_test.go` with sqlmock coverage for the SQL shape.

### Identifiers

- Use `ids.NewULID()` for new primary keys — 26-char CHAR(26) ULID.
- Never string-concat SQL. GORM parameters only.

### Validation

- Validate at system boundaries (handler, agent command, envelope deserialise). Don't re-validate internally.
- Return 422 for domain-rule violations (known kind, required field, range).
- Return 400 for malformed JSON.
- Never trust the agent just because it's localhost — mirror the allowlist on the panel-api side (M14 defense-in-depth).

### Errors

```go
if err != nil {
    return fmt.Errorf("create user: %w", err)
}
```

- Always `%w` so callers can `errors.Is` / `errors.As`.
- Log at info+ with `slog`; never use `log.Println`.
- Sentinel errors: package-level `var ErrXxx = errors.New("pkg: xxx")`; callers use `errors.Is`.

### Testing

- `go test -race ./panel-api/...` must be clean.
- Table-driven tests where the input varies.
- Prefer narrow interfaces that fakes can implement with 3–5 methods (see `eventsources.SSLCertLister`) over implementing the full repo interface in tests.
- Integration tests use miniredis for Redis, sqlmock for DB. No real services in CI.
- TMPDIR on this box is small — set `TMPDIR=/home/shuki/tmp-go` when running large test matrices.

---

## Go — panel-agent

### Command handler pattern

One file per command at `panel-agent/internal/commands/<command>.go`:

```go
func xxxHandler(ctx context.Context, params json.RawMessage) (any, error) {
    if len(params) == 0 {
        return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
    }
    var p xxxParams
    if err := json.Unmarshal(params, &p); err != nil {
        return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
    }
    // ... validate p ...
    return result, nil
}

func init() {
    Default.Register("xxx.verb", xxxHandler)
}
```

- `Default.Register` in `init()` — no centralised switch statement.
- Wire contract lives in `agentwire/` — both panel-api and panel-agent depend on it.
- Return `*agentwire.AgentError` (typed) for known failure modes; any other error is mapped to `CodeInternal`.
- Agent never opens outbound HTTPS to third parties. HTTPS goes through panel-api (ADR-0050); agent calls panel-api over `/run/jabali-panel/api.sock`.

---

## TypeScript — panel-ui

### Framework stack (ADR-0037)

- **React 18** + **Vite 6** + **TypeScript strict**
- **AntD 5** for every primitive — never hand-roll a button/modal. For authoritative component API + live demos use the `antd` skill or `mcp__antd__*` tools (`antd_doc`, `antd_demo`, `antd_list`, `antd_token`, `antd_semantic`, `antd_changelog`). Don't guess prop shapes from memory — AntD 5.x changes fast.
- **TanStack Query v5** for server state (no Redux, no Zustand)
- **react-router v6** — route tree in `App.tsx`
- **lucide-react** icons via the `@icons` shim — **never import from `@ant-design/icons`**

### Icon shim

All icons imported from `@icons` (alias for `src/icons/index.tsx`):

```tsx
import { BellOutlined, EditOutlined, PlusOutlined } from "@icons";
```

- Names match AntD's old `*Outlined` form so existing code reads the same.
- Strokes are lucide's default weight (1 — see `shim()`).
- Adding a new icon: import the lucide component in `icons/index.tsx`, wrap with `shim()`, export as `XxxOutlined`.
- Adding the lucide component also requires adding it to the import list at the top of `icons/index.tsx` — the shim file has a flat list, not path imports.

### Admin shell route

New admin page:

1. Create `panel-ui/src/shells/admin/<resource>/<ResourceList>.tsx`.
2. Import + mount in `App.tsx` under `<Route path="/jabali-admin">`.
3. Add entry to `adminNav` in `src/nav.ts` — `{key, label, icon: navIcon(XxxOutlined), path}`.
4. The sidebar and drawer pick it up automatically via `AdminLayout.tsx`.

Same 4 steps for user shell routes under `<Route path="/jabali-panel">` + `userNav`.

### List page pattern

Copy `src/shells/admin/ips/AdminIPList.tsx` as the reference. Key pieces:

```tsx
const query = useTableURL<Row>({
  resource: "admin/xxx",        // panel-api path; hooks build /api/v1/ prefix
  defaultSort: "created_at",
  defaultOrder: "desc",
});
const deleteMutation = useDeleteMutation({ resource: "admin/xxx" });

<SearchableTableStringQ<Row>
  rowKey="id"
  loading={query.isLoading}
  dataSource={query.items}
  initialSearch={query.params.q}
  searchPlaceholder="Search …"
  onSearchChange={(q) => query.setParams({ q, page: 1 })}
  pagination={{
    current: query.params.page,
    pageSize: query.params.pageSize,
    total: query.total,
  }}
  scroll={{ x: "max-content" }}  // mandatory on every Table (ADR-0046 responsive)
>
  <Table.Column dataIndex="name" title="Name" />
  …
</SearchableTableStringQ>
```

- **Children are `<Table.Column>` elements.** Not a `columns` prop. The wrapper hoists them.
- `query.items` + `query.total` — don't dig into `.data.data`.
- Always set `scroll={{ x: "max-content" }}` so tables stay usable on mobile (M23 lesson).
- Row click that edits: `render={(name, row) => <a onClick={() => openEdit(row)}>{name}</a>}`.

### Drawer for create + edit

```tsx
<Drawer
  title={isEdit ? `Edit ${existing.name}` : "Add xxx"}
  open={open}
  onClose={onClose}
  width={isDesktop ? 520 : undefined}   // fullscreen <lg per ADR-0046
  placement="right"
  destroyOnClose
>
  <Form<FormValues> form={form} layout="vertical" onFinish={handleSubmit} />
</Drawer>
```

- `destroyOnClose` — avoids stale form state between create + edit.
- Shared drawer for create + edit (pass `existing?: Row` prop).
- Use `Grid.useBreakpoint()` + `screens.lg !== false` for the desktop check, same as AdminLayout.

### Destructive action pattern

- `<Popconfirm title="Delete X?" onConfirm={...}>` wrapping the Delete button.
- `message.success` / `message.error` for mutation outcomes — never `alert()` or silent failures.

### Row action buttons (Tables → "Actions" column)

- Use `<RowActionButton icon={...}>Label</RowActionButton>` from `src/components/RowActionButton.tsx` for non-destructive row actions (Edit, View, DNS, Open in PMA, Clone, …). Filled style, default `color="primary"`. Icon required.
- Use `<RowDeleteButton ... />` (or `<RowActionButton danger icon={<DeleteOutlined/>}>Delete</RowActionButton>` inside a Popconfirm for compound flows) for destructive actions. Filled style, `color="danger"`.
- DO NOT use `type="text"` or `type="link"` for row actions. They render visually inconsistent across light/dark themes, blend into the row background, and produced the M-period of "are these clickable?" UX bugs.
- Toolbar buttons (above the table — Create, Refresh, Bulk Actions) keep `type="primary"` / default outlined style. Only the per-row column standardizes on filled.

### Hooks

All CRUD flows through `src/hooks/useQueries.ts`:

| Hook | Purpose |
|---|---|
| `useListQuery<T>({resource, params})` | GET list; returns `{data, total, page, pageSize, isLoading}` |
| `useGetQuery<T>({resource, id})` | GET one |
| `useCreateMutation<T>({resource})` | POST |
| `useUpdateMutation<T>({resource})` | PATCH |
| `useDeleteMutation({resource})` | DELETE |

Wrapper pattern: cross-resource side effects (e.g. "after domain create, invalidate SSL list") go in the component's `onSuccess` callback, not a new hook.

### Responsive

- Every admin/user table: `scroll={{ x: "max-content" }}`.
- AdminLayout swaps Sider → Drawer below lg breakpoint automatically.
- `Grid.useBreakpoint()` inside components that need finer control.
- Test mobile/tablet in Playwright before merging framework bumps (M23 lesson: `feedback_framework_bump_needs_e2e`).

### Responsive Drawer width

- Desktop: 520px standard. Tables-in-drawer: 800px.
- Mobile: omit `width` so AntD defaults to 100vw.

---

## Patterns + decisions by feature

| Concern | Convention | Reference |
|---|---|---|
| Auth source | Kratos only (panel-api never stores passwords post-M20) | ADR-0034 |
| Ingress | nginx → panel-api unix socket `/run/jabali-panel/api.sock` | ADR-0050 |
| Panel↔agent wire | NDJSON over unix socket `/run/jabali/agent.sock`, one request per connection | ADR-0001, `agentwire/` |
| DB access | GORM; migrations via golang-migrate; next free migration number grep `panel-api/internal/db/migrations/` | ADR-0005 |
| Primary keys | ULID (`CHAR(26)`), generated via `ids.NewULID()` | — |
| Background jobs | In-process goroutines tied to `ctx.Done()`. No external queue for per-panel work. | — |
| Event pub/sub | M14 Redis Streams (`jabali:notifications:*`) | ADR-0056 |
| Rate limiting | Shared `middleware.RateLimiter` (per-IP) + per-resource bucket where user identity matters (M14 broadcast per-admin) | — |
| Secrets | DB or `/etc/jabali/panel.env`. Never logged, never echoed in list/get responses (`json:"-"` on struct fields) | — |
| HMAC outbound | `hmac.New(sha256.New, secret)` → `sha256=<hex>` in `X-Jabali-Signature` | ADR-0056 / senders/webhook.go |
| Web Push | VAPID via SherClockHolmes/webpush-go; keys on `server_settings`; subscriptions per-browser | ADR-0057 |
| Mobile/tablet | Sider → Drawer <lg; `scroll={{ x: "max-content" }}` on every table | ADR-0046 |
| Icons | lucide-react via `@icons` shim — no `@ant-design/icons` imports | — |
| Commits | Conventional format (`feat:`, `fix:`, …); no `Co-Authored-By`; NEVER push from an agent worktree | CONTRIBUTING.md + CLAUDE.md |
| Git flow | Feature branch off latest `origin/main`; rebase before final report; dispatcher is the only entity that pushes | CLAUDE.md |
| Automation API auth | HMAC-SHA256 over `METHOD || PATH || ts || sha256(BODY)`, 5-min skew window, scope-checked per route via `RequireScope` | ADR-0093 / `middleware/automation_hmac.go` |
| Per-row denormalized stats | List handler does ONE batch lookup (e.g. `BWDaily.SumByDomainIDs`) and writes the result onto each list-row struct field. Avoid N+1 fetches and per-row `useQuery` calls. | M13.1 `domains.go` list path |
| AppArmor profiles | Author against AA 4.x rules verified by `make aa-smoke` (per-profile `aa-exec` + socket dial probe). Never ship a profile that hasn't passed the harness. | ADR-0086 amendment / `tools/aa-smoke/` |

---

## When adding a new milestone / large feature

1. **Blueprint first**: write `plans/m<N>-<slug>.md` with 5-9 steps; each step gets a branch name + dispatchable context brief.
2. **ADRs**: open `docs/adr/00XX-<slug>.md` for any decision future maintainers will second-guess.
3. **Opus-review** the plan before executing (existing pattern — see `project_plan_m14_notifications`).
4. **Memory**: write `project_plan_<slug>.md` before work starts, `project_<slug>_shipped.md` after merge. Link both from `MEMORY.md`.
5. **BLUEPRINT update**: flip status in the milestone table + the prose block at the top of the milestone's section.
6. **Runbook**: if operator actions are non-obvious, add `plans/<slug>-runbook.md`.

---

## Anti-patterns already learnt

These have bitten us. Don't repeat:

| Don't | Because | Memory link |
|---|---|---|
| Import from `@ant-design/icons` | We use lucide via `@icons` shim — M21 swap | — |
| Use `{items, total}` envelope | Handler emits `{data, total, page, page_size}`; UI will silently render empty | `feedback_verify_wire_contract` |
| Seed data from migrations referencing app-populated tables | Migration 000057 broke fresh installs | `feedback_migration_data_seed_ordering` |
| Add `--legacy-peer-deps` to npm flows | Produces a lockfile `npm ci` rejects in prod | `feedback_no_legacy_peer_deps_with_overrides` |
| Skip `scroll={{ x: "max-content" }}` on a Table | Mobile truncation; M23 regression | — |
| Use `@icons` and inline `@ant-design/icons` in the same file | Import confusion; every icon goes through the shim | — |
| Register an internal route off `v1` | `RequireKratosSession` blocks the agent; internal routes mount off root `r` | M14 Step 4 |
| Dispatch sub-agents for well-specified work | Drift from plan contracts; rework cost > just doing it | `feedback_never_agents` |
| Commit to `main` from a feature worktree | Dispatcher is the only pushing entity; agents must branch | CLAUDE.md |
| Skip the pre-merge rebase | Phantom "N ahead" reports + merge conflicts surface at push time | `feedback_fetch_rebase_before_deploy` |
| Hand-roll a SQL query string | GORM parameters only; any `WHERE ... = '" + id + "'"` fails code review | security.md |
| Hand an agent-written file to panel-api via a `/tmp` path | `jabali-panel` runs `PrivateTmp=yes` → the agent's `/tmp` file is invisible to it (`os.Open` ENOENT). Stage in a shared `/var/lib/jabali-*` dir (e.g. `/var/lib/jabali-uploads`) that's in panel-api's ReadWritePaths + AppArmor | `feedback_agent_panel_file_handoff_not_tmp` (GH #756) |

---

## Pointers

- ADR index: `docs/adr/README.md`
- Milestone status: `docs/BLUEPRINT.md` (table at the bottom)
- Runbooks: `docs/runbooks/` + `plans/<milestone>-runbook.md`
- Per-worktree context: `CLAUDE.md` at repo root
