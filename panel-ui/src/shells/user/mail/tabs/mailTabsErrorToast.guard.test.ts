// The share and forwarder tabs showed the API's raw error code in their
// toasts ("already_shared", "share_apply_failed") and dropped its detail.
// They go through extractApiError, which prefers detail, and toastCreateResult,
// which shows a create warning. This guard keeps the raw-code read out.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const TABS = ["SharedFoldersTab.tsx", "ForwardersTab.tsx"];

describe("mail tab error toasts", () => {
  for (const name of TABS) {
    it(`${name} shows the API detail, not the raw error code`, () => {
      const src = readFileSync(join(__dirname, name), "utf8");
      expect(src).not.toMatch(/response\?\.data\?\.error/);
      expect(src).toMatch(/extractApiError\(/);
      expect(src).toMatch(/toastCreateResult\(/);
    });
  }
});
