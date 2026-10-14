// supportChat.test.ts — the "Ask Jabali" support chat loads only after the
// page has loaded and gone idle, once, from support.jabali-panel.com; a layout
// that turns it off hides it; the bot-reply ding and the Jabali icon follow
// the snippet the chat's operators supplied.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  SUPPORT_CHAT_BASE_URL,
  disableSupportChat,
  enableSupportChat,
  resetSupportChatForTests,
} from "./supportChat";

const sdkScript = () => document.getElementById("jabali-support-chat-sdk") as HTMLScriptElement | null;

let idleCallbacks: Array<() => void>;
let readyState: DocumentReadyState;

// happy-dom never fetches scripts; by default it fires `error` on one, which
// the loader treats as an unreachable chat. Have it fire `load` instead, so the
// tag stays and the tests drive onload/onerror themselves.
type HappyDOMSettings = { handleDisabledFileLoadingAsSuccess: boolean };
const happySettings = (window as unknown as { happyDOM: { settings: HappyDOMSettings } }).happyDOM.settings;
const loadingAsSuccess = happySettings.handleDisabledFileLoadingAsSuccess;

const runIdle = () => {
  const cbs = idleCallbacks;
  idleCallbacks = [];
  cbs.forEach((cb) => cb());
};

beforeEach(() => {
  resetSupportChatForTests();
  happySettings.handleDisabledFileLoadingAsSuccess = true;
  idleCallbacks = [];
  readyState = "loading";
  vi.spyOn(document, "readyState", "get").mockImplementation(() => readyState);
  window.requestIdleCallback = vi.fn((cb: IdleRequestCallback) => {
    idleCallbacks.push(() => cb({ didTimeout: false, timeRemaining: () => 50 }));
    return idleCallbacks.length;
  }) as unknown as typeof window.requestIdleCallback;
});

afterEach(() => {
  happySettings.handleDisabledFileLoadingAsSuccess = loadingAsSuccess;
  vi.restoreAllMocks();
  resetSupportChatForTests();
});

const finishPageLoad = () => {
  readyState = "complete";
  window.dispatchEvent(new Event("load"));
};

describe("support chat loading", () => {
  it("waits for the page load and then for idle time", () => {
    enableSupportChat();
    expect(sdkScript()).toBeNull();
    expect(window.requestIdleCallback).not.toHaveBeenCalled();

    finishPageLoad();
    expect(sdkScript()).toBeNull();
    expect(window.requestIdleCallback).toHaveBeenCalledWith(expect.any(Function), { timeout: 5000 });

    runIdle();
    const s = sdkScript();
    expect(s).not.toBeNull();
    expect(s!.src).toBe(`${SUPPORT_CHAT_BASE_URL}/packs/js/sdk.js`);
    expect(s!.async).toBe(true);
    expect(window.chatwootSettings).toEqual({ position: "right", type: "expanded_bubble", launcherTitle: "Ask Jabali" });
  });

  it("schedules on idle at once when the page has already loaded", () => {
    readyState = "complete";
    enableSupportChat();
    expect(sdkScript()).toBeNull();
    runIdle();
    expect(sdkScript()).not.toBeNull();
  });

  it("adds the script once", () => {
    readyState = "complete";
    enableSupportChat();
    enableSupportChat();
    runIdle();
    enableSupportChat();
    runIdle();
    expect(document.querySelectorAll("#jabali-support-chat-sdk")).toHaveLength(1);
  });

  it("loads nothing when turned off before the page went idle", () => {
    readyState = "complete";
    enableSupportChat();
    disableSupportChat();
    runIdle();
    expect(sdkScript()).toBeNull();
  });

  it("starts the SDK with the inbox's website token when the script loads", () => {
    readyState = "complete";
    enableSupportChat();
    runIdle();
    const run = vi.fn();
    window.chatwootSDK = { run };
    sdkScript()!.onload!(new Event("load"));
    expect(run).toHaveBeenCalledWith({ websiteToken: "2DCyH3egZP4ggnQ4Er2CL7HU", baseUrl: SUPPORT_CHAT_BASE_URL });
  });

  it("tries again on a later enable after the script failed to load", () => {
    readyState = "complete";
    enableSupportChat();
    runIdle();
    sdkScript()!.onerror!(new Event("error"));
    expect(sdkScript()).toBeNull();
    enableSupportChat();
    runIdle();
    expect(sdkScript()).not.toBeNull();
  });
});

describe("support chat once loaded", () => {
  const load = () => {
    readyState = "complete";
    enableSupportChat();
    runIdle();
    window.chatwootSDK = { run: vi.fn() };
    sdkScript()!.onload!(new Event("load"));
  };

  it("dings once per bot reply only", () => {
    load();
    const ding = vi.fn();
    window.playAudioAlert = ding;
    const msg = (detail: object) => window.dispatchEvent(new CustomEvent("chatwoot:on-message", { detail }));

    msg({ message_type: 1, sender_type: "AgentBot" });
    expect(ding).toHaveBeenCalledTimes(1);
    msg({ message_type: "1", sender_type: "AgentBot" });
    expect(ding).toHaveBeenCalledTimes(2);

    msg({ message_type: 0, sender_type: "Contact" }); // the user's own message
    msg({ message_type: 1, sender_type: "User" }); // a human agent
    msg({ message_type: 1, sender_type: "AgentBot", previous_changes: { content: [] } }); // a form update
    expect(ding).toHaveBeenCalledTimes(2);
  });

  it("swaps the chat button's icon for the Jabali mark", () => {
    load();
    const holder = document.createElement("div");
    const icon = document.createElement("img");
    icon.id = "woot-widget-bubble-icon";
    holder.appendChild(icon);
    document.body.appendChild(holder);

    window.dispatchEvent(new Event("chatwoot:ready"));
    const swapped = document.getElementById("woot-widget-bubble-icon")!;
    expect(swapped.tagName.toLowerCase()).toBe("svg");
    expect(swapped.getAttribute("data-jabali")).toBe("1");
    expect(swapped.getAttribute("viewBox")).toBe("0 0 147.26 96.573");
    expect(swapped.querySelector("path")?.getAttribute("d")?.startsWith("m79.472 73.244")).toBe(true);

    // A second ready event leaves it alone.
    window.dispatchEvent(new Event("chatwoot:ready"));
    expect(document.querySelectorAll("#woot-widget-bubble-icon")).toHaveLength(1);
    holder.remove();
  });

  it("hides the widget when turned off and shows it again", () => {
    load();
    const toggleBubbleVisibility = vi.fn();
    const toggle = vi.fn();
    window.$chatwoot = { toggleBubbleVisibility, toggle };

    disableSupportChat();
    expect(toggle).toHaveBeenCalledWith("close");
    expect(toggleBubbleVisibility).toHaveBeenLastCalledWith("hide");

    enableSupportChat();
    expect(toggleBubbleVisibility).toHaveBeenLastCalledWith("show");
    expect(document.querySelectorAll("#jabali-support-chat-sdk")).toHaveLength(1);
  });

  it("hides the widget when it gets ready after it was turned off", () => {
    load();
    disableSupportChat(); // no $chatwoot yet
    const toggleBubbleVisibility = vi.fn();
    window.$chatwoot = { toggleBubbleVisibility, toggle: vi.fn() };
    window.dispatchEvent(new Event("chatwoot:ready"));
    expect(toggleBubbleVisibility).toHaveBeenCalledWith("hide");
  });
});
