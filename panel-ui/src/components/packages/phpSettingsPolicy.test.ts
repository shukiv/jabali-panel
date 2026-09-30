// GH #1701: the package editor's codec for php_settings_policy.
import { describe, expect, it } from "vitest";

import {
  PHP_SETTING_DIRECTIVES,
  decodePHPSettingsPolicy,
  defaultPHPSettingsPolicy,
  encodePHPSettingsPolicy,
} from "./phpSettingsPolicy";

describe("php settings policy codec (GH #1701)", () => {
  it("an untouched policy encodes to the canonical empty string", () => {
    expect(encodePHPSettingsPolicy(defaultPHPSettingsPolicy())).toBe("");
    expect(encodePHPSettingsPolicy(undefined)).toBe("");
  });

  it("encodes only what differs from the default, keys sorted", () => {
    const p = defaultPHPSettingsPolicy();
    p.post_max_size = "admin_only";
    p.memory_limit = "admin_only";
    expect(encodePHPSettingsPolicy(p)).toBe('{"memory_limit":"admin_only","post_max_size":"admin_only"}');
  });

  it("decodes every catalog directive, with the default where none is stored", () => {
    const p = decodePHPSettingsPolicy('{"memory_limit":"admin_only"}');
    expect(p.memory_limit).toBe("admin_only");
    for (const d of PHP_SETTING_DIRECTIVES.filter((d) => d !== "memory_limit")) {
      expect(p[d], d).toBe("tenant_allowed");
    }
  });

  it("keeps a stored key outside the catalog through a round trip", () => {
    // A security-sensitive directive opted in through the CLI must survive a
    // save from the editor, which does not render it.
    const stored = '{"memory_limit":"admin_only","open_basedir":"tenant_privileged"}';
    expect(encodePHPSettingsPolicy(decodePHPSettingsPolicy(stored))).toBe(stored);
  });

  it("a malformed stored value loads as the defaults", () => {
    expect(decodePHPSettingsPolicy("{nope")).toEqual(defaultPHPSettingsPolicy());
    expect(decodePHPSettingsPolicy('["memory_limit"]')).toEqual(defaultPHPSettingsPolicy());
  });
});
