// GH #2131: the DNS tab edits the secondary DNS servers (AXFR + NOTIFY) as a
// list, while the API stores them as one comma-separated string.
import { App } from "antd";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));
// The tab also renders these two cards; each loads its own data.
vi.mock("./DNSResolversCard", () => ({ DNSResolversCard: () => null }));
vi.mock("./DNSPermissionsCard", () => ({ DNSPermissionsCard: () => null }));

import { apiClient } from "../../../apiClient";
import { DNSSettingsTab } from "./ServerSettingsPage";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const settings = {
  id: 1,
  ns1_name: "ns1.example.com",
  ns1_ipv4: "203.0.113.5",
  ns2_name: "ns2.example.com",
  ns2_ipv4: "198.51.100.7",
  dns_secondary_ips: "192.0.2.53,2001:db8::53",
  default_dns_ttl: 3600,
};

afterEach(() => {
  cleanup();
  mocked.get.mockReset();
  mocked.patch.mockReset();
});

describe("DNSSettingsTab secondary DNS servers (GH #2131)", () => {
  it("shows the stored list and saves an added server as a comma string", async () => {
    mocked.get.mockResolvedValue({ status: 200, data: settings });
    mocked.patch.mockImplementation((_url: string, body: Record<string, unknown>) =>
      Promise.resolve({ status: 200, data: { ...settings, ...body } }),
    );
    render(
      <App>
        <DNSSettingsTab />
      </App>,
    );

    // The tags input re-renders its items after measuring them, so wait
    // for the settled DOM rather than holding the first node found.
    await waitFor(() => {
      expect(screen.getByText("192.0.2.53")).toBeInTheDocument();
      expect(screen.getByText("2001:db8::53")).toBeInTheDocument();
    });

    // A trailing separator turns the typed text into a tag.
    fireEvent.change(screen.getByLabelText("Secondary DNS servers"), { target: { value: "198.51.100.9," } });
    await waitFor(() => expect(screen.getByText("198.51.100.9")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: /Save DNS Settings/ }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    const [url, body] = mocked.patch.mock.calls[0];
    expect(url).toBe("/admin/settings");
    expect(body.dns_secondary_ips).toBe("192.0.2.53,2001:db8::53,198.51.100.9");
  });

  it("saves an empty list as an empty string", async () => {
    mocked.get.mockResolvedValue({ status: 200, data: { ...settings, dns_secondary_ips: "" } });
    mocked.patch.mockResolvedValue({ status: 200, data: { ...settings, dns_secondary_ips: "" } });
    render(
      <App>
        <DNSSettingsTab />
      </App>,
    );

    await waitFor(() => expect(mocked.get).toHaveBeenCalled());
    const save = await screen.findByRole("button", { name: /Save DNS Settings/ });
    await waitFor(() => expect(save).not.toBeDisabled());
    fireEvent.click(save);
    await waitFor(() => expect(mocked.patch).toHaveBeenCalledTimes(1));
    expect(mocked.patch.mock.calls[0][1].dns_secondary_ips).toBe("");
  });
});
