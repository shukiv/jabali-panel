// GH #1701: "View error log" on a domain's PHP Settings tab opens that domain's
// error-log stream in place, instead of sending the user to Logs & Statistics
// to find the domain and its action there.
import { test, expect, mockApi, signIn, user } from "./fixtures";

const DOMAIN_ID = "01KPDOMAIN0000000000000000";

test("tenant PHP settings: View error log opens this domain's error log on the page (#1701)", async ({ page }) => {
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
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ php_version: "8.3" }) }),
  );
  let streamRequest: Record<string, unknown> | null = null;
  await page.route("**/api/v1/logs/access", (route) => {
    streamRequest = route.request().postDataJSON() as Record<string, unknown>;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ stream_key: "k1", websocket_url: "/api/v1/logs/access/k1/ws" }),
    });
  });

  await signIn(page, user);
  const settingsPath = `/jabali-panel/domains/${DOMAIN_ID}/php-settings`;
  await page.goto(settingsPath);

  await page.getByRole("button", { name: "View error log" }).click();
  await expect.poll(() => streamRequest).toEqual({ log_type: "error", domain_id: DOMAIN_ID });
  await expect(page.getByRole("dialog").getByText("Error Log Stream")).toBeVisible();
  // Still on the PHP Settings tab: no detour through Logs & Statistics.
  expect(new URL(page.url()).pathname).toBe(settingsPath);
});
