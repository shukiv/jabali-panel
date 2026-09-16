// GH #1628 slice 4: tenant per-domain webmail Settings tab — real-browser pass.
//
// Verifies in Chromium (built SPA, mocked API) what happy-dom cannot: the new
// Settings tab registers and route-activates on a direct load, the webmail
// Switch renders + flips → PATCH /domains/:id { webmail_enabled } → toast, the
// email-off domain hides the switch behind an "enable email first" prompt, and
// the 10-tab Card strip does not overflow the document at phone width.
import { mockApi, signIn, test, expect, user, expectNoHorizontalOverflow } from "./fixtures";
import type { Page } from "@playwright/test";

const DOMAIN_ID = "01KDOM0000000000000000MAIL";

async function setup(
  page: Page,
  opts: { emailEnabled: boolean; webmailEnabled: boolean },
): Promise<{ patched: Record<string, unknown>[] }> {
  const patched: Record<string, unknown>[] = [];

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

  return { patched };
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
    await setup(page, { emailEnabled: false, webmailEnabled: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);

    await expect(page.getByText(/enable email for this domain first/i)).toBeVisible();
    await expect(page.getByRole("switch", { name: "Webmail client" })).toHaveCount(0);
  });

  test("the 10-tab strip does not overflow the document at phone width", async ({ page }) => {
    await setup(page, { emailEnabled: true, webmailEnabled: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto(`/jabali-panel/mail-domains/${DOMAIN_ID}/settings`);
    await expect(page.getByRole("switch", { name: "Webmail client" })).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });
});
