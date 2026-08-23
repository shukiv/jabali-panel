// JAB-326: the tenant channel drawer offers exactly the kinds the server
// allows (allowedKinds prop), not a hardcoded four. When an admin widens the
// policy, webhook/email appear; kinds outside the policy do not.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { MyChannelDrawer } from "./MyChannelDrawer";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function renderDrawer(allowedKinds?: Parameters<typeof MyChannelDrawer>[0]["allowedKinds"]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <MyChannelDrawer open onClose={() => {}} allowedKinds={allowedKinds} />
      </App>
    </QueryClientProvider>,
  );
  // Open the Kind <Select> (the first combobox) so its options render.
  fireEvent.mouseDown(screen.getAllByRole("combobox")[0]);
}

describe("MyChannelDrawer kind picker (JAB-326)", () => {
  it("offers admin-widened kinds (webhook, email) and hides kinds outside policy", () => {
    renderDrawer(["ntfy", "webhook", "email"]);
    expect(screen.getByText("Generic webhook")).toBeInTheDocument();
    expect(screen.getByText("Email")).toBeInTheDocument();
    // Telegram is not in the configured policy → not offered.
    expect(screen.queryByText("Telegram")).toBeNull();
  });

  it("falls back to the safe defaults when no policy is provided", () => {
    renderDrawer(undefined);
    expect(screen.getByText("Telegram")).toBeInTheDocument();
    expect(screen.getByText("Discord")).toBeInTheDocument();
    // Risky kinds are not in the safe default set.
    expect(screen.queryByText("Generic webhook")).toBeNull();
  });

  // AC4 (edit path): editing an existing channel whose kind was since removed
  // from policy still renders its config form — visibility is preserved.
  it("still renders the config form when editing a now-disallowed kind", () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <App>
          <MyChannelDrawer
            open
            onClose={() => {}}
            allowedKinds={["ntfy"]}
            existing={{
              id: "c1",
              name: "old hook",
              kind: "webhook",
              config: { url: "https://x.test/h", hmac_secret: "s" },
              enabled: true,
            }}
          />
        </App>
      </QueryClientProvider>,
    );
    expect(screen.getByText(/Edit old hook/)).toBeInTheDocument();
    // The webhook config fields render even though webhook is out of policy.
    expect(screen.getByText("Target URL")).toBeInTheDocument();
  });
});
