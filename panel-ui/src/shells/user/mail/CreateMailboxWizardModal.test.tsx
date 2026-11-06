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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { feedback } from "../../../lib/feedback";
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
  lockDomain?: boolean;
  onCancel?: () => void;
  onCreated?: (resp: { id: string; email: string }) => void;
} = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <CreateMailboxWizardModal
        open={opts.open ?? true}
        domains={opts.domains ?? DOMAINS}
        lockDomain={opts.lockDomain}
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

describe("CreateMailboxWizardModal — GH #1387 lockDomain (drill-down)", () => {
  it("hides the Domain picker, pre-fills the domain, and reveals account fields", async () => {
    renderModal({ domains: [DOMAINS[0]], lockDomain: true });

    // No Domain select — the drill already scoped to one domain.
    expect(screen.queryByLabelText(/^Domain$/i)).toBeNull();
    // A hint tells the user where the mailbox lands.
    await screen.findByText(/adding a mailbox to/i);
    // Exact match targets the hint's <strong>first.test</strong>, not the
    // email field's "@first.test" addon.
    expect(screen.getByText("first.test")).toBeInTheDocument();
    // Account fields materialise immediately (step-2), no picking needed.
    await screen.findByLabelText(/email address/i);
  });

  it("posts to the locked domain without a pick step", async () => {
    mocked.post.mockResolvedValueOnce({
      data: { id: "mb1", email: "bob@first.test", quota_bytes: 1 << 30 },
    });
    const onCreated = vi.fn();
    renderModal({ domains: [DOMAINS[0]], lockDomain: true, onCreated });

    fireEvent.change(await screen.findByLabelText(/email address/i), {
      target: { value: "bob" },
    });
    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith(
        "/domains/dom1/mailboxes",
        expect.objectContaining({ local_part: "bob" }),
      ),
    );
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

describe("CreateMailboxWizardModal — groups, aliases and forwards that fail", () => {
  const GROUPS = { data: { data: [{ id: "g1", display_name: "Sales team", email: "sales-team@second.test" }] } };

  // The warning stays until dismissed; clear it so no test sees another's.
  afterEach(() => feedback.message.destroy());

  async function fillAndSubmit(aliases = "sales, info") {
    fireEvent.mouseDown(await screen.findByLabelText(/^Domain$/i));
    fireEvent.click(await screen.findByText(/^second\.test$/));
    fireEvent.change(await screen.findByLabelText(/email address/i), { target: { value: "alice" } });

    fireEvent.click(screen.getByRole("tab", { name: /forwarding/i }));
    fireEvent.change(await screen.findByLabelText(/aliases/i), { target: { value: aliases } });
    fireEvent.change(await screen.findByLabelText(/forward to/i), { target: { value: "ext@example.org" } });

    fireEvent.click(screen.getByRole("tab", { name: /groups/i }));
    const groups = await screen.findByLabelText(/add to groups/i);
    await waitFor(() => expect(groups).not.toBeDisabled());
    fireEvent.mouseDown(groups);
    fireEvent.click(await screen.findByText(/^Sales team$/));

    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));
  }

  it("keeps the mailbox and lists each one that was not set up", async () => {
    mocked.get.mockResolvedValue(GROUPS);
    mocked.post.mockImplementation(async (url: string, body?: { type?: string; local_part?: string }) => {
      if (url === "/domains/dom2/mailboxes") return { data: { id: "mb9", email: "alice@second.test" } };
      if (url === "/mailgroups/g1/members/mb9") throw new Error("group is full");
      if (url === "/mailboxes/mb9/forwarders" && body?.type === "alias" && body.local_part === "info") {
        throw new Error("address already in use");
      }
      if (url === "/mailboxes/mb9/forwarders" && body?.type === "external") throw new Error("target refused");
      return { data: {} };
    });
    const onCreated = vi.fn();
    renderModal({ onCreated });

    await fillAndSubmit();

    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.objectContaining({ id: "mb9" })));
    expect(await screen.findByText("alice@second.test was created, but not all of it was set up")).toBeInTheDocument();
    expect(screen.getByText("Alias info@second.test: address already in use")).toBeInTheDocument();
    expect(screen.getByText("Forward to ext@example.org: target refused")).toBeInTheDocument();
    expect(screen.getByText("Group Sales team: group is full")).toBeInTheDocument();
    // The alias that was created is not reported, and the mailbox is not
    // reported as failed.
    expect(screen.queryByText(/Alias sales@second\.test/)).toBeNull();
    expect(screen.queryByText(/failed to create mailbox/i)).toBeNull();
    expect(mocked.post).toHaveBeenCalledWith(
      "/mailboxes/mb9/forwarders",
      expect.objectContaining({ type: "alias", local_part: "sales" }),
    );
  });

  it("shows no warning when all of them are set up", async () => {
    mocked.get.mockResolvedValue(GROUPS);
    mocked.post.mockImplementation(async (url: string) =>
      url === "/domains/dom2/mailboxes" ? { data: { id: "mb9", email: "alice@second.test" } } : { data: {} },
    );
    const onCreated = vi.fn();
    renderModal({ onCreated });

    await fillAndSubmit();

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(mocked.post).toHaveBeenCalledWith("/mailgroups/g1/members/mb9");
    expect(mocked.post).toHaveBeenCalledWith(
      "/mailboxes/mb9/forwarders",
      expect.objectContaining({ type: "external", target: "ext@example.org" }),
    );
    expect(screen.queryByText(/was created, but not all of it was set up/)).toBeNull();
  });

  it("sends a repeated alias once, and words a failure without a message", async () => {
    mocked.get.mockResolvedValue(GROUPS);
    mocked.post.mockImplementation(async (url: string, body?: { type?: string }) => {
      if (url === "/domains/dom2/mailboxes") return { data: { id: "mb9", email: "alice@second.test" } };
      if (url === "/mailboxes/mb9/forwarders" && body?.type === "alias") throw { status: 500 };
      return { data: {} };
    });
    const onCreated = vi.fn();
    renderModal({ onCreated });

    await fillAndSubmit("info, INFO,info");

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    const aliasCalls = mocked.post.mock.calls.filter(
      ([url, body]) => url === "/mailboxes/mb9/forwarders" && (body as { type?: string })?.type === "alias",
    );
    expect(aliasCalls).toHaveLength(1);
    expect(await screen.findByText("Alias info@second.test: the request failed")).toBeInTheDocument();
  });
});
