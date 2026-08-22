// JAB-353: the dashboard must persistently surface a bad OS security-patch
// posture — auto-updates off, a pending security count, or a reboot needed —
// separately from the Jabali-panel-behind banner. As with that banner, the
// cases that matter are the SILENT ones: it must not appear while loading or on
// a query error (a permanent false banner trains operators to ignore it).
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { OsSecurityUpdatesBanner } from "./UpdatesPendingBanner";

const mockState = vi.fn();
const mockAuto = vi.fn();

vi.mock("../../../hooks/useSystemUpdates", () => ({
  useUpdateState: () => mockState(),
  useAutoupdateConfig: () => mockAuto(),
}));

const renderBanner = () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <OsSecurityUpdatesBanner />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const stateOK = (over: Record<string, unknown> = {}) => ({
  data: { jabali_behind: 0, apt_total: 0, apt_security: 0, apt_reboot_required: false, ...over },
  isLoading: false,
  isError: false,
});

describe("OsSecurityUpdatesBanner (JAB-353)", () => {
  beforeEach(() => {
    mockState.mockReset();
    mockAuto.mockReset();
    mockAuto.mockReturnValue({ data: { apt_enabled: true }, isLoading: false });
  });

  it("is silent when auto-updates are on, nothing pending, no reboot", () => {
    mockState.mockReturnValue(stateOK());
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("is silent while loading and on query error", () => {
    mockState.mockReturnValue({ data: undefined, isLoading: true, isError: false });
    expect(renderBanner().container).toBeEmptyDOMElement();
    mockState.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    expect(renderBanner().container).toBeEmptyDOMElement();
  });

  it("shows a hard warning when OS security auto-updates are OFF", () => {
    mockState.mockReturnValue(stateOK({ apt_security: 5 }));
    mockAuto.mockReturnValue({ data: { apt_enabled: false }, isLoading: false });
    renderBanner();
    expect(screen.getByText(/OS security auto-updates are OFF/i)).toBeInTheDocument();
    expect(screen.getByText(/5 pending/i)).toBeInTheDocument();
    expect(screen.getByText(/never been applied/i)).toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/jabali-admin/updates");
  });

  it("warns when a reboot is required even though updates are enabled", () => {
    mockState.mockReturnValue(stateOK({ apt_reboot_required: true, apt_last_applied_at: "2026-08-20T03:30:00Z" }));
    renderBanner();
    expect(screen.getByText(/Reboot required/i)).toBeInTheDocument();
  });

  it("warns when security updates are pending while enabled", () => {
    mockState.mockReturnValue(stateOK({ apt_security: 3 }));
    renderBanner();
    expect(screen.getByText(/3 OS security updates pending/i)).toBeInTheDocument();
  });
});
