// DomainAliasesPanel.test — GH #1625. Each alias row shows the first wait that
// applies: an unverified alias its TXT record (never the A-record hint), a
// verified one not on the certificate yet what to point here, a live one
// "On the certificate". Approve / Revoke are admin-only.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), delete: vi.fn() }));
vi.mock("../../apiClient", () => ({ apiClient: api }));
const msg = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
const modal = vi.hoisted(() => ({ confirm: vi.fn() }));
vi.mock("../../lib/feedback", () => ({ feedback: { message: msg, modal } }));

import { DomainAliasesPanel, type DomainAlias } from "./DomainAliasesPanel";

const A_HINT = "point an A/AAAA record for this hostname directly at 203.0.113.9";

const own = (status: string, extra: Partial<DomainAlias["ownership"]> = {}) => ({
  status,
  method: status === "verified" ? "parent" : "",
  challenge_name: "",
  challenge_value: "",
  last_result: "",
  ...extra,
});

const aliases: DomainAlias[] = [
  {
    id: "a1",
    domain_id: "d1",
    hostname: "shop.brandy.io",
    cert_status: "pending",
    pending_reason: A_HINT,
    ownership: own("pending", {
      challenge_name: "_jabali-challenge.shop.brandy.io",
      challenge_value: "jabali-verify=tok1",
      last_result: "not_found",
    }),
    created_at: "2026-11-08T10:00:00Z",
  },
  {
    id: "a2",
    domain_id: "d1",
    hostname: "blog.site.tld",
    cert_status: "pending",
    pending_reason: A_HINT,
    ownership: own("verified"),
    created_at: "2026-11-08T10:00:00Z",
  },
  {
    id: "a3",
    domain_id: "d1",
    hostname: "site.net",
    cert_status: "active",
    ownership: own("verified", { method: "dns_txt" }),
    created_at: "2026-11-08T10:00:00Z",
  },
];

function renderPanel(admin = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DomainAliasesPanel domain={{ id: "d1", name: "site.tld" }} admin={admin} />
    </QueryClientProvider>,
  );
}

const row = async (hostname: string) => (await screen.findByText(hostname)).closest("tr") as HTMLElement;

beforeEach(() => {
  api.get.mockReset().mockResolvedValue({ data: { data: aliases, total: aliases.length } });
  api.post.mockReset().mockResolvedValue({ data: { result: "not_found" } });
  api.delete.mockReset().mockResolvedValue({ data: { deleted: true } });
  Object.values(msg).forEach((f) => f.mockReset());
  modal.confirm.mockReset();
});

describe("DomainAliasesPanel (GH #1625)", () => {
  it("shows an unverified alias's TXT record, not the A-record hint, and runs Verify now", async () => {
    renderPanel();
    const r = await row("shop.brandy.io");
    expect(api.get).toHaveBeenCalledWith("/domains/d1/aliases");
    expect(within(r).getByText("Unverified")).toBeInTheDocument();
    expect(within(r).getByText("_jabali-challenge.shop.brandy.io")).toBeInTheDocument();
    expect(within(r).getByText("jabali-verify=tok1")).toBeInTheDocument();
    expect(within(r).getByText(/No record found yet/)).toBeInTheDocument();
    expect(within(r).queryByText(A_HINT)).not.toBeInTheDocument();

    fireEvent.click(within(r).getByRole("button", { name: /Verify now/ }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith("/domains/d1/aliases/a1/ownership/verify"));
    await waitFor(() => expect(msg.info).toHaveBeenCalled());
  });

  it("shows the certificate hint for a verified alias not on the certificate, and nothing to do for a live one", async () => {
    renderPanel();
    const pending = await row("blog.site.tld");
    expect(within(pending).getByText("Certificate pending")).toBeInTheDocument();
    expect(within(pending).getByText(A_HINT)).toBeInTheDocument();
    expect(within(pending).queryByRole("button", { name: /Verify now/ })).not.toBeInTheDocument();

    const live = await row("site.net");
    expect(within(live).getByText("Active")).toBeInTheDocument();
    expect(within(live).getByText("On the certificate.")).toBeInTheDocument();
  });

  it("adds an alias by its trimmed hostname and points at the TXT record when it is unverified", async () => {
    api.post.mockResolvedValueOnce({ data: { ...aliases[0], id: "a9", hostname: "new.brandy.io" } });
    renderPanel();
    await row("shop.brandy.io");
    fireEvent.change(screen.getByLabelText("Alias hostname"), { target: { value: "  new.brandy.io " } });
    fireEvent.click(screen.getByRole("button", { name: /Add alias/ }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith("/domains/d1/aliases", { hostname: "new.brandy.io" }));
    await waitFor(() => expect(msg.info).toHaveBeenCalledWith(expect.stringMatching(/TXT record/)));
  });

  it("shows the server's reason when an alias is refused", async () => {
    api.post.mockRejectedValueOnce(
      Object.assign(new Error("Request failed"), {
        isAxiosError: true,
        response: { status: 400, data: { error: "alias_is_www", detail: "enable www via the domain's www option, not as an alias" } },
      }),
    );
    renderPanel();
    await row("shop.brandy.io");
    fireEvent.change(screen.getByLabelText("Alias hostname"), { target: { value: "www.site.tld" } });
    fireEvent.click(screen.getByRole("button", { name: /Add alias/ }));
    await waitFor(() =>
      expect(msg.error).toHaveBeenCalledWith(expect.stringContaining("enable www via the domain's www option")),
    );
  });

  it("gives a tenant no Approve or Revoke", async () => {
    renderPanel();
    await row("shop.brandy.io");
    expect(screen.queryByRole("button", { name: /Approve/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument();
  });

  it("gives an admin Approve on an unverified alias and Revoke on a verified one", async () => {
    renderPanel(true);
    const unverified = await row("shop.brandy.io");
    fireEvent.click(within(unverified).getByRole("button", { name: /Approve/ }));
    expect(modal.confirm).toHaveBeenCalledTimes(1);
    await modal.confirm.mock.calls[0][0].onOk();
    expect(api.post).toHaveBeenCalledWith("/admin/domain-ownership/aliases/a1/approve");

    const verified = await row("site.net");
    expect(within(verified).queryByRole("button", { name: /Approve/ })).not.toBeInTheDocument();
    expect(within(verified).getByRole("button", { name: /Revoke/ })).toBeInTheDocument();
    expect(within(verified).getByText("by DNS record")).toBeInTheDocument();
  });
});
