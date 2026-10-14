// supportChat — the "Ask Jabali" AI support chat: a Chatwoot widget served from
// support.jabali-panel.com, opted into per panel under Server Settings ->
// General -> Support chat (support_chat_admin / support_chat_clients).
//
// Loading never competes with the panel's own page load: nothing is fetched
// until the window `load` event has fired and the browser reports idle time
// (requestIdleCallback, with a timeout so a busy page still gets the chat).
// The SDK script itself is then added async.
//
// The panel's CSP allows the chat's origin only while the chat is on in one of
// the panels (agent nginx.panel_csp.apply), and a CSP applies to the document
// it came with: right after an admin turns the chat on, the widget appears on
// the next page load.
//
// WEBSITE_TOKEN is the Chatwoot inbox's public website token. It ships in every
// page that embeds the widget and only names the inbox the chat posts to; it
// is not a credential.

export const SUPPORT_CHAT_BASE_URL = "https://support.jabali-panel.com";
const WEBSITE_TOKEN = "2DCyH3egZP4ggnQ4Er2CL7HU";
const SCRIPT_ID = "jabali-support-chat-sdk";
const IDLE_TIMEOUT_MS = 5000;

// The Jabali mark for the chat button, in place of Chatwoot's default icon.
const JABALI_ICON_SVG =
  '<svg xmlns="http://www.w3.org/2000/svg" fill="#fff" width="31" height="20" aria-hidden="true" viewBox="0 0 147.26 96.573" xml:space="preserve"><g transform="translate(-34.391 -47.118)"><g transform="translate(-3.541 -25.332)"><path d="m79.472 73.244c-4.0065 0.95163-8.277 4.0636-9.2604 8.2021h1.8521v0.52917c-5.293 0.24188-10.217 5.6941-15.081 7.7231-1.114 0.46467-4.676 1.2393-5.1043 2.4417-0.38218 1.073 1.0399 2.7743 1.5975 3.5935 2.4662 3.6227 5.8771 5.0144 10.121 3.9687-1.5506 5.3455-7.6205 6.9269-11.24 2.3802-3.2616-4.0969-3.312-10.11-3.312-15.08h0.26458v0.52917h0.26458l-1.0583-2.9104h-0.26458c-0.70254 1.8212-1.2726 3.6092-1.5118 5.5562-0.11276 0.91811 0.18619 2.2623-0.34767 3.0641-1.4776 2.2192-6.4692 0.93492-8.4593 0.37544 0.20517 2.3499 1.304 8.2501 3.0454 9.9049 1.8549 1.7628 6.4774 1.7367 8.8609 1.7367v0.26459l-3.175 0.52916v0.26459c7.2825 4.4949 15.112 11.721 18.785 19.579h0.26458c-0.01183-4.2856-2.0574-7.5834-4.2333-11.112 4.5342 3.0843 9.258 7.8497 14.552 9.525l-1.5875-5.2917h0.26458c0.84444 4.7343 6.8962 10.084 11.377 11.377-1.2956-5.6515-1.6033-9.8179 0.52917-15.346h0.26458c-2.5132 10.202 3.9699 16.869 9.2604 24.606l-14.023-5.5226-9.7896-7.1774c1.5844 2.7814 4.9469 6.0214 5.4293 9.2604 0.40039 2.6886-0.9934 6.31-2.0295 8.7312-2.8259 6.6039-7.6431 11.451-13.189 15.927-2.8919 2.334-6.6625 4.4647-8.9958 7.3563 4.0856 0 8.6528 1.3244 10.054-3.4396h0.26458v2.3812l6.6146-4.986 10.236-8.6639 1.5197-3.2003 5.9717-2.2681 4.4979-2.888 3.175-2.6291 5.5562 0.55836-3.9687-7.6729c3.7228 2.2978 7.9806 5.5165 10.332 9.2604 1.5432 2.4574 1.9452 5.5757 3.5984 7.9375 2.1589 3.0843 5.4308 5.7197 8.1059 8.3544 1.9816 1.9517 4.3458 5.5336 7.0692 6.4219 1.6243 0.52978 3.1232-1.294 2.7832-2.87-0.2334-1.0821-1.2485-1.8971-1.9905-2.6458l2.3812 0.52917c-0.0648-2.6792-1.8771-2.9045-3.7042-4.3366-2.004-1.5708-4.6598-4.1729-5.7406-6.5113-0.71228-1.5411-0.50959-3.8743-0.63288-5.5562-0.29051-3.9631-0.28284-8.2826-1.1249-12.171-0.40549-1.8724-2.2649-3.3993-3.2295-5.0271-1.422-2.3996-2.1467-5.1916-2.5013-7.9375h0.26458c0.93579 2.8711 2.3848 5.5274 4.2064 7.9375 1.0104 1.3367 2.6091 2.627 3.1628 4.2333 0.62267 1.8067 0.73913 4.1838 0.82253 6.0854 0.0342 0.77985-0.23556 2.0716 0.43464 2.6489 1.1663 1.0046 4.7066 0.78333 6.1904 0.79049 5.8026 0.028 12.063-0.25522 17.462-2.6456-0.77197-2.0367-2.2275-3.7778-3.0552-5.8208-1.5912-3.928-1.4637-7.2919-0.91359-11.377h0.26458c0 4.5242 1.0259 8.7475 3.2881 12.7 2.2975 4.0142 6.1699 8.2606 5.6616 13.229-0.88324 8.6337-11.013 11.804-12.918 19.844 3.1087 0 5.5125 0.6513 6.8792-2.6458h0.26458v1.8521c2.9712-1.5415 3.1185-4.5507 5.0836-6.8792 2.8528-3.3803 8.621-6.0668 10.516-10.054 0.93566-1.9692-1.193-4.2544-5e-3 -6.35 1.0544-1.8601 3.0177-3.2355 4.2421-5.0271 3.2956-4.822 5.503-10.318 6.0481-16.14 0.21876-2.3368-0.37598-5.0017-1.2819-7.1438-0.44877-1.0612-1.8038-2.5236-1.6796-3.7042 0.1146-1.0894 1.3436-2.0376 1.9205-2.9104 1.0226-1.5471 1.8056-3.4197 1.8682-5.2917 0.0962-2.8782-1.4065-5.0444-3.432-6.9241-0.85752-0.79578-2.9299-2.2711-2.5035-3.6541 0.34257-1.1112 1.5139-1.3081 2.5074-1.3268 1.6838-0.03161 3.4012 0.16952 5.0271-0.36094 2.3027-0.75128 3.8049-2.2333 6.35-2.2861-1.7653-2.0607-4.7845-2.9032-7.4083-2.9104l1.3229-1.0583v-0.26458c-1.7525 0-3.572-0.1527-5.2917 0.23498-1.0777 0.24295-2.2263 0.49727-3.175 1.0865-5.9935 3.7225-0.17836 9.6913 1.4638 14.024 1.0121 2.6706-0.24648 6.0015-3.0513 7.0055-1.3226 0.47346-2.5954-0.66607-3.7042-1.2419-2.3547-1.2228-4.8913-2.1202-7.4083-2.9406-8.2756-2.6974-17.5-4.146-26.194-4.146v-0.26458l4.4979-1.8521c-3.7368-2.2824-9.3016-1.8521-13.494-1.8521v-0.26458l7.4083-3.175v-0.26458l-14.552-0.79375v-0.26458l5.8208-2.3812v-0.26458c-4.5419-0.36146-9.4692-1.8779-14.023-0.79375l7.6729-4.4979c-3.1942-1.2051-9.5711-1.7051-12.666 0.08023-1.0502 0.60585-1.5876 1.9605-2.4392 2.8055-1.8354 1.8212-4.3154 2.6495-6.8553 2.6705v-0.26458c7.3819-1.0902 9.4631-9.4281 7.1438-15.61-3.6173 0.74663-7.4063 2.0646-10.315 4.4103-1.2534 1.0109-2.7138 4.3127-4.257 4.5994-1.0995 0.20428-1.4526-1.0575-1.6183-1.8662-0.45208-2.2056 0.32443-4.3161 1.109-6.3498m-11.112 15.875c-0.98324 2.0589-2.9464 2.3799-5.0271 2.3812 0.97375-2.0991 2.7849-3.4588 5.0271-2.3812m-6.8792 25.4-0.26458 0.26458 0.26458-0.26458m108.48 17.727c-1.2104 4.4255-4.1608 9.5105-7.9375 12.171v0.52916c3.1505 1.7947 7.6489 3.4777 10.315 5.8698 4.7761 4.2855 6.0439 12.285 5.5601 18.207 4.1709 0 7.4428-0.0408 5.8208-5.0271l1.3229 0.79375c0.87387-3.6809-2.2376-6.5639-3.5443-9.7896-1.5868-3.9171-1.5007-8.3848-2.9653-12.16-0.67252-1.7337-3.2676-2.9299-4.5271-4.2674-1.79-1.9009-2.4102-4.4741-4.0445-6.3263m13.758 31.485-0.26459 0.26459 0.26459-0.26459m-38.629 1.8521-0.26458 0.26458z"/></g></g></svg>';

interface ChatwootSDK {
  run(opts: { websiteToken: string; baseUrl: string }): void;
}

interface ChatwootAPI {
  toggle?(state?: "open" | "close"): void;
  toggleBubbleVisibility?(visibility: "show" | "hide"): void;
}

declare global {
  interface Window {
    chatwootSettings?: Record<string, unknown>;
    chatwootSDK?: ChatwootSDK;
    $chatwoot?: ChatwootAPI;
    playAudioAlert?: () => void;
  }
}

// idle: nothing done. scheduled: waiting for load + idle. injected: the SDK
// script is in the page (it stays for the page's life; turning the chat off
// hides the widget instead).
let state: "idle" | "scheduled" | "injected" = "idle";
// Whether the current layout wants the chat. A layout can turn it off while
// the load is still scheduled or before the SDK reports ready.
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

// Ding once per bot reply (not for the user's own messages, human agents, or
// form updates).
function onMessage(e: Event) {
  const m = ((e as CustomEvent).detail ?? {}) as {
    message_type?: unknown;
    sender_type?: unknown;
    previous_changes?: unknown;
  };
  if (
    Number(m.message_type) === 1 &&
    m.sender_type === "AgentBot" &&
    !m.previous_changes &&
    typeof window.playAudioAlert === "function"
  ) {
    try {
      window.playAudioAlert();
    } catch {
      // A blocked AudioContext must not break the chat.
    }
  }
}

// The Jabali icon instead of the default chat icon on the "Ask Jabali" button.
function onReady() {
  const icon = document.getElementById("woot-widget-bubble-icon");
  if (icon && !icon.getAttribute("data-jabali")) {
    const parsed = new DOMParser().parseFromString(JABALI_ICON_SVG, "image/svg+xml").documentElement;
    const svg = document.importNode(parsed, true) as unknown as SVGElement;
    svg.id = "woot-widget-bubble-icon";
    svg.setAttribute("data-jabali", "1");
    svg.style.setProperty("fill", "#fff", "important");
    svg.style.setProperty("width", "31px", "important");
    icon.parentNode?.replaceChild(svg, icon);
  }
  if (!wanted) setVisible(false);
}

function inject() {
  state = "injected";
  window.chatwootSettings = { position: "right", type: "expanded_bubble", launcherTitle: "Ask Jabali" };
  window.addEventListener("chatwoot:ready", onReady);
  const g = document.createElement("script");
  g.id = SCRIPT_ID;
  g.src = `${SUPPORT_CHAT_BASE_URL}/packs/js/sdk.js`;
  g.async = true;
  g.onload = () => {
    window.chatwootSDK?.run({ websiteToken: WEBSITE_TOKEN, baseUrl: SUPPORT_CHAT_BASE_URL });
    window.addEventListener("chatwoot:on-message", onMessage);
  };
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
  window.removeEventListener("chatwoot:on-message", onMessage);
  document.getElementById(SCRIPT_ID)?.remove();
  delete window.chatwootSettings;
  delete window.chatwootSDK;
  delete window.$chatwoot;
  delete window.playAudioAlert;
}
