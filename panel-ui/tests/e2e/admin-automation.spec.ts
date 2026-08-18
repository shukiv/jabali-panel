// M44: Admin Automation API tokens E2E.
//
// Covers the mint Drawer → list Table → revoke flow on
// /jabali-admin/automation. The one-time-secret reveal Modal is
// asserted alongside; copy-to-clipboard is browser-permission gated
// and cannot be scripted cross-vendor, so we only assert the
// plaintext appears in the modal body.
//
// Required env:
//   E2E_BASE_URL   https://jabali-panel.local
//   E2E_USERNAME   admin@example.com
//   E2E_PASSWORD   ...
//
// Auto-skips when any of the above is missing (dev boxes without a
// real panel API).

import { expect, test, type Page } from "@playwright/test";

const SKIP_REASON =
  !process.env.E2E_USERNAME || !process.env.E2E_PASSWORD
    ? "E2E_USERNAME / E2E_PASSWORD not set"
    : "";

if (SKIP_REASON) {
  test.skip(() => true, `M44 E2E skipped: ${SKIP_REASON}`);
} else {
  test.describe("M44: Automation API tokens", () => {
    test.setTimeout(60_000);

    test.use({
      ignoreHTTPSErrors: true,
      baseURL: process.env.E2E_BASE_URL || "https://jabali-panel.local",
    });

    const username = process.env.E2E_USERNAME || "";
    const password = process.env.E2E_PASSWORD || "";
    const probeName = `e2e-automation-${Date.now()}`;

    async function login(page: Page): Promise<void> {
      await page.goto("/login");
      await page.getByLabel(/email/i).fill(username);
      await page.getByLabel(/password/i).fill(password);
      await page.getByRole("button", { name: /sign in|log in/i }).click();
      await page.waitForURL(/\/jabali-(admin|panel)/);
    }

    test("mint → reveal modal → revoke flow", async ({ page }) => {
      await login(page);
      await page.goto("/jabali-admin/automation");
      await expect(
        page.getByRole("heading", { name: /Automation API Tokens/i }),
      ).toBeVisible();

      // Mint.
      await page.getByRole("button", { name: /Mint Token/i }).click();
      await page.getByLabel(/^Name$/i).fill(probeName);
      // Default 'read:status' scope is preselected — no extra ticks needed.
      await page.getByRole("button", { name: /^Mint$/i }).click();

      // One-time-reveal modal appears with the plaintext secret.
      const modalTitle = page.getByText(`Token "${probeName}" minted`);
      await expect(modalTitle).toBeVisible({ timeout: 10_000 });
      // Plaintext = 64 hex chars; the secret renders in a read-only
      // CopyableInput, so assert on the input's display value without
      // leaking it into test output.
      await expect(page.getByDisplayValue(/^[0-9a-f]{64}$/i)).toBeVisible();

      // Acknowledge + dismiss.
      await page.getByRole("button", { name: /I've saved it/i }).click();

      // Row appears in the list with the expected scopes tag.
      const row = page.getByRole("row", { name: new RegExp(probeName) });
      await expect(row).toBeVisible({ timeout: 10_000 });
      await expect(row.getByText(/active/i)).toBeVisible();
      await expect(row.getByText(/read:status/i)).toBeVisible();

      // Revoke via Popconfirm.
      await row.getByRole("button", { name: /^Revoke$/i }).click();
      await page.getByRole("button", { name: /^Revoke$/i }).last().click();

      // Row stays visible but flips to revoked tag.
      await expect(row.getByText(/revoked/i)).toBeVisible({ timeout: 10_000 });
    });

    test("scope checkboxes — read:* shortcut + per-resource ticks", async ({
      page,
    }) => {
      await login(page);
      await page.goto("/jabali-admin/automation");
      await page.getByRole("button", { name: /Mint Token/i }).click();

      // Drawer opens with the four granular options + the wildcard.
      await expect(page.getByLabel(/read:\* \(everything below\)/i)).toBeVisible();
      await expect(page.getByLabel(/^read:domains$/i)).toBeVisible();
      await expect(page.getByLabel(/^read:users$/i)).toBeVisible();
      await expect(page.getByLabel(/^read:applications$/i)).toBeVisible();
      await expect(page.getByLabel(/^read:status$/i)).toBeVisible();

      // Cancel without minting (cleanup-friendly — this test only
      // asserts the form layout).
      await page.getByRole("button", { name: /^Cancel$/i }).click();
    });
  });
}
