// GH #1686 items 3+4: the admin Create Cron drawer's help must be target-aware.
// When the target switches Tenant -> root the top paragraph must change (item 4),
// and the command-restriction copy must be shown near the command field (item 3).
// This mirrors the vitest guard (AdminCreateCronModal.test.tsx) in Chromium
// against the built SPA, per the .tsx real-browser rule.
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
}

test.describe("GH #1686 — admin cron drawer target-aware help", () => {
  test("switching Tenant -> root swaps the help paragraph and shows command restrictions", async ({ page }) => {
    await setup(page);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);

    await page.goto("/jabali-admin/cron");

    // Open the Create drawer.
    await page.getByRole("button", { name: /new cron job/i }).click();
    const drawer = page.getByRole("dialog");
    await expect(drawer.getByText(/create a cron job under any tenant/i)).toBeVisible();

    // Command restrictions are shown near the command field (item 3).
    await expect(drawer.getByText(/Commands must start with/i)).toBeVisible();
    await expect(drawer.getByText(/will not work/i)).toBeVisible();

    // Switch the target to root (item 4): paragraph swaps and the drawer title
    // updates. The title asserts the root locale key resolves (the vitest can't
    // reach this — it mocks t() to return keys).
    await drawer.getByRole("radio", { name: /root/i }).click();
    await expect(drawer.getByText(/system-scoped systemd timer/i)).toBeVisible();
    await expect(page.getByRole("dialog", { name: /as root/i })).toBeVisible();
  });
});
