// PR #2180: Server Settings → Nginx carries the "Allow proxy_pass to local /
// private addresses" switch. It loads from GET /admin/settings and saves with
// the rest of the card in the PATCH.
import { App } from "antd";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { NginxSettingsCard } from "./NginxSettingsCard";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
};

const settings = {
  nginx_client_max_body_size: "512m",
  nginx_keepalive_timeout: "65s",
  nginx_server_tokens: false,
  nginx_gzip: true,
  nginx_client_body_timeout: "60s",
  nginx_client_header_timeout: "60s",
  nginx_send_timeout: "60s",
  nginx_proxy_connect_timeout: "300s",
  nginx_proxy_read_timeout: "300s",
  nginx_proxy_send_timeout: "300s",
  nginx_worker_processes: "auto",
  nginx_worker_connections: 1024,
  nginx_custom_http: "",
  nginx_cache_max_size_gb: 4,
  nginx_cache_keyzone_mb: 64,
  nginx_cache_inactive_min: 1440,
  nginx_proxy_allow_internal: false,
};

afterEach(() => {
  cleanup();
  mocked.get.mockReset();
  mocked.patch.mockReset();
});

describe("NginxSettingsCard proxy_pass to local / private addresses (PR #2180)", () => {
  it("loads the switch and saves it with the card", async () => {
    mocked.get.mockResolvedValue({ status: 200, data: settings });
    mocked.patch.mockResolvedValue({ status: 200, data: {} });
    render(
      <App>
        <NginxSettingsCard />
      </App>,
    );

    // The label's tooltip icon adds its own name after the text.
    const sw = await screen.findByRole("switch", { name: /^Allow proxy_pass to local \/ private addresses/ });
    expect(sw).not.toBeChecked();
    fireEvent.click(sw);
    fireEvent.click(screen.getByRole("button", { name: "Save nginx settings" }));

    await waitFor(() =>
      expect(mocked.patch).toHaveBeenCalledWith(
        "/admin/settings",
        expect.objectContaining({ nginx_proxy_allow_internal: true }),
      ),
    );
  });
});
