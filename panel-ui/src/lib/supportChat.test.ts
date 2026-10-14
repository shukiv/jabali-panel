// supportChat.test.ts — the "Ask Jabali" support chat loads only after the
// page has loaded and gone idle, once, as the one hosted script tag from
// support.jabali-panel.com; a layout that turns it off hides it. The widget's
// settings, inbox token, bot-reply ding and Jabali icon live in that hosted
// script, so the panel must not set up a second copy of them.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  SUPPORT_CHAT_BASE_URL,
  disableSupportChat,
  enableSupportChat,
  resetSupportChatForTests,
} from "./supportChat";

const chatScript = () => document.getElementById("jabali-support-chat") as HTMLScriptElement | null;

let idleCallbacks: Array<() => void>;
let readyState: DocumentReadyState;

// happy-dom never fetches scripts; by default it fires `error` on one, which
// the loader treats as an unreachable chat. Have it fire `load` instead, so the
// tag stays and the tests drive onerror themselves.
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
    expect(chatScript()).toBeNull();
    expect(window.requestIdleCallback).not.toHaveBeenCalled();

    finishPageLoad();
    expect(chatScript()).toBeNull();
    expect(window.requestIdleCallback).toHaveBeenCalledWith(expect.any(Function), { timeout: 5000 });

    runIdle();
    const s = chatScript();
    expect(s).not.toBeNull();
    expect(s!.src).toBe(`${SUPPORT_CHAT_BASE_URL}/ask_jabali.js`);
    expect(s!.async).toBe(true);
  });

  it("leaves the widget's settings, token, ding and icon to the hosted script", () => {
    readyState = "complete";
    enableSupportChat();
    runIdle();
    const run = vi.fn();
    window.chatwootSDK = { run };
    chatScript()!.onload?.(new Event("load"));

    expect(window.chatwootSettings).toBeUndefined();
    expect(run).not.toHaveBeenCalled();
    expect(document.querySelectorAll(`script[src^="${SUPPORT_CHAT_BASE_URL}"]`)).toHaveLength(1);
    expect(document.querySelector(`script[src*="/packs/js/sdk.js"]`)).toBeNull();
  });

  it("schedules on idle at once when the page has already loaded", () => {
    readyState = "complete";
    enableSupportChat();
    expect(chatScript()).toBeNull();
    runIdle();
    expect(chatScript()).not.toBeNull();
  });

  it("adds the script once", () => {
    readyState = "complete";
    enableSupportChat();
    enableSupportChat();
    runIdle();
    enableSupportChat();
    runIdle();
    expect(document.querySelectorAll("#jabali-support-chat")).toHaveLength(1);
  });

  it("loads nothing when turned off before the page went idle", () => {
    readyState = "complete";
    enableSupportChat();
    disableSupportChat();
    runIdle();
    expect(chatScript()).toBeNull();
  });

  it("tries again on a later enable after the script failed to load", () => {
    readyState = "complete";
    enableSupportChat();
    runIdle();
    chatScript()!.onerror!(new Event("error"));
    expect(chatScript()).toBeNull();
    enableSupportChat();
    runIdle();
    expect(chatScript()).not.toBeNull();
  });
});

describe("support chat once loaded", () => {
  const load = () => {
    readyState = "complete";
    enableSupportChat();
    runIdle();
  };

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
    expect(document.querySelectorAll("#jabali-support-chat")).toHaveLength(1);
  });

  it("hides the widget when it gets ready after it was turned off", () => {
    load();
    disableSupportChat(); // no $chatwoot yet
    const toggleBubbleVisibility = vi.fn();
    window.$chatwoot = { toggleBubbleVisibility, toggle: vi.fn() };
    window.dispatchEvent(new Event("chatwoot:ready"));
    expect(toggleBubbleVisibility).toHaveBeenCalledWith("hide");
  });

  it("leaves a ready widget shown while the chat is on", () => {
    load();
    const toggleBubbleVisibility = vi.fn();
    window.$chatwoot = { toggleBubbleVisibility, toggle: vi.fn() };
    window.dispatchEvent(new Event("chatwoot:ready"));
    expect(toggleBubbleVisibility).not.toHaveBeenCalled();
  });
});
