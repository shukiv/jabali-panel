// GH #2164: GET /me carries the panel clock, and the whoami fetch hands it to
// the shared formatter before any protected page renders.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { apiClient } from "../apiClient";
import { getPanelClock, setPanelClock, shortDateTime } from "../utils/datetime";
import { AuthProvider, useAuth } from "./AuthContext";

function Who() {
  const { user, isLoading } = useAuth();
  if (isLoading || !user) return null;
  return <span>{`${user.email} ${shortDateTime("2026-10-16T00:00:00Z")}`}</span>;
}

afterEach(() => {
  vi.restoreAllMocks();
  setPanelClock("UTC", undefined);
});

describe("AuthProvider panel clock", () => {
  it("adopts the timezone and date format from /me", async () => {
    vi.spyOn(apiClient, "get").mockResolvedValue({
      data: {
        id: "01K",
        email: "t@example.com",
        is_admin: false,
        timezone: "Asia/Tokyo",
        date_format: "YYYY-MM-DD HH:mm",
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <AuthProvider>
          <Who />
        </AuthProvider>
      </QueryClientProvider>,
    );
    // The first render of a signed-in page already reads server time.
    expect(await screen.findByText("t@example.com 2026-10-16 09:00")).toBeInTheDocument();
    expect(getPanelClock().timezone).toBe("Asia/Tokyo");
    expect(getPanelClock().preset.dateTime).toBe("YYYY-MM-DD HH:mm");
  });
});
