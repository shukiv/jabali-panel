// UserDomainDrawer — the tenant Add Web Domain flow (GH #1541), plus the mail
// module gating (GH #1409) and the create-time document root (GH #1413), both
// reworked by #1541: mail is now an "Add Mail Domain" checkbox and the document
// root moved under a collapsed "Advanced" section.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const caps = vi.hoisted(() => ({ mail: false, dns: true }));
const mutate = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { mail_enabled: caps.mail, dns_enabled: caps.dns } }),
}));
vi.mock("../../../hooks/useQueries", () => ({
  useCreateMutation: () => ({ mutateAsync: mutate, isPending: false }),
}));
// Avoid needing an <App> context for the themed toasts / result modal.
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
    modal: { success: vi.fn(), confirm: vi.fn() },
  },
}));

import { UserDomainDrawer } from "./UserDomainDrawer";

function renderDrawer(mode: "web" | "dns" | "mail" = "web") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <UserDomainDrawer open mode={mode} onClose={() => {}} />
    </QueryClientProvider>,
  );
}

const submit = () => fireEvent.click(screen.getByRole("button", { name: "Add" }));
const typeName = async (name: string) => {
  fireEvent.change(await screen.findByPlaceholderText("e.g., example.com"), {
    target: { value: name },
  });
};

beforeEach(() => {
  caps.mail = false;
  caps.dns = true;
  mutate.mockReset();
  mutate.mockResolvedValue({ id: "d1" });
});

describe("UserDomainDrawer Add Mail Domain (GH #1409/#1541)", () => {
  it("checks Add Mail Domain by default when the mail module IS installed; DNS Template hidden", async () => {
    caps.mail = true;
    renderDrawer();
    const cb = await screen.findByRole("checkbox", { name: /add mail domain/i });
    expect(cb).toBeChecked();
    expect(cb).not.toBeDisabled();
    // The external-mail DNS Template is only for the unchecked case.
    expect(screen.queryByText("DNS Template")).not.toBeInTheDocument();
  });

  it("unchecks + disables Add Mail Domain and shows the DNS Template when mail is NOT installed", async () => {
    caps.mail = false;
    renderDrawer();
    const cb = await screen.findByRole("checkbox", { name: /add mail domain/i });
    await waitFor(() => expect(cb).not.toBeChecked());
    expect(cb).toBeDisabled();
    expect(screen.getByText("DNS Template")).toBeInTheDocument();
  });

  it("unchecking Add Mail Domain reveals the DNS Template select", async () => {
    caps.mail = true;
    renderDrawer();
    const cb = await screen.findByRole("checkbox", { name: /add mail domain/i });
    expect(screen.queryByText("DNS Template")).not.toBeInTheDocument();
    fireEvent.click(cb);
    await waitFor(() => expect(screen.getByText("DNS Template")).toBeInTheDocument());
  });
});

describe("UserDomainDrawer document root under Advanced (GH #1413/#1541)", () => {
  it("keeps Document root out of the default form, shows it under Advanced, hides it for a reverse proxy", async () => {
    caps.mail = true;
    renderDrawer();
    // Not part of the simple form.
    expect(screen.queryByLabelText("Document root")).not.toBeInTheDocument();
    // Expand Advanced → the field registers and renders.
    fireEvent.click(await screen.findByText("Advanced"));
    await waitFor(() => expect(screen.getByLabelText("Document root")).toBeInTheDocument());
    // A reverse-proxy domain has no docroot — toggling it removes the field.
    fireEvent.click(screen.getByRole("checkbox", { name: /set up as a reverse proxy/i }));
    await waitFor(() =>
      expect(screen.queryByLabelText("Document root")).not.toBeInTheDocument(),
    );
  });
});

describe("UserDomainDrawer web create payload (GH #1541)", () => {
  it("Add Mail Domain checked → Jabali mail, auto-www for an apex, no drawer-only fields", async () => {
    caps.mail = true;
    caps.dns = true;
    renderDrawer();
    await typeName("example.com");
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.mail_provider).toBe("jabali");
    expect(body.manage_dns).toBe(true);
    expect(body.create_www).toBe(true); // example.com is an apex (two labels)
    // Drawer-only / dropped fields never reach POST /domains.
    expect(body).not.toHaveProperty("add_mail");
    expect(body).not.toHaveProperty("enable_webmail");
    expect(body).not.toHaveProperty("temp_url_enabled");
  });

  it("Add Mail Domain unchecked (external mail None) → no Jabali mail, and a subdomain gets no www", async () => {
    caps.mail = true;
    caps.dns = true;
    renderDrawer();
    await typeName("blog.example.com");
    fireEvent.click(await screen.findByRole("checkbox", { name: /add mail domain/i }));
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.mail_provider).toBe("none"); // DNS Template default
    expect(body.create_www).toBe(false); // blog.example.com is a subdomain (three labels)
  });

  it("forces manage_dns false when the DNS module is off (no Add DNS Zone checkbox)", async () => {
    caps.mail = true;
    caps.dns = false;
    renderDrawer();
    expect(screen.queryByText("Add DNS Zone")).not.toBeInTheDocument();
    await typeName("example.org");
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    expect(mutate.mock.calls[0][0].manage_dns).toBe(false);
  });
});
