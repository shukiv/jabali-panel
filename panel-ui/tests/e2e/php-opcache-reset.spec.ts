// GH #1701: a domain's PHP Settings tab offers Reset OPcache when the API says
// the caller may reset it. The reset restarts the pool's PHP, so it waits for
// the confirm, then POSTs and names the restarted version. Without permission
// the action is not offered at all.
import { test, expect, mockApi, signIn, user } from "./fixtures";
import type { Page } from "@playwright/test";

const DOMAIN_ID = "01KPDOMAIN0000000000000000";

async function mockDomain(page: Page, resetAllowed: boolean) {
  await mockApi(page, {
    me: user,
    domains: [
      {
        id: DOMAIN_ID,
        user_id: user.id,
        name: "example.com",
        doc_root: "/home/user/example.com",
        is_enabled: true,
        nginx_custom_directives: "",
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      },
    ],
  });
  await page.route("**/api/v1/php/versions", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ versions: ["8.3"] }) }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/php-settings`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ php_version: "8.3", opcache_reset_allowed: resetAllowed }),
    }),
  );
}

test("tenant PHP settings: Reset OPcache asks first, then restarts the domain's PHP (#1701)", async ({ page }) => {
  await mockDomain(page, true);
  const resets: string[] = [];
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/php-settings/opcache-reset`, (route) => {
    resets.push(route.request().method());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ restarted: true, php_version: "8.3" }),
    });
  });

  await signIn(page, user);
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/php-settings`);

  await page.getByRole("button", { name: "Reset OPcache" }).click();
  const confirm = page.locator(".ant-popconfirm");
  await expect(confirm).toBeVisible();
  await expect(confirm).toContainText("restarts PHP 8.3 for every site on this domain's PHP pool");
  expect(resets).toEqual([]);

  await confirm.getByRole("button", { name: "Reset" }).click();
  await expect.poll(() => resets).toEqual(["POST"]);
  await expect(page.getByText("OPcache reset (PHP 8.3 restarted)")).toBeVisible();
});

test("tenant PHP settings: no Reset OPcache when the package does not allow it (#1701)", async ({ page }) => {
  await mockDomain(page, false);
  await signIn(page, user);
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/php-settings`);

  await expect(page.getByRole("button", { name: "Save Changes" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Reset OPcache" })).toHaveCount(0);
});
