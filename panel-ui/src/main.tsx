import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
import "antd/dist/reset.css";
import "./global.css";

import "./i18n";
import { message, notification } from "antd";
import App from "./App";
import { registerServiceWorker } from "./lib/registerServiceWorker";

// GH #970: give the STATIC message/notification methods (still used across the
// app) the same house style as the App-scoped config in App.tsx, so every toast
// shares one placement + duration regardless of which mechanism a page uses.
// message has no placement (always top-centre); notification is pulled to `top`
// to sit with it. Theme/RTL awareness for static methods still needs the
// lib/feedback bridge — see that file — but position is unified here.
message.config({ top: 24, duration: 3, maxCount: 3 });
notification.config({ placement: "top", top: 24, duration: 4.5, maxCount: 3 });

// A dynamic import (lazy route chunk) failing almost always means the tab was
// open across a panel deploy: the shell in memory references old asset hashes
// that no longer exist on the server (now a 404). Reload once to pull the fresh
// index.html + hashes instead of leaving the user on a blank screen. Guarded so
// a genuinely broken chunk can't loop-reload; the flag is cleared on any
// successful load below.
window.addEventListener("vite:preloadError", () => {
  // Reload at most once per 10s: a stale chunk recovers on the first reload,
  // while a genuinely-missing asset (persistent 404) can't spin a reload loop.
  const last = Number(sessionStorage.getItem("jabali-chunk-reload-at") || 0);
  if (Date.now() - last > 10_000) {
    sessionStorage.setItem("jabali-chunk-reload-at", String(Date.now()));
    window.location.reload();
  }
});

const rootEl = document.getElementById("root");
if (!rootEl) {
  // This would mean index.html was modified incorrectly. Fail loud so
  // nobody ships a page that renders nothing.
  throw new Error("root element missing; check index.html");
}

createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

// #434: register the app service worker so the panel is an installable PWA.
registerServiceWorker();
