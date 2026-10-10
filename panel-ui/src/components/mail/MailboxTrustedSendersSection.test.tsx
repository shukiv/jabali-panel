// MailboxTrustedSendersSection.test.tsx — GH #2017: the senders a mailbox
// trusts, in the Edit mailbox drawer. Only apiClient is mocked; the real
// antd components and TanStack Query run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn(), patch: vi.fn() },
}));

import { apiClient } from "../../apiClient";
import { MailboxTrustedSendersSection } from "./MailboxTrustedSendersSection";
import { EditMailboxModal } from "./EditMailboxModal";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPost = apiClient.post as ReturnType<typeof vi.fn>;
const mockDelete = apiClient.delete as ReturnType<typeof vi.fn>;

const rows = [
  { id: "t1", address: "alice@example.com", created_at: "2026-10-10T10:00:00Z" },
  { id: "t2", address: "news+weekly@shop.example", created_at: "2026-10-10T10:00:00Z" },
];

function serve(data = rows, max = 500) {
  mockGet.mockResolvedValue({ data: { data, total: data.length, max } });
}

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <MailboxTrustedSendersSection mailboxId="mb1" />
      </App>
    </QueryClientProvider>,
  );
}

const addressInput = () => screen.getByRole("textbox", { name: "Sender address" });

describe("MailboxTrustedSendersSection", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists the mailbox's trusted senders and the count against the cap", async () => {
    serve();
    renderSection();
    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("news+weekly@shop.example")).toBeInTheDocument();
    expect(screen.getByText("2 of 500")).toBeInTheDocument();
    expect(mockGet).toHaveBeenCalledWith("/mailboxes/mb1/trusted-senders");
  });

  it("explains the exact-match and SPF/DMARC conditions", async () => {
    serve([]);
    renderSection();
    expect(await screen.findByText(/passes SPF or DMARC/)).toBeInTheDocument();
    expect(screen.getByText(/\+tag/)).toBeInTheDocument();
  });

  it("adds the typed address, trimmed", async () => {
    serve([]);
    mockPost.mockResolvedValue({ data: { id: "t3", address: "bob@x.com", created_at: "" } });
    renderSection();
    await screen.findByText("0 of 500");
    fireEvent.change(addressInput(), { target: { value: "  Bob@X.com " } });
    fireEvent.click(screen.getByRole("button", { name: /Add/ }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith("/mailboxes/mb1/trusted-senders", { address: "Bob@X.com" }),
    );
    expect(await screen.findByText("bob@x.com is trusted")).toBeInTheDocument();
    await waitFor(() => expect(addressInput()).toHaveValue(""));
    expect(mockGet).toHaveBeenCalledTimes(2); // refetched
  });

  it("adds on Enter", async () => {
    serve([]);
    mockPost.mockResolvedValue({ data: { id: "t3", address: "bob@x.com", created_at: "" } });
    renderSection();
    await screen.findByText("0 of 500");
    fireEvent.change(addressInput(), { target: { value: "bob@x.com" } });
    fireEvent.keyDown(addressInput(), { key: "Enter", code: "Enter" });
    await waitFor(() => expect(mockPost).toHaveBeenCalled());
  });

  it("does not post an empty address", async () => {
    serve([]);
    renderSection();
    await screen.findByText("0 of 500");
    expect(screen.getByRole("button", { name: /Add/ })).toBeDisabled();
    fireEvent.change(addressInput(), { target: { value: "   " } });
    expect(screen.getByRole("button", { name: /Add/ })).toBeDisabled();
  });

  it("shows the warning when the mail server has not taken it yet", async () => {
    serve([]);
    mockPost.mockResolvedValue({
      data: { id: "t3", address: "bob@x.com", created_at: "", warning: "Saved. The mail server has not taken it yet; the panel retries within a few minutes." },
    });
    renderSection();
    await screen.findByText("0 of 500");
    fireEvent.change(addressInput(), { target: { value: "bob@x.com" } });
    fireEvent.click(screen.getByRole("button", { name: /Add/ }));
    expect(await screen.findByText(/has not taken it yet/)).toBeInTheDocument();
  });

  it("shows the server's reason when it refuses the address", async () => {
    serve([]);
    mockPost.mockRejectedValue({
      response: { status: 422, data: { error: "invalid_address", detail: "Enter one full address, like name@example.com." } },
    });
    renderSection();
    await screen.findByText("0 of 500");
    fireEvent.change(addressInput(), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: /Add/ }));
    expect(await screen.findByText("Enter one full address, like name@example.com.")).toBeInTheDocument();
    expect(addressInput()).toHaveValue("nope"); // kept for fixing
  });

  it("says when the sender is already trusted", async () => {
    serve();
    mockPost.mockRejectedValue({ response: { status: 409, data: { error: "already_trusted" } } });
    renderSection();
    await screen.findByText("alice@example.com");
    fireEvent.change(addressInput(), { target: { value: "alice@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: /Add/ }));
    expect(await screen.findByText("This sender is already trusted")).toBeInTheDocument();
  });

  it("removes a sender", async () => {
    serve();
    mockDelete.mockResolvedValue({});
    renderSection();
    await screen.findByText("alice@example.com");
    fireEvent.click(screen.getByRole("button", { name: "Remove alice@example.com" }));
    await waitFor(() => expect(mockDelete).toHaveBeenCalledWith("/mailboxes/mb1/trusted-senders/t1"));
  });

  it("says the sender is still trusted when removing fails", async () => {
    serve();
    mockDelete.mockRejectedValue({
      response: { status: 502, data: { error: "mail_server_unavailable", detail: "The mail server could not remove the sender, so it is still trusted. Try again." } },
    });
    renderSection();
    await screen.findByText("alice@example.com");
    fireEvent.click(screen.getByRole("button", { name: "Remove alice@example.com" }));
    expect(await screen.findByText(/so it is still trusted/)).toBeInTheDocument();
  });

  it("stops adding at the cap", async () => {
    serve(rows, 2);
    renderSection();
    await screen.findByText("2 of 2");
    fireEvent.change(addressInput(), { target: { value: "bob@x.com" } });
    expect(screen.getByRole("button", { name: /Add/ })).toBeDisabled();
  });
});

describe("EditMailboxModal", () => {
  it("has a Trusted senders tab", async () => {
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <App>
          <EditMailboxModal
            open
            onClose={() => {}}
            mailbox={{
              id: "mb1", domain_id: "d1", email: "me@example.com", display_name: "", quota_bytes: 0,
              is_disabled: false, send_only: false, last_usage_bytes: 0, created_at: "", updated_at: "",
            }}
          />
        </App>
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("tab", { name: "Trusted senders" })).toBeInTheDocument();
  });
});
