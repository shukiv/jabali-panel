// admin-migrations-wizard.spec.ts — 4-step CreateMigrationWizard
// happy path (M35.1, ADR-0095). Mocks every backend call and walks
// the WHM bulk flow end to end.
import { admin, expect, mockApi, signIn, test } from "./fixtures";

const DRAFT_ID = "01J0DRAFT00000000000000000";

test.describe("admin migrations — CreateMigrationWizard (M35.1)", () => {
  test("WHM happy path: draft → connection → discover → bulk", async ({ page }) => {
    await mockApi(page, { me: admin });

    // Capture state across route handlers.
    let draftBody: { source_kind?: string; state?: string } | null = null;
    let patchBody: { source_host?: string; source_user?: string } | null = null;
    let secretsBody: { ssh_password?: string; ssh_private_key?: string } | null = null;
    let bulkBody: { accounts?: string[]; source_host?: string } | null = null;

    // Most-specific routes first — Playwright matches in registration
    // order, so the catch-all list handler stays last.
    await page.route(`**/api/v1/admin/migrations/${DRAFT_ID}/discover-accounts`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          accounts: [
            { id: "1", login: "alice", domain: "alice.example.com", bytes_total: 1024 },
            { id: "2", login: "bob",   domain: "bob.example.com",   bytes_total: 2048 },
            { id: "3", login: "carol", domain: "carol.example.com", bytes_total: 4096 },
          ],
          total: 3,
        }),
      });
    });

    await page.route(`**/api/v1/admin/migrations/${DRAFT_ID}/secrets`, async (route) => {
      secretsBody = route.request().postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ok: true }),
      });
    });

    await page.route(`**/api/v1/admin/migrations/${DRAFT_ID}`, async (route) => {
      if (route.request().method() === "PATCH") {
        patchBody = route.request().postDataJSON();
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            id: DRAFT_ID,
            source_host: patchBody!.source_host,
            source_user: patchBody!.source_user,
            source_kind: "whm_pkgacct",
            state: "draft",
          }),
        });
        return;
      }
      await route.fallback();
    });

    await page.route("**/api/v1/admin/migrations/bulk", async (route) => {
      bulkBody = route.request().postDataJSON();
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          batch_id: "01J0BATCH0000000000000000",
          jobs: bulkBody!.accounts!.map((u) => ({ id: `j_${u}`, source_user: u })),
        }),
      });
    });

    // Catch-all for /admin/migrations: handle POST(create) and GET(list).
    await page.route("**/api/v1/admin/migrations*", async (route) => {
      const req = route.request();
      const m = req.method();
      if (m === "POST") {
        draftBody = req.postDataJSON();
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          body: JSON.stringify({
            id: DRAFT_ID,
            source_kind: draftBody!.source_kind,
            source_host: "__draft_xxx",
            source_user: "__draft_yyy",
            state: "draft",
          }),
        });
        return;
      }
      if (m === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ data: [], total: 0, page: 1, page_size: 50 }),
        });
        return;
      }
      await route.fallback();
    });

    await signIn(page, admin);
    await page.goto("/jabali-admin/migrations");

    // Open the wizard.
    await page.getByRole("button", { name: "Wizard" }).click();

    // Step 1: WHM is the default radio — click Next.
    await expect(page.getByText(/pick the source panel type/i)).toBeVisible();
    await page.getByRole("button", { name: /next: connection/i }).click();
    await expect.poll(() => draftBody).not.toBeNull();
    expect(draftBody!.source_kind).toBe("whm_pkgacct");
    expect(draftBody!.state).toBe("draft");

    // Step 2: host + admin user + password.
    await page.getByPlaceholder("src.example.com").fill("whm.example.com");
    await page.getByPlaceholder("root").fill("rootadmin");
    await page.locator('input[type="password"]').first().fill("hunter2");
    await page.getByRole("button", { name: /next: discover accounts/i }).click();

    // Step 3 shows once patch+secrets+discover all land — assert UI
    // visibility first, THEN inspect the captured request bodies.
    await expect(page.getByText(/found 3 accounts/i)).toBeVisible({ timeout: 10_000 });
    expect(patchBody?.source_host).toBe("whm.example.com");
    expect(secretsBody?.ssh_password).toBe("hunter2");
    await page.getByRole("checkbox", { name: /alice/i }).check();
    await page.getByRole("checkbox", { name: /bob/i }).check();
    await page.getByRole("button", { name: /next: review 2 accounts/i }).click();

    // Step 4: review + Create batch.
    await expect(page.getByText(/2 selected/i)).toBeVisible();
    await page.getByRole("button", { name: /create batch/i }).click();

    await expect.poll(() => bulkBody?.accounts).toEqual(["alice", "bob"]);
    expect(bulkBody!.source_host).toBe("whm.example.com");
  });
});
