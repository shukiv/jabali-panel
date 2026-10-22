// ADR-0175: Node.js apps on the Application Manager (the Python Apps page).
//
// Verifies:
//   - node_apps_enabled alone shows the page as "Node.js Apps", with no
//     runtime choice and no (Python-only) framework catalog, and a create
//     sends a Node.js body with no Python field
//   - with both runtimes on, the dialog offers the choice; switching to
//     Node.js after the dialog filled in a Python version sends no Python
//     field
//   - the entrypoint rules match the API's
//
// All API calls are mocked; the agent side is covered by the Go tests and the
// test box drill in the backend PR.
import { mockApi, signIn, test, expect, user } from "./fixtures";
import type { Page } from "@playwright/test";

const DOMAIN_ID = "01KPDOMAIN0000000000000000";

const nodeApp = {
  id: "01KNODEAPP0000000000000001",
  user_id: user.id,
  domain_id: DOMAIN_ID,
  name: "web",
  runtime: "node",
  python_version: "22",
  app_root: "apps/web",
  app_type: "npm",
  entrypoint: "start",
  base_uri: "/",
  status: "running",
  created_at: "2026-10-22T00:00:00Z",
};

async function setup(page: Page, opts: { python: boolean; node: boolean }) {
  const created: Record<string, unknown>[] = [];
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
        python_apps_enabled: opts.python,
        node_apps_enabled: opts.node,
      }),
    }),
  );
  await page.route("**/api/v1/python-apps", async (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      created.push(body);
      return route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({ app: { ...nodeApp, id: "01KNODEAPP0000000000000NEW", ...body, status: "pending" } }),
      });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: [nodeApp] }) });
  });
  await page.route("**/api/v1/python-apps/versions", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ versions: ["3.12"], default: "3.12" }) }),
  );
  await page.route("**/api/v1/python-apps/frameworks", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ frameworks: [] }) }),
  );
  await signIn(page, user);
  return { created };
}

async function pick(page: Page, label: string, option: RegExp) {
  await page.getByRole("dialog").locator(".ant-form-item").filter({ hasText: label }).locator(".ant-select").click();
  await page.locator(".ant-select-dropdown:visible .ant-select-item-option").filter({ hasText: option }).click();
}

test("Node.js apps alone: the page, no runtime choice, a Node.js create body", async ({ page }) => {
  const { created } = await setup(page, { python: false, node: true });

  // The entry sits in the sidebar's Tools group.
  await page.getByRole("menuitem", { name: "Tools" }).click();
  await expect(page.getByRole("menuitem", { name: "Node.js Apps" })).toBeVisible();
  await page.goto("/jabali-panel/python-apps");
  await expect(page.getByRole("heading", { name: "Node.js Apps" })).toBeVisible();
  // The framework catalog is Python's.
  await expect(page.getByRole("tab", { name: "Catalog" })).toHaveCount(0);
  await expect(page.getByRole("row", { name: /web/ })).toContainText("Node.js 22");
  await expect(page.getByRole("row", { name: /web/ })).toContainText("NPM");

  await page.getByRole("button", { name: "Create app" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Create Node.js app")).toBeVisible();
  await expect(dialog.getByRole("radio", { name: "Node.js" })).toHaveCount(0);
  // The form's initial type is wsgi; a Node.js app starts as node.
  await expect(dialog.locator(".ant-form-item").filter({ hasText: /^Type/ })).toContainText("node <script>");

  await dialog.getByLabel("Name").fill("api");
  await pick(page, "Domain", /^example\.com$/);
  await dialog.getByLabel("App directory").fill("apps/api");
  // The API's rule: a script under the app directory.
  await dialog.getByLabel("Entrypoint").fill("../server.js");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(dialog.getByText("Expected a .js, .mjs or .cjs file under the app directory")).toBeVisible();
  expect(created).toHaveLength(0);

  await dialog.getByLabel("Entrypoint").fill("server.js");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect.poll(() => created.length).toBe(1);
  expect(created[0]).toEqual({
    domain_id: DOMAIN_ID,
    name: "api",
    runtime: "node",
    app_root: "apps/api",
    base_uri: "/",
    node_version: "22",
    app_type: "node",
    entrypoint: "server.js",
  });
});

test("both runtimes: the dialog offers the choice and a switch to Node.js sends no Python field", async ({ page }) => {
  const { created } = await setup(page, { python: true, node: true });

  // The entry sits in the sidebar's Tools group.
  await page.getByRole("menuitem", { name: "Tools" }).click();
  await expect(page.getByRole("menuitem", { name: "Python & Node.js Apps" })).toBeVisible();
  await page.goto("/jabali-panel/python-apps");
  await expect(page.getByRole("tab", { name: "Catalog" })).toBeVisible();

  await page.getByRole("button", { name: "Create app" }).click();
  const dialog = page.getByRole("dialog");
  // Python first; the dialog fills in the server's Python version.
  await expect(dialog.getByRole("radio", { name: "Python" })).toBeChecked();
  await expect(dialog.getByText("Create Python app")).toBeVisible();
  await expect(dialog.locator(".ant-form-item").filter({ hasText: /^Python/ })).toContainText("3.12");

  await dialog.getByText("Node.js", { exact: true }).click();
  await expect(dialog.getByText("Create Node.js app")).toBeVisible();
  await dialog.getByLabel("Name").fill("web2");
  await pick(page, "Domain", /^example\.com$/);
  await dialog.getByLabel("App directory").fill("apps/web2");
  await pick(page, "Type", /npm run/);
  // An npm app's empty script is the server's default, start.
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect.poll(() => created.length).toBe(1);
  expect(created[0]).toEqual({
    domain_id: DOMAIN_ID,
    name: "web2",
    runtime: "node",
    app_root: "apps/web2",
    base_uri: "/",
    node_version: "22",
    app_type: "npm",
  });
});

test("a Node.js app's environment refuses PORT and HOST", async ({ page }) => {
  await setup(page, { python: false, node: true });
  let saved = false;
  await page.route(`**/api/v1/python-apps/${nodeApp.id}`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ app: nodeApp, env: [] }) }),
  );
  await page.route(`**/api/v1/python-apps/${nodeApp.id}/env`, (route) => {
    saved = true;
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });

  await page.goto("/jabali-panel/python-apps");
  await page.getByRole("row", { name: /web/ }).getByRole("button", { name: "More actions" }).click();
  await page.getByRole("menuitem", { name: "Environment" }).click();
  const drawer = page.getByRole("dialog", { name: "Environment — web" });
  await expect(drawer.getByText("PORT and HOST are set by the panel")).toBeVisible();

  await drawer.getByPlaceholder("KEY").fill("PORT");
  await expect(drawer.getByText("The panel sets PORT and HOST")).toBeVisible();
  await drawer.getByRole("button", { name: "Save" }).click();
  await page.waitForTimeout(300);
  expect(saved).toBe(false);
});
