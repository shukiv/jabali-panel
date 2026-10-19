// panel-ssl.spec.ts — admin SSL Manager → Panel SSL tab (M32.x, ADR-0105,
// GH #2166), where the panel's own certs are managed, and the Server
// Settings → General summary card that links there. Mocks
// /admin/panel-certificate, which returns {certs:[hostname,mail]}, so the
// spec needs no live panel-api.
import type { Page } from "@playwright/test";
import { admin, expect, mockApi, signIn, test } from "./fixtures";

const PANEL_TAB = "/jabali-admin/ssl?tab=panel";

function cert(kind: "hostname" | "mail", over: Record<string, unknown> = {}) {
  return {
    kind,
    id: 1,
    hostname: kind === "mail" ? "mail.panel.example.com" : "panel.example.com",
    status: "self_signed",
    cert_pem_path:
      kind === "mail"
        ? "/etc/jabali/tls/panel-mail.crt"
        : "/etc/jabali/tls/panel.crt",
    attempt_count: 0,
    staging: false,
    use_le: false,
    updated_at: new Date().toISOString(),
    routable: true,
    ...over,
  };
}

function body(hostOver = {}, mailOver = {}) {
  return JSON.stringify({
    certs: [cert("hostname", hostOver), cert("mail", mailOver)],
  });
}

// mockCerts answers the panel cert GET with certs, and the SSL Manager's
// other lists with nothing.
async function mockCerts(page: Page, certs: string) {
  await page.route("**/api/v1/admin/panel-certificate", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: certs }),
  );
  await page.route("**/api/v1/admin/ssl-certificates**", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [] }) }),
  );
  await page.route("**/api/v1/admin/certificates/shared**", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: [] }) }),
  );
}

test.describe("admin SSL Manager → Panel SSL", () => {
  test("renders self-signed status + routable + toggle", async ({ page }) => {
    await mockApi(page, { me: admin });
    await mockCerts(page, body());
    await signIn(page, admin);
    await page.goto(PANEL_TAB);

    // Two cert cards → "Self-signed" tag appears per kind; scope to the
    // AntD Tag and assert at least one is visible (strict-mode safe).
    await expect(
      page.locator(".ant-tag", { hasText: "Self-signed" }).first(),
    ).toBeVisible();
    await expect(
      page.locator(".ant-tag", { hasText: "Routable" }).first(),
    ).toBeVisible();
    await expect(
      page.getByText("Use Let's Encrypt for the panel"),
    ).toBeVisible();
    // GH #2166: the compact SYSTEM band above the tabs is gone.
    await expect(page.getByText("SYSTEM", { exact: true })).toHaveCount(0);
  });

  test("toggle is disabled when hostname not routable", async ({ page }) => {
    await mockApi(page, { me: admin });
    // hostname not routable (governs the toggle); mail routable so its
    // "Not routable" doesn't double the match.
    await mockCerts(
      page,
      body(
        { routable: false, routable_reason: "non-routable hostname suffix" },
        { routable: true },
      ),
    );
    await signIn(page, admin);
    await page.goto(PANEL_TAB);

    await expect(
      page.locator(".ant-tag", { hasText: "Not routable" }).first(),
    ).toBeVisible();
    await expect(
      page.getByText(/non-routable hostname suffix/).first(),
    ).toBeVisible();
    await expect(page.getByRole("switch").first()).toBeDisabled();
  });

  // GH #2166: with Let's Encrypt off the hostname's action regenerates the
  // self-signed cert (no routability check), even behind a proxy.
  test("Let's Encrypt off: the hostname regenerates the self-signed cert", async ({ page }) => {
    await mockApi(page, { me: admin });
    await mockCerts(page, body({ routable: false, routable_reason: "dns points elsewhere" }));
    let issued: string | null = null;
    await page.route("**/api/v1/admin/panel-certificate/issue**", (route) => {
      issued = route.request().url();
      return route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          self_signed: true,
          regenerating: true,
          detail: "Regenerating the self-signed certificate for panel.example.com.",
        }),
      });
    });
    await signIn(page, admin);
    await page.goto(PANEL_TAB);

    // The hostname's card comes first.
    await page.getByRole("button", { name: /Regenerate self-signed/ }).first().click();
    const confirm = page.locator(".ant-popconfirm", { hasText: "Regenerate the self-signed certificate?" });
    await expect(confirm).toBeVisible();
    await confirm.getByRole("button", { name: "OK" }).click();
    await expect.poll(() => issued).toContain("kind=hostname");
    await expect(page.getByText("Regenerating the self-signed certificate for panel.example.com.")).toBeVisible();
    // The mail cert follows the toggle too.
    await expect(page.getByRole("button", { name: /Regenerate self-signed/ })).toHaveCount(2);
    await expect(page.getByRole("button", { name: /Issue \/ retry/ })).toHaveCount(0);
  });

  // GH #2166: the mail cert follows the toggle. The hostname row's use_le
  // governs both, even when the mail row's own flag is stale.
  test("Let's Encrypt off: the mail cert regenerates its self-signed cert", async ({ page }) => {
    await mockApi(page, { me: admin });
    await mockCerts(page, body({}, { use_le: true }));
    let issued: string | null = null;
    await page.route("**/api/v1/admin/panel-certificate/issue**", (route) => {
      issued = route.request().url();
      return route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          self_signed: true,
          regenerating: true,
          detail: "Regenerating the self-signed mail certificate for mail.panel.example.com.",
        }),
      });
    });
    await signIn(page, admin);
    await page.goto(PANEL_TAB);

    await page.getByRole("button", { name: /Regenerate self-signed/ }).nth(1).click();
    const confirm = page.locator(".ant-popconfirm", { hasText: "Regenerate the self-signed mail certificate?" });
    await expect(confirm).toBeVisible();
    await confirm.getByRole("button", { name: "OK" }).click();
    await expect.poll(() => issued).toContain("kind=mail");
    await expect(page.getByText("Regenerating the self-signed mail certificate for mail.panel.example.com.")).toBeVisible();
  });

  test("issued hostname cert shows expiry hint", async ({ page }) => {
    const future = new Date(Date.now() + 60 * 24 * 3600 * 1000).toISOString();
    await mockApi(page, { me: admin });
    await mockCerts(
      page,
      body(
        {
          status: "issued",
          use_le: true,
          issued_at: new Date().toISOString(),
          expires_at: future,
          served: {
            type: "lets_encrypt",
            issuer: "Let's Encrypt",
            subject: "panel.example.com",
            names: ["panel.example.com"],
            not_before: new Date().toISOString(),
            not_after: future,
            fingerprint_sha256: "AB:CD",
          },
        },
        { status: "pending_acme_retry", use_le: true },
      ),
    );
    await signIn(page, admin);
    await page.goto(PANEL_TAB);

    await expect(
      page.getByText(/Issued by Let's Encrypt/).first(),
    ).toBeVisible();
    await expect(page.getByText(/Expires in \d+ days/)).toBeVisible();
    // The mail row independently shows its own pending state — proof
    // the two kinds render separately (ADR-0105).
    await expect(
      page.locator(".ant-tag", { hasText: "Pending retry" }).first(),
    ).toBeVisible();
  });

  // GH #2166: the details come from the cert file the panel serves.
  test("shows the served cert's details", async ({ page }) => {
    await mockApi(page, { me: admin });
    await mockCerts(
      page,
      body({
        served: {
          type: "self_signed",
          issuer: "Jabali Panel",
          subject: "panel.example.com",
          names: ["panel.example.com", "mail.panel.example.com", "203.0.113.10"],
          not_before: "2026-10-17T06:00:00Z",
          not_after: "2036-10-17T06:00:00Z",
          fingerprint_sha256: "AB:CD:EF:01",
        },
      }),
    );
    await signIn(page, admin);
    await page.goto(PANEL_TAB);

    const host = page.locator(".ant-card", { has: page.locator(".ant-card-head code", { hasText: /^panel\.example\.com$/ }) });
    await expect(host.getByText("Jabali Panel")).toBeVisible();
    await expect(host.locator(".ant-tag code", { hasText: "203.0.113.10" })).toBeVisible();
    await expect(host.getByText("AB:CD:EF:01")).toBeVisible();
    await expect(host.getByText("SHA-256 fingerprint")).toBeVisible();
  });
});

test.describe("admin Server Settings → Panel SSL", () => {
  test("shows each cert's status and links to the SSL Manager tab", async ({ page }) => {
    await mockApi(page, { me: admin });
    await mockCerts(page, body());
    await signIn(page, admin);
    await page.goto("/jabali-admin/settings");

    const card = page.locator(".ant-card", { has: page.locator(".ant-card-head", { hasText: "Panel SSL" }) });
    await expect(card.getByText("panel.example.com", { exact: true })).toBeVisible();
    await expect(card.getByRole("button")).toHaveCount(0);
    await card.getByRole("link", { name: "Manage in SSL Manager" }).click();
    await expect(page).toHaveURL(/\/jabali-admin\/ssl\?tab=panel$/);
    await expect(page.getByText("Use Let's Encrypt for the panel")).toBeVisible();
  });
});
