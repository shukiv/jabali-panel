// GH #1701: a hosting package picks its disabled PHP functions one by one, and
// a domain's PHP Settings tab shows, read-only, what the domain's PHP pool
// really runs with.
import { test, expect, mockApi, signIn, admin, user } from "./fixtures";

const PACKAGE_ID = "01KPPACKAGE000000000000000";
const DOMAIN_ID = "01KPDOMAIN0000000000000000";

test("admin package editor: uncheck shell_exec and disable mail, the save sends the list (#1701)", async ({ page }) => {
  const pkg = {
    id: PACKAGE_ID,
    name: "Dev plan",
    ssh_enabled: false,
    cgi_enabled: false,
    webmail_enabled: true,
    fpm_user_can_edit: false,
    fpm_advanced_mode: false,
    fpm_max_children_cap: 20,
    fpm_worker_mem_mb: 64,
    disk_quota_mb: 1024,
    cpu_quota_percent: 0,
    memory_limit_mb: 0,
    io_read_mbps: 0,
    io_write_mbps: 0,
    max_tasks: 0,
    bandwidth_quota_mb: 0,
    max_domains: 5,
    max_email_accounts: 10,
    max_databases: 5,
    max_database_users: 5,
    max_docker_apps: 0,
    max_python_apps: 0,
    max_ftp_accounts: 0,
    max_backups: 0,
    max_backup_schedules: 1,
    scheduled_backups_enabled: false,
    allowed_backup_destination_kinds: "",
    backup_retention_policy: "reject",
    egress_ssh_out: false,
    egress_ssh_out_cidrs: "",
    egress_icmp: false,
    docker_app_slugs: "",
    php_settings_policy: "",
    // A package saved before GH #1701: no list, exec forbidden.
    php_exec_enabled: false,
    php_disabled_functions: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  await mockApi(page, { me: admin });

  let patched: Record<string, unknown> | null = null;
  await page.route(`**/api/v1/packages/${PACKAGE_ID}`, async (route) => {
    if (route.request().method() === "PATCH") {
      patched = route.request().postDataJSON() as Record<string, unknown>;
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...pkg, ...patched }) });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(pkg) });
  });

  await signIn(page, admin);
  await page.goto(`/jabali-admin/packages/edit/${PACKAGE_ID}`);

  // A package at the default disables every command-execution function.
  const shellExec = page.getByRole("checkbox", { name: "shell_exec" });
  await expect(shellExec).toBeChecked();
  await expect(page.getByRole("checkbox", { name: "exec", exact: true })).toBeChecked();

  // click + retrying assertion: antd's controlled checkbox re-renders after
  // the click, which locator.uncheck() checks too early.
  await shellExec.click();
  await expect(shellExec).not.toBeChecked();
  const extra = page.getByLabel("Also disable");
  await extra.click();
  await page.keyboard.type("mail");
  await page.keyboard.press("Enter");

  await page.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => patched).not.toBeNull();
  expect(patched!.php_disabled_functions).toBe("exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl,mail");
  // The backend derives php_exec_enabled from the list; the editor no longer sends it.
  expect(patched).not.toHaveProperty("php_exec_enabled");
});

test("tenant PHP settings: the read-only section shows what the pool really runs with (#1701)", async ({ page }) => {
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

  // The package allows shell_exec only, and also disables mail. PHP Defense
  // enforces, with the pool's own copy lifting its shell_exec ban.
  const pkgDisabled = ["exec", "passthru", "system", "proc_open", "popen", "pcntl_exec", "pcntl_fork", "proc_nice", "dl", "mail"];
  let effectiveCalls = 0;
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/php-settings/effective`, (route) => {
    effectiveCalls++;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        php_version: "8.3",
        pool_found: true,
        disabled_functions: [
          ...pkgDisabled.map((name) => ({ name, source: "pool" })),
          { name: "pcntl_alarm", source: "php.ini" },
        ],
        php_defense: {
          active: true,
          mode: "enforce",
          pool_rules: true,
          functions: ["exec", "passthru", "popen", "proc_open", "pcntl_exec", "system"].map((name) => ({ name, state: "blocked" })),
        },
        include_path: { value: ".:/usr/share/php", source: "php.ini" },
        session_save_path: { value: "/home/user/tmp", source: "pool" },
      }),
    });
  });

  await signIn(page, user);
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/php-settings`);

  const header = page.getByRole("button", { name: /Disabled functions and paths/ });
  await expect(header).toBeVisible();
  await expect(page.getByText("Read-only", { exact: true })).toBeVisible();
  // Collapsed: the agent read is not paid for until the section opens.
  expect(effectiveCalls).toBe(0);
  await header.click();

  const row = (fn: string) => page.getByRole("row").filter({ has: page.locator("code", { hasText: new RegExp(`^${fn}$`) }) });
  await expect(row("shell_exec")).toContainText("Allowed");
  await expect(row("exec")).toContainText("Disabled by the hosting package");
  await expect(row("mail")).toContainText("Disabled by the hosting package");
  await expect(row("pcntl_alarm")).toContainText("Disabled server-wide (php.ini)");
  await expect(page.getByText("This site's hosting package lifts its ban", { exact: false })).toBeVisible();

  const includeRow = page.getByRole("row").filter({ hasText: "include_path" });
  await expect(includeRow).toContainText(".:/usr/share/php");
  await expect(includeRow).toContainText("Server php.ini");
  const sessionRow = page.getByRole("row").filter({ hasText: "session.save_path" });
  await expect(sessionRow).toContainText("/home/user/tmp");
  await expect(sessionRow).toContainText("PHP pool setting");
  expect(effectiveCalls).toBe(1);
});
