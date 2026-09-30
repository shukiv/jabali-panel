// GH #1701: the tenant's domain PHP Settings tab honors the owner's package
// policy returned by GET /domains/:id/php-settings. A directive the package
// keeps for admins renders read-only with "Set by your administrator", the
// others stay editable, and a save sends the locked value back unchanged so
// the API sees no change to it.
import { test, expect, mockApi, signIn, user } from "./fixtures";

const DOMAIN_ID = "01KPDOMAIN0000000000000000";

const DIRECTIVES = [
  "memory_limit",
  "upload_max_filesize",
  "post_max_size",
  "max_input_vars",
  "max_execution_time",
  "max_input_time",
  "display_errors",
  "error_reporting",
  "date.timezone",
];

test("tenant PHP settings: a package-locked directive is read-only and sent back unchanged (#1701)", async ({
  page,
}) => {
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
  let patched: Record<string, unknown> | null = null;
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/php-settings`, async (route) => {
    if (route.request().method() === "PATCH") {
      patched = route.request().postDataJSON() as Record<string, unknown>;
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ success: true }) });
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        php_version: "8.3",
        php_memory_limit: "256M",
        policy: Object.fromEntries(
          DIRECTIVES.map((d) => [d, d === "memory_limit" ? "admin_only" : "tenant_allowed"]),
        ),
        editable: DIRECTIVES.filter((d) => d !== "memory_limit"),
      }),
    });
  });

  await signIn(page, user);
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/php-settings`);

  const lockTag = page.getByText("Set by your administrator");
  await expect(lockTag).toHaveCount(1);
  await expect(lockTag).toBeVisible();

  // The locked select is disabled; a permitted one is not.
  const memoryItem = page.locator(".ant-form-item").filter({ has: page.getByText("Set by your administrator") });
  await expect(memoryItem.locator(".ant-select")).toHaveClass(/ant-select-disabled/);
  const timezoneItem = page.locator(".ant-form-item").filter({ hasText: "Timezone" });
  await expect(timezoneItem.locator(".ant-select")).not.toHaveClass(/ant-select-disabled/);

  // Change a permitted directive and save: the locked one goes back as stored.
  await timezoneItem.locator(".ant-select").click();
  await page.keyboard.type("Asia/Jerusalem");
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Save Changes" }).click();
  await expect.poll(() => patched).not.toBeNull();
  expect(patched!.php_timezone).toBe("Asia/Jerusalem");
  expect(patched!.php_memory_limit).toBe("256M");
});
