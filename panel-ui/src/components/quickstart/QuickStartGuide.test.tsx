// QuickStartGuide.test.tsx — JAB-297. The shared Quick Start Module owns the
// display policy, the late-response cancel guard, and persistence; the two
// shells only supply an audience. These are the behavioral ACs:
//   AC1 — missing preference opens; dismissed ("1") stays closed; a failed
//         lookup stays closed.
//   AC2 — a response that arrives after the request is superseded/unmounted is
//         ignored (the cancelled flag).
//   AC3 — "I'll read later" persists nothing; "Never show again" writes the
//         audience's own preference key.
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { QuickStartGuide, type QuickStartAudience } from "./QuickStartGuide";
import { QuickStartModal as AdminQuickStart } from "../../shells/admin/QuickStartModal";
import { QuickStartModal as UserQuickStart } from "../../shells/user/QuickStartModal";

// apiClient is the only side-effecting seam; control it per test.
const { getMock, putMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  putMock: vi.fn(),
}));
vi.mock("../../apiClient", () => ({
  apiClient: { get: getMock, put: putMock },
}));

// A signed-in user is required for the effect to fire; the id value is opaque.
vi.mock("../../auth/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1" } }),
}));

const audience = (prefKey = "quickstart_test"): QuickStartAudience => ({
  prefKey,
  steps: [
    {
      number: 1,
      title: "First Step",
      desc: "do the first thing",
      href: "/first",
      color: "#2563eb",
    },
  ],
  renderSupport: (close) => (
    <a href="https://example.com/docs" onClick={close}>
      docs link
    </a>
  ),
});

const renderGuide = (a: QuickStartAudience = audience()) =>
  render(
    <MemoryRouter>
      <QuickStartGuide audience={a} />
    </MemoryRouter>,
  );

// Flush the get().then() microtask chain and any resulting state update.
const flush = () => act(async () => { await Promise.resolve(); });

beforeEach(() => {
  getMock.mockReset();
  putMock.mockReset();
  putMock.mockResolvedValue({});
});

describe("QuickStartGuide display policy (AC1)", () => {
  it("opens when the preference is missing", async () => {
    getMock.mockResolvedValue({ data: { prefs: {} } });
    renderGuide();
    expect(await screen.findByText(/Welcome to Jabali Panel/)).toBeInTheDocument();
    // The audience's own step content renders inside the open guide.
    expect(screen.getByText("First Step")).toBeInTheDocument();
  });

  it("stays closed when the preference is already dismissed", async () => {
    getMock.mockResolvedValue({ data: { prefs: { quickstart_test: "1" } } });
    renderGuide();
    await flush();
    expect(screen.queryByText(/Welcome to Jabali Panel/)).not.toBeInTheDocument();
  });

  it("stays closed when the lookup fails", async () => {
    getMock.mockRejectedValue(new Error("network down"));
    renderGuide();
    await flush();
    expect(screen.queryByText(/Welcome to Jabali Panel/)).not.toBeInTheDocument();
  });
});

describe("QuickStartGuide late-response guard (AC2)", () => {
  it("ignores a response that resolves after unmount", async () => {
    let resolveGet!: (v: unknown) => void;
    getMock.mockReturnValue(
      new Promise((r) => {
        resolveGet = r;
      }),
    );
    const { unmount } = renderGuide();
    unmount();
    // Resolving with "missing preference" would open the guide were it not
    // cancelled; after unmount it must be a no-op that does not throw.
    await act(async () => {
      resolveGet({ data: { prefs: {} } });
      await Promise.resolve();
    });
    expect(screen.queryByText(/Welcome to Jabali Panel/)).not.toBeInTheDocument();
  });

  it("ignores a stale response after the request is superseded", async () => {
    // First request hangs; a prefKey change supersedes it (cleanup → cancelled)
    // and fires a second request whose "dismissed" answer keeps the guide shut.
    let resolveStale!: (v: unknown) => void;
    getMock.mockImplementationOnce(
      () =>
        new Promise((r) => {
          resolveStale = r;
        }),
    );
    getMock.mockResolvedValue({ data: { prefs: { second_key: "1" } } });

    const { rerender } = renderGuide(audience("first_key"));
    rerender(
      <MemoryRouter>
        <QuickStartGuide audience={audience("second_key")} />
      </MemoryRouter>,
    );
    await flush();

    // The stale answer says "missing preference" (would open under first_key);
    // the guard must ignore it because that request was cancelled.
    await act(async () => {
      resolveStale({ data: { prefs: {} } });
      await Promise.resolve();
    });
    expect(screen.queryByText(/Welcome to Jabali Panel/)).not.toBeInTheDocument();
  });
});

describe("QuickStartGuide persistence (AC3)", () => {
  it("'I'll read later' closes without persisting", async () => {
    getMock.mockResolvedValue({ data: { prefs: {} } });
    renderGuide();
    await screen.findByText(/Welcome to Jabali Panel/);
    fireEvent.click(screen.getByText(/read later/));
    // The whole point of "read later" is that it persists nothing (both footer
    // buttons close the guide; only "Never show again" writes a preference).
    expect(putMock).not.toHaveBeenCalled();
  });

  it("'Never show again' persists the audience's own preference key", async () => {
    getMock.mockResolvedValue({ data: { prefs: {} } });
    renderGuide(audience("quickstart_admin"));
    await screen.findByText(/Welcome to Jabali Panel/);
    fireEvent.click(screen.getByText(/Never show again/));
    expect(putMock).toHaveBeenCalledWith("/me/ui-prefs/quickstart_admin", {
      value: "1",
    });
  });
});

// AC4 — the real shell Adapters must keep their own copy, destinations, and
// support variant. The apiClient / AuthContext mocks above resolve by module
// id, so they cover each adapter's transitive imports too.
describe("shell adapters preserve their audience (AC4)", () => {
  it("admin: admin steps, admin routes, Support link + docs emoji", async () => {
    getMock.mockResolvedValue({ data: { prefs: {} } });
    render(
      <MemoryRouter>
        <AdminQuickStart />
      </MemoryRouter>,
    );
    await screen.findByText(/Welcome to Jabali Panel/);
    expect(screen.getByText("Server Settings").closest("a")).toHaveAttribute(
      "href",
      "/jabali-admin/settings",
    );
    expect(screen.getByText("Support").closest("a")).toHaveAttribute(
      "href",
      "/jabali-admin/support",
    );
    expect(screen.getByLabelText("docs")).toBeInTheDocument();
  });

  it("tenant: tenant steps, tenant routes, docs-only support", async () => {
    getMock.mockResolvedValue({ data: { prefs: {} } });
    render(
      <MemoryRouter>
        <UserQuickStart />
      </MemoryRouter>,
    );
    await screen.findByText(/Welcome to Jabali Panel/);
    expect(
      screen.getByText("Personal API Tokens").closest("a"),
    ).toHaveAttribute("href", "/jabali-panel/api-tokens");
    // No admin-style Support link, no 📖 emoji — docs-only.
    expect(screen.queryByText("Support")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("docs")).not.toBeInTheDocument();
  });
});
