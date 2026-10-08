// GH #1993: before Restore, the restore drawer shows the uploaded backup
// checked against this server (the restore preflight) — real-browser pass for
// RestoreFromUploadDrawer, per the .tsx real-browser rule. The checks below are
// the ones the server returned on the test box for a backup whose sites use
// PHP 7.4 (blocked), and for one with PostgreSQL, mail, DNS and Docker apps
// turned off there (warnings).
import { admin, mockApi, signIn, test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const KEPT = {
  id: "01KKEPT0000000000000000000",
  file_name: "pf1993.tar.zst",
  size_bytes: 10034,
  account_username: "pf1993",
  account_email: "pf1993@example.com",
  components: ["home", "db", "mail"],
  retention: "keep",
  expires_at: null,
  uploaded_by: "01KADMIN000000000000000000",
  restore_status: "",
  restore_started_at: null,
  restored_at: null,
  restore_target: "",
  file_present: true,
  target_exists: true,
  create_supported: true,
  created_at: "2026-10-08T01:00:00Z",
};

const BLOCKED = {
  blocked: true,
  checks: [
    {
      area: "php",
      level: "block",
      message: "PHP 7.4 isn't installed on this server, and the account's sites use it. Install it under PHP Versions, then restore.",
    },
    { area: "mail", level: "ok", message: "The backup has 2 mailboxes. Mail is on here." },
    { area: "dns", level: "ok", message: "The backup has 1 custom DNS record. DNS is on here." },
  ],
};

const WARNINGS = {
  blocked: false,
  checks: [
    { area: "php", level: "ok", message: "PHP 8.5, which the account's sites use, is installed." },
    {
      area: "php_extensions",
      level: "warn",
      message:
        "PHP 8.5 here lacks gmp, imap, ldap, mailparse and soap, which the source server had enabled. Sites that need them won't work until you enable them under PHP Versions.",
    },
    {
      area: "postgres",
      level: "warn",
      message:
        "PostgreSQL is turned off on this server, so the backup's 1 PostgreSQL database (pf1993_pg) and 1 PostgreSQL database user won't be restored. To restore them, turn PostgreSQL on under Server Settings first.",
    },
    {
      area: "mail",
      level: "warn",
      message: "Mail is turned off on this server, so the backup's 2 mailboxes won't be restored. The domains keep their mail settings.",
    },
  ],
};

async function setup(page: Page, preflight: unknown): Promise<{ restores: unknown[] }> {
  const restores: unknown[] = [];
  // Anything the Backups page lists that this test doesn't care about is empty.
  await page.route(/\/api\/v1\/(admin\/)?(backups|backup-|destinations|schedules)/, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: [], items: [], total: 0 }) }),
  );
  await mockApi(page, { me: admin });
  await page.route(/\/api\/v1\/admin\/uploaded-backups(\?.*)?$/, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [KEPT], total: 1, page: 1, page_size: 25 }),
    }),
  );
  await page.route(`**/api/v1/admin/uploaded-backups/${KEPT.id}`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: KEPT }) }),
  );
  await page.route(`**/api/v1/admin/uploaded-backups/${KEPT.id}/preflight`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: preflight }) }),
  );
  await page.route(`**/api/v1/admin/uploaded-backups/${KEPT.id}/restore`, (route) => {
    restores.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ status: "restoring", id: KEPT.id }) });
  });
  return { restores };
}

async function openRestore(page: Page) {
  await signIn(page, admin);
  await page.waitForURL(/\/jabali-admin/);
  await page.goto("/jabali-admin/backups");
  const row = page.getByRole("row", { name: /pf1993\.tar\.zst/ });
  await expect(row).toBeVisible();
  const direct = row.getByRole("button", { name: /^restore$/i });
  if (await direct.count()) {
    await direct.click();
  } else {
    await row.getByRole("button", { name: /more actions/i }).click();
    await page.getByRole("menuitem", { name: /^restore$/i }).click();
  }
  const drawer = page.getByRole("dialog", { name: /restore uploaded backup/i });
  await expect(drawer).toBeVisible();
  // Let the drawer finish sliding in before anything is measured or captured.
  await page.locator(".ant-drawer-content-wrapper").evaluate((el) =>
    Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)),
  );
  return drawer;
}

test.describe("GH #1993 — the restore drawer shows the restore preflight", () => {
  test("a PHP version this server lacks blocks Restore", async ({ page }, info) => {
    const { restores } = await setup(page, BLOCKED);
    const drawer = await openRestore(page);
    await expect(drawer.getByText("This backup can't be restored here yet")).toBeVisible();
    await expect(drawer.getByText(/PHP 7\.4 isn't installed on this server/)).toBeVisible();
    await expect(drawer.getByText("The backup has 2 mailboxes. Mail is on here.")).toBeVisible();
    const restore = drawer.getByRole("button", { name: /restore into pf1993/i });
    await expect(restore).toBeDisabled();
    await drawer.screenshot({ path: info.outputPath("preflight-blocked.png") });
    expect(restores).toEqual([]);
  });

  test("warnings name what won't be restored, and Restore runs", async ({ page }, info) => {
    const { restores } = await setup(page, WARNINGS);
    const drawer = await openRestore(page);
    await expect(drawer.getByText("Some of this backup won't be restored as it is")).toBeVisible();
    await expect(drawer.getByText(/lacks gmp, imap, ldap, mailparse and soap/)).toBeVisible();
    await expect(drawer.getByText(/PostgreSQL is turned off on this server/)).toBeVisible();
    await drawer.screenshot({ path: info.outputPath("preflight-warnings.png") });
    const restore = drawer.getByRole("button", { name: /restore into pf1993/i });
    await expect(restore).toBeEnabled();
    await restore.click();
    await expect.poll(() => restores.length).toBe(1);
  });
});
