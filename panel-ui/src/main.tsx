import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
import "antd/dist/reset.css";
import "./global.css";

import App from "./App";
import { registerServiceWorker } from "./lib/registerServiceWorker";

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
