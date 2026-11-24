// LoginAllowlistCard.test.tsx — "Never ban login IPs": lists the IPs the agent
// keeps off CrowdSec's bans and removes one after a confirm. Only apiClient is
// mocked.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { LoginAllowlistCard } from "./AdminSecurityCrowdsec";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <LoginAllowlistCard />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocked.get.mockReset();
  mocked.patch.mockReset();
  mocked.delete.mockReset();
  mocked.get.mockImplementation((url: string) => {
    if (url === "/admin/settings") {
      return Promise.resolve({ data: { crowdsec_login_allowlist_enabled: true, crowdsec_login_allowlist_ttl_hours: 168 } });
    }
    if (url === "/admin/security/crowdsec/login-ips") {
      return Promise.resolve({
        data: { items: [{ value: "203.0.113.7", reason: "ssh login root", expires_at: "2026-12-01T21:08:42Z" }] },
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  mocked.delete.mockResolvedValue({ data: {} });
});

describe("LoginAllowlistCard", () => {
  it("lists the exempt login IPs and says the WAF still applies", async () => {
    renderCard();
    expect(await screen.findByText("203.0.113.7")).toBeTruthy();
    expect(screen.getByText("ssh login root")).toBeTruthy();
    expect(screen.getByText("Never ban login IPs (server-wide)")).toBeTruthy();
    expect(screen.getByText(/the WAF still inspects its requests/)).toBeTruthy();
  });

  it("removes an IP after the confirm", async () => {
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: "Remove 203.0.113.7" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(mocked.delete).toHaveBeenCalledWith("/admin/security/crowdsec/login-ips", { params: { value: "203.0.113.7" } }),
    );
  });
});
