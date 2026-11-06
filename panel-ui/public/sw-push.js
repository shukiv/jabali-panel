// sw-push.js — service worker for M14 Web Push delivery ONLY.
//
// Registered at /sw-push.js (scope = site root) from the early PWA
// registration (registerServiceWorker, every load) and the push-subscribe flow
// (idempotent on the same URL).
//
// HISTORY / why this has NO fetch handler (GH blank-screen saga): earlier
// versions cached the app shell + intercepted /assets/*.js with the fetch
// event to add offline support. That repeatedly bricked the panel after a
// deploy — a stale/broken SW intercepting the new build's hashed assets returns
// an error, the SPA bundle never loads, the page goes blank, and because the
// page is blank the fix-code never runs to replace the SW (self-lockout). The
// offline shell was never worth that risk for an online-only admin panel. So:
// this SW does push notifications and NOTHING to network traffic. Assets and
// navigations always hit the network directly — a broken SW can no longer blank
// the panel. `activate` also purges every legacy jabali-shell-* cache so old
// clients recover the moment this version activates.

self.addEventListener("install", (event) => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Purge ALL legacy app-shell caches from the old caching SW.
      const keys = await caches.keys();
      await Promise.all(
        keys.filter((k) => k.startsWith("jabali-shell-")).map((k) => caches.delete(k)),
      );
      await self.clients.claim();
    })(),
  );
});

// NO fetch handler: never intercept navigations or assets.

self.addEventListener("push", (event) => {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch (_err) {
    payload = {};
  }
  const title = payload.title || "Jabali Panel";
  const options = {
    body: payload.body || "",
    icon: "/favicon.svg",
    badge: "/favicon.svg",
    data: {
      deeplink: payload.deeplink || "/jabali-admin/notifications/channels",
      severity: payload.severity || "info",
    },
  };
  event.waitUntil(
    Promise.all([
      self.registration.showNotification(title, options),
      self.clients
        .matchAll({ type: "window", includeUncontrolled: true })
        .then((list) => {
          for (const c of list) {
            c.postMessage({ type: "jabali/notification", payload });
          }
        }),
    ]),
  );
});

// Notification links are panel paths. Resolve the link against this origin
// and open "/" instead when it points anywhere else ("https://other",
// "//other", "/\other", "javascript:").
function panelTarget(link) {
  if (typeof link !== "string") {
    return "/";
  }
  try {
    const url = new URL(link, self.location.origin);
    if (url.origin === self.location.origin) {
      return url.pathname + url.search + url.hash;
    }
  } catch (_err) {
    // Not a URL at all: fall through.
  }
  return "/";
}

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = panelTarget((event.notification.data && event.notification.data.deeplink) || "/");
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      for (const client of list) {
        if ("focus" in client) {
          client.navigate(target);
          return client.focus();
        }
      }
      if (self.clients.openWindow) {
        return self.clients.openWindow(target);
      }
      return undefined;
    }),
  );
});
