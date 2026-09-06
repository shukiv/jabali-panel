import { describe, expect, it } from "vitest";

import {
  PACKAGE_DEFAULTS,
  PACKAGE_FIELD_PARITY,
  PACKAGE_LIMIT_FIELDS,
  SPECIAL_LIMIT_FIELDS,
  decodePackageForm,
  encodePackagePayload,
  type PackageRecord,
} from "./packageFields";

// JAB-331 (replaces the JAB-328 source-scrape parity test): PackageCreate and
// PackageEdit render through one shared PackageEditor Module, so an entitlement
// can no longer exist in one mode and not the other. What still needs guarding is
// that the canonical model stays complete against the wire contract — a numeric
// entitlement added to the type but not rendered (the JAB-328 "silently ships 0"
// bug), or a rendered field with no default. Those are enforced here (runtime)
// and, more strongly, at the type level via PACKAGE_FIELD_PARITY under `tsc -b`.

describe("Package entitlement model parity (JAB-331)", () => {
  const covered = [
    ...PACKAGE_LIMIT_FIELDS.map((f) => f.name),
    ...SPECIAL_LIMIT_FIELDS,
  ] as string[];

  it("holds the compile-time parity flag (every numeric wire field is rendered exactly once)", () => {
    // The real teeth is the TYPE of PACKAGE_FIELD_PARITY: adding a numeric field
    // to PackageFormValues without listing it in PACKAGE_LIMIT_FIELDS or
    // SPECIAL_LIMIT_FIELDS makes this a compile error under `tsc -b` (CI build).
    expect(PACKAGE_FIELD_PARITY).toBe(true);
  });

  it("renders a form field for every numeric default (no silently zero-shipping entitlement)", () => {
    const numericDefaults = Object.entries(PACKAGE_DEFAULTS)
      .filter(([, v]) => typeof v === "number")
      .map(([k]) => k);
    const unrendered = numericDefaults.filter((k) => !covered.includes(k)).sort();
    expect(unrendered, `numeric defaults with no form field: ${unrendered.join(", ")}`).toEqual([]);
  });

  it("gives every rendered entitlement field a create default", () => {
    const missing = covered.filter((name) => !(name in PACKAGE_DEFAULTS)).sort();
    expect(missing, `fields missing a default: ${missing.join(", ")}`).toEqual([]);
  });

  it("includes the FTP and database-user entitlements (JAB-328 / JAB-329)", () => {
    expect(covered).toContain("max_ftp_accounts");
    expect(covered).toContain("max_database_users");
  });

  it("has no duplicate field names", () => {
    const names = PACKAGE_LIMIT_FIELDS.map((f) => f.name);
    expect(new Set(names).size).toBe(names.length);
  });

  it("gives each field a resolvable min and width", () => {
    for (const f of PACKAGE_LIMIT_FIELDS) {
      expect(typeof f.min, `${f.name} min`).toBe("number");
      expect(f.width, `${f.name} width`).toBeTruthy();
    }
  });
});

describe("Package CSV codecs round-trip (JAB-331 AC2)", () => {
  it("decodes CSV wire fields to arrays and re-encodes them to CSV", () => {
    const record: PackageRecord = {
      ...PACKAGE_DEFAULTS,
      id: "pkg-1",
      docker_app_slugs: "wordpress,ghost",
      allowed_backup_destination_kinds: "local,s3",
    };
    const form = decodePackageForm(record);
    expect(form.docker_app_slugs).toEqual(["wordpress", "ghost"]);
    expect(form.allowed_backup_destination_kinds).toEqual(["local", "s3"]);

    const payload = encodePackagePayload(form);
    expect(payload.docker_app_slugs).toBe("wordpress,ghost");
    expect(payload.allowed_backup_destination_kinds).toBe("local,s3");
  });

  it("encodes empty multi-selects as empty strings", () => {
    const payload = encodePackagePayload({ ...PACKAGE_DEFAULTS });
    expect(payload.docker_app_slugs).toBe("");
    expect(payload.allowed_backup_destination_kinds).toBe("");
  });

  it("decodes a record with empty CSV fields to empty arrays", () => {
    const record: PackageRecord = {
      ...PACKAGE_DEFAULTS,
      id: "pkg-2",
      docker_app_slugs: "",
      allowed_backup_destination_kinds: "",
    };
    const form = decodePackageForm(record);
    expect(form.docker_app_slugs).toEqual([]);
    expect(form.allowed_backup_destination_kinds).toEqual([]);
  });
});
