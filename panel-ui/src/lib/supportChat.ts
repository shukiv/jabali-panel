// supportChat — the "Ask Jabali" AI support chat: a Chatwoot widget served from
// support.jabali-panel.com, opted into per panel under Server Settings ->
// General -> Support chat (support_chat_admin / support_chat_clients).
//
// Loading never competes with the panel's own page load: nothing is fetched
// until the window `load` event has fired and the browser reports idle time
// (requestIdleCallback, with a timeout so a busy page still gets the chat).
// Then the one hosted script tag is added async.
//
// That script (ask_jabali.js) carries the whole widget: the Chatwoot settings
// and inbox, the SDK, the bot-reply sound, the Jabali icon on the launcher,
// and the light/dark theme, which follows the data-theme attribute that
// ThemeModeContext puts on <html>. The chat's operators edit it on
// support.jabali-panel.com, so the panel only adds the tag and shows or hides
// the widget.
//
// The panel's CSP allows the chat's origin only while the chat is on in one of
// the panels (agent nginx.panel_csp.apply), and a CSP applies to the document
// it came with: right after an admin turns the chat on, the widget appears on
// the next page load.

export const SUPPORT_CHAT_BASE_URL = "https://support.jabali-panel.com";
const SCRIPT_ID = "jabali-support-chat";
const IDLE_TIMEOUT_MS = 5000;

interface ChatwootAPI {
  toggle?(state?: "open" | "close"): void;
  toggleBubbleVisibility?(visibility: "show" | "hide"): void;
}

declare global {
  interface Window {
    chatwootSettings?: Record<string, unknown>;
    chatwootSDK?: { run(opts: { websiteToken: string; baseUrl: string }): void };
    $chatwoot?: ChatwootAPI;
    __askJabaliLoaded?: boolean;
  }
}

// idle: nothing done. scheduled: waiting for load + idle. injected: the chat
// script is in the page (it stays for the page's life; turning the chat off
// hides the widget instead).
let state: "idle" | "scheduled" | "injected" = "idle";
// Whether the current layout wants the chat. A layout can turn it off while
// the load is still scheduled or before the widget reports ready.
let wanted = false;

function whenPageIdle(cb: () => void) {
  const idle = () => {
    if (typeof window.requestIdleCallback === "function") {
      window.requestIdleCallback(() => cb(), { timeout: IDLE_TIMEOUT_MS });
    } else {
      window.setTimeout(cb, 1);
    }
  };
  if (document.readyState === "complete") idle();
  else window.addEventListener("load", idle, { once: true });
}

function setVisible(visible: boolean) {
  const cw = window.$chatwoot;
  if (!cw) return;
  if (!visible) cw.toggle?.("close");
  cw.toggleBubbleVisibility?.(visible ? "show" : "hide");
}

// The widget came up after a layout turned the chat off: hide it.
function onReady() {
  if (!wanted) setVisible(false);
}

function inject() {
  state = "injected";
  window.addEventListener("chatwoot:ready", onReady);
  const g = document.createElement("script");
  g.id = SCRIPT_ID;
  g.src = `${SUPPORT_CHAT_BASE_URL}/ask_jabali.js`;
  g.async = true;
  g.onerror = () => {
    // Unreachable, or blocked by a CSP that predates turning the chat on.
    // Let a later enable (a fresh page) try again.
    g.remove();
    window.removeEventListener("chatwoot:ready", onReady);
    state = "idle";
  };
  document.head.appendChild(g);
}

/** Show the chat: load it once the page is idle, or show it again. */
export function enableSupportChat() {
  wanted = true;
  if (state === "injected") {
    setVisible(true);
    return;
  }
  if (state === "scheduled") return;
  state = "scheduled";
  whenPageIdle(() => {
    if (!wanted) {
      state = "idle";
      return;
    }
    inject();
  });
}

/** Hide the chat, or cancel a load that hasn't started. */
export function disableSupportChat() {
  wanted = false;
  if (state === "injected") setVisible(false);
}

/** Test-only: forget every state so each test starts from a fresh page. */
export function resetSupportChatForTests() {
  state = "idle";
  wanted = false;
  window.removeEventListener("chatwoot:ready", onReady);
  document.getElementById(SCRIPT_ID)?.remove();
  delete window.chatwootSettings;
  delete window.chatwootSDK;
  delete window.$chatwoot;
  delete window.__askJabaliLoaded;
}
