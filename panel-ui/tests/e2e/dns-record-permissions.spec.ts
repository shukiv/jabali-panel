// Wave D E2E for GH #466: the tenant DNS records manager (DNS > Zones > a
// zone, /jabali-panel/dns/:id since GH #1920) must honor the admin's per-type
// permission matrix returned by GET /dns/policy. With a locked-down
// policy (A/AAAA/CNAME only), an MX record is read-only ("Restricted") while an
// A record keeps its Edit/Delete actions, and the Add Record type picker offers
// only creatable types.
import type { Page } from "@playwright/test";
import { test, expect, mockApi, signIn, user } from "./fixtures";

const DOMAIN_ID = "01KPDOMAIN0000000000000000";

// The records page sits behind the dns_enabled capability gate (GH #1920);
// mockApi does not serve /me/server-capabilities.
async function mockDNSEnabled(page: Page) {
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
}

const lockedDownPolicy = {
  A: { create: true, edit: true, delete: true },
  AAAA: { create: true, edit: true, delete: true },
  CNAME: { create: true, edit: true, delete: true },
  MX: { create: false, edit: false, delete: false },
  TXT: { create: false, edit: false, delete: false },
  SRV: { create: false, edit: false, delete: false },
  CAA: { create: false, edit: false, delete: false },
};

const records = [
  {
    id: "01KPRECMX000000000000000000",
    zone_id: "01KPZONE00000000000000000",
    name: "@",
    type: "MX",
    content: "mail.example.com.",
    ttl: 3600,
    priority: 10,
    managed: false,
    is_enabled: true,
  },
  {
    id: "01KPRECA0000000000000000000",
    zone_id: "01KPZONE00000000000000000",
    name: "www",
    type: "A",
    content: "192.0.2.1",
    ttl: 3600,
    priority: 0,
    managed: false,
    is_enabled: true,
  },
];

test("tenant DNS page honors a locked-down record-type policy (#466)", async ({ page }) => {
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
  await mockDNSEnabled(page);

  // DNS-specific endpoints (not covered by mockApi).
  await page.route("**/api/v1/dns/policy", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ policy: lockedDownPolicy, is_admin: false }),
    }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/dns/zone`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        zone: {
          id: "01KPZONE00000000000000000",
          domain_id: DOMAIN_ID,
          name: "example.com",
          serial: 1,
          refresh_seconds: 3600,
          retry_seconds: 600,
          expire_seconds: 604800,
          minimum_ttl: 3600,
          is_enabled: true,
        },
      }),
    }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/dns/records`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ records }),
    }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/dns/system-records`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ system_records: [] }),
    }),
  );

  await signIn(page, user);
  await page.goto(`/jabali-panel/dns/${DOMAIN_ID}`);

  await expect(page.getByRole("heading", { name: "DNS Records for example.com" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add Record" }).first()).toBeVisible();

  // The MX row is restricted (no Edit/Delete) under locked-down policy.
  const mxRow = page.getByRole("row").filter({ hasText: "mail.example.com" });
  await expect(mxRow.getByText("Restricted")).toBeVisible();

  // The A row keeps actions (locked-down allows A edit/delete) — it must NOT
  // be marked Restricted.
  const aRow = page.getByRole("row").filter({ hasText: "192.0.2.1" });
  await expect(aRow.getByText("Restricted")).toHaveCount(0);

  // Add Record stays enabled because at least one type (A) is creatable.
  await expect(page.getByRole("button", { name: "Add Record" }).first()).toBeEnabled();
});

// A policy that permits nothing must disable the Add Record button entirely —
// the create-side counterpart to the per-row edit/delete gating above.
test("tenant DNS Add button is disabled when no type is creatable (#466)", async ({ page }) => {
  const denyAll = Object.fromEntries(
    ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA"].map((t) => [t, { create: false, edit: false, delete: false }]),
  );
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
  await mockDNSEnabled(page);
  await page.route("**/api/v1/dns/policy", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ policy: denyAll, is_admin: false }) }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/dns/zone`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ zone: { id: "01KPZONE00000000000000000", domain_id: DOMAIN_ID, name: "example.com", serial: 1, refresh_seconds: 3600, retry_seconds: 600, expire_seconds: 604800, minimum_ttl: 3600, is_enabled: true } }),
    }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/dns/records`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ records: [] }) }),
  );
  await page.route(`**/api/v1/domains/${DOMAIN_ID}/dns/system-records`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ system_records: [] }) }),
  );

  await signIn(page, user);
  // Enter through the old Web Domain DNS-tab URL: a bookmark of it must land
  // on the zone's records page under DNS (GH #1920).
  await page.goto(`/jabali-panel/domains/${DOMAIN_ID}/dns`);
  await page.waitForURL(`**/jabali-panel/dns/${DOMAIN_ID}`);
  // The Add Record button is the load anchor (and here must be disabled).
  await expect(page.getByRole("button", { name: "Add Record" }).first()).toBeDisabled();
});
