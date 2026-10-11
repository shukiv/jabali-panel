// UserCloudflareTokenCard.test.tsx — GH #2032: a tenant's own Cloudflare
// token for DNS-01. The token is write-only (never shown back), Verify &
// Save reports the zones it checked, and a refusal shows the server's
// reason.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: toast.success, error: toast.error, info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { UserCloudflareTokenCard } from "./UserCloudflareTokenCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPut = apiClient.put as ReturnType<typeof vi.fn>;
const mockDelete = apiClient.delete as ReturnType<typeof vi.fn>;

const refusal = (detail: string) => ({ response: { status: 422, data: { error: "token_cannot_write", detail } } });

describe("UserCloudflareTokenCard", () => {
  beforeEach(() => vi.clearAllMocks());

  it("says when no token is saved", async () => {
    mockGet.mockResolvedValue({ data: { configured: false } });
    render(<UserCloudflareTokenCard />);
    expect(await screen.findByText("Not set")).toBeInTheDocument();
    expect(mockGet).toHaveBeenCalledWith("/me/cloudflare-token");
    expect(screen.queryByRole("button", { name: /Remove token/ })).not.toBeInTheDocument();
  });

  it("verifies and saves the typed token, then names the zones it checked", async () => {
    mockGet.mockResolvedValue({ data: { configured: false } });
    mockPut.mockResolvedValue({
      data: { configured: true, verified_at: "2026-10-11T10:00:00Z", zones: ["example.com", "other.org"], zones_not_checked: 2 },
    });
    render(<UserCloudflareTokenCard />);
    const field = await screen.findByLabelText("Cloudflare API token");
    fireEvent.change(field, { target: { value: "  cf-token-123  " } });
    fireEvent.click(screen.getByRole("button", { name: /Verify & save/ }));

    await waitFor(() => expect(mockPut).toHaveBeenCalledWith("/me/cloudflare-token", { token: "cf-token-123" }));
    expect(await screen.findByText("Token saved")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith(expect.stringContaining("example.com, other.org"));
    expect(screen.getByText(/2 more zones holding your domains were not checked/)).toBeInTheDocument();
    expect((field as HTMLInputElement).value).toBe("");
  });

  it("shows the server's reason when the token is refused, and keeps the field", async () => {
    mockGet.mockResolvedValue({ data: { configured: false } });
    mockPut.mockRejectedValue(refusal("the token cannot add a TXT record in example.com (it needs DNS:Edit there)"));
    render(<UserCloudflareTokenCard />);
    const field = await screen.findByLabelText("Cloudflare API token");
    fireEvent.change(field, { target: { value: "cf-token-123" } });
    fireEvent.click(screen.getByRole("button", { name: /Verify & save/ }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("the token cannot add a TXT record in example.com (it needs DNS:Edit there)"),
    );
    expect(screen.getByText("Not set")).toBeInTheDocument();
    expect((field as HTMLInputElement).value).toBe("cf-token-123");
  });

  it("does not send an empty token", async () => {
    mockGet.mockResolvedValue({ data: { configured: false } });
    render(<UserCloudflareTokenCard />);
    await screen.findByText("Not set");
    expect(screen.getByRole("button", { name: /Verify & save/ })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Cloudflare API token"), { target: { value: "   " } });
    expect(screen.getByRole("button", { name: /Verify & save/ })).toBeDisabled();
    expect(mockPut).not.toHaveBeenCalled();
  });

  it("removes a saved token after confirming", async () => {
    mockGet.mockResolvedValue({ data: { configured: true, verified_at: "2026-10-11T10:00:00Z" } });
    mockDelete.mockResolvedValue({ data: { configured: false } });
    render(<UserCloudflareTokenCard />);
    expect(await screen.findByText("Token saved")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Remove token/ }));
    const confirm = await screen.findByRole("button", { name: "Remove" });
    expect(mockDelete).not.toHaveBeenCalled();
    fireEvent.click(confirm);

    await waitFor(() => expect(mockDelete).toHaveBeenCalledWith("/me/cloudflare-token"));
    expect(await screen.findByText("Not set")).toBeInTheDocument();
  });
});
