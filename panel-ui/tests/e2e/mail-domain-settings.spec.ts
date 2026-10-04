// GH #1628 slice 4: tenant per-domain webmail Settings tab — real-browser pass.
// GH #1915: the disclaimer moved from its own tab onto Settings.
// GH #1916: so did the catch-all.
//
// Verifies in Chromium (built SPA, mocked API) what happy-dom cannot: the new
// Settings tab registers and route-activates on a direct load, the webmail
// Switch renders + flips → PATCH /domains/:id { webmail_enabled } → toast, the
// email-off domain hides the switch behind an "enable email first" prompt, the
// disclaimer form loads the saved values and saves → PUT /domains/:id/disclaimer,
// an old /disclaimer link lands on Settings, the catch-all is set and cleared
// from Settings (PUT / DELETE /domains/:id/catchall) and its old /catchall link
// lands there too, and the tab strip does not overflow the document at phone
// width.
import { mockApi, signIn, test, expect, user, expectNoHorizontalOverflow } from "./fixtures";
import type { Page } from "@playwright/test";

const DOMAIN_ID = "01KDOM0000000000000000MAIL";

async function setup(
  page: Page,
  opts: { emailEnabled: boolean; webmailEnabled: boolean },
): Promise<{
  patched: Record<string, unknown>[];
  put: Record<string, unknown>[];
  disclaimerGets: number[];
  catchAllPut: Record<string, unknown>[];
  catchAllDeletes: number[];
  catchAllGets: number[];
}> {
  const patched: Record<string, unknown>[] = [];
  const put: Record<string, unknown>[] = [];
  const disclaimerGets: number[] = [];
  const catchAllPut: Record<string, unknown>[] = [];
  const catchAllDeletes: number[] = [];
  const catchAllGets: number[] = [];
  let disclaimer = { enabled: true, text: "Confidential.", updated_at: "2026-09-30T00:00:00Z" };
  let catchAll: { target: string | null; updated_at: string } = { target: null, updated_at: "2026-10-04T00:00:00Z" };

  await mockApi(page, { me: user });

  // Tenant shell reads capabilities on mount; advertise mail on.
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

  // Owner-scoped GET/PATCH /domains/:id — override the generic fixtures route
  // (registered later ⇒ matches first) to carry email_enabled + webmail_enabled.
  await page.route(`**/api/v1/domains/${DOMAIN_ID}`, async (route) => {
    const method = route.request().method();
    if (method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: DOMAIN_ID,
          user_id: user.id,
          name: "example.com",
          is_enabled: true,
          nginx_custom_directives: "",
          email_enabled: opts.emailEnabled,
          webmail_enabled: opts.webmailEnabled,
        }),
      });
    }
    if (method === "PATCH") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      patched.push(body);
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ id: DOMAIN_ID, ...body }),
      });
    }
    return route.fallback();
  });

  await page.route(`**/api/v1/domains/${DOMAIN_ID}/disclaimer`, async (route) => {
    const method = route.request().method();
    if (method === "GET") {
      disclaimerGets.push(1);
    } else if (method === "PUT") {
      const body = route.request().postDataJSON() as { enabled: boolean; text: string };
      put.push(body);
      disclaimer = { ...body, updated_at: "2026-09-30T00:00:01Z" };
    } else {
      return route.fallback();
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ domain_id: DOMAIN_ID, domain_name: "example.com", ...disclaimer }),
    });
  });

  await page.route(`**/api/v1/domains/${DOMAIN_ID}/catchall`, async (route) => {
    const method = route.request().method();
    if (method === "GET") {
      catchAllGets.push(1);
    } else if (method === "PUT") {
      const body = route.request().postDataJSON() as { target: string };
      catchAllPut.push(body);
      catchAll = { target: body.target, updated_at: "2026-10-04T00:00:01Z" };
    } else if (method === "DELETE") {
      catchAllDeletes.push(1);
      catchAll = { target: null, updated_at: "2026-10-04T00:00:02Z" };
    } else {
      return route.fallback();
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ domain_id: DOMAIN_ID, domain_name: "example.com", ...catchAll }),
    });
  });

  await page.route(`**/api/v1/domains/${DOMAIN_ID}/mailboxes?*`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          { id: "m1", email: "info@example.com" },
          { id: "m2", email: "sales@example.com" },
        ],
        total: 2,
        page: 1,
        page_size: 200,
      }),
    }),
  );

  return { patched, put, disclaimerGets, catchAllPut, catchAllDeletes, catchAllGets };
}

test.describe("GH #1628 slice 4 — tenant per-domain webmail Settings tab", () => {
  test("Settings tab shows the webmail switch and PATCHes on flip", async ({ page }) => {
    const { patched } = await setup(page, { emailEnabled: true, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);

    await expect(page.getByRole("tab", { name: "Settings" })).toBeVisible();

    const sw = page.getByRole("switch", { name: "Webmail client" });
    await expect(sw).toBeVisible();
    await expect(sw).toBeChecked();

    await sw.click();

    await expect.poll(() => patched.length).toBeGreaterThan(0);
    expect(patched[0]).toEqual({ webmail_enabled: false });
    await expect(page.getByText(/webmail disabled for this domain/i)).toBeVisible();
  });

  test("email-off domain hides the switch and prompts to enable email", async ({ page }) => {
    const { disclaimerGets, catchAllGets } = await setup(page, { emailEnabled: false, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);

    await expect(page.getByText(/enable email for this domain first/i)).toBeVisible();
    await expect(page.getByRole("switch", { name: "Webmail client" })).toHaveCount(0);
    await expect(page.getByRole("switch", { name: "Enable Disclaimer" })).toHaveCount(0);
    expect(disclaimerGets).toHaveLength(0);
    expect(catchAllGets).toHaveLength(0);
  });

  test("GH #1915: Settings shows the saved disclaimer and saves it via PUT", async ({ page }) => {
    const { put } = await setup(page, { emailEnabled: true, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);

    const sw = page.getByRole("switch", { name: "Enable Disclaimer" });
    await expect(sw).toBeChecked();
    const text = page.getByRole("textbox", { name: "Disclaimer Text" });
    await expect(text).toHaveValue("Confidential.");

    await text.fill("");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Text required when enabled")).toBeVisible();
    expect(put).toHaveLength(0);

    await text.fill("Legal notice.");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => put.length).toBe(1);
    expect(put[0]).toEqual({ enabled: true, text: "Legal notice." });
    await expect(page.getByText("Disclaimer saved")).toBeVisible();
    // The form remounts from the refetched row and keeps the saved text.
    await expect(page.getByRole("textbox", { name: "Disclaimer Text" })).toHaveValue("Legal notice.");
  });

  test("GH #1915: the Disclaimer tab is gone and its old link opens Settings", async ({ page }) => {
    await setup(page, { emailEnabled: true, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/disclaimer`);

    await page.waitForURL(new RegExp(`/mail-domains/${DOMAIN_ID}/settings$`));
    await expect(page.getByRole("tab", { name: "Settings", selected: true })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Disclaimer" })).toHaveCount(0);
    await expect(page.getByRole("switch", { name: "Enable Disclaimer" })).toBeVisible();
  });

  test("GH #1916: Settings sets the catch-all to a mailbox and clears it", async ({ page }) => {
    const { catchAllPut, catchAllDeletes } = await setup(page, { emailEnabled: true, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);

    const card = page.locator(".ant-card").filter({ has: page.getByText("Catch-All", { exact: true }) });
    await expect(card.getByText(/^Not set: mail to an unknown address at/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Clear catch-all" })).toHaveCount(0);

    await page.getByLabel("Target mailbox").click();
    await page.keyboard.type("sales");
    await page.keyboard.press("Enter");
    await page.getByRole("button", { name: "Set catch-all" }).click();
    await expect.poll(() => catchAllPut.length).toBe(1);
    expect(catchAllPut[0]).toEqual({ target: "sales@example.com" });
    await expect(page.getByText("Catch-all saved")).toBeVisible();
    // The card remounts from the refetched row.
    await expect(card.locator("code", { hasText: "sales@example.com" })).toBeVisible();

    await page.getByRole("button", { name: "Clear catch-all" }).click();
    await page.getByRole("button", { name: "Clear", exact: true }).click();
    await expect.poll(() => catchAllDeletes.length).toBe(1);
    await expect(page.getByText("Catch-all cleared")).toBeVisible();
    await expect(card.getByText(/^Not set: mail to an unknown address at/)).toBeVisible();
  });

  test("GH #1916: the Catch-All tab is gone and its old link opens Settings", async ({ page }) => {
    await setup(page, { emailEnabled: true, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/catchall`);

    await page.waitForURL(new RegExp(`/mail-domains/${DOMAIN_ID}/settings$`));
    await expect(page.getByRole("tab", { name: "Settings", selected: true })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Catch-All" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Set catch-all" })).toBeVisible();
  });

  test("the tab strip does not overflow the document at phone width", async ({ page }) => {
    await setup(page, { emailEnabled: true, webmailEnabled: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);
    await expect(page.getByRole("switch", { name: "Webmail client" })).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });
});
