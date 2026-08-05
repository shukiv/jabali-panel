// JAB-221: the dashboard must say when the box is behind, because the
// commits-behind count lived only inside the Updates Center and two production
// incidents came from hosts silently running builds that predated an
// already-merged fix.
//
// The cases that matter are the SILENT ones. A banner that shows up while
// loading, or because a query failed, gets dismissed by reflex — and then it is
// not there on the day it counts.
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UpdatesPendingBanner } from "./UpdatesPendingBanner";

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
        <UpdatesPendingBanner />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const ok = (jabali_behind: number) => ({
  data: { jabali_behind, apt_total: 0, apt_security: 0 },
  isLoading: false,
  isError: false,
});

describe("UpdatesPendingBanner (JAB-221)", () => {
  beforeEach(() => {
    mockState.mockReset();
    mockAuto.mockReset();
    mockAuto.mockReturnValue({ data: { jabali_enabled: false, jabali_time: "04:30" } });
  });

  it("says nothing when the box is up to date", () => {
    mockState.mockReturnValue(ok(0));
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("says nothing while the check is still loading", () => {
    mockState.mockReturnValue({ data: undefined, isLoading: true, isError: false });
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("says nothing when the state query failed", () => {
    // A failed query is not evidence of being behind. Warning on it would put a
    // permanent banner on every dashboard where the endpoint is unreachable.
    mockState.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    const { container } = renderBanner();
    expect(container).toBeEmptyDOMElement();
  });

  it("warns with the count, and explains that an old binary undoes merged fixes", () => {
    mockState.mockReturnValue(ok(7));
    renderBanner();

    expect(screen.getByText(/7 commits behind/i)).toBeInTheDocument();
    // The count alone reads as cosmetic. The reason it is not — embedded
    // templates rewriting a fixed config back to the broken version — is the
    // part an operator cannot infer, so it must be on screen.
    expect(screen.getByText(/gets undone on the next render/i)).toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/jabali-admin/updates");
  });

  it("uses singular wording for a single commit", () => {
    mockState.mockReturnValue(ok(1));
    renderBanner();
    expect(screen.getByText(/1 commit behind/i)).toBeInTheDocument();
    expect(screen.queryByText(/commits behind/i)).not.toBeInTheDocument();
  });

  it("drops to informational once auto-update is on, and names the time", () => {
    // Same drift, different advice: it converges tonight, so this must not read
    // as an unattended problem or it becomes noise on a healthy fleet.
    mockState.mockReturnValue(ok(4));
    mockAuto.mockReturnValue({ data: { jabali_enabled: true, jabali_time: "04:30" } });
    renderBanner();

    expect(screen.getByText(/auto-update is on/i)).toBeInTheDocument();
    expect(screen.getByText(/04:30/)).toBeInTheDocument();
    expect(screen.queryByText(/gets undone on the next render/i)).not.toBeInTheDocument();
  });
});
