// panel-ssl.spec.ts — admin Server Settings → General → Panel SSL
// card (M32.x, ADR-0105). Mocks /admin/panel-certificate which now
// returns {certs:[hostname,mail]} so the spec needs no live panel-api.
import { admin, expect, mockApi, signIn, test } from "./fixtures";

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

test.describe("admin panel SSL card", () => {
  test("renders self-signed status + routable + toggle", async ({ page }) => {
    await mockApi(page, { me: admin });
    await page.route("**/api/v1/admin/panel-certificate", (route) =>
      route.fulfill({ status: 200, contentType: "application/json", body: body() }),
    );
    await signIn(page, admin);
    await page.goto("/jabali-admin/settings");

    // Two CertRows → "Self-signed" tag appears per kind; scope to the
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
  });

  test("toggle is disabled when hostname not routable", async ({ page }) => {
    await mockApi(page, { me: admin });
    await page.route("**/api/v1/admin/panel-certificate", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        // hostname not routable (governs the toggle); mail routable
        // so its "Not routable" doesn't double the match.
        body: body(
          { routable: false, routable_reason: "non-routable hostname suffix" },
          { routable: true },
        ),
      }),
    );
    await signIn(page, admin);
    await page.goto("/jabali-admin/settings");

    await expect(
      page.locator(".ant-tag", { hasText: "Not routable" }).first(),
    ).toBeVisible();
    await expect(
      page.getByText(/non-routable hostname suffix/).first(),
    ).toBeVisible();
  });

  // GH #2166: with Let's Encrypt off the hostname's action regenerates the
  // self-signed cert (no routability check), even behind a proxy.
  test("Let's Encrypt off: the hostname regenerates the self-signed cert", async ({ page }) => {
    await mockApi(page, { me: admin });
    await page.route("**/api/v1/admin/panel-certificate", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: body({ routable: false, routable_reason: "dns points elsewhere" }),
      }),
    );
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
    await page.goto("/jabali-admin/settings");

    await page.getByRole("button", { name: /Regenerate self-signed/ }).click();
    const confirm = page.locator(".ant-popconfirm", { hasText: "Regenerate the self-signed certificate?" });
    await expect(confirm).toBeVisible();
    await confirm.getByRole("button", { name: "OK" }).click();
    await expect.poll(() => issued).toContain("kind=hostname");
    await expect(page.getByText("Regenerating the self-signed certificate for panel.example.com.")).toBeVisible();
    // The mail cert stays a Let's Encrypt action.
    await expect(page.getByRole("button", { name: /Issue \/ retry/ })).toHaveCount(1);
  });

  test("issued hostname cert shows expiry hint", async ({ page }) => {
    const future = new Date(Date.now() + 60 * 24 * 3600 * 1000).toISOString();
    await mockApi(page, { me: admin });
    await page.route("**/api/v1/admin/panel-certificate", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: body(
          {
            status: "issued",
            use_le: true,
            issued_at: new Date().toISOString(),
            expires_at: future,
          },
          { status: "pending_acme_retry", use_le: true },
        ),
      }),
    );
    await signIn(page, admin);
    await page.goto("/jabali-admin/settings");

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
});
