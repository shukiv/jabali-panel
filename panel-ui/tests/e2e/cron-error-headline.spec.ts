// GH #1686 item 5: raw cronops validation errors must reach the user as a
// friendly, per-field message. This mirrors the vitest guard
// (AdminCreateCronModal.test.tsx) in Chromium against the built SPA, per the
// .tsx real-browser rule. The backend now sends { error, field, code, detail }
// with the structured cronvalidate code; the shared cronErrorHeadline map turns
// that code into a human sentence the same way on the tenant and admin doors.
import { admin, mockApi, signIn, test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

async function setup(page: Page): Promise<void> {
  await mockApi(page, { me: admin });
  // Admin cron list — empty is fine; we only exercise the Create drawer.
  await page.route("**/api/v1/admin/cron", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [] }),
    });
  });
  // The create POST is rejected with the structured validation_failed shape
  // the fixed API now emits: a code plus the CLEAN validator detail.
  await page.route("**/api/v1/cron", (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    return route.fulfill({
      status: 400,
      contentType: "application/json",
      body: JSON.stringify({
        error: "validation_failed",
        field: "command",
        code: "binary_not_allowed",
        detail: 'first token must be "wp", got "ls"',
      }),
    });
  });
}

test.describe("GH #1686 item 5 — friendly cron validation errors", () => {
  test("admin Create shows the mapped headline, never the raw backend detail", async ({ page }) => {
    await setup(page);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);

    await page.goto("/jabali-admin/cron");

    // Open the Create drawer.
    await page.getByRole("button", { name: /new cron job/i }).click();
    const drawer = page.getByRole("dialog");
    await expect(drawer).toBeVisible();

    // Root target avoids the tenant picker's required user_id.
    await drawer.getByRole("radio", { name: /root/i }).click();

    await drawer.getByPlaceholder(/nightly-backup/i).fill("nightly");
    await drawer.getByPlaceholder(/\/root\/maintenance\/cleanup\.php/i).fill("ls -la");

    await drawer.getByRole("button", { name: /^create$/i }).click();

    // The shared friendly headline is shown to the user on BOTH surfaces:
    // the command field error inside the drawer...
    await expect(
      drawer.getByText("Command must start with wp, php, python, or node"),
    ).toBeVisible();
    // ...and the toast notification.
    await expect(
      page.getByRole("alert").getByText("Command must start with wp, php, python, or node"),
    ).toBeVisible();
    // ...and the raw backend detail is never rendered anywhere.
    await expect(page.getByText('first token must be "wp"')).toHaveCount(0);
  });
});
