// UserDashboard.test.tsx — GH #1417. With the mail module disabled the
// dashboard must not surface Email pieces: the "Recent mailboxes" card and the
// "Mailboxes" stat tile both link to /mail/mailboxes, which redirects to an
// inaccessible page. Same server-capabilities gate the sidebar + Disk Usage use.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../../hooks/useQueries", () => ({
  useListQuery: () => ({ items: [], total: 0, isLoading: false }),
}));
vi.mock("@tanstack/react-query", async (orig) => ({
  ...(await orig<typeof import("@tanstack/react-query")>()),
  useQueries: () => [],
}));
vi.mock("../../identity", () => ({ getIdentity: () => Promise.resolve(null) }));
vi.mock("./MyProfileUsageCard", () => ({ MyProfileUsageCard: () => null }));
vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(async () => ({ data: { data: [], total: 0 } })) },
}));

let mailEnabled = true;
vi.mock("../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: mailEnabled } }),
}));

import { UserDashboard } from "./UserDashboard";

function renderDash() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <UserDashboard />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("UserDashboard mail-module gating (GH #1417)", () => {
  beforeEach(() => {
    mailEnabled = true;
  });

  it("shows the mailboxes card + stat tile when mail is enabled", async () => {
    renderDash();
    await waitFor(() => expect(screen.getByText("userdashboard.recent_mailboxes")).toBeInTheDocument());
    expect(screen.getByText("userdashboard.mailboxes")).toBeInTheDocument();
  });

  it("hides the mailboxes card + stat tile when the mail module is disabled", async () => {
    mailEnabled = false;
    renderDash();
    // Domains always renders — anchor on it, then assert the mail pieces are gone.
    await waitFor(() => expect(screen.getByText("userdashboard.recent_domains")).toBeInTheDocument());
    expect(screen.queryByText("userdashboard.recent_mailboxes")).not.toBeInTheDocument();
    expect(screen.queryByText("userdashboard.mailboxes")).not.toBeInTheDocument();
  });
});
