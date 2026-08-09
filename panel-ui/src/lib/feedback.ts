// Centralised, theme-aware toast/dialog access (GH #970).
//
// AntD's static `message` / `notification` / `Modal` methods render OUTSIDE the
// React tree, so they do NOT inherit the ConfigProvider theme or direction and
// default to inconsistent placements (message top-center, notification
// top-right). That is exactly why the panel showed toasts in different
// positions, sizes, and light/dark backgrounds depending on the page.
//
// Import the App-scoped instances from here instead. They are theme- and
// RTL-aware, honour the global config set on <App>, and — unlike the
// `App.useApp()` hook — can be used from NON-component code too (query error
// handlers, api interceptors) where hooks are illegal. This is AntD's own
// documented "global scene" pattern.
//
// The instances are bound once by <FeedbackBridge> (rendered inside <App>).
// Before that binding runs — during the very first paint, or in unit tests that
// don't mount the bridge — the static fallbacks are used so no call ever throws.
import { App, Modal, message as staticMessage, notification as staticNotification } from "antd";
import { useEffect } from "react";

type FeedbackApi = ReturnType<typeof App.useApp>;

// Mutable holder. Import `feedback` and call `feedback.message.success(...)`.
export const feedback = {
  message: staticMessage,
  notification: staticNotification,
  modal: Modal,
} as unknown as FeedbackApi;

// FeedbackBridge swaps the static fallbacks for the App-scoped, theme-aware
// instances. Render exactly once, INSIDE <App>. Renders no DOM.
export function FeedbackBridge(): null {
  const api = App.useApp();
  useEffect(() => {
    feedback.message = api.message;
    feedback.notification = api.notification;
    feedback.modal = api.modal;
  }, [api]);
  return null;
}
