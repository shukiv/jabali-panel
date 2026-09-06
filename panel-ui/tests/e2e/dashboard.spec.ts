// Dashboard E2E — verifies the trimmed admin dashboard (post-M31)
// renders the welcome card, the stat tiles, and the deep-link to
// /jabali-admin/server-status.
//
// The deep system info / services / network etc that used to live on
// the dashboard now lives on /jabali-admin/server-status; those
// assertions belong in server-status.spec.ts.
import { admin, expect, mockApi, signIn, test } from "./fixtures";

test.describe("admin dashboard", () => {
  test("trimmed landing card + deep link to server status", async ({ page }) => {
    await mockApi(page, { me: admin });
    // Stub the server-status envelope so the welcome card's hostname
    // line resolves to a deterministic value rather than "—".
    await page.route("**/api/v1/admin/server-status*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          as_of: new Date().toISOString(),
          alerts: [],
          host: {
            hostname: "test-server",
            os: "Debian 13",
            kernel: "6.12.74",
            cpu_model: "Intel Xeon",
            timezone: "UTC",
            uptime_seconds: 60,
            load_avg: [0, 0, 0],
            cpu_count: 1,
            mem_total_kb: 1_000_000,
            mem_available_kb: 800_000,
            partitions: [],
          },
          services: [],
        }),
      }),
    );
    await signIn(page, admin);

    await expect(page).toHaveURL(/\/jabali-admin\/dashboard/);
    // bf136db: dashboard dropped the redundant "Dashboard" heading. The
    // welcome card's hostname assertion below is the deterministic
    // proof that the dashboard rendered.

    // Welcome card surfaces the hostname from the envelope.
    await expect(page.getByText("test-server")).toBeVisible();

    // The "View server status →" CTA links into the M31 page.
    await page.getByRole("link", { name: /View server status/i }).click();
    await expect(page).toHaveURL(/\/jabali-admin\/server-status/);
  });
});
