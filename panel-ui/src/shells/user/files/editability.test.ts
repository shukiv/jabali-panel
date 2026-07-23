import { describe, expect, it } from "vitest";

import { EDIT_MAX_BYTES, isTextEditable } from "./editability";
import type { FileEntry } from "./filesApi";

function entry(over: Partial<FileEntry>): FileEntry {
  return {
    name: "test.txt",
    is_dir: false,
    size: 10,
    is_symlink: false,
    ...over,
  } as FileEntry;
}

describe("isTextEditable", () => {
  // GH #532: a freshly-created 0-byte file must be editable — the whole point
  // of "create then edit" — even though it renders "—" and has size 0.
  it("allows empty (0-byte) files", () => {
    expect(isTextEditable(entry({ size: 0 }))).toBe(true);
  });

  it("allows a normal small text file", () => {
    expect(isTextEditable(entry({ size: 1234 }))).toBe(true);
  });

  it("allows a file just under the cap", () => {
    expect(isTextEditable(entry({ size: EDIT_MAX_BYTES - 1 }))).toBe(true);
  });

  it("rejects a file at/over the cap (editor loads the full file)", () => {
    expect(isTextEditable(entry({ size: EDIT_MAX_BYTES }))).toBe(false);
    expect(isTextEditable(entry({ size: EDIT_MAX_BYTES + 1 }))).toBe(false);
  });

  it("rejects directories and symlinks", () => {
    expect(isTextEditable(entry({ is_dir: true, size: 0 }))).toBe(false);
    expect(isTextEditable(entry({ is_symlink: true, size: 5 }))).toBe(false);
  });
});
