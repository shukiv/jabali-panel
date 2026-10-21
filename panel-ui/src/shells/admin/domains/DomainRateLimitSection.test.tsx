// DomainRateLimitSection.test.tsx — the admin's per-domain request and
// connection limits: shows the stored values and PATCHes both, 0 for blank.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { patch: vi.fn().mockResolvedValue({}) } }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { DomainRateLimitSection } from "./DomainRateLimitSection";

function renderSection(rps: number, conns: number) {
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <DomainRateLimitSection domainId="d1" rateLimitRPS={rps} connectionLimit={conns} />
    </QueryClientProvider>,
  );
}

describe("DomainRateLimitSection", () => {
  it("shows the stored limits", async () => {
    renderSection(20, 0);
    await waitFor(() =>
      expect((screen.getByRole("spinbutton", { name: "Requests per second" }) as HTMLInputElement).value).toBe("20"),
    );
    expect((screen.getByRole("spinbutton", { name: "Open connections" }) as HTMLInputElement).value).toBe("0");
  });

  it("PATCHes both limits, 0 for a cleared field", async () => {
    const patch = vi.mocked(apiClient.patch);
    patch.mockClear();
    renderSection(20, 5);
    const rps = screen.getByRole("spinbutton", { name: "Requests per second" });
    fireEvent.change(rps, { target: { value: "50" } });
    fireEvent.blur(rps);
    const conns = screen.getByRole("spinbutton", { name: "Open connections" });
    fireEvent.change(conns, { target: { value: "" } });
    fireEvent.blur(conns);
    fireEvent.click(screen.getByRole("button", { name: "Save limits" }));
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith("/domains/d1", { rate_limit_rps: 50, connection_limit: 0 }),
    );
  });
});
