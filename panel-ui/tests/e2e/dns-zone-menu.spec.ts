// GH #1918 (johnnyq): the DNS page is one zone list. Real-browser pass (built
// SPA, mocked API) over what happy-dom cannot show: the DNSSEC tab and the
// "Manage Records" button are gone, the domain name links to the zone's
// records, and the row's ⋯ menu enables DNSSEC (PUT, tag flips to Signed),
// opens View DS & keys, and disables DNSSEC only after a confirm that warns
// about the registrar's DS record. The list does not overflow at phone width.
import { mockApi, signIn, test, expect, user, expectNoHorizontalOverflow } from "./fixtures";
import type { Page } from "@playwright/test";

const ZONE_ID = "01KDOM00000000000000000DNS";

async function setup(page: Page, opts: { signed: boolean }): Promise<{ puts: { enabled: boolean }[] }> {
  const puts: { enabled: boolean }[] = [];
  let signed = opts.signed;

  await mockApi(page, { me: user });

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

  await page.route(/\/api\/v1\/dns\/zones(\?.*)?$/, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          {
            id: ZONE_ID,
            user_id: "u1",
            name: "example.com",
            provisioned: true,
            record_count: 7,
            effective_ttl: 3600,
            dnssec_enabled: signed,
            registrar_expires_at: null,
            dns_disabled: false,
            web_disabled: false,
            email_enabled: true,
          },
        ],
        total: 1,
        page: 1,
        page_size: 20,
      }),
    }),
  );

  const state = () => ({
    domain_id: ZONE_ID,
    domain_name: "example.com",
    enabled: signed,
    keys: signed
      ? [{ key_tag: 31337, key_type: "KSK", algorithm: 13, public_key: "pk", active: true }]
      : [],
  });

  await page.route(`**/api/v1/domains/${ZONE_ID}/dnssec`, async (route) => {
    const method = route.request().method();
    if (method === "PUT") {
      const body = route.request().postDataJSON() as { enabled: boolean };
      puts.push(body);
      signed = body.enabled;
    } else if (method !== "GET") {
      return route.fallback();
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(state()) });
  });

  await page.route(`**/api/v1/domains/${ZONE_ID}/dnssec/ds`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        domain_id: ZONE_ID,
        domain_name: "example.com",
        ds_records: [{ key_tag: 31337, algorithm: 13, digest_type: 2, digest: "C0FFEE00DEADBEEF" }],
      }),
    }),
  );

  return { puts };
}

const row = (page: Page) => page.getByRole("row").filter({ hasText: "example.com" });

test.describe("DNS zone list (GH #1918)", () => {
  test("one list: no DNSSEC tab, the name links to records, ⋯ enables DNSSEC", async ({ page }) => {
    const { puts } = await setup(page, { signed: false });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto("/jabali-panel/dns");

    await expect(page.getByRole("link", { name: "example.com" })).toHaveAttribute(
      "href",
      `/jabali-panel/domains/${ZONE_ID}/dns`,
    );
    await expect(page.getByRole("tab", { name: "DNSSEC" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Manage Records" })).toHaveCount(0);
    await expect(row(page).getByText("Unsigned")).toBeVisible();

    await row(page).getByRole("button", { name: "Actions for example.com" }).click();
    await expect(page.getByRole("menuitem", { name: /View DS & keys/ })).toHaveCount(0);
    await page.getByRole("menuitem", { name: /Enable DNSSEC/ }).click();

    await expect.poll(() => puts).toEqual([{ enabled: true }]);
    await expect(row(page).getByText("Signed", { exact: true })).toBeVisible();
  });

  test("a signed zone shows its DS and keys, and disables only after a confirm", async ({ page }) => {
    const { puts } = await setup(page, { signed: true });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto("/jabali-panel/dns");
    await expect(row(page).getByText("Signed", { exact: true })).toBeVisible();

    await row(page).getByRole("button", { name: "Actions for example.com" }).click();
    await page.getByRole("menuitem", { name: /View DS & keys/ }).click();
    const modal = page.getByRole("dialog", { name: "DNSSEC keys · example.com" });
    await expect(modal.getByText("C0FFEE00DEADBEEF")).toBeVisible();
    await expect(modal.getByRole("cell", { name: "31337" }).first()).toBeVisible();
    await modal.locator(".ant-modal-footer").getByRole("button", { name: "Close" }).click();
    await expect(modal).toBeHidden();

    await row(page).getByRole("button", { name: "Actions for example.com" }).click();
    await page.getByRole("menuitem", { name: "Disable DNSSEC", exact: true }).click();
    const confirm = page.getByRole("dialog").filter({ hasText: "Disable DNSSEC for example.com?" });
    await expect(confirm.getByText(/Remove the DS record at your registrar first/)).toBeVisible();
    expect(puts).toHaveLength(0);
    await confirm.getByRole("button", { name: "Disable DNSSEC" }).click();

    await expect.poll(() => puts).toEqual([{ enabled: false }]);
    await expect(row(page).getByText("Unsigned")).toBeVisible();
  });

  test("the zone list does not overflow at phone width", async ({ page }) => {
    await setup(page, { signed: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await signIn(page, user);
    await page.waitForURL(/\/jabali-panel/);

    await page.goto("/jabali-panel/dns");
    await expect(page.getByRole("link", { name: "example.com" })).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });
});
