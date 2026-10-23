// GH #2244: on the API docs (Personal API Tokens → Automation API, and
// /api-docs), picking a section in the left-hand list did nothing in the
// admin panel. The list was an antd Anchor, which scrolls the window; the
// admin shell scrolls its own content column, so the window never moved.
// The page now shows one section at a time, picked from the list, so it
// works the same in both shells. Real layout, so Chromium, not happy-dom.
import { admin, mockApi, signIn, test, expect, user } from "./fixtures";
import type { MockUser } from "./fixtures";
import type { Page } from "@playwright/test";

// Section 1 is long, so the page scrolls well before the later sections.
function spec() {
  const tags = Array.from({ length: 30 }, (_, i) => ({ name: `Section ${i + 1}` }));
  const paths: Record<string, unknown> = {};
  tags.forEach((t, i) => {
    const n = i === 0 ? 60 : 6;
    for (let j = 0; j < n; j++) {
      paths[`/section-${i + 1}/item-${j}`] = { get: { tags: [t.name], summary: `Item ${j} of ${t.name}` } };
    }
  });
  return { info: { title: "Jabali API", version: "1.0" }, tags, paths };
}

async function open(page: Page, who: MockUser, url: string) {
  await mockApi(page, { me: who });
  await page.route("**/api/v1/_meta/openapi.json", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(spec()) }),
  );
  await signIn(page, who);
  await page.waitForURL(who.is_admin ? /\/jabali-admin/ : /\/jabali-panel/);
  await page.goto(url);
  await expect(page.getByText("/section-1/item-0", { exact: true })).toBeVisible();
}

// The section's entry in the left-hand list (the first element with its name).
const navItem = (page: Page, name: string) => page.getByText(name, { exact: true }).first();

for (const [shell, who, url] of [
  ["admin", admin, "/jabali-admin/api-docs"],
  ["tenant", user, "/jabali-panel/api-docs"],
] as const) {
  test(`${shell}: picking a section in the list shows its endpoints (GH #2244)`, async ({ page }) => {
    await open(page, who, url);
    await navItem(page, "Section 25").click();
    await expect(page.getByRole("heading", { name: /^Section 25\b/ })).toBeInViewport();
    await expect(page.getByText("/section-25/item-0", { exact: true })).toBeInViewport();
  });

  test(`${shell}: picking a section after scrolling down brings it into view (GH #2244)`, async ({ page }) => {
    await open(page, who, url);
    // Scroll whichever element scrolls this shell: the window (tenant) or
    // the content column (admin).
    await page.getByText("/section-1/item-30", { exact: true }).scrollIntoViewIfNeeded();
    await page.mouse.wheel(0, 2000);
    await navItem(page, "Section 7").click();
    // The section's title too, not only its first rows: Section 7 is short,
    // so the page cannot keep the scroll it had in Section 1.
    await expect(page.getByRole("heading", { name: /^Section 7\b/ })).toBeInViewport();
    await expect(page.getByText("/section-7/item-0", { exact: true })).toBeInViewport();
  });
}

test("admin: a section title just above the top of the content column is brought back (GH #2244)", async ({ page }) => {
  await open(page, admin, "/jabali-admin/api-docs");
  // Scroll the admin content column until the section's top is 20px above
  // the column's top edge: still inside the window, but cut off by the
  // column, so checking against the window alone would miss it.
  await page.evaluate(() => {
    const section = document.querySelector("nav[aria-label='API sections']")!.nextElementSibling as HTMLElement;
    let col = section.parentElement;
    while (col && !(getComputedStyle(col).overflowY === "auto" && col.scrollHeight > col.clientHeight)) {
      col = col.parentElement;
    }
    col!.scrollTop += section.getBoundingClientRect().top - col!.getBoundingClientRect().top + 20;
  });
  await navItem(page, "Section 7").click();
  await expect(page.getByRole("heading", { name: /^Section 7\b/ })).toBeInViewport({ ratio: 1 });
});
