// public/sw-push.js opens a notification's link when it is clicked. The link
// must stay on the panel's own origin; a link anywhere else opens "/".
// The worker file runs here as-is, with a stub `self`.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { runInNewContext } from "node:vm";
import { describe, expect, it, vi } from "vitest";

const SOURCE = readFileSync(join(__dirname, "../../public/sw-push.js"), "utf8");
const ORIGIN = "https://panel.example.test:8443";

type Handler = (event: unknown) => void;

interface FakeWindow {
  focus: () => Promise<undefined>;
  navigate: ReturnType<typeof vi.fn>;
}

async function click(deeplink: unknown, windows: FakeWindow[] = []) {
  const handlers: Record<string, Handler> = {};
  const openWindow = vi.fn(async (_url: string) => undefined);
  const self = {
    location: new URL(`${ORIGIN}/sw-push.js`),
    addEventListener: (type: string, fn: Handler) => {
      handlers[type] = fn;
    },
    clients: { matchAll: async () => windows, openWindow, claim: async () => undefined },
    registration: { showNotification: async () => undefined },
    skipWaiting: async () => undefined,
  };
  runInNewContext(SOURCE, { self, URL, caches: { keys: async () => [], delete: async () => true } });

  let done: Promise<unknown> = Promise.resolve();
  handlers.notificationclick({
    notification: { close: () => undefined, data: { deeplink } },
    waitUntil: (p: Promise<unknown>) => {
      done = p;
    },
  });
  await done;
  return openWindow;
}

const elsewhere = [
  "https://elsewhere.example.test/x",
  "//elsewhere.example.test/x",
  "/\\elsewhere.example.test/x",
  "javascript:alert(1)",
  "data:text/html,x",
  "http://panel.example.test:8443/jabali-admin",
  "https://panel.example.test:9443/jabali-admin",
];

describe("sw-push notificationclick", () => {
  it("opens a panel path as given", async () => {
    const openWindow = await click("/jabali-admin/backups?tab=jobs#last");
    expect(openWindow).toHaveBeenCalledWith("/jabali-admin/backups?tab=jobs#last");
  });

  it("opens a same-origin absolute link as its path", async () => {
    const openWindow = await click(`${ORIGIN}/jabali-admin/updates`);
    expect(openWindow).toHaveBeenCalledWith("/jabali-admin/updates");
  });

  it.each(elsewhere)("opens / instead of %s", async (link) => {
    const openWindow = await click(link);
    expect(openWindow).toHaveBeenCalledWith("/");
  });

  it.each([42, true, { href: "/x" }, ["/x"]])("opens / for a link that is not a string (%s)", async (link) => {
    const openWindow = await click(link);
    expect(openWindow).toHaveBeenCalledWith("/");
  });

  it.each(elsewhere)("moves an open panel window to / instead of %s", async (link) => {
    const win: FakeWindow = { focus: async () => undefined, navigate: vi.fn() };
    const openWindow = await click(link, [win]);
    expect(win.navigate).toHaveBeenCalledWith("/");
    expect(openWindow).not.toHaveBeenCalled();
  });
});
