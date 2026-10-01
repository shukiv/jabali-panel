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
  "log_errors",
  "file_uploads",
  "short_open_tag",
  // GH #1701 Slice 3: security-sensitive, opted in with tenant_privileged.
  "open_basedir",
  "allow_url_fopen",
];
const SENSITIVE = new Set(["open_basedir", "allow_url_fopen"]);

// The policy level of every directive, with `locked` admin-only.
function policyLocking(locked: string): Record<string, string> {
  return Object.fromEntries(
    DIRECTIVES.map((d) => [
      d,
      d === locked ? "admin_only" : SENSITIVE.has(d) ? "tenant_privileged" : "tenant_allowed",
    ]),
  );
}

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
        policy: policyLocking("memory_limit"),
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

// GH #1701 Slice 2: the flags follow the same policy. A locked short_open_tag
// is read-only and goes back as stored; a permitted file_uploads change is sent.
test("tenant PHP settings: a locked flag is sent back unchanged, a permitted flag is saved (#1701 slice 2)", async ({
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
        php_short_open_tag: true,
        pool_defaults: { log_errors: "1", file_uploads: "1", short_open_tag: "" },
        policy: policyLocking("short_open_tag"),
        editable: DIRECTIVES.filter((d) => d !== "short_open_tag"),
      }),
    });
  });

  await signIn(page, user);
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/php-settings`);

  const shortItem = page.locator(".ant-form-item").filter({ hasText: "Short open tag" });
  await expect(shortItem.locator(".ant-select")).toHaveClass(/ant-select-disabled/);
  await expect(shortItem.getByText("Set by your administrator")).toBeVisible();

  const uploadsItem = page.locator(".ant-form-item").filter({ hasText: "File uploads" });
  await expect(uploadsItem).toContainText("On (Default)");
  await uploadsItem.locator(".ant-select").click();
  await page.locator(".ant-select-dropdown:visible .ant-select-item-option").filter({ hasText: /^Off$/ }).click();
  await page.getByRole("button", { name: "Save Changes" }).click();
  await expect.poll(() => patched).not.toBeNull();
  expect(patched!.php_file_uploads).toBe(false);
  expect(patched!.php_short_open_tag).toBe(true);
  expect(patched!.php_log_errors).toBeNull();
});

// GH #1701 Slice 3: a tenant whose package opts them in picks an open_basedir
// preset and turns allow_url_fopen off; a save sends both. A tenant without
// the opt-in sees both read-only (covered by the unit tests).
test("tenant PHP settings: open_basedir preset and allow_url_fopen are saved (#1701 slice 3)", async ({
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
        pool_defaults: { allow_url_fopen: "1" },
        policy: policyLocking(""),
        editable: DIRECTIVES,
      }),
    });
  });

  await signIn(page, user);
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/php-settings`);

  const basedirItem = page.locator(".ant-form-item").filter({ hasText: "Allowed folders (open_basedir)" });
  await basedirItem.locator("input").click();
  await page
    .locator(".ant-select-dropdown:visible .ant-select-item-option")
    .filter({ hasText: "This domain's folder + temp folders" })
    .click();
  await expect(basedirItem.locator("input")).toHaveValue("{DOCROOT}:{TMP}");

  const fopenItem = page.locator(".ant-form-item").filter({ hasText: "Remote file access (allow_url_fopen)" });
  await expect(fopenItem).toContainText("On (Default)");
  await fopenItem.locator(".ant-select").click();
  await page.locator(".ant-select-dropdown:visible .ant-select-item-option").filter({ hasText: /^Off$/ }).click();

  await page.getByRole("button", { name: "Save Changes" }).click();
  await expect.poll(() => patched).not.toBeNull();
  expect(patched!.php_open_basedir).toBe("{DOCROOT}:{TMP}");
  expect(patched!.php_allow_url_fopen).toBe(false);
});
