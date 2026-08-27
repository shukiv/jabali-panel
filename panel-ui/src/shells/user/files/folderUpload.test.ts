// GH #1243 — dropping a folder must preserve its structure, not write a binary
// file named after the folder. collectDroppedEntries is the traversal that turns
// dropped file-system entries into a flat list of files, each tagged with its
// subfolder path. This pins the reporter's exact example.
import { describe, it, expect } from "vitest";
import { collectDroppedEntries, ancestorDirs } from "./FileManagerPage";

// Minimal FileSystemEntry fakes matching the browser's entries API.
function fileEntry(name: string): FileSystemEntry {
  return {
    isFile: true,
    isDirectory: false,
    name,
    file: (ok: (f: File) => void) => ok(new File(["x"], name)),
  } as unknown as FileSystemEntry;
}

function dirEntry(name: string, children: FileSystemEntry[]): FileSystemEntry {
  return {
    isFile: false,
    isDirectory: true,
    name,
    // Each createReader() gets its own "served" latch: readEntries returns the
    // batch once, then an empty array (the browser's end-of-directory signal).
    createReader: () => {
      let served = false;
      return {
        readEntries: (ok: (e: FileSystemEntry[]) => void) => {
          if (served) return ok([]);
          served = true;
          ok(children);
        },
      };
    },
  } as unknown as FileSystemEntry;
}

describe("GH #1243 — collectDroppedEntries", () => {
  it("flattens a dropped folder into files tagged with their subfolder", async () => {
    // my-folder/
    // ├── index.php
    // ├── test.txt
    // └── assets/
    //     └── style.css
    const roots = [
      dirEntry("my-folder", [
        fileEntry("index.php"),
        fileEntry("test.txt"),
        dirEntry("assets", [fileEntry("style.css")]),
      ]),
    ];

    const out = await collectDroppedEntries(roots);
    const byName = Object.fromEntries(out.map((o) => [o.file.name, o.relDir]));

    expect(out).toHaveLength(3);
    expect(byName["index.php"]).toBe("my-folder");
    expect(byName["test.txt"]).toBe("my-folder");
    expect(byName["style.css"]).toBe("my-folder/assets");
  });

  it("leaves a plain dropped file at the root (empty relDir)", async () => {
    const out = await collectDroppedEntries([fileEntry("readme.md")]);
    expect(out).toEqual([{ file: expect.any(File), relDir: "" }]);
  });
});

describe("GH #1243 — ancestorDirs (every level created individually)", () => {
  it("expands to each level, shallow first", () => {
    expect(ancestorDirs(["my-folder", "my-folder/assets"])).toEqual([
      "my-folder",
      "my-folder/assets",
    ]);
  });

  it("fills in intermediates for a deeply-nested-only folder", () => {
    // Files only at the bottom — every level must still be created (each as its
    // own single-level mkdir, so the agent chowns it to the tenant; a one-shot
    // mkdir -p would leave my-folder + my-folder/a root-owned).
    expect(ancestorDirs(["my-folder/a/b"])).toEqual([
      "my-folder",
      "my-folder/a",
      "my-folder/a/b",
    ]);
  });

  it("dedupes and ignores empty (root) entries", () => {
    expect(ancestorDirs(["", "x/y", "x/y", "x"])).toEqual(["x", "x/y"]);
  });
});
