import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

const PKG_DIR = resolve(process.cwd(), "src/shells/admin/packages");

// JAB-328: the package Create form silently omitted max_ftp_accounts, so every
// panel-created package submitted the backend default 0 and FTP was disabled +
// hidden until an operator edited the package. Guard the whole class of bug:
// every package *limit* field (name="max_...") present on the Edit form — the
// authoritative full entitlement set — must also render on Create, or a future
// entitlement added to one screen but not the other silently ships zero.

function limitFields(file: string): Set<string> {
  const src = readFileSync(resolve(PKG_DIR, file), "utf8");
  const names = new Set<string>();
  for (const m of src.matchAll(/name="(max_[a-z_]+)"/g)) {
    names.add(m[1]);
  }
  return names;
}

describe("Package create/edit entitlement field parity (JAB-328)", () => {
  it("Create renders every max_* limit field that Edit has", () => {
    const create = limitFields("PackageCreate.tsx");
    const edit = limitFields("PackageEdit.tsx");
    const missingOnCreate = [...edit].filter((f) => !create.has(f)).sort();
    expect(
      missingOnCreate,
      `PackageCreate.tsx is missing limit fields present in PackageEdit.tsx: ${missingOnCreate.join(", ")}`,
    ).toEqual([]);
  });

  it("Create renders the FTP account limit (the reported gap)", () => {
    expect(limitFields("PackageCreate.tsx").has("max_ftp_accounts")).toBe(true);
  });
});
