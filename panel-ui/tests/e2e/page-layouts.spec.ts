// GH #2251: pages laid out like Security and Backups. Each page opens with a
// level-3 heading that carries the sidebar's icon; a page with tabs puts them
// in the tab bar of one card below that heading, not bare on the page, and a
// tab does not repeat the page's heading inside the card. Real-browser pass
// against the built SPA, since happy-dom has no layout to compare.
import { admin, user, mockApi, signIn, test, expect } from "./fixtures";
import type { Locator, Page } from "@playwright/test";

async function expectPageHeading(page: Page, name: RegExp): Promise<Locator> {
  const heading = page.getByRole("heading", { level: 3, name });
  await expect(heading).toBeVisible();
  await expect(heading.locator("svg")).toHaveCount(1);
  return heading;
}

// The tabs sit in the head of one card, and that card starts below the heading.
async function expectTabsInCardBelow(page: Page, heading: Locator, tabs: string[]): Promise<void> {
  const head = page.locator(".ant-card-head").filter({ has: page.getByRole("tab", { name: tabs[0] }) });
  await expect(head).toHaveCount(1);
  for (const name of tabs) {
    await expect(head.getByRole("tab", { name })).toBeVisible();
  }
  await expect(page.locator(".ant-tabs").filter({ has: page.getByRole("tab", { name: tabs[0] }) })).toHaveCount(1);
  const h = await heading.boundingBox();
  const card = await head.boundingBox();
  expect(h && card && h.y + h.height <= card.y).toBe(true);
}

async function mockCron(page: Page): Promise<void> {
  await page.route("**/api/v1/admin/cron", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [] }) }),
  );
  await page.route("**/api/v1/admin/system-jobs", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [], total: 0, page: 1, page_size: 0 }),
    }),
  );
}

test.describe("GH #2251 — admin page layouts", () => {
  test.beforeEach(async ({ page }) => {
    await mockApi(page, { me: admin });
    await mockCron(page);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);
  });

  test("Logs & Statistics: heading with its icon, tabs in the card below it", async ({ page }) => {
    await page.goto("/jabali-admin/logs");
    const heading = await expectPageHeading(page, /Logs & Statistics/);
    await expectTabsInCardBelow(page, heading, ["Domains", "Audit Log", "Backups", "Updates", "Mail"]);

    await page.getByRole("tab", { name: "Updates" }).click();
    await expect(page).toHaveURL(/[?&]tab=updates/);
    await expect(page.getByRole("tab", { name: "Updates", selected: true })).toBeVisible();
  });

  test("Cron Jobs: the heading sits above the tabs, and neither tab repeats it", async ({ page }) => {
    await page.goto("/jabali-admin/cron");
    const heading = await expectPageHeading(page, /^Cron Jobs$/);
    await expectTabsInCardBelow(page, heading, ["Tenant jobs", "System jobs"]);
    await expect(page.getByRole("heading", { name: /Cron Jobs/ })).toHaveCount(1);
    await expect(page.getByRole("button", { name: /New Cron Job/ })).toBeVisible();

    await page.goto("/jabali-admin/cron?tab=system");
    await expect(page.getByRole("tab", { name: "System jobs", selected: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: /System jobs/ })).toHaveCount(0);
  });

  test("Databases and Migrations have a heading with their icon", async ({ page }) => {
    await page.goto("/jabali-admin/databases");
    await expectPageHeading(page, /^Databases$/);

    await page.goto("/jabali-admin/migrations");
    await expectPageHeading(page, /^Migrations$/);
  });
});

test.describe("GH #2251 — tenant page layouts", () => {
  test("Logs & Statistics: heading with its icon, tabs in the card below it", async ({ page }) => {
    await mockApi(page, { me: user });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto("/jabali-panel/logs");
    const heading = await expectPageHeading(page, /Logs & Statistics/);
    await expectTabsInCardBelow(page, heading, ["Domain logs", "Account activity"]);

    // The old /activity link still lands on the Account activity tab, which
    // the tab names: no second heading inside the card.
    await page.goto("/jabali-panel/activity");
    await expect(page).toHaveURL(/\/jabali-panel\/logs\?tab=activity/);
    await expect(page.getByRole("tab", { name: "Account activity", selected: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: /Account Activity/i })).toHaveCount(0);
  });
});
