// The shell's content column must let `position: sticky` work. It clips
// horizontal overflow (so one wide element cannot sideways-scroll the page on
// a phone), and it used to do that with `overflow-x: hidden`. Any overflow
// other than visible makes an element a scroll container, so every sticky
// element inside a page bound to the content column, which never scrolls
// (the window does in the user shell, an inner layout in the admin shell),
// and never stuck: the Server Settings tab bar (GH #688) and the File Manager
// bulk-action bar scrolled away with the page. `overflow-x: clip` clips the
// same way without making a scroll container.
import { admin, mockApi, signIn, test, expect, user } from "./fixtures";
import type { Page } from "@playwright/test";

// A sticky probe at the top of the routed page plus enough height to scroll.
async function addStickyProbe(page: Page): Promise<void> {
  await page.evaluate(() => {
    const main = document.querySelector("main.ant-layout-content");
    if (!main) throw new Error("content column not found");
    const wrap = document.createElement("div");
    wrap.style.height = "4000px";
    const probe = document.createElement("div");
    probe.id = "sticky-probe";
    probe.textContent = "sticky probe";
    probe.style.position = "sticky";
    probe.style.top = "0";
    probe.style.height = "24px";
    wrap.appendChild(probe);
    main.prepend(wrap);
  });
}

test("a sticky element in a user-shell page stays in view while the page scrolls", async ({ page }) => {
  await mockApi(page, { me: user, domains: [] });
  await signIn(page, user);
  await page.goto("/jabali-panel/domains");
  await page.locator("main.ant-layout-content").waitFor();
  await addStickyProbe(page);

  const probe = page.locator("#sticky-probe");
  await page.mouse.move(640, 400);
  await page.mouse.wheel(0, 2000);
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(1000);
  await expect(probe).toBeInViewport();
});

test("a sticky element in an admin-shell page stays in view while the page scrolls", async ({ page }) => {
  await mockApi(page, { me: admin });
  await signIn(page, admin);
  await page.goto("/jabali-admin/users");
  await page.locator("main.ant-layout-content").waitFor();
  await addStickyProbe(page);

  // The admin shell scrolls an inner layout, not the window: scroll whatever
  // is under the pointer and check the probe's first scrollable ancestor moved.
  const probe = page.locator("#sticky-probe");
  await page.mouse.move(640, 400);
  await page.mouse.wheel(0, 2000);
  await expect
    .poll(() =>
      page.evaluate(() => {
        let n = document.getElementById("sticky-probe")?.parentElement ?? null;
        let scrolled = window.scrollY;
        while (n) {
          scrolled = Math.max(scrolled, n.scrollTop);
          n = n.parentElement;
        }
        return scrolled;
      }),
    )
    .toBeGreaterThan(1000);
  await expect(probe).toBeInViewport();
});
