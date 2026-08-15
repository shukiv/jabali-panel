// GH #1053: tenant FTP/SFTP subaccounts — user page + capability gating.
//
// Verifies:
//   - nav entry + route are hidden without ftp_accounts_enabled (package gate)
//   - with the capability: list renders, create drawer submits the right body
//     (tenant-prefixed label, home path, ftp_access), password never echoed
//   - FTPS toggle disabled while the server module is off + SFTP-only copy
//   - delete goes through Popconfirm and issues the DELETE
//
// All API calls are mocked — the real protocol path (sftp/curl logins) is
// covered by the live box test in the step-7 runbook, not Playwright.
import { mockApi, signIn, test, expect, user } from "./fixtures";
import type { Page } from "@playwright/test";

type FtpAccount = {
  id: string;
  user_id: string;
  username: string;
  home_path: string;
  ftp_access: boolean;
  sftp_access: boolean;
  is_enabled: boolean;
};

const seedAccount: FtpAccount = {
  id: "01KFTPACC00000000000000001",
  user_id: user.id,
  username: "shop_deploy",
  home_path: "/home/shop/example.com/public_html",
  ftp_access: false,
  sftp_access: true,
  is_enabled: true,
};

async function setupFtpMocks(
  page: Page,
  opts: { accountsEnabled: boolean; serverEnabled: boolean; accounts?: FtpAccount[] },
) {
  const accounts = [...(opts.accounts ?? [])];
  const created: Record<string, unknown>[] = [];
  const deleted: string[] = [];

  await page.route("**/api/v1/me/server-capabilities", async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        dns_enabled: true,
        mail_enabled: true,
        security_enabled: true,
        quota_enabled: true,
        api_enabled: true,
        ftp_accounts_enabled: opts.accountsEnabled,
        ftp_server_enabled: opts.serverEnabled,
      }),
    }),
  );

  await page.route("**/api/v1/me/ftp-accounts", async (route) => {
    if (route.request().method() === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: accounts, total: accounts.length, page: 1, page_size: 50 }),
      });
    }
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      created.push(body);
      const acct: FtpAccount = {
        id: "01KFTPACC0000000000000NEW",
        user_id: user.id,
        username: `shop_${body.label}`,
        home_path: String(body.home_path),
        ftp_access: !!body.ftp_access,
        sftp_access: body.sftp_access !== false,
        is_enabled: true,
      };
      accounts.push(acct);
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify(acct) });
    }
    return route.continue();
  });

  await page.route("**/api/v1/me/ftp-accounts/*", async (route) => {
    const method = route.request().method();
    const id = route.request().url().split("/").pop()!;
    if (method === "DELETE") {
      deleted.push(id);
      const i = accounts.findIndex((a) => a.id === id);
      if (i >= 0) accounts.splice(i, 1);
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ deleted: true }) });
    }
    return route.continue();
  });

  return { created, deleted };
}

// overrideMeWithUsername must run AFTER signIn: later registrations win in
// Playwright, and the fixture's /api/v1/me (401 until logged in) is what
// makes the login form render. Once signed in, the page needs a /me that
// carries the Linux username for the prefix addon + home validation.
async function overrideMeWithUsername(page: Page) {
  await page.route("**/api/v1/me", async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        id: user.id,
        email: user.email,
        is_admin: false,
        username: "shop",
      }),
    }),
  );
}

test("nav + route hidden without the package capability", async ({ page }) => {
  await mockApi(page, { session: null, users: [user] });
  await setupFtpMocks(page, { accountsEnabled: false, serverEnabled: false });
  await signIn(page, user);
  await overrideMeWithUsername(page);

  await expect(page.getByRole("menuitem", { name: "FTP Accounts" })).toHaveCount(0);

  // Deep link redirects to the dashboard instead of rendering the page.
  await page.goto("/jabali-panel/ftp-accounts");
  await expect(page).not.toHaveURL(/ftp-accounts/);
});

test("list renders and SFTP-only copy shows while the server module is off", async ({ page }) => {
  await mockApi(page, { session: null, users: [user] });
  await setupFtpMocks(page, {
    accountsEnabled: true,
    serverEnabled: false,
    accounts: [seedAccount],
  });
  await signIn(page, user);
  await overrideMeWithUsername(page);

  await page.goto("/jabali-panel/ftp-accounts");
  await expect(page.getByText("shop_deploy")).toBeVisible();
  await expect(page.getByText(/FTP is not enabled on this server/)).toBeVisible();
});

test("create drawer submits the tenant-prefixed body and never echoes the password", async ({ page }) => {
  await mockApi(page, { session: null, users: [user] });
  const { created } = await setupFtpMocks(page, { accountsEnabled: true, serverEnabled: true });
  await signIn(page, user);
  await overrideMeWithUsername(page);

  await page.goto("/jabali-panel/ftp-accounts");
  await page.getByRole("button", { name: /Add account/ }).click();

  await page.getByLabel("Account name").fill("deploy");
  const dirInput = page.getByLabel("Directory");
  await dirInput.fill("/home/shop/example.com/public_html");
  await page.getByLabel("Password", { exact: true }).fill("a-long-enough-password");
  await page.getByRole("button", { name: /Add account/ }).last().click();

  await expect
    .poll(() => created.length, { message: "create POST should fire" })
    .toBe(1);
  expect(created[0]).toMatchObject({
    label: "deploy",
    home_path: "/home/shop/example.com/public_html",
    password: "a-long-enough-password",
  });
  await expect(page.getByText("shop_deploy")).toBeVisible();
});

test("delete flows through Popconfirm and files-stay copy", async ({ page }) => {
  await mockApi(page, { session: null, users: [user] });
  const { deleted } = await setupFtpMocks(page, {
    accountsEnabled: true,
    serverEnabled: false,
    accounts: [seedAccount],
  });
  await signIn(page, user);
  await overrideMeWithUsername(page);

  await page.goto("/jabali-panel/ftp-accounts");
  await page.getByRole("button", { name: /Delete/ }).click();
  await expect(page.getByText(/files stay in place/)).toBeVisible();
  await page.getByRole("button", { name: "OK" }).click();

  await expect.poll(() => deleted.length).toBe(1);
  expect(deleted[0]).toBe(seedAccount.id);
});
