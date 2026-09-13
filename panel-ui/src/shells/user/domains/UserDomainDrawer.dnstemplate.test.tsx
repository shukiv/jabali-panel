// UserDomainDrawer — custom DNS template picker (GH #1627). PR-1 wired
// dns_template_id through POST /domains; this is the tenant-facing selection.
// The admin-defined templates are appended to the existing provider <Select> in
// both the Add Web Domain (external-mail case) and Add DNS Zone forms. Picking
// one must send dns_template_id and OMIT mail_provider — createDomainOp rejects a
// template alongside an explicit provider (template_and_provider_exclusive).
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const caps = vi.hoisted(() => ({ mail: true, dns: true, ipv4: "192.0.2.1", ipv6: "2001:db8::1" }));
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
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
    modal: { success: vi.fn(), confirm: vi.fn() },
  },
}));
// Stub only the network fetch — keep the real splitTemplateSelection /
// templateOptionGroup so the option renders and the payload mapping is exercised.
vi.mock("../../../components/dns/dnsTemplates", async (importActual) => {
  const actual = await importActual<typeof import("../../../components/dns/dnsTemplates")>();
  return { ...actual, useDNSTemplates: () => ({ data: [{ id: "tmpl-9", name: "Acme SaaS" }] }) };
});

import { UserDomainDrawer } from "./UserDomainDrawer";
import { splitTemplateSelection } from "../../../components/dns/dnsTemplates";

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
  caps.mail = true;
  caps.dns = true;
  mutate.mockReset();
  mutate.mockResolvedValue({ id: "d1" });
});

describe("splitTemplateSelection (GH #1627)", () => {
  it("maps a tmpl: value to dns_template_id and omits mail_provider", () => {
    expect(splitTemplateSelection("tmpl:abc")).toEqual({ dns_template_id: "abc" });
  });
  it("passes a provider value through as mail_provider", () => {
    expect(splitTemplateSelection("m365")).toEqual({ mail_provider: "m365" });
  });
});

describe("UserDomainDrawer custom DNS template picker (GH #1627)", () => {
  it("web: picking a custom template sends dns_template_id and omits mail_provider", async () => {
    renderDrawer("web");
    await typeName("shop.example.com");
    // Uncheck Add Mail Domain to reveal the DNS Template select (external mail).
    fireEvent.click(await screen.findByRole("checkbox", { name: /add mail domain/i }));
    // Open that select (target it by its current "None" display) and pick the template.
    fireEvent.mouseDown(await screen.findByText("None (no external mail)"));
    fireEvent.click(await screen.findByText("Acme SaaS"));
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.dns_template_id).toBe("tmpl-9");
    expect(body.mail_provider).toBeUndefined();
    expect(body.manage_dns).toBe(true);
  });

  it("dns: picking a custom template sends dns_template_id and omits mail_provider", async () => {
    renderDrawer("dns");
    await typeName("zone.example.com");
    fireEvent.mouseDown(screen.getByRole("combobox"));
    fireEvent.click(await screen.findByText("Acme SaaS"));
    submit();
    await waitFor(() => expect(mutate).toHaveBeenCalled());
    const body = mutate.mock.calls[0][0];
    expect(body.dns_template_id).toBe("tmpl-9");
    expect(body.mail_provider).toBeUndefined();
    expect(body.web_enabled).toBe(false);
    expect(body.manage_dns).toBe(true);
  });

  it("web: a custom template is disabled while Add DNS Zone is unchecked (template_requires_dns)", async () => {
    renderDrawer("web");
    await typeName("shop.example.com");
    fireEvent.click(await screen.findByRole("checkbox", { name: /add mail domain/i }));
    // Uncheck Add DNS Zone → the template option must be disabled.
    fireEvent.click(await screen.findByRole("checkbox", { name: /add dns zone/i }));
    fireEvent.mouseDown(await screen.findByText("None (no external mail)"));
    const option = await screen.findByText("Acme SaaS");
    const optionEl = option.closest(".ant-select-item-option");
    expect(optionEl?.className).toContain("ant-select-item-option-disabled");
  });
});
