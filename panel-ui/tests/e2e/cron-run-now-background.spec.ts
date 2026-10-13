// GH #2122: Run now waits about 20 s. A job still running then keeps running
// in the background (202 running), and a run already under way answers 409
// already_running. Real-browser pass of the result overlay and the toast,
// mirroring the vitest guards in CronJobWorkspace.test.tsx.
import { mockApi, signIn, test, expect, user } from "./fixtures";
import type { Page } from "@playwright/test";

const JOB = {
  id: "01KCRON0000000000000RVNNOW",
  user_id: user.id,
  name: "nightly-import",
  command: "php /home/u/example.com/public_html/import.php",
  schedule: "0 3 * * *",
  enabled: true,
  last_run_at: null,
  last_exit_code: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function setup(page: Page, runNow: { status: number; body: unknown }): Promise<void> {
  await mockApi(page, { me: user });

  await page.route("**/api/v1/me/server-capabilities", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        dns_enabled: true,
        mail_enabled: true,
        security_enabled: true,
        quota_enabled: true,
        api_enabled: true,
      }),
    }),
  );

  await page.route("**/api/v1/cron", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [JOB] }),
    });
  });

  await page.route(`**/api/v1/cron/${JOB.id}/run-now`, (route) =>
    route.fulfill({
      status: runNow.status,
      contentType: "application/json",
      body: JSON.stringify(runNow.body),
    }),
  );
}

async function clickRunNow(page: Page): Promise<void> {
  await signIn(page, user);
  await page.waitForURL(/\/jabali-panel/);
  await page.goto("/jabali-panel/cron");
  await expect(page.getByText(JOB.name)).toBeVisible();
  const row = page.getByRole("row", { name: new RegExp(JOB.name) });
  await row.getByRole("button", { name: /run now/i }).click();
}

test.describe("GH #2122 — Run now leaves a long job running", () => {
  test("a job still running after the wait is reported as running, with no exit code", async ({ page }) => {
    await setup(page, { status: 202, body: { exit_code: 0, stdout: "", stderr: "", running: true } });
    await clickRunNow(page);

    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Still running")).toBeVisible();
    await expect(dialog.getByText(/keeps running in the background/)).toBeVisible();
    await expect(dialog.getByText(/Exit code/)).toHaveCount(0);
  });

  test("a finished run shows its exit code and combined output", async ({ page }) => {
    await setup(page, {
      status: 200,
      body: { exit_code: 3, stdout: "", stderr: "", output: "importing\nTraceback: boom" },
    });
    await clickRunNow(page);

    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Exit code: 3")).toBeVisible();
    await expect(dialog.getByText("Output", { exact: true })).toBeVisible();
    await expect(dialog.getByText(/Traceback: boom/)).toBeVisible();
  });

  test("a run already under way is reported, not shown as a failure", async ({ page }) => {
    await setup(page, { status: 409, body: { error: "already_running" } });
    await clickRunNow(page);

    await expect(page.getByText(/This job is already running/)).toBeVisible();
    await expect(page.getByText(/status code 409/)).toHaveCount(0);
  });
});
