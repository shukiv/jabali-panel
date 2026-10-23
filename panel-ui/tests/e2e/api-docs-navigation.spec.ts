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

async function open(page: Page, who: MockUser, url: string, firstRow: string | null = "/section-1/item-0") {
  await mockApi(page, { me: who });
  await page.route("**/api/v1/_meta/openapi.json", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(spec()) }),
  );
  // The Personal API Tokens page: an empty token list, and the REST API on.
  await page.route("**/api/v1/me/api-tokens", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [] }) }),
  );
  await page.route("**/api/v1/me/server-capabilities", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "{}" }),
  );
  await signIn(page, who);
  await page.waitForURL(who.is_admin ? /\/jabali-admin/ : /\/jabali-panel/);
  await page.goto(url);
  if (firstRow) await expect(page.getByText(firstRow, { exact: true })).toBeVisible();
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

// The reporter's follow-up: the Personal API Tokens page should read like
// Notifications > Events. The page's heading sits under the breadcrumbs, then
// one card holds everything: the figures above the rail on the left, the
// search above the section on the right.
for (const [shell, who, base] of [
  ["admin", admin, "/jabali-admin"],
  ["tenant", user, "/jabali-panel"],
] as const) {
  test(`${shell}: Personal API Tokens → Automation API is one card laid out like Events (GH #2244)`, async ({ page }) => {
    await open(page, who, `${base}/api-tokens`, null);
    const heading = page.getByRole("heading", { level: 3, name: /Personal API Tokens/ });
    await expect(heading).toBeVisible();
    await page.getByRole("tab", { name: "Automation API" }).click();
    await expect(page.getByText("/section-1/item-0", { exact: true })).toBeVisible();

    const box = async (l: ReturnType<Page["locator"]>) => (await l.boundingBox())!;
    const crumbs = await box(page.locator(".ant-breadcrumb").first());
    const head = await box(heading);
    const cards = page.locator(".ant-card");
    await expect(cards).toHaveCount(1);
    const card = await box(cards.first());
    expect(crumbs.y + crumbs.height).toBeLessThanOrEqual(head.y);
    expect(head.y + head.height).toBeLessThanOrEqual(card.y);

    const nav = page.getByRole("navigation", { name: "API sections" });
    const rail = await box(nav);
    // The search box's outer frame, not the bare <input> inside it.
    const search = await box(
      page.locator(".ant-input-affix-wrapper", { has: page.getByRole("textbox", { name: "Search all endpoints" }) }),
    );
    const stat = await box(page.getByText("endpoints", { exact: true }).locator("xpath=.."));
    const section = await box(nav.locator("xpath=following-sibling::section"));
    for (const b of [rail, search, stat, section]) {
      expect(b.x).toBeGreaterThanOrEqual(card.x);
      expect(b.x + b.width).toBeLessThanOrEqual(card.x + card.width + 0.5);
    }
    // The figures start where the rail starts, above it.
    expect(Math.abs(stat.x - rail.x)).toBeLessThanOrEqual(1);
    expect(stat.y + stat.height).toBeLessThanOrEqual(rail.y);
    // The search ends where the section ends, above it and right of the rail.
    expect(Math.abs(search.x + search.width - (section.x + section.width))).toBeLessThanOrEqual(1);
    expect(search.y + search.height).toBeLessThanOrEqual(section.y);
    expect(search.x).toBeGreaterThan(rail.x + rail.width);
  });

  test(`${shell}: the list of sections stays in view down a long section (GH #2244)`, async ({ page }) => {
    await open(page, who, `${base}/api-tokens#tag-section-1`);
    await page.getByText("/section-1/item-50", { exact: true }).scrollIntoViewIfNeeded();
    const nav = page.getByRole("navigation", { name: "API sections" });
    await expect(nav).toBeInViewport();
    await expect(nav.getByRole("button", { name: /^Section 1\s*60$/ })).toBeInViewport();
  });
}
