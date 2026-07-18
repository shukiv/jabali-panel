// Drift guard for JAB-14: the admin Support page renders the CLI-only runbook
// list from cli-only-runbooks.ts, but the recorded intent lives in
// docs/operations/cli-only-commands.md. If a command is added/renamed/removed
// in the doc without updating the structured source (or vice versa), the GUI
// would quietly show a stale set. This test fails on that drift.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import { ALL_CLI_ONLY_RUNBOOKS } from "./cli-only-runbooks";

// Vitest runs with cwd = panel-ui/ (the vite.config.ts dir); the doc lives at
// the repo root under docs/operations/.
const DOC_PATH = resolve(
  process.cwd(),
  "../docs/operations/cli-only-commands.md",
);

const doc = readFileSync(DOC_PATH, "utf8");

describe("cli-only runbooks source matches the doc", () => {
  it("every structured runbook command appears in cli-only-commands.md", () => {
    const missing = ALL_CLI_ONLY_RUNBOOKS.filter(
      (rb) => !doc.includes(rb.command),
    ).map((rb) => rb.command);
    expect(
      missing,
      `commands in cli-only-runbooks.ts but not in the doc: ${missing.join(", ")}`,
    ).toEqual([]);
  });

  it("every command backticked in the doc is present in the structured source", () => {
    // Pull `jabali`-ish subcommands out of the doc's markdown tables. The doc
    // wraps each in backticks and some cells list two (e.g. `audit verify` /
    // `audit prune`), so match every backtick span in the two command columns.
    const known = new Set(ALL_CLI_ONLY_RUNBOOKS.map((rb) => rb.command));
    const docCommands = new Set<string>();
    for (const line of doc.split("\n")) {
      // Only the command tables have a leading "| `cmd`" cell.
      const m = /^\|\s*`([^`]+)`(?:\s*\/\s*`([^`]+)`)?\s*\|/.exec(line);
      if (!m) continue;
      docCommands.add(m[1]);
      if (m[2]) docCommands.add(m[2]);
    }
    // Sanity: the doc parse found the tables at all.
    expect(docCommands.size).toBeGreaterThan(10);
    const unlisted = [...docCommands].filter((c) => !known.has(c));
    expect(
      unlisted,
      `commands in the doc tables but not in cli-only-runbooks.ts: ${unlisted.join(", ")}`,
    ).toEqual([]);
  });
});
