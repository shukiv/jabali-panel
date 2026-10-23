// GH #2243: while an admin is in "View as", the amber "Viewing as" banner
// sticks to the top of the window. The tenant sidebar and the File Manager's
// selection bar stuck to the same line, so once the header scrolled away the
// banner covered their top. They now stick just below the banner. happy-dom
// has no layout, so this reads real boxes in Chromium.
import { mockApi, signIn, test, expect, user } from "./fixtures";
import type { Locator, Page } from "@playwright/test";

const HOME = "/home/user";
// Enough rows that the page scrolls well past the header.
const ENTRIES = Array.from({ length: 80 }, (_, i) => ({
  name: `file-${String(i).padStart(2, "0")}.txt`,
  is_dir: false,
  size: 100 + i,
  mode: "-rw-r--r--",
  mod_time: "2026-01-01T00:00:00Z",
  is_symlink: false,
}));

async function setup(page: Page, viewingAs: boolean) {
  if (viewingAs) {
    // The grant the admin's "View as" leaves in this tab (impersonation.ts).
    await page.addInitScript(() => {
      sessionStorage.setItem("jabali_act_as", JSON.stringify({ id: "grant-1", username: "alice" }));
    });
  }
  await mockApi(page, { me: user });
  await page.route("**/api/v1/files/home", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ path: HOME }) }),
  );
  await page.route("**/api/v1/files/tree?*", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ path: HOME, entries: [] }) }),
  );
  await page.route("**/api/v1/files?*", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ path: HOME, entries: ENTRIES }) }),
  );
  await signIn(page, user);
  await page.waitForURL(/\/jabali-panel/);
  await page.goto("/jabali-panel/files");
  await expect(page.getByText("file-79.txt")).toBeAttached();
}

async function box(l: Locator) {
  const b = await l.boundingBox();
  if (!b) throw new Error("element has no box");
  return b;
}

async function scrollDown(page: Page) {
  await page.evaluate(() => window.scrollTo(0, 1500));
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(400);
}

const banner = (page: Page) => page.getByRole("alert").filter({ hasText: "Viewing as alice" });
const sider = (page: Page) => page.locator(".ant-layout-sider");

test("View as: the sidebar sits below the banner when the page scrolls (GH #2243)", async ({ page }) => {
  await setup(page, true);
  await expect(banner(page)).toBeVisible();
  await scrollDown(page);

  const b = await box(banner(page));
  const s = await box(sider(page));
  expect(b.y).toBeLessThanOrEqual(1);
  // The sidebar starts where the banner ends, and still ends at the window's
  // foot, so its last items are not pushed off screen.
  expect(Math.abs(s.y - (b.y + b.height))).toBeLessThanOrEqual(1);
  const viewport = page.viewportSize()!;
  expect(Math.abs(s.y + s.height - viewport.height)).toBeLessThanOrEqual(1);
});

test("View as: the File Manager selection bar sits below the banner (GH #2243)", async ({ page }) => {
  await setup(page, true);
  await page.locator(".ant-table-tbody tr.ant-table-row").first().locator(".ant-checkbox-input").check();
  // The bar is the first <div> around its "1 selected" label.
  const bar = page.getByText("1 selected", { exact: true }).locator("xpath=ancestor::div[1]");
  await expect(bar).toBeVisible();
  await scrollDown(page);

  const b = await box(banner(page));
  const bb = await box(bar);
  expect(Math.abs(bb.y - (b.y + b.height))).toBeLessThanOrEqual(1);
});

test("without View as, the sidebar still sticks to the top of the window", async ({ page }) => {
  await setup(page, false);
  await expect(page.getByText(/Viewing as/)).toHaveCount(0);
  await scrollDown(page);

  const s = await box(sider(page));
  expect(Math.abs(s.y)).toBeLessThanOrEqual(1);
  expect(Math.abs(s.height - page.viewportSize()!.height)).toBeLessThanOrEqual(1);
});
