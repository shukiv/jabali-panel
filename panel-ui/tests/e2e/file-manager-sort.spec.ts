// GH #1184 follow-up: the File Manager's Name, Size and Modified columns sort
// when the user clicks their header; until then the listing keeps its
// default order (folders first, then by name).
import { mockApi, signIn, test, expect, user } from "./fixtures";
import type { Page } from "@playwright/test";

const HOME = "/home/user";
const ENTRIES = [
  { name: "docs", is_dir: true, size: 4096, mode: "drwxr-x---", mod_time: "2026-01-05T00:00:00Z", is_symlink: false },
  { name: "b.txt", is_dir: false, size: 300, mode: "-rw-r--r--", mod_time: "2026-01-02T00:00:00Z", is_symlink: false },
  { name: "a.txt", is_dir: false, size: 100, mode: "-rw-r--r--", mod_time: "2026-01-03T00:00:00Z", is_symlink: false },
  { name: "c.txt", is_dir: false, size: 200, mode: "-rw-r--r--", mod_time: "2026-01-01T00:00:00Z", is_symlink: false },
];

async function setup(page: Page) {
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
}

// The file names in table order.
async function rowNames(page: Page): Promise<string[]> {
  const rows = await page.locator(".ant-table-tbody tr.ant-table-row").allInnerTexts();
  return rows.map((r) => ENTRIES.find((e) => r.includes(e.name))?.name ?? "?");
}

test("File Manager: Name, Size and Modified sort on header click (GH #1184)", async ({ page }) => {
  await setup(page);
  await signIn(page, user);
  await page.waitForURL(/\/jabali-panel/);
  await page.goto("/jabali-panel/files");

  // No header clicked: folders first, then by name.
  await expect.poll(() => rowNames(page)).toEqual(["docs", "a.txt", "b.txt", "c.txt"]);

  await page.getByRole("columnheader", { name: "Name", exact: true }).click();
  await expect.poll(() => rowNames(page)).toEqual(["a.txt", "b.txt", "c.txt", "docs"]);

  await page.getByRole("columnheader", { name: "Size", exact: true }).click();
  await expect.poll(() => rowNames(page)).toEqual(["a.txt", "c.txt", "b.txt", "docs"]);

  await page.getByRole("columnheader", { name: "Modified", exact: true }).click();
  await expect.poll(() => rowNames(page)).toEqual(["c.txt", "b.txt", "a.txt", "docs"]);
});
