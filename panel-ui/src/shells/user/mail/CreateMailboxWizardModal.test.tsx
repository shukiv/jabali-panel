// Tests for the Create Mailbox wizard modal's 2-step flow.
//
// Step 1 is "domain only" — the rest of the form must NOT be mounted
// until the user picks a domain. The tests below lock in that gating
// plus the submit path.
import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CreateMailboxWizardModal } from "./CreateMailboxWizardModal";

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  patch: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

const DOMAINS = [
  { id: "dom1", name: "first.test" },
  { id: "dom2", name: "second.test" },
];

function renderModal(opts: {
  open?: boolean;
  domains?: typeof DOMAINS;
  onCancel?: () => void;
  onCreated?: (resp: { id: string; email: string }) => void;
} = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <CreateMailboxWizardModal
        open={opts.open ?? true}
        domains={opts.domains ?? DOMAINS}
        onCancel={opts.onCancel ?? (() => {})}
        // Lift the response type to satisfy the wizard's prop; tests
        // only verify the fields they care about.
        onCreated={opts.onCreated as never}
      />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocked.post.mockReset();
});

describe("CreateMailboxWizardModal — step gating", () => {
  it("renders only the Domain select when no domain is picked (step 1)", async () => {
    renderModal();

    // Domain select is there.
    await screen.findByLabelText(/^Domain$/i);

    // Rest of the form is NOT mounted yet.
    expect(screen.queryByLabelText(/email address/i)).toBeNull();
    expect(screen.queryByLabelText(/^Password$/i)).toBeNull();
    expect(screen.queryByLabelText(/quota/i)).toBeNull();
  });

  it("reveals email + password + quota once a domain is picked (step 2)", async () => {
    renderModal();

    const domainField = await screen.findByLabelText(/^Domain$/i);
    // AntD Select uses mouseDown + click on the option text to pick.
    fireEvent.mouseDown(domainField);
    const option = await screen.findByText(/^first\.test$/);
    fireEvent.click(option);

    // Now the cascading inputs materialise.
    await screen.findByLabelText(/email address/i);
    expect(screen.getByLabelText(/^Password$/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/quota/i)).toBeInTheDocument();
  });
});

describe("CreateMailboxWizardModal — submit routing", () => {
  it("posts to the selected domain id and forwards the reveal-once response", async () => {
    mocked.post.mockResolvedValueOnce({
      data: {
        id: "mb9",
        email: "alice@second.test",
        quota_bytes: 1 << 30,
        password: "REVEAL-ONCE-ABC",
      },
    });
    const onCreated = vi.fn();
    renderModal({ onCreated });

    fireEvent.mouseDown(await screen.findByLabelText(/^Domain$/i));
    fireEvent.click(await screen.findByText(/^second\.test$/));

    fireEvent.change(await screen.findByLabelText(/email address/i), {
      target: { value: "alice" },
    });

    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith(
        "/domains/dom2/mailboxes",
        expect.objectContaining({ local_part: "alice" }),
      ),
    );
    await waitFor(() =>
      expect(onCreated).toHaveBeenCalledWith(
        expect.objectContaining({
          email: "alice@second.test",
          password: "REVEAL-ONCE-ABC",
        }),
      ),
    );
  });

  it("surfaces validation errors inline without closing the modal", async () => {
    const onCreated = vi.fn();
    renderModal({ onCreated });

    // Submit without picking a domain at all — AntD's rule should
    // surface "Pick a domain" and we must NOT have called the API.
    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    await waitFor(() =>
      expect(screen.getByText(/pick a domain/i)).toBeInTheDocument(),
    );
    expect(mocked.post).not.toHaveBeenCalled();
    expect(onCreated).not.toHaveBeenCalled();
  });
});

describe("CreateMailboxWizardModal — GH #529 tabbed drawer", () => {
  it("renders Account / Forwarding / Groups / Send As tabs (parity with Edit)", async () => {
    renderModal();
    await screen.findByRole("tab", { name: /account/i });
    expect(screen.getByRole("tab", { name: /forwarding/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /groups/i })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /send as/i })).toBeInTheDocument();
  });

  it("Send As tab explains the grant is available only after the mailbox exists", async () => {
    renderModal();
    // Send As can't be granted pre-creation (no mailbox id yet); the tab
    // must point the operator at Edit rather than offer a dead control.
    fireEvent.click(await screen.findByRole("tab", { name: /send as/i }));
    await screen.findByText(/only be granted after this mailbox exists/i);
  });

  it("keeps the domain-first gating: account fields stay hidden until a domain is picked", async () => {
    renderModal();
    // The Account tab is active by default; its cascade must still be gated.
    await screen.findByRole("tab", { name: /account/i });
    expect(screen.queryByLabelText(/email address/i)).toBeNull();
    expect(screen.queryByLabelText(/^Password$/i)).toBeNull();
  });
});
