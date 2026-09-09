// GH #1604: useApplyBrandingToTitle used to overwrite document.title with a
// host-less string on mount (brandText === "" while the branding query is
// pending, and again when an unbranded panel resolves to ""), which silently
// defeated #1618's "<host> | Jabali Panel" tab title on every panel. These
// tests pin that the branding hook now composes through buildPageTitle so the
// address-bar host always survives. The first case is RED against the old
// inline `brandText ? \`${brandText} — Panel\` : "Jabali Panel"` write.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn() },
}));

import { apiClient } from "../apiClient";
import { useApplyBrandingToTitle } from "./useBranding";
import { buildPageTitle } from "../lib/pageTitle";

const mockGet = apiClient.get as unknown as ReturnType<typeof vi.fn>;

function makeWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
}

const brandingResponse = (brandText: string) => ({
  data: { panel_brand_text: brandText },
});

describe("useApplyBrandingToTitle (GH #1604 — keep the host)", () => {
  beforeEach(() => {
    mockGet.mockReset();
    document.title = "boot";
  });

  it("keeps the hostname when no custom brand is set", async () => {
    mockGet.mockResolvedValue(brandingResponse(""));
    renderHook(() => useApplyBrandingToTitle(), { wrapper: makeWrapper() });
    const host = window.location.hostname; // jsdom default: "localhost"
    await waitFor(() => {
      expect(document.title).toBe(buildPageTitle(host, ""));
    });
    // Explicit: the host is preserved, not overwritten to the bare product
    // name (the pre-fix behavior this guards against).
    expect(document.title).toBe(`${host} | Jabali Panel`);
  });

  it("composes a custom brand after the host once branding loads", async () => {
    mockGet.mockResolvedValue(brandingResponse("Acme"));
    renderHook(() => useApplyBrandingToTitle(), { wrapper: makeWrapper() });
    const host = window.location.hostname;
    await waitFor(() => {
      expect(document.title).toBe(`${host} | Acme — Panel`);
    });
  });
});
