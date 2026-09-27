// JAB-390: the Email card shows the shared mail hostname in effect and lets
// the admin request a change, follow it, cancel it while it is not being
// applied, and switch back to mail.<hostname>. The request is only recorded;
// the reconciler applies it, so the card must show progress, not success.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

// JAB-390: before a change is activated the card shows the DNS records the
// admin must create for the new name, with this server's public addresses.
// The switchover checks that public DNS returns the public IPv4 for the name,
// so the A record is required and a missing public IPv4 is a warning.
describe("EmailCard DNS records for a new mail hostname (JAB-390)", () => {
  function serve(email: ReturnType<typeof body>, caps: { public_ipv4: string; public_ipv6: string }) {
    mocked.get.mockImplementation((url: string) =>
      Promise.resolve(url === "/me/server-capabilities" ? { status: 200, data: caps } : email),
    );
  }

  function recordRows() {
    const table = screen.queryByRole("table", { name: "DNS records to create" });
    if (!table) return [];
    return within(table)
      .getAllByRole("row")
      .slice(1)
      .map((r) => within(r).getAllByRole("cell").map((c) => c.textContent));
  }

  it("lists the A and AAAA records for the name being typed", async () => {
    serve(body(), { public_ipv4: "203.0.113.10", public_ipv6: "2001:db8::10" });
    renderCard();

    fireEvent.change(await screen.findByLabelText("Mail hostname"), { target: { value: " MX.Example.NET " } });
    await waitFor(() =>
      expect(recordRows()).toEqual([
        ["A", "mx.example.net", "203.0.113.10"],
        ["AAAA", "mx.example.net", "2001:db8::10"],
      ]),
    );
    expect(screen.getByText(/The AAAA record is optional/)).toBeInTheDocument();
  });

  it("lists the records for a pending change when nothing is typed", async () => {
    serve(
      body({
        switchover: {
          desired: "mx.example.net",
          status: "pending",
          last_error: "",
          next_retry_at: null,
          updated_at: "2026-09-27T10:50:00Z",
        },
      }),
      { public_ipv4: "203.0.113.10", public_ipv6: "" },
    );
    renderCard();

    await waitFor(() => expect(recordRows()).toEqual([["A", "mx.example.net", "203.0.113.10"]]));
  });

  it("lists nothing for a change that is already done", async () => {
    serve(
      body({
        mail_hostname: { effective: "mx.example.net", applied: "mx.example.net" },
        switchover: {
          desired: "mx.example.net",
          status: "done",
          last_error: "",
          next_retry_at: null,
          updated_at: "2026-09-27T10:50:00Z",
        },
      }),
      { public_ipv4: "203.0.113.10", public_ipv6: "" },
    );
    renderCard();

    expect(await screen.findByText(/point an A record for the new name at 203\.0\.113\.10/)).toBeInTheDocument();
    expect(recordRows()).toEqual([]);
  });

  it("without a public IPv6, lists no AAAA record and says not to add one", async () => {
    serve(body(), { public_ipv4: "203.0.113.10", public_ipv6: "" });
    renderCard();

    fireEvent.change(await screen.findByLabelText("Mail hostname"), { target: { value: "mx.example.net" } });
    await waitFor(() => expect(recordRows()).toEqual([["A", "mx.example.net", "203.0.113.10"]]));
    expect(screen.getByText(/has no public IPv6 address set, so do not add an AAAA record/)).toBeInTheDocument();
  });

  it("with nothing typed or pending, shows only the address to point the A record at", async () => {
    serve(body(), { public_ipv4: "203.0.113.10", public_ipv6: "" });
    renderCard();

    expect(await screen.findByText(/point an A record for the new name at 203\.0\.113\.10/)).toBeInTheDocument();
    expect(recordRows()).toEqual([]);
  });

  it("does not list records for text that is not a hostname", async () => {
    serve(body(), { public_ipv4: "203.0.113.10", public_ipv6: "" });
    renderCard();

    fireEvent.change(await screen.findByLabelText("Mail hostname"), { target: { value: "https://mx.example.net/" } });
    expect(await screen.findByText(/point an A record for the new name at 203\.0\.113\.10/)).toBeInTheDocument();
    expect(recordRows()).toEqual([]);
  });

  it("warns when the server's public IPv4 is not set", async () => {
    serve(body(), { public_ipv4: "", public_ipv6: "" });
    renderCard();

    fireEvent.change(await screen.findByLabelText("Mail hostname"), { target: { value: "mx.example.net" } });
    expect(await screen.findByText(/public IPv4 address is not set/)).toBeInTheDocument();
    expect(screen.getByText(/Settings → General → Public IPv4/)).toBeInTheDocument();
    expect(recordRows()).toEqual([]);
  });
});
