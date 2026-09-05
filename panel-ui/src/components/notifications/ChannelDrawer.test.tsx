// ChannelDrawer.test — the shared drawer's audience-neutral behaviour (JAB-336).
// Focus is the write-only masked-secret affordance (AC3) — on edit a secret field
// advertises "leave blank to keep" (the GET redacted it; the server preserves an
// empty secret) but on create shows its real placeholder — and the policy-driven
// notes (admin webpush / tenant forced-email) that stand in for config fields.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ChannelDrawer } from "./ChannelDrawer";
import {
  ADMIN_CHANNEL_POLICY,
  tenantChannelPolicy,
  type NotificationChannel,
} from "./channelPolicy";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function renderDrawer(props: Partial<Parameters<typeof ChannelDrawer>[0]>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <ChannelDrawer open onClose={() => {}} policy={ADMIN_CHANNEL_POLICY} {...props} />
      </App>
    </QueryClientProvider>,
  );
}

// A webhook row — hmac_secret is a password field. A GET redacts the secret, so
// the seeded config carries only the public url.
const webhookRow: NotificationChannel = {
  id: "c1",
  name: "hook",
  kind: "webhook",
  enabled: true,
  config: { url: "https://x.test/h" },
};

describe("ChannelDrawer masked-secret affordance (AC3)", () => {
  it("advertises 'leave blank to keep' for a secret field when editing", () => {
    renderDrawer({ existing: webhookRow });
    expect(screen.getByText(/Edit hook/)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/leave blank to keep/i)).toBeInTheDocument();
  });

  it("shows no masked hint for a secret field when creating", () => {
    renderDrawer({ policy: { ...ADMIN_CHANNEL_POLICY, defaultKind: "webhook" } });
    // The HMAC secret field still renders (it's a create form for a webhook)…
    expect(screen.getByText("HMAC secret")).toBeInTheDocument();
    // …but without the write-only "leave blank to keep" placeholder.
    expect(screen.queryByPlaceholderText(/leave blank to keep/i)).toBeNull();
  });
});

describe("ChannelDrawer policy-driven notes", () => {
  it("shows the admin webpush note in place of config fields", () => {
    renderDrawer({ policy: { ...ADMIN_CHANNEL_POLICY, defaultKind: "webpush" } });
    expect(screen.getByText(ADMIN_CHANNEL_POLICY.webpushNote.message)).toBeInTheDocument();
  });

  it("shows the tenant forced-email note and hides the destination fields (AC5)", () => {
    const p = tenantChannelPolicy(["email"]);
    renderDrawer({ policy: p });
    expect(screen.getByText(p.emailNote!.message)).toBeInTheDocument();
    // Forced email → the Recipient / SMTP fields are not rendered.
    expect(screen.queryByText("Recipient")).toBeNull();
  });
});
