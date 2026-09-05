// GH #1540: the admin "Add DNS Zone" drawer creates a web-off, DNS-managed
// domain on behalf of a tenant. It carries the same simple form as the tenant
// flow (Domain Name / IP / Template) plus an Owner picker.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mutate = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({
    data: { public_ipv4: "192.0.2.1", public_ipv6: "2001:db8::1" },
  }),
}));
vi.mock("../../../hooks/useQueries", () => ({
  useCreateMutation: () => ({ mutateAsync: mutate, isPending: false }),
}));
vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn().mockResolvedValue({
      data: { data: [{ id: "u1", email: "alice@example.com", username: "alice" }] },
    }),
  },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
    modal: { success: vi.fn() },
  },
}));

import { AdminDNSZoneDrawer } from "./AdminDNSZoneDrawer";

function renderDrawer() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AdminDNSZoneDrawer open onClose={() => {}} />
    </QueryClientProvider>,
  );
}

const add = () => fireEvent.click(screen.getByRole("button", { name: "Add" }));

beforeEach(() => {
  mutate.mockReset();
  mutate.mockResolvedValue({ id: "d1" });
});

describe("AdminDNSZoneDrawer (GH #1540)", () => {
  it("requires an owner", async () => {
    renderDrawer();
    fireEvent.change(await screen.findByPlaceholderText("e.g., example.com"), {
      target: { value: "zone.example.com" },
    });
    add();
    await screen.findByText("Owner is required");
    expect(mutate).not.toHaveBeenCalled();
  });

  it("sends a web-off DNS-only payload with the owner, prefilled IP and default template", async () => {
    renderDrawer();
    // The apex IP is prefilled with the panel's public IPv4.
    const ip = (await screen.findByPlaceholderText("e.g., 203.0.113.10")) as HTMLInputElement;
    expect(ip.value).toBe("192.0.2.1");
    // Pick the owner (first combobox; Template is the second).
    fireEvent.mouseDown(screen.getAllByRole("combobox")[0]);
    fireEvent.click(await screen.findByText("alice@example.com (alice)"));
    fireEvent.change(screen.getByPlaceholderText("e.g., example.com"), {
      target: { value: "zone.example.com" },
    });
    add();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.user_id).toBe("u1");
    expect(body.name).toBe("zone.example.com");
    expect(body.web_enabled).toBe(false);
    expect(body.manage_dns).toBe(true);
    expect(body.ssl_mode).toBe("none");
    expect(body.ip_address).toBe("192.0.2.1");
    expect(body.ip6_address).toBe("2001:db8::1");
    expect(body.mail_provider).toBe("none");
  });
});
