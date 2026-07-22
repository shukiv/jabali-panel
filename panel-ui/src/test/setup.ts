// Test setup — runs before every test file via vitest.setupFiles.
//
// Keeps to a minimum:
//   - jest-dom matchers (toBeInTheDocument, etc.)
//   - window.matchMedia polyfill (AntD uses it for responsive bits;
//     happy-dom doesn't ship it)
//   - i18n, so components render English text rather than raw catalog keys
import "@testing-library/jest-dom/vitest";
// Components call useTranslation(); without an initialised instance i18next
// echoes the key back, so queries like getByLabelText(/^Domain$/) miss. main.tsx
// does this for the app — the same import keeps tests on the real i18n path
// instead of a mock that could drift from it.
import "../i18n";

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
