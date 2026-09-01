// GH #1409: the Add-domain drawer must default Mail to None when the mail module
// isn't installed, and to Jabali Mail when it is.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const caps = vi.hoisted(() => ({ mail: false }));
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: caps.mail } }),
}));
vi.mock("../../../hooks/useQueries", () => ({
  useCreateMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

import { UserDomainDrawer } from "./UserDomainDrawer";

function renderDrawer() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <UserDomainDrawer open onClose={() => {}} />
    </QueryClientProvider>,
  );
}

describe("UserDomainDrawer mail default (GH #1409)", () => {
  it("defaults Mail to None when the mail module is NOT installed", async () => {
    caps.mail = false;
    renderDrawer();
    // The Select shows its selected option's label; None must be selected.
    await waitFor(() => expect(screen.getByText("No mail")).toBeInTheDocument());
    expect(screen.queryByText("Jabali mail (this server)")).not.toBeInTheDocument();
  });

  it("defaults Mail to Jabali Mail when the module IS installed", async () => {
    caps.mail = true;
    renderDrawer();
    await waitFor(() =>
      expect(screen.getByText("Jabali mail (this server)")).toBeInTheDocument(),
    );
  });
});

describe("UserDomainDrawer document root (GH #1413)", () => {
  it("shows the Document root field by default and hides it for a reverse proxy", async () => {
    caps.mail = true;
    renderDrawer();
    // Present for a normal website.
    await waitFor(() =>
      expect(screen.getByLabelText("Document root")).toBeInTheDocument(),
    );
    // A reverse-proxy domain has no docroot — toggling it removes the field
    // (which also drops any typed value from the submitted payload).
    fireEvent.click(screen.getByRole("checkbox", { name: /set up as a reverse proxy/i }));
    await waitFor(() =>
      expect(screen.queryByLabelText("Document root")).not.toBeInTheDocument(),
    );
  });
});
