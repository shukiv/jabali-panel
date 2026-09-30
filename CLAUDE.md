# Coding conventions

Repo-wide patterns (route families, SearchableTable, Drawer for create+edit,
icon shim, list envelope, rate limits, etc.) live in
[docs/CONVENTIONS.md](docs/CONVENTIONS.md). **Read it before writing a new
handler, page, or hook** — every worktree and agent follows the same
patterns so merges across `main` / `wt-a` / `wt-b` / `wt-c` stay clean.

Anti-patterns already learnt the hard way are listed at the bottom of
CONVENTIONS.md; they're the quick way to avoid reopening a closed regression.

---

<!-- cbm:start -->
# codebase-memory-mcp — Code Intelligence

This project is indexed by **codebase-memory-mcp** as project ID `home-shuki-projects-jabali2` (9,272 nodes / 25,192 edges at last full index). Use its MCP tools for code discovery, impact analysis, and navigation instead of grep/find/Read when looking up code structure.

## Shared index across worktrees

Every worktree under `/home/shuki/projects/jabali2*` (main, wt-a, wt-b, wt-c) uses the **same** project ID `home-shuki-projects-jabali2`. Pass that literal string as `project` to every `mcp__codebase-memory-mcp__*` call, regardless of which worktree the session is in. The graph reflects `main` (or the last branch that was full-indexed); it will NOT reflect uncommitted edits in wt-a/b/c. Re-run `index_repository` only when a worktree's branch has substantive diff from main AND you need graph queries against that diff.

## Always Do

- **Use MCP graph tools first for code discovery.** `search_graph` / `trace_path` / `get_code_snippet` / `query_graph` / `get_architecture` — these short-circuit the grep→read→read→read loop for symbol lookup.
- **Run `detect_changes` before committing** to verify your edits touched the symbols you intended. Flag any surprise scope.
- **For renames**, prefer semantic rename patterns (Cypher query for `MATCH (n)-[:CALLS|REFERENCES]->(target)` + rewrite call sites as a unit) — naïve find-and-replace misses call-graph linkages.

## Never Do

- NEVER grep the codebase for a symbol name when `search_graph({project: "home-shuki-projects-jabali2", query: "…"})` would answer.
- NEVER assume a file is isolated. Every edit has upstream callers. Use `query_graph` (Cypher) for "who calls X" when unsure.
- NEVER commit without running `detect_changes` if the edit crossed package boundaries.

## Tools (load schemas via ToolSearch before first use)

| Tool | Use for |
|------|---------|
| `mcp__codebase-memory-mcp__search_graph` | Find functions, classes, routes by name / pattern / natural-language query |
| `mcp__codebase-memory-mcp__trace_path` | Call chains, data flow, cross-service HTTP links |
| `mcp__codebase-memory-mcp__get_code_snippet` | Read source of a specific symbol (use instead of Read for code) |
| `mcp__codebase-memory-mcp__query_graph` | Raw Cypher queries against the knowledge graph |
| `mcp__codebase-memory-mcp__get_architecture` | High-level structure — packages, layers, services |
| `mcp__codebase-memory-mcp__search_code` | Graph-augmented text search (grep-like fallback) |
| `mcp__codebase-memory-mcp__index_repository` | Build/refresh index for a given `repo_path` |
| `mcp__codebase-memory-mcp__index_status` | Check index age + node/edge counts |
| `mcp__codebase-memory-mcp__detect_changes` | Diff affected symbols vs last index (pre-commit gate) |
| `mcp__codebase-memory-mcp__manage_adr` | Read/write ADRs into the graph |

## Reindex cadence

Full reindex on main after merging substantive structural changes (new package, deleted file, renamed type). The index does NOT auto-refresh — call `index_repository({repo_path: "/home/shuki/projects/jabali2"})` explicitly.

<!-- cbm:end -->

---

# MCP Tools — Use These First

Three MCP categories are available in every session. Reach for them **before** falling back to built-in knowledge, grep, or manual browser checks.

## Context7 — Live Library Docs

Use **before** writing any code that calls a library API (GORM, Gin, antd, React, Stalwart JMAP, etc.). Training-data snapshots lag; Context7 returns version-pinned, current docs.

```
1. mcp__plugin_everything-claude-code_context7__resolve-library-id  → get library ID
2. mcp__plugin_everything-claude-code_context7__query-docs           → fetch docs / examples
```

**Trigger**: any time you're about to write a call to an external package and you're not 100% certain of the current API signature or option names.

## Chrome DevTools / claude-in-chrome — UI Validation

Use after any UI change instead of saying "should work" or "test manually".

```
mcp__chrome-devtools__take_screenshot   → capture current state
mcp__chrome-devtools__navigate_page     → open a URL
mcp__chrome-devtools__evaluate_script   → run JS in page context
mcp__claude-in-chrome__tabs_context_mcp → see open tabs before creating new ones
```

**Trigger**: after any `.tsx` change, after styling fixes, after API wiring changes that affect UI output.

## antd / shadcn-ui — Component Docs

Use before writing or customizing any antd or shadcn component. Avoid guessing prop names.

```
mcp__antd__antd_info    → component props + API
mcp__antd__antd_demo    → runnable example
mcp__antd__antd_token   → design tokens for theming
mcp__shadcn-ui__get_component → shadcn component source + usage
```

**Trigger**: any new antd/shadcn component, any theming or token change.

---

## Sub-agent mandate (applies to every agent spawned in this project)

Any agent launched via the `Agent` tool in the jabali2 worktree MUST:

1. **Before first edit** to any `.go`, `.tsx`, `.ts` or migration file — call `mcp__codebase-memory-mcp__search_graph` and `mcp__codebase-memory-mcp__trace_path` on the target symbol (project=`home-shuki-projects-jabali2`) and include the blast radius (direct callers + call chain depth) in its reasoning. If the target is a widely-called function (>10 callers, or touched by an HTTP route), stop and report to the dispatcher BEFORE editing.

2. **Before committing** — call `mcp__codebase-memory-mcp__detect_changes` (project=`home-shuki-projects-jabali2`) and verify affected symbols match the intended scope. If the change set includes unexpected symbols, pause and report.

3. **For unfamiliar code exploration** (any "how does X work?" type question) — prefer `mcp__codebase-memory-mcp__search_graph` / `query_graph` / `get_code_snippet` over `Grep`/`Read`. Text search is the fallback, not the default.

4. **For renames** — use semantic-rename pattern via `query_graph` (Cypher lookup of all call sites, then Edit each) rather than find-and-replace across files.

5. **Agents commit to branches, never to `main`. Agents never `git push`.**
   - Before your first commit, create a feature branch: `git checkout -b <wave-or-task-slug>` (e.g. `wave-c-http-handlers`, `fix-ssl-badge`, `fix-impersonate-port`, `chore-update-deps`). Use a short, descriptive slug — no ticket numbers required.
   - If you arrive on `main` and need to commit, switch to a new branch first. Committing directly to `main` is a dispatch failure; the dispatcher will revert your work.
   - Never run `git push`, `git push origin`, `git push --force`, or anything that publishes to remote. The dispatcher is the ONLY entity that pushes, after independent verification and a deliberate merge to `main`.
   - Never run destructive git (`reset --hard`, `checkout --`, `clean -fd`, `branch -D`) outside your own feature branch — you can corrupt a concurrent agent's working tree.
   - **Before your final report, rebase onto latest `origin/main` and re-run tests.** This is a dispatch-check item, not optional. Concretely:
       ```
       git fetch origin main
       git rebase origin/main        # resolve any conflicts HERE, with full context
       # re-run the relevant test suite for your change
       ```
     Rationale: conflicts between your work and what landed on `main` during your session belong to you — you have the intent and the context. If the dispatcher resolves them at merge time, they're guessing with `grep`. Two recent incidents (wt-a catalog expansion vs applications.go, wt-a jabali-app CLI vs deleted `internal/clientapi`) shipped only because the worktree author hadn't rebased; both cost cycles and one came close to landing a half-working CLI. If your rebase is clean, merge to `main` is a fast-forward — no three-way merge, no guessing.
   - Your final report MUST include: the branch name, the commit SHAs on that branch, a `git log main..<your-branch>` summary, AND confirmation that you rebased onto the latest `origin/main` and re-ran tests post-rebase.

This applies to planner, coder, backend-dev, security-architect, Explore, and every other sub-agent type. When a wave brief asks you to modify a symbol, the dispatcher assumes you've run impact analysis first AND your work lives on a feature branch. Acknowledge both in your summary output.

If the codebase-memory-mcp index is missing or stale for `home-shuki-projects-jabali2`, call `mcp__codebase-memory-mcp__index_repository({repo_path: "/home/shuki/projects/jabali2"})` once and retry. Never skip the step silently.
