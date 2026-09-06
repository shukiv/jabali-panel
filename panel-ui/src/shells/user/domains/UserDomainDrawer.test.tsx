// UserDomainDrawer — the tenant Add Web Domain flow (GH #1541), plus the mail
// module gating (GH #1409) and the create-time document root (GH #1413), both
// reworked by #1541: mail is now an "Add Mail Domain" checkbox and the document
// root moved under a collapsed "Advanced" section.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const caps = vi.hoisted(() => ({ mail: false, dns: true, ipv4: "192.0.2.1", ipv6: "2001:db8::1" }));
const mutate = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({
    data: {
      mail_enabled: caps.mail,
      dns_enabled: caps.dns,
      public_ipv4: caps.ipv4,
      public_ipv6: caps.ipv6,
    },
  }),
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
  caps.ipv4 = "192.0.2.1";
  caps.ipv6 = "2001:db8::1";
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

describe("UserDomainDrawer dns mode — Add DNS Zone (GH #1540)", () => {
  it("prefills the apex IPv4 + IPv6 with the panel IPs and sends a web-off DNS-only payload", async () => {
    renderDrawer("dns");
    // IP Address is prefilled with the panel's public IPv4.
    const ip = (await screen.findByPlaceholderText("e.g., 203.0.113.10")) as HTMLInputElement;
    expect(ip.value).toBe("192.0.2.1");
    // The optional IPv6 field is prefilled with the panel's public IPv6.
    const ip6 = (await screen.findByPlaceholderText("e.g., 2001:db8::1")) as HTMLInputElement;
    expect(ip6.value).toBe("2001:db8::1");
    await typeName("zone.example.com");
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.web_enabled).toBe(false);
    expect(body.manage_dns).toBe(true);
    expect(body.ssl_mode).toBe("none");
    expect(body.ip_address).toBe("192.0.2.1");
    expect(body.ip6_address).toBe("2001:db8::1");
    // Default template ⇒ no mail records.
    expect(body.mail_provider).toBe("none");
  });

  it("leaves the AAAA out when the server has no IPv6 and the field is blank", async () => {
    caps.ipv6 = "";
    renderDrawer("dns");
    const ip6 = (await screen.findByPlaceholderText("e.g., 2001:db8::1")) as HTMLInputElement;
    expect(ip6.value).toBe("");
    await typeName("zone.example.com");
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.ip_address).toBe("192.0.2.1");
    expect(body.ip6_address).toBeFalsy();
  });

  it("blocks submit on an invalid apex IPv6", async () => {
    renderDrawer("dns");
    const ip6 = await screen.findByPlaceholderText("e.g., 2001:db8::1");
    fireEvent.change(ip6, { target: { value: "1.2.3.4" } });
    await typeName("zone.example.com");
    submit();
    await screen.findByText("Enter a valid IPv6 address (e.g. 2001:db8::1)");
    expect(mutate).not.toHaveBeenCalled();
  });

  it("Template Microsoft 365 maps to mail_provider m365 with the tenant helper", async () => {
    renderDrawer("dns");
    await typeName("zone.example.com");
    // Open the Template select and pick Microsoft 365.
    fireEvent.mouseDown(screen.getByRole("combobox"));
    fireEvent.click(await screen.findByText("Microsoft 365"));
    fireEvent.change(await screen.findByPlaceholderText("contoso.onmicrosoft.com (optional)"), {
      target: { value: "contoso.onmicrosoft.com" },
    });
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.mail_provider).toBe("m365");
    expect(body.m365_onmicrosoft).toBe("contoso.onmicrosoft.com");
    expect(body.web_enabled).toBe(false);
    expect(body.ip_address).toBe("192.0.2.1");
  });

  it("prefills the apex IP on reopen (survives the drawer's resetFields)", async () => {
    // The real "add a second zone" flow: open → close → open. destroyOnClose
    // remounts the form body in the same commit as the open flip, so the child
    // prefill effect must not lose to the drawer's resetFields effect.
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrap = (open: boolean) => (
      <QueryClientProvider client={qc}>
        <UserDomainDrawer open={open} mode="dns" onClose={() => {}} />
      </QueryClientProvider>
    );
    const { rerender } = render(wrap(false));
    rerender(wrap(true));
    await screen.findByPlaceholderText("e.g., 203.0.113.10");
    rerender(wrap(false));
    rerender(wrap(true));
    const ip = (await screen.findByPlaceholderText("e.g., 203.0.113.10")) as HTMLInputElement;
    expect(ip.value).toBe("192.0.2.1");
    const ip6 = (await screen.findByPlaceholderText("e.g., 2001:db8::1")) as HTMLInputElement;
    expect(ip6.value).toBe("2001:db8::1");
  });

  it("blocks submit on an invalid apex IP", async () => {
    renderDrawer("dns");
    const ip = await screen.findByPlaceholderText("e.g., 203.0.113.10");
    fireEvent.change(ip, { target: { value: "not-an-ip" } });
    await typeName("zone.example.com");
    submit();
    // Client-side IPv4 validation stops the create.
    await screen.findByText("Enter a valid IPv4 address (e.g. 203.0.113.10)");
    expect(mutate).not.toHaveBeenCalled();
  });
});
