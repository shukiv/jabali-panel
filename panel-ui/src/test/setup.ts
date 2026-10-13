// Test setup — runs before every test file via vitest.setupFiles.
//
// Keeps to a minimum:
//   - jest-dom matchers (toBeInTheDocument, etc.)
//   - window.matchMedia polyfill (AntD uses it for responsive bits;
//     happy-dom doesn't ship it)
//   - i18n, so components render English text rather than raw catalog keys
//   - toasts bound to a per-test <App>, so RTL's cleanup removes them
import "@testing-library/jest-dom/vitest";
// Components call useTranslation(); without an initialised instance i18next
// echoes the key back, so queries like getByLabelText(/^Domain$/) miss. main.tsx
// does this for the app — the same import keeps tests on the real i18n path
// instead of a mock that could drift from it.
import "../i18n";

import { render } from "@testing-library/react";
import { App } from "antd";
import { createElement } from "react";
import { beforeEach } from "vitest";

import { FeedbackBridge } from "../lib/feedback";

if (typeof window !== "undefined" && !window.matchMedia) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  });
}

// lib/feedback falls back to AntD's static message/notification/Modal until a
// <FeedbackBridge> binds it. The static ones render into a React root outside
// the test tree that nothing unmounts, so a toast fired near the end of a file
// rendered after the environment was torn down ("window is not defined", one
// unhandled error, red CI). Bind the bridge before every test, as App.tsx does
// for the app; RTL's cleanup unmounts it, and its toasts, with the test.
beforeEach(() => {
  render(createElement(App, null, createElement(FeedbackBridge)));
});
