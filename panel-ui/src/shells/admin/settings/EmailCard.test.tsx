// JAB-390: the Email card shows the shared mail hostname in effect and lets
// the admin request a change, follow it, cancel it while it is not being
// applied, and switch back to mail.<hostname>. The request is only recorded;
// the reconciler applies it, so the card must show progress, not success.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { EmailCard } from "./EmailCard";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

function body(over: Record<string, unknown> = {}) {
  return {
    status: 200,
    data: {
      primary_domain_name: "mx.example.com",
      webmail_url: "https://mail.mx.example.com/",
      dkim_published: true,
      email_enabled_at: null,
      mail_hostname: { effective: "mail.mx.example.com", applied: null },
      switchover: null,
      ...over,
    },
  };
}

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <App>
      <QueryClientProvider client={qc}>
        <EmailCard />
      </QueryClientProvider>
    </App>,
  );
}

afterEach(() => {
  cleanup();
  mocked.get.mockReset();
  mocked.put.mockReset();
  mocked.delete.mockReset();
});

describe("EmailCard mail hostname (JAB-390)", () => {
  it("shows the default name and sends a trimmed change request", async () => {
    mocked.get.mockResolvedValue(body());
    mocked.put.mockResolvedValue({ status: 202, data: {} });
    renderCard();

    expect(await screen.findByText("mail.mx.example.com")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Switch back to/ })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Mail hostname"), { target: { value: " mx.example.net " } });
    fireEvent.click(screen.getByRole("button", { name: "Request change" }));
    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/admin/settings/email/mail-hostname", {
        mail_hostname: "mx.example.net",
      }),
    );
  });

  it("with a custom name applied, offers switching back to mail.<hostname>", async () => {
    mocked.get.mockResolvedValue(body({ mail_hostname: { effective: "mx.example.net", applied: "mx.example.net" } }));
    mocked.put.mockResolvedValue({ status: 202, data: {} });
    renderCard();

    expect(await screen.findByText("Custom")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Switch back to mail.mx.example.com" }));
    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/admin/settings/email/mail-hostname", {
        mail_hostname: "mail.mx.example.com",
      }),
    );
  });

  it("shows a failed change with its reason and cancels it", async () => {
    mocked.get.mockResolvedValue(
      body({
        switchover: {
          desired: "mx.example.net",
          status: "failed",
          last_error: "mx.example.net does not point at this server (dns lookup failed)",
          next_retry_at: null,
          updated_at: "2026-09-27T10:50:00Z",
        },
      }),
    );
    mocked.delete.mockResolvedValue({ status: 200, data: {} });
    renderCard();

    expect(await screen.findByText("Could not change the mail hostname to mx.example.net")).toBeInTheDocument();
    expect(screen.getByText("mx.example.net does not point at this server (dns lookup failed)")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel change" }));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/admin/settings/email/mail-hostname"));
  });

  it("while a certificate is issued, shows progress and allows neither a new request nor cancel", async () => {
    mocked.get.mockResolvedValue(
      body({
        switchover: {
          desired: "mx.example.net",
          status: "issuing",
          last_error: "",
          next_retry_at: null,
          updated_at: "2026-09-27T10:50:00Z",
        },
      }),
    );
    renderCard();

    expect(await screen.findByText(/Issuing a certificate for mx.example.net/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel change" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Mail hostname")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Request change" })).toBeDisabled();
  });

  it("surfaces the server's refusal reason", async () => {
    mocked.get.mockResolvedValue(body());
    mocked.put.mockRejectedValue(
      Object.assign(new Error("Request failed"), {
        isAxiosError: true,
        response: {
          status: 409,
          data: { error: "mail_hostname_refused", detail: "a hosted domain answers or controls that name: tenant.net" },
        },
      }),
    );
    renderCard();

    fireEvent.change(await screen.findByLabelText("Mail hostname"), { target: { value: "mx.tenant.net" } });
    fireEvent.click(screen.getByRole("button", { name: "Request change" }));
    expect(await screen.findByText(/a hosted domain answers or controls that name: tenant.net/)).toBeInTheDocument();
  });
});
