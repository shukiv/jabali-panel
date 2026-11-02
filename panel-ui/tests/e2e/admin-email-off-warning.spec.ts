// Turning email off while mailboxes are left asks first (follow-up to the
// mail-off MX routing, PR #2350). Real browser, mocked backend: the confirm
// names the mailboxes kept, Cancel leaves the (controlled) Email switch on and
// sends nothing, OK sends DELETE /domains/:id/email.
import { admin, expect, mockApi, signIn, test, type MockDomain } from "./fixtures";

const now = "2026-01-01T00:00:00Z";

const domain: MockDomain = {
  id: "01CKBYK3W2VHYBR9V8FT9X3Z8T",
  user_id: admin.id,
  name: "example.com",
  doc_root: "/home/admin/domains/example.com/public_html",
  is_enabled: true,
  nginx_custom_directives: "",
  created_at: now,
  updated_at: now,
};

test("admin Email switch: confirm names the mailboxes; Cancel keeps email on; OK turns it off", async ({ page }) => {
  await mockApi(page, { me: admin, domains: [domain] });

  // Registered after mockApi, so these win for the email + mailbox calls.
  const deletes: string[] = [];
  await page.route(new RegExp(`/api/v1/domains/${domain.id}/email$`), async (route) => {
    if (route.request().method() === "DELETE") {
      deletes.push(route.request().url());
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        domain_id: domain.id,
        domain_name: domain.name,
        email_enabled: true,
        records: [],
      }),
    });
  });
  await page.route(new RegExp(`/api/v1/domains/${domain.id}/mailboxes(\\?.*)?$`), (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [], total: 2, page: 1, page_size: 1 }),
    }),
  );

  await signIn(page, admin);
  await page.waitForURL(/\/jabali-admin/);
  await page.goto(`/jabali-admin/domains/edit/${domain.id}`);
  await page.getByRole("tab", { name: "Email" }).click();

  // The Email tab's first switch is the Email on/off switch (the Mail
  // provider section above it has none; webmail and DMARC follow it).
  const emailSwitch = page.getByRole("tabpanel").getByRole("switch").first();
  await expect(emailSwitch).toHaveAttribute("aria-checked", "true");

  await emailSwitch.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Turn off email for example.com?");
  await expect(dialog).toContainText("2 mailboxes on example.com are kept");

  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  await expect(emailSwitch).toHaveAttribute("aria-checked", "true");
  expect(deletes).toHaveLength(0);

  await emailSwitch.click();
  await page.getByRole("dialog").getByRole("button", { name: "Turn off email" }).click();
  await expect.poll(() => deletes.length).toBe(1);
});
