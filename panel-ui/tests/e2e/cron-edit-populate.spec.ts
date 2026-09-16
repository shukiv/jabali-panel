// GH #1686 item 1: opening a tenant cron in Edit mode must populate Name and
// Command immediately, with no browser refresh — real-browser pass.
//
// happy-dom cannot catch this: the bug is the antd persistent-form + preserve
// interaction across a real create→close→edit sequence in the built SPA. Here
// we open the Create drawer once (seeding the persistent form store with the
// empty create defaults), cancel it, then open Edit for an existing job and
// assert the fields carry the job's values — without reloading the page.
import { mockApi, signIn, test, expect, user } from "./fixtures";
import type { Page } from "@playwright/test";

const JOB = {
  id: "01KCRON0000000000000000JOB",
  user_id: user.id,
  name: "nightly-backup",
  command: "php /home/u/example.com/public_html/backup.php",
  schedule: "0 3 * * *",
  enabled: true,
  last_run_at: null,
  last_exit_code: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function setup(page: Page): Promise<void> {
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

  // Tenant cron list.
  await page.route("**/api/v1/cron", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [JOB] }),
    });
  });
}

test.describe("GH #1686 — tenant cron Edit populates on open", () => {
  test("opening Edit after using the Create form loads the job's name and command", async ({ page }) => {
    await setup(page);
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto("/jabali-panel/cron");
    await expect(page.getByText(JOB.name)).toBeVisible();

    // 1) Open the Create drawer, then cancel it — this seeds the persistent
    //    form store with the empty create defaults that used to shadow the
    //    Edit values.
    await page.getByRole("button", { name: /new cron job/i }).click();
    const createDrawer = page.getByRole("dialog");
    await expect(createDrawer.getByText(/create cron job/i)).toBeVisible();
    await createDrawer.getByRole("button", { name: /cancel/i }).click();
    await expect(page.getByText(/create cron job/i)).toHaveCount(0);

    // 2) Open Edit for the existing job (Edit lives in the row overflow menu).
    const row = page.getByRole("row", { name: new RegExp(JOB.name) });
    await row.getByRole("button", { name: /more actions/i }).click();
    await page.getByRole("menuitem", { name: /^edit$/i }).click();

    // 3) The Edit drawer must show the job's values immediately — no reload.
    const editDrawer = page.getByRole("dialog");
    await expect(editDrawer.getByText(/edit cron job/i)).toBeVisible();
    await expect(editDrawer.locator("input#name")).toHaveValue(JOB.name);
    await expect(editDrawer.locator("textarea#command")).toHaveValue(JOB.command);
  });
});
