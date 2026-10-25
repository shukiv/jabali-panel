// MailboxSendAsSection.test.tsx — the Send As tab of the Edit mailbox drawer
// (GH #347). Only apiClient is mocked; the real antd components and TanStack
// Query run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn(), patch: vi.fn() },
}));

import { apiClient } from "../../apiClient";
import { MailboxSendAsSection } from "./MailboxSendAsSection";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockDelete = apiClient.delete as ReturnType<typeof vi.fn>;

const grantor = {
  id: "d1",
  grantor_mailbox_id: "mb2",
  grantor_email: "billing@example.com",
  delegate_mailbox_id: "mb1",
  delegate_email: "me@example.com",
  created_at: "2026-10-25T10:00:00Z",
};

function renderSection() {
  mockGet.mockImplementation(async (url: string) =>
    url === "/mailboxes/mb1/send-as"
      ? { data: { data: [grantor] } }
      : { data: { data: [], total: 0 } },
  );
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <MailboxSendAsSection mailboxId="mb1" domainId="dom1" />
      </App>
    </QueryClientProvider>,
  );
}

describe("MailboxSendAsSection", () => {
  beforeEach(() => vi.clearAllMocks());

  // GH #2267: when the mail server does not take a change, the toast reads the
  // server's sentence, not its error code.
  it("shows the server's reason when the mail server refuses a removal", async () => {
    mockDelete.mockRejectedValue({
      response: {
        status: 500,
        data: {
          error: "sendas_not_applied",
          detail: "The mail server did not take the change, so nothing was changed.",
        },
      },
    });
    renderSection();
    const row = (await screen.findByText("billing@example.com")).closest("li") as HTMLElement;
    fireEvent.click(within(row).getByRole("button"));
    expect(
      await screen.findByText("The mail server did not take the change, so nothing was changed."),
    ).toBeInTheDocument();
    expect(mockDelete).toHaveBeenCalledWith("/mailboxes/mb1/send-as/mb2");
  });
});
