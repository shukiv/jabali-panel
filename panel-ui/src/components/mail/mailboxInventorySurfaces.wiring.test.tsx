// mailboxInventorySurfaces.wiring.test.tsx (JAB-333) — pins the surface wiring
// this slice retired: the admin + tenant mailbox inventories now trigger a
// password rotate with ONE click (auto-generate + reveal-once, no form modal),
// and the tenant delete uses the shared declarative RowActions `confirm:` prop
// instead of a hand-rolled feedback.modal.confirm. The rotate/reveal/delete CORE
// is behavior-tested in mailboxInventory.test.tsx + useMailboxes.test.tsx; this
// guards that the two adapters keep routing through it and do not grow a fourth
// copy (a form, or an inline modal) back.
//
// The negative pins are non-vacuous: each removed string was present in these
// files before this slice (a `<Modal>` reset form with `<PasswordInput>`, a
// tenant `feedback.modal.confirm` delete), so re-introducing either reddens.

import { readFileSync } from "fs";
import { resolve } from "path";
import { describe, expect, it } from "vitest";

// vitest runs with cwd = the panel-ui package root.
const adminSrc = readFileSync(
  resolve(process.cwd(), "src/shells/admin/mail/AdminMailPage.tsx"),
  "utf8",
);
const tenantSrc = readFileSync(
  resolve(process.cwd(), "src/shells/user/mail/tabs/MailboxesTab.tsx"),
  "utf8",
);

describe("AdminMailPage password rotate wiring", () => {
  it("rotates on one click via the shared hook (no reset form modal)", () => {
    expect(adminSrc).toContain('label: "Rotate password"');
    expect(adminSrc).toContain("rotatePassword({ id: row.id, email: row.email");
  });

  it("no longer renders the custom-password reset form", () => {
    // The form's state, its PasswordInput, and the antd Form import are gone.
    expect(adminSrc).not.toContain("setResetTarget");
    expect(adminSrc).not.toContain("PasswordInput");
  });
});

describe("MailboxesTab (tenant) rotate + delete wiring", () => {
  it("rotates on one click via the shared hook (no reset form modal)", () => {
    expect(tenantSrc).toContain('label: "Rotate password"');
    expect(tenantSrc).toContain("rotatePassword({ id: row.id, email: row.email");
    expect(tenantSrc).not.toContain("PasswordInput");
  });

  it("delete uses the shared declarative RowActions confirm, not a hand-rolled modal", () => {
    // Retired: the copied `feedback.modal.confirm({ ... onOk })` delete flow.
    expect(tenantSrc).not.toContain("feedback.modal.confirm");
    // Present: the same declarative `confirm:` prop the other two surfaces use.
    expect(tenantSrc).toContain('okText: "Delete" }');
    expect(tenantSrc).toContain("confirm: {");
  });
});
