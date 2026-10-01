// GH #1686: Admin → Cron Jobs has a separate System jobs tab listing the
// scheduled jobs Jabali installs. Real-browser pass against the built SPA:
// the tab is reachable and linkable (?tab=system), Run now asks first and then
// POSTs, a job whose settings live on Updates links there instead of offering
// Run now, and View log opens the job's journal.
import { admin, mockApi, signIn, test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const ROWS = [
  {
    id: "retention-sweep",
    kind: "timer",
    label: "Log retention sweep",
    description: "Deletes expired rows from the panel's log and report tables.",
    category: "maintenance",
    schedule: "Daily at 03:40",
    schedule_format: "text",
    status: "scheduled",
    last_result: "success",
    last_run_at: "2026-10-01T03:45:27Z",
    next_run_at: "2099-01-01T03:45:00Z",
    can_run_now: true,
    has_log: true,
  },
  {
    id: "panel-update",
    kind: "timer",
    label: "Panel auto-update",
    description: "Updates Jabali on its release channel.",
    category: "updates",
    schedule: "Daily at 04:30",
    schedule_format: "text",
    status: "disabled",
    last_result: "never",
    last_run_at: null,
    next_run_at: null,
    can_run_now: false,
    has_log: true,
    managed_by: "updates",
  },
];

async function setup(page: Page): Promise<{ runs: string[] }> {
  const runs: string[] = [];
  await mockApi(page, { me: admin });
  await page.route("**/api/v1/admin/cron", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [] }) }),
  );
  await page.route("**/api/v1/admin/system-jobs", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: ROWS, total: ROWS.length, page: 1, page_size: ROWS.length }),
    }),
  );
  await page.route("**/api/v1/admin/system-jobs/*/run", (route) => {
    runs.push(new URL(route.request().url()).pathname);
    return route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ started: true }) });
  });
  await page.route("**/api/v1/admin/system-jobs/retention-sweep/log*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ log: "retention-sweep: 0 rows (dry-run=false)\n", lines: 200 }),
    }),
  );
  return { runs };
}

test.describe("GH #1686 — admin System jobs", () => {
  test("System jobs tab: Run now asks first, update jobs link to Updates, View log opens the journal", async ({ page }) => {
    const { runs } = await setup(page);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);
    await page.goto("/jabali-admin/cron");

    await page.getByRole("tab", { name: "System jobs" }).click();
    await expect(page).toHaveURL(/[?&]tab=system/);
    await expect(page.getByRole("heading", { name: /System jobs/ })).toBeVisible();

    const sweep = page.getByRole("row", { name: /Log retention sweep/ });
    await expect(sweep.getByText("Scheduled")).toBeVisible();

    // Run now confirms first; nothing is sent until the admin accepts.
    await sweep.getByRole("button", { name: /Run now/ }).click();
    const confirm = page.getByRole("dialog").filter({ hasText: 'Run "Log retention sweep" now?' });
    await expect(confirm).toBeVisible();
    expect(runs).toEqual([]);
    await confirm.getByRole("button", { name: "Run now" }).click();
    await expect.poll(() => runs).toEqual(["/api/v1/admin/system-jobs/retention-sweep/run"]);
    await expect(page.getByText("Log retention sweep started")).toBeVisible();

    // The self-update job has no Run now here; it links to Updates.
    const update = page.getByRole("row", { name: /Panel auto-update/ });
    await expect(update.getByText("Disabled")).toBeVisible();
    await expect(update.getByRole("button", { name: /Run now/ })).toHaveCount(0);
    await expect(update.getByRole("button", { name: /Open Updates/ })).toBeVisible();

    // View log (in the row's overflow menu) opens the job's journal.
    await sweep.getByRole("button", { name: /more actions/i }).click();
    await page.getByRole("menuitem", { name: "View log" }).click();
    const drawer = page.getByRole("dialog", { name: "Log retention sweep: log" });
    await expect(drawer).toBeVisible();
    await expect(drawer.getByText("retention-sweep: 0 rows (dry-run=false)")).toBeVisible();
  });

  test("?tab=system opens the System jobs tab directly", async ({ page }) => {
    await setup(page);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);
    await page.goto("/jabali-admin/cron?tab=system");
    await expect(page.getByRole("tab", { name: "System jobs", selected: true })).toBeVisible();
    await expect(page.getByRole("row", { name: /Log retention sweep/ })).toBeVisible();
  });
});
