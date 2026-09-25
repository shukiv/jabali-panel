// GH #1686 item 2: an admin can edit a tenant's cron job from the admin Cron
// Jobs screen — real-browser pass.
//
// This mirrors the vitest guard (AdminCreateCronModal.test.tsx) in Chromium
// against the built SPA, per the .tsx real-browser rule. We open the Create
// drawer once (seeding the persistent form store), cancel it, then open Edit
// for a tenant's job from the row overflow menu: the drawer must show the job's
// values and the owner read-only, and Save must PATCH only the editable fields.
import { admin, mockApi, signIn, test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const JOB = {
  id: "01KCRON0000000000000000JOB",
  user_id: "01KTENANT00000000000000000",
  username: "alice",
  name: "nightly-backup",
  command: "php /home/alice/example.com/public_html/backup.php",
  schedule: "0 3 * * *",
  enabled: true,
  run_as_root: false,
  last_run_at: null,
  last_exit_code: null,
  last_error: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function setup(page: Page): Promise<{ patches: unknown[] }> {
  const patches: unknown[] = [];
  await mockApi(page, { me: admin });

  await page.route("**/api/v1/admin/cron", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [JOB] }),
    });
  });

  await page.route(`**/api/v1/cron/${JOB.id}`, (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    const body = route.request().postDataJSON();
    patches.push(body);
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ...JOB, ...body }),
    });
  });

  return { patches };
}

test.describe("GH #1686 — admin edits a tenant's cron job", () => {
  test("Edit loads the job, keeps the owner fixed, and PATCHes only the editable fields", async ({ page }) => {
    const { patches } = await setup(page);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);

    await page.goto("/jabali-admin/cron");
    await expect(page.getByText(JOB.name)).toBeVisible();

    // 1) Open the Create drawer, then cancel it — this seeds the persistent
    //    form store, which must not shadow the Edit values.
    await page.getByRole("button", { name: /new cron job/i }).click();
    await expect(page.getByRole("dialog", { name: /new cron job/i })).toBeVisible();
    await page.getByRole("dialog").getByRole("button", { name: /cancel/i }).click();
    await expect(page.getByRole("dialog", { name: /new cron job/i })).toHaveCount(0);

    // 2) Open Edit for the tenant's job (Edit lives in the row overflow menu).
    const row = page.getByRole("row", { name: new RegExp(JOB.name) });
    await row.getByRole("button", { name: /more actions/i }).click();
    await page.getByRole("menuitem", { name: /^edit$/i }).click();

    // 3) The Edit drawer shows the job's values and the owner, read-only.
    const drawer = page.getByRole("dialog", { name: /edit cron job \(as tenant\)/i });
    await expect(drawer).toBeVisible();
    await expect(drawer.locator("input#name")).toHaveValue(JOB.name);
    await expect(drawer.locator("textarea#command")).toHaveValue(JOB.command);
    await expect(drawer.locator("form").getByText(JOB.username, { exact: true })).toBeVisible();
    await expect(drawer.getByRole("radio", { name: /root/i })).toHaveCount(0);

    // 4) Save sends a PATCH with only name / command / schedule.
    await drawer.locator("input#name").fill("nightly-backup-v2");
    await drawer.getByRole("button", { name: /^save$/i }).click();
    await expect(page.getByText("Cron job updated")).toBeVisible();
    expect(patches).toEqual([{ name: "nightly-backup-v2", command: JOB.command, schedule: JOB.schedule }]);
  });
});
