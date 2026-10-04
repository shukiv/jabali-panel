// DomainSettingsButton.frontcontroller.test.tsx — GH #1999 front controller.
//
// The tenant Rule Builder offers a "Front Controller" kind that sets the
// try_files fallback of the panel's own `location /`. A domain has at most one:
// the picker stops offering it once one exists, and an imported one replaces
// the existing rule instead of adding a second (which Save would refuse).
//
// Only apiClient is mocked; the real AntD widgets + in-file RuleBuilder render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  TenantNginxRulesPanel,
  type DomainSettingsTarget,
} from "./DomainSettingsButton";

vi.mock("../apiClient", () => ({ apiClient: { patch: vi.fn(), post: vi.fn() } }));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as {
  patch: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

function renderTenant(domain: DomainSettingsTarget) {
  const qc = new QueryClient();
  render(
    <QueryClientProvider client={qc}>
      <App>
        <TenantNginxRulesPanel domain={domain} />
      </App>
    </QueryClientProvider>,
  );
}

describe("GH #1999 — tenant Rule Builder front controller", () => {
  beforeEach(() => {
    mocked.patch.mockReset().mockResolvedValue({ data: {} });
    mocked.post.mockReset();
  });

  it("adds a front controller, previews the try_files line and saves it", async () => {
    renderTenant({ id: "d1", name: "admin.example.com", nginx_rules: [] });
    fireEvent.click(screen.getByRole("button", { name: /Add Rule/i }));
    fireEvent.click(await screen.findByText("Front Controller"));

    const query = await screen.findByPlaceholderText("$query_string");
    expect(screen.getByPlaceholderText("/index.php")).toHaveValue("/index.php");
    expect(screen.getByText("try_files $uri $uri/ /index.php?$query_string;")).toBeInTheDocument();

    fireEvent.change(query, { target: { value: "mod=$uri&$args" } });
    expect(screen.getByText("try_files $uri $uri/ /index.php?mod=$uri&$args;")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(mocked.patch).toHaveBeenCalledWith("/domains/d1", {
      nginx_rules: [{ type: "front_controller", script: "/index.php", query: "mod=$uri&$args" }],
    });
  });

  it("stops offering a second front controller", async () => {
    renderTenant({
      id: "d1",
      name: "admin.example.com",
      nginx_rules: [{ type: "front_controller", script: "/index.php", query: "mod=$uri&$args" }],
    });
    expect(screen.getByText("other requests → /index.php?mod=$uri&$args")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Add Rule/i }));
    expect(await screen.findByText("Deny Paths")).toBeInTheDocument();
    expect(screen.queryByText("Front Controller", { selector: ".ant-modal strong" })).toBeNull();
  });

  it("an imported front controller replaces the existing one", async () => {
    mocked.post.mockResolvedValue({
      data: {
        rules: [{ type: "front_controller", script: "/index.php", query: "mod=$uri&$args" }],
        warnings: [],
        notes: [],
      },
    });
    renderTenant({
      id: "d1",
      name: "admin.example.com",
      nginx_rules: [
        { type: "deny_paths", extensions: ["env"] },
        { type: "front_controller", script: "/app.php" },
      ],
    });
    fireEvent.click(screen.getByRole("button", { name: /Import from nginx config/i }));
    const textarea = await screen.findByPlaceholderText(/paste an nginx snippet here/i);
    fireEvent.change(textarea, {
      target: { value: "location / {\n    try_files $uri $uri/ /index.php?mod=$uri&$args;\n}" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Convert$/i }));
    fireEvent.click(await screen.findByRole("button", { name: /Add to Rule Builder/i }));

    fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(mocked.patch).toHaveBeenCalled());
    expect(mocked.patch).toHaveBeenCalledWith("/domains/d1", {
      nginx_rules: [
        { type: "deny_paths", extensions: ["env"] },
        { type: "front_controller", script: "/index.php", query: "mod=$uri&$args" },
      ],
    });
  });
});
