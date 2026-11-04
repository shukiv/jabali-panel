// GH #2377 (johnnyq): after a migration every zone read "Provisioned" while
// PowerDNS had none of them. Real-browser pass (built SPA, mocked API): the
// zone status follows what the DNS server accepted, the failed status's
// tooltip explains it (with the server's error for an admin only, as the API
// sends it), and the column still fits at phone width.
import { mockApi, signIn, test, expect, admin, user, expectNoHorizontalOverflow } from "./fixtures";
import type { Page } from "@playwright/test";
import type { MockUser } from "./fixtures";

const SERVER_ERROR = "agent: internal: powerdns backend not available: dial unix /var/run/mysqld/mysqld.sock";

function zone(id: string, name: string, extra: Record<string, unknown>) {
  return {
    id,
    user_id: "u1",
    username: "alice",
    name,
    provisioned: true,
    record_count: 5,
    effective_ttl: 3600,
    dnssec_enabled: false,
    registrar_expires_at: null,
    dns_disabled: false,
    web_disabled: false,
    email_enabled: true,
    ...extra,
  };
}

async function setup(page: Page, who: MockUser, isAdmin: boolean): Promise<void> {
  await mockApi(page, { me: who });
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
  // The API sends last_push_error to admins only.
  const err = isAdmin ? { last_push_error: SERVER_ERROR } : {};
  await page.route(/\/api\/v1\/dns\/zones(\?.*)?$/, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          zone("01KDOM0000000000000000REF1", "refused.example", { push_status: "failed", ...err }),
          zone("01KDOM0000000000000000STA1", "stale.example", {
            push_status: "failed",
            last_pushed_at: "2026-11-04T18:00:00Z",
            ...err,
          }),
          zone("01KDOM0000000000000000PEN1", "pending.example", { push_status: "pending" }),
          zone("01KDOM0000000000000000ONS1", "onserver.example", {
            push_status: "on_server",
            last_pushed_at: "2026-11-04T18:00:00Z",
          }),
        ],
        total: 4,
        page: 1,
        page_size: 20,
      }),
    }),
  );
}

const row = (page: Page, name: string) => page.getByRole("row").filter({ hasText: name });

test.describe("DNS zone status follows the DNS server (GH #2377)", () => {
  test("tenant: a refused zone does not read as Provisioned, and the tooltip explains it", async ({ page }) => {
    await setup(page, user, false);
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);
    await page.goto("/jabali-panel/dns");

    await expect(row(page, "refused.example").getByText("Not on DNS server")).toBeVisible();
    await expect(row(page, "refused.example").getByText("Provisioned", { exact: true })).toHaveCount(0);
    await expect(row(page, "stale.example").getByText("Last update failed")).toBeVisible();
    await expect(row(page, "pending.example").getByText("Pending", { exact: true })).toBeVisible();
    await expect(row(page, "onserver.example").getByText("Provisioned", { exact: true })).toBeVisible();

    await row(page, "refused.example").getByText("Not on DNS server").hover();
    const tip = page.getByRole("tooltip");
    await expect(tip).toContainText("The DNS server did not accept this zone.");
    await expect(tip).not.toContainText("powerdns");
  });

  test("admin: the tooltip carries the server's error", async ({ page }) => {
    await setup(page, admin, true);
    await signIn(page, admin);
    await page.waitForURL(/\/jabali-admin/);
    await page.goto("/jabali-admin/dns");

    await row(page, "stale.example").getByText("Last update failed").hover();
    const tip = page.getByRole("tooltip");
    await expect(tip).toContainText("The DNS server still has the version from");
    await expect(tip).toContainText(SERVER_ERROR);
  });

  test("the status column fits at phone width", async ({ page }) => {
    await setup(page, user, false);
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);
    await page.goto("/jabali-panel/dns");

    await expect(row(page, "refused.example").getByText("Not on DNS server")).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });
});
