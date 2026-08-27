// DomainMailboxesSection.test.tsx — component tests covering the two
// things a generic hook test can't catch:
//
//   1. Create-modal end-to-end — typing a local part, leaving the
//      password blank, submitting, and seeing the reveal-once modal.
//      If the password panel stops rendering on auto-generated creates
//      the operator loses the secret forever.
//
//   2. Quota bar rendering at the 90% danger threshold — we flip the
//      <Progress> status to `exception` so admins see red, not a green
//      bar inching toward full.
//
// Only apiClient is mocked; the TanStack + AntD chain runs for real.
import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DomainMailboxesSection } from "./DomainMailboxesSection";

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

function renderSection(
  domainId = "dom1",
  opts: {
    domainOptions?: Array<{ id: string; name: string }>;
    onDomainCreated?: (id: string) => void;
  } = {},
): { root: ReactNode } {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const root = (
    <QueryClientProvider client={qc}>
      <DomainMailboxesSection
        domainId={domainId}
        domainOptions={opts.domainOptions}
        onDomainCreated={opts.onDomainCreated}
      />
    </QueryClientProvider>
  );
  render(root);
  return { root };
}

// Canned GETs — the section issues two on mount: one for the email
// state, one for the mailbox list. The `url` discriminator is enough
// to route; no need for a full URL matcher.
function mockInitialFetches(params: {
  emailEnabled: boolean;
  mailboxes?: Array<{
    id: string;
    email: string;
    quota_bytes: number;
    last_usage_bytes: number;
    is_disabled?: boolean;
  }>;
}) {
  mocked.get.mockImplementation(async (url: string) => {
    if (url.includes("/email")) {
      return {
        data: {
          domain_id: "dom1",
          domain_name: "example.com",
          email_enabled: params.emailEnabled,
          records: [],
        },
      };
    }
    if (url.includes("/mailboxes")) {
      return {
        data: {
          data: (params.mailboxes ?? []).map((m) => ({
            domain_id: "dom1",
            is_disabled: false,
            created_at: "2026-04-21T00:00:00Z",
            updated_at: "2026-04-21T00:00:00Z",
            ...m,
          })),
          total: (params.mailboxes ?? []).length,
        },
      };
    }
    throw new Error(`unexpected GET ${url}`);
  });
}

beforeEach(() => {
  mocked.get.mockReset();
  mocked.post.mockReset();
  mocked.delete.mockReset();
});

describe("DomainMailboxesSection — guard", () => {
  it("shows enable-first hint + retry button when email is disabled on the domain", async () => {
    mockInitialFetches({ emailEnabled: false });
    renderSection();

    await waitFor(() =>
      expect(
        screen.getByText(/Email isn't enabled on this domain/i),
      ).toBeInTheDocument(),
    );
    // Create button must not be reachable while email is off — users
    // who bypass the guard would hit a 409 with no useful recovery.
    expect(screen.queryByRole("button", { name: /create mailbox/i })).toBeNull();
    // The retry affordance IS reachable so the operator can recover
    // from an auto-enable failure without leaving this page.
    expect(
      screen.getByRole("button", { name: /^enable email$/i }),
    ).toBeInTheDocument();
  });
});

describe("DomainMailboxesSection — create modal reveal-once flow", () => {
  it("shows the auto-generated password in the DatabaseUserPasswordModal after submit", async () => {
    mockInitialFetches({ emailEnabled: true, mailboxes: [] });
    mocked.post.mockResolvedValueOnce({
      data: {
        id: "mb1",
        email: "alice@example.com",
        quota_bytes: 1 << 30,
        password: "GEN-PWD-1234567890",
      },
    });
    renderSection();

    // Wait for the initial render to finish.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /create mailbox/i })).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    // Local part is required; password left blank → server generates.
    const localPartInput = await screen.findByLabelText(/local part/i);
    fireEvent.change(localPartInput, { target: { value: "alice" } });
    fireEvent.click(screen.getByRole("button", { name: /^create$/i }));

    // Reveal-once modal fires with the generated password.
    // DatabaseUserPasswordModal title defaults vary; we check for the
    // "saved it now" framing + the password text (revealed via the
    // eye toggle would be masked, so assert on presence in the DOM
    // input's value attribute via a looser query).
    //
    // Modal reveal involves: mutation success -> setState -> Modal
    // mount -> Modal animation -> text render. Gitea CI runner
    // crossed the default 1s waitFor in run #3089 (2026-06-04);
    // bumped to 5s to match the sibling progress test from PR #182.
    await waitFor(
      () =>
        expect(
          screen.getByText(/This password will never be shown again\./i),
        ).toBeInTheDocument(),
      { timeout: 5000 },
    );
    // The input holds the plaintext password (masked or not; value is
    // the source of truth in the DOM).
    const inputs = screen.getAllByDisplayValue(
      /GEN-PWD-1234567890|•+/,
    );
    expect(inputs.length).toBeGreaterThan(0);
  });
});

describe("DomainMailboxesSection — quota progress bar", () => {
  it("renders status=exception when usage ≥ 90% of quota", async () => {
    mockInitialFetches({
      emailEnabled: true,
      mailboxes: [
        {
          id: "mb-full",
          email: "near-full@example.com",
          quota_bytes: 1_000_000_000,
          last_usage_bytes: 950_000_000, // 95%
        },
      ],
    });
    renderSection();

    // AntD's Progress applies `ant-progress-status-exception` when the
    // status prop is "exception"; that's our signal the operator will
    // see the red-warning styling. waitFor's default 1s timeout is too
    // short on the Gitea VPS runner (CI environment is slower than the
    // dev box) — bump to 5s so async row-data + Progress rendering
    // finishes before the first assertion.
    await waitFor(
      () => {
        const progressEl = document.querySelector(".ant-progress");
        expect(progressEl).not.toBeNull();
        expect(progressEl?.className).toContain("ant-progress-status-exception");
      },
      { timeout: 5000 },
    );
  });

  it("renders normal-status progress bar below the 90% threshold", async () => {
    mockInitialFetches({
      emailEnabled: true,
      mailboxes: [
        {
          id: "mb-half",
          email: "half@example.com",
          quota_bytes: 1_000_000_000,
          last_usage_bytes: 500_000_000, // 50%
        },
      ],
    });
    renderSection();

    await waitFor(
      () => {
        const progressEl = document.querySelector(".ant-progress");
        expect(progressEl).not.toBeNull();
        expect(progressEl?.className).not.toContain("ant-progress-status-exception");
      },
      { timeout: 5000 },
    );
  });
});

describe("DomainMailboxesSection — webmail SSO opener isolation (JAB-330)", () => {
  it("severs popup.opener synchronously, before the async SSO mint (reverse-tabnab guard)", async () => {
    mockInitialFetches({
      emailEnabled: true,
      mailboxes: [
        {
          id: "mb1",
          email: "alice@example.com",
          quota_bytes: 1 << 30,
          last_usage_bytes: 0,
        },
      ],
    });
    // The SSO mint never resolves: the opener MUST already be null by then, so
    // this proves the severing happens up front, not in onSuccess (where the
    // reverse-tabnabbing window is already open).
    mocked.post.mockReturnValue(new Promise<never>(() => {}));

    const fakePopup = {
      opener: {} as unknown,
      location: { href: "" },
      close: vi.fn(),
    };
    const openSpy = vi
      .spyOn(window, "open")
      .mockReturnValue(fakePopup as unknown as Window);

    try {
      renderSection();
      // Default findBy timeout is 1s; the initial fetches + render cross it on
      // the resource-starved nightly runner (same flake the waitFors above bump
      // to 5s — run #3089). 5s is a wide margin that still catches a real
      // never-renders regression.
      const btn = await screen.findByRole(
        "button",
        { name: /open webmail/i },
        { timeout: 5000 },
      );
      fireEvent.click(btn);

      // Popup opened blank (blocker-dodge) and opener severed synchronously.
      expect(openSpy).toHaveBeenCalledWith("about:blank", "_blank");
      expect(fakePopup.opener).toBeNull();
    } finally {
      openSpy.mockRestore();
    }
  });
});

describe("DomainMailboxesSection — in-dialog domain picker", () => {
  it("does NOT render the picker when domainOptions is omitted (single-domain contexts)", async () => {
    mockInitialFetches({ emailEnabled: true, mailboxes: [] });
    renderSection();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /create mailbox/i })).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    // Local part field is in the dialog; Domain field is not.
    await screen.findByLabelText(/local part/i);
    expect(screen.queryByLabelText(/^Domain$/i)).toBeNull();
  });

  it("does NOT render the picker when only one domain is in domainOptions", async () => {
    mockInitialFetches({ emailEnabled: true, mailboxes: [] });
    renderSection("dom1", {
      domainOptions: [{ id: "dom1", name: "example.com" }],
    });

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /create mailbox/i })).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    await screen.findByLabelText(/local part/i);
    // No Select — user only has one domain to pick from, collapses
    // back to the pre-picker UX.
    expect(screen.queryByLabelText(/^Domain$/i)).toBeNull();
  });

  it("renders the picker and routes create to the chosen domain when user picks a different one", async () => {
    mockInitialFetches({ emailEnabled: true, mailboxes: [] });
    mocked.post.mockResolvedValueOnce({
      data: {
        id: "mb42",
        email: "alice@other.test",
        quota_bytes: 1 << 30,
        password: "GEN-OTHER-PWD",
      },
    });
    const onDomainCreated = vi.fn();
    renderSection("dom1", {
      domainOptions: [
        { id: "dom1", name: "example.com" },
        { id: "dom2", name: "other.test" },
      ],
      onDomainCreated,
    });

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /create mailbox/i })).toBeInTheDocument(),
    );
    fireEvent.click(screen.getByRole("button", { name: /create mailbox/i }));

    // Picker is present, defaulting to the section's current domain.
    const picker = await screen.findByLabelText(/^Domain$/i);
    expect(picker).toBeInTheDocument();

    // Type a local part.
    fireEvent.change(screen.getByLabelText(/local part/i), { target: { value: "alice" } });

    // Switch the picker to dom2 via AntD's keyboard-friendly flow:
    // open the dropdown, then click the other.test option. AntD
    // renders the actual options in a portal attached to body.
    fireEvent.mouseDown(picker);
    const option = await screen.findByText(/^other\.test$/);
    fireEvent.click(option);

    fireEvent.click(screen.getByRole("button", { name: /^create$/i }));

    // The POST must be routed to the chosen domain, not the section's
    // initial domainId.
    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith(
        expect.stringContaining("/domains/dom2/mailboxes"),
        expect.any(Object),
      ),
    );

    // onDomainCreated fires with the new domain so the host page can
    // pivot its top-level selector onto it.
    await waitFor(() => expect(onDomainCreated).toHaveBeenCalledWith("dom2"));
  });
});
