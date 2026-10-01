// DomainOwnershipPanel.test — GH #1816 / ADR-0170. A pending domain shows the
// TXT record that proves it and a Verify now button; a verified one shows
// nothing to its tenant, and the admin view offers Revoke except on the
// panel's own domain and docker-app domains.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../../apiClient", () => ({ apiClient: api }));
const msg = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock("../../lib/feedback", () => ({ feedback: { message: msg } }));

import { DomainOwnershipPanel, OwnershipTag } from "./DomainOwnershipPanel";
import { isOwnershipPending } from "./ownership";

const view = {
  status: "pending",
  method: "",
  challenge_name: "_jabali-challenge.site.tld",
  challenge_value: "jabali-verify=abc123",
  last_result: "not_found",
  expires_at: "2026-10-12T12:00:00Z",
};

function renderPanel(domain: Parameters<typeof DomainOwnershipPanel>[0]["domain"], admin = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DomainOwnershipPanel domain={domain} admin={admin} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  api.get.mockReset().mockResolvedValue({ data: view });
  api.post.mockReset().mockResolvedValue({ data: { result: "not_found" } });
  Object.values(msg).forEach((f) => f.mockReset());
});

describe("DomainOwnershipPanel (GH #1816)", () => {
  it("shows a pending domain's record and runs Verify now", async () => {
    renderPanel({ id: "d1", name: "site.tld", ownership_status: "pending" });
    expect(await screen.findByText("_jabali-challenge.site.tld")).toBeInTheDocument();
    expect(screen.getByText("jabali-verify=abc123")).toBeInTheDocument();
    expect(screen.getByText(/No record found yet/)).toBeInTheDocument();
    expect(screen.getByText(/the domain is removed from the account/)).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith("/domains/d1/ownership");

    fireEvent.click(screen.getByRole("button", { name: "Verify now" }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith("/domains/d1/ownership/verify"));
    await waitFor(() => expect(msg.info).toHaveBeenCalled());
    // A tenant never sees the admin approval.
    expect(screen.queryByRole("button", { name: /Approve/ })).not.toBeInTheDocument();
  });

  it("sends the tenant to the Preview URL switch while it is off, and links it once it is on", async () => {
    const { unmount } = renderPanel({ id: "d1", name: "site.tld", ownership_status: "pending", temp_url_enabled: false });
    expect(await screen.findByText(/turn on Preview URL on the Overview tab/)).toBeInTheDocument();
    expect(screen.queryByText(/already works through its preview URL/)).not.toBeInTheDocument();
    unmount();

    renderPanel({
      id: "d1",
      name: "site.tld",
      ownership_status: "pending",
      temp_url_enabled: true,
      temp_url: "https://site-tld.preview.panel.tld",
    });
    expect(await screen.findByText(/already works through its preview URL/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "site-tld.preview.panel.tld" })).toHaveAttribute(
      "href",
      "https://site-tld.preview.panel.tld",
    );
  });

  it("treats an empty or unknown status as pending, like the server", () => {
    expect(isOwnershipPending({ ownership_status: "" })).toBe(true);
    expect(isOwnershipPending({ ownership_status: "weird" })).toBe(true);
    expect(isOwnershipPending({ ownership_status: "verified" })).toBe(false);
    expect(isOwnershipPending({})).toBe(false); // an older server: no field, no banner
  });

  it("shows a verified domain nothing on the tenant side", () => {
    const { container } = renderPanel({ id: "d1", name: "site.tld", ownership_status: "verified" });
    expect(container).toBeEmptyDOMElement();
    expect(api.get).not.toHaveBeenCalled();
  });

  it("offers the admin Revoke on a verified domain", () => {
    renderPanel({ id: "d1", name: "site.tld", ownership_status: "verified", ownership_method: "dns_txt" }, true);
    expect(screen.getByRole("button", { name: "Revoke verification" })).toBeInTheDocument();
    expect(screen.getByText(/by DNS record/)).toBeInTheDocument();
  });

  it.each([
    ["the panel's own domain", { is_panel_primary: true }],
    ["a docker app's domain", { managed_by: "docker_app" }],
  ])("hides Revoke on %s", (_label, extra) => {
    renderPanel({ id: "d1", name: "site.tld", ownership_status: "verified", ...extra }, true);
    expect(screen.queryByRole("button", { name: "Revoke verification" })).not.toBeInTheDocument();
  });

  it("offers the admin Approve on a pending domain", async () => {
    renderPanel({ id: "d1", name: "site.tld", ownership_status: "pending" }, true);
    expect(await screen.findByRole("button", { name: "Approve as administrator" })).toBeInTheDocument();
  });
});

describe("OwnershipTag", () => {
  it("marks only an unverified domain", () => {
    const { rerender } = render(<OwnershipTag status="pending" />);
    expect(screen.getByText("Unverified")).toBeInTheDocument();
    rerender(<OwnershipTag status="verified" />);
    expect(screen.queryByText("Unverified")).not.toBeInTheDocument();
    rerender(<OwnershipTag />);
    expect(screen.queryByText("Unverified")).not.toBeInTheDocument();
  });
});
