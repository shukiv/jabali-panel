// SettingsTab — GH #1628 slice 4: tenant per-domain webmail toggle; GH #1915:
// the per-domain disclaimer lives here too (it used to be its own tab); GH
// #1916: so does the catch-all.
//
// The core assertions are the owner-scoped PATCH /domains/:id { webmail_enabled }
// on flip and the PUT /domains/:id/disclaimer { enabled, text } on save (RED if
// either mutation is dropped), plus the email-off gate that mirrors the admin
// domain-email section (nothing to configure until the domain has mail on).
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SettingsTab } from "./SettingsTab";

vi.mock("../../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function mockGets(opts: {
  emailEnabled: boolean;
  disclaimer?: { enabled: boolean; text: string };
  catchAll?: string | null;
}) {
  mocked.get.mockImplementation((url: string) => {
    if (url === "/domains/d1/catchall") {
      return Promise.resolve({
        data: { domain_id: "d1", domain_name: "on.test", target: opts.catchAll ?? null, updated_at: "2026-10-04T00:00:00Z" },
      });
    }
    if (url.startsWith("/domains/d1/mailboxes?")) {
      return Promise.resolve({
        data: { data: [{ id: "m1", email: "info@on.test" }, { id: "m2", email: "sales@on.test" }] },
      });
    }
    if (url === "/domains/d1/disclaimer") {
      return Promise.resolve({
        data: {
          domain_id: "d1",
          domain_name: "on.test",
          enabled: opts.disclaimer?.enabled ?? false,
          text: opts.disclaimer?.text ?? "",
          updated_at: "2026-09-30T00:00:00Z",
        },
      });
    }
    if (url === "/domains/d1") {
      return Promise.resolve({
        data: {
          id: "d1",
          name: "on.test",
          email_enabled: opts.emailEnabled,
          webmail_enabled: true,
        },
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <SettingsTab domainId="d1" />
      </App>
    </QueryClientProvider>,
  );
}

describe("GH #1628 slice 4 — SettingsTab per-domain webmail toggle", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("toggles the per-domain webmail flag via PATCH /domains/:id", async () => {
    mockGets({ emailEnabled: true });
    mocked.patch.mockResolvedValue({ data: {} });

    renderTab();

    const sw = await screen.findByRole("switch", { name: "Webmail client" });
    expect(sw).toBeChecked();

    fireEvent.click(sw);

    await waitFor(() =>
      expect(mocked.patch).toHaveBeenCalledWith("/domains/d1", { webmail_enabled: false }),
    );
  });

  it("hides every setting and prompts to enable email when the domain has mail off", async () => {
    mockGets({ emailEnabled: false });

    renderTab();

    expect(
      await screen.findByText(/enable email for this domain first/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Webmail client" })).toBeNull();
    expect(screen.queryByRole("switch", { name: "Enable Disclaimer" })).toBeNull();
    expect(mocked.patch).not.toHaveBeenCalled();
    // The disclaimer endpoint answers 403 email_not_enabled; don't ask it.
    expect(mocked.get).not.toHaveBeenCalledWith("/domains/d1/disclaimer");
    expect(mocked.get).not.toHaveBeenCalledWith("/domains/d1/catchall");
  });
});

describe("GH #1915 — SettingsTab per-domain disclaimer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows the saved disclaimer", async () => {
    mockGets({ emailEnabled: true, disclaimer: { enabled: true, text: "Confidential." } });

    renderTab();

    const sw = await screen.findByRole("switch", { name: "Enable Disclaimer" });
    expect(sw).toBeChecked();
    expect(screen.getByRole("textbox", { name: "Disclaimer Text" })).toHaveValue("Confidential.");
  });

  it("saves the disclaimer via PUT /domains/:id/disclaimer", async () => {
    mockGets({ emailEnabled: true });
    mocked.put.mockResolvedValue({
      data: { domain_id: "d1", domain_name: "on.test", enabled: true, text: "Legal notice.", updated_at: "x" },
    });

    renderTab();

    const sw = await screen.findByRole("switch", { name: "Enable Disclaimer" });
    expect(sw).not.toBeChecked();
    fireEvent.click(sw);
    fireEvent.change(screen.getByRole("textbox", { name: "Disclaimer Text" }), {
      target: { value: "Legal notice." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/domains/d1/disclaimer", {
        enabled: true,
        text: "Legal notice.",
      }),
    );
  });

  it("refuses to enable the disclaimer with no text", async () => {
    mockGets({ emailEnabled: true });

    renderTab();

    fireEvent.click(await screen.findByRole("switch", { name: "Enable Disclaimer" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Text required when enabled")).toBeInTheDocument();
    expect(mocked.put).not.toHaveBeenCalled();
  });
});

describe("GH #1916 — SettingsTab per-domain catch-all", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  async function pickTarget(email: string) {
    fireEvent.mouseDown(await screen.findByLabelText("Target mailbox"));
    fireEvent.click(await screen.findByText(email, { selector: ".ant-select-item-option-content" }));
  }

  it("sets the catch-all to a mailbox via PUT /domains/:id/catchall", async () => {
    mockGets({ emailEnabled: true });
    mocked.put.mockResolvedValue({
      data: { domain_id: "d1", domain_name: "on.test", target: "sales@on.test", updated_at: "x" },
    });

    renderTab();

    expect(await screen.findByText(/^Not set: mail to an unknown address at/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear catch-all" })).toBeNull();
    await pickTarget("sales@on.test");
    fireEvent.click(screen.getByRole("button", { name: "Set catch-all" }));

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/domains/d1/catchall", { target: "sales@on.test" }),
    );
  });

  it("refuses to set the catch-all with no mailbox picked", async () => {
    mockGets({ emailEnabled: true });

    renderTab();

    fireEvent.click(await screen.findByRole("button", { name: "Set catch-all" }));

    expect(await screen.findByText("Select a target mailbox")).toBeInTheDocument();
    expect(mocked.put).not.toHaveBeenCalled();
  });

  it("shows the saved target and clears it via DELETE /domains/:id/catchall", async () => {
    mockGets({ emailEnabled: true, catchAll: "info@on.test" });
    mocked.delete.mockResolvedValue({ data: {} });

    renderTab();

    fireEvent.click(await screen.findByRole("button", { name: "Clear catch-all" }));
    expect(screen.getByText("info@on.test", { selector: "code" })).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Clear" }));

    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/domains/d1/catchall"));
    expect(mocked.put).not.toHaveBeenCalled();
  });

  it("keeps a current target that is not one of the domain's mailboxes", async () => {
    mockGets({ emailEnabled: true, catchAll: "me+catchall@gmail.example" });
    mocked.put.mockResolvedValue({
      data: { domain_id: "d1", domain_name: "on.test", target: "me+catchall@gmail.example", updated_at: "x" },
    });

    renderTab();

    expect(await screen.findByText("me+catchall@gmail.example (current)")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Set catch-all" }));

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/domains/d1/catchall", { target: "me+catchall@gmail.example" }),
    );
  });
});
