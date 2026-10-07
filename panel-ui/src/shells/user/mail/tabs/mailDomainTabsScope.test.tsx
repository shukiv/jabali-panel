// GH #1997: a mail domain's Forwarders tab listed the forwarders of every
// mail domain in the account, and its Shared Folders tab every share. The
// tabs now ask the API for their domain's rows (?domain_id=), and the hooks
// read every page instead of the API's default first 50 rows.
//
// The mocked API answers as the real one does: without domain_id it lists
// the whole account; without page_size it answers 50 rows a page.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ForwardersTab } from "./ForwardersTab";
import { SharedFoldersTab } from "./SharedFoldersTab";
import { useForwarders } from "../../../../hooks/useForwarders";

vi.mock("../../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../../apiClient";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

const DOMAINS = [
  { id: "d1", name: "one.test", email_enabled: true },
  { id: "d2", name: "two.test", email_enabled: true },
];
const nameOf = (id: string) => DOMAINS.find((d) => d.id === id)?.name ?? "";

const fwd = (id: string, domainId: string, localPart: string, mailbox: string) => ({
  id,
  mailbox_id: `mb-${mailbox}-${domainId}`,
  mailbox_email: `${mailbox}@${nameOf(domainId)}`,
  domain_id: domainId,
  domain_name: nameOf(domainId),
  type: "alias",
  local_part: localPart,
  target: "",
  keep_copy: false,
  enabled: true,
  created_at: "2026-10-07T00:00:00Z",
});

const share = (id: string, owner: string, withEmail: string) => ({
  id,
  owner_mailbox_id: `mb-${owner}`,
  owner_mailbox_email: owner,
  shared_with_mailbox_id: `mb-${withEmail}`,
  shared_with_mailbox_email: withEmail,
  rights: { mayRead: true },
  created_at: "2026-10-07T00:00:00Z",
});

// page answers a list request the way the panel API does.
function page<T>(url: string, rows: T[], inDomain: (row: T, domainId: string) => boolean) {
  const u = new URL(url, "http://panel.test");
  const domainId = u.searchParams.get("domain_id");
  const size = Math.min(Number(u.searchParams.get("page_size") ?? 50), 200);
  const n = Number(u.searchParams.get("page") ?? 1);
  const all = domainId ? rows.filter((r) => inDomain(r, domainId)) : rows;
  return { data: { data: all.slice((n - 1) * size, n * size), total: all.length, page: n, page_size: size } };
}

function serve(opts: { forwarders?: ReturnType<typeof fwd>[]; shares?: ReturnType<typeof share>[] }) {
  const imported = [
    { id: "i1", domain_id: "d1", domain_name: "one.test", type: "alias", local_part: "old", target: "x@out.test", enabled: true, managed_by: "da-import", created_at: "" },
    { id: "i2", domain_id: "d2", domain_name: "two.test", type: "alias", local_part: "legacy", target: "y@out.test", enabled: true, managed_by: "da-import", created_at: "" },
  ];
  mocked.get.mockImplementation((url: string) => {
    if (url.startsWith("/domains?")) return Promise.resolve({ data: { data: DOMAINS, total: DOMAINS.length } });
    const mbx = url.match(/^\/domains\/(d\d)\/mailboxes\?/);
    if (mbx) {
      const dom = nameOf(mbx[1]);
      return Promise.resolve({
        data: { data: [{ id: `mb-info-${mbx[1]}`, email: `info@${dom}`, domain_id: mbx[1] }], total: 1 },
      });
    }
    if (url.startsWith("/mail/forwarders/domain-scoped")) {
      const r = page(url, imported, (row, d) => row.domain_id === d);
      return Promise.resolve({ data: { data: r.data.data } });
    }
    if (url.startsWith("/mail/forwarders")) {
      return Promise.resolve(page(url, opts.forwarders ?? [], (row, d) => row.domain_id === d));
    }
    if (url.startsWith("/mail/shares")) {
      const touches = (row: ReturnType<typeof share>, d: string) =>
        [row.owner_mailbox_email, row.shared_with_mailbox_email].some((e) => e.endsWith(`@${nameOf(d)}`));
      return Promise.resolve(page(url, opts.shares ?? [], touches));
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <App>{children}</App>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  mocked.get.mockReset();
});

describe("mail domain tabs list their own domain only (GH #1997)", () => {
  it("Forwarders lists this domain's forwarders and imported aliases, not another domain's", async () => {
    serve({ forwarders: [fwd("f1", "d1", "sales", "info"), fwd("f2", "d2", "sales", "info")] });
    render(<ForwardersTab domainId="d1" />, { wrapper });

    expect(await screen.findByText("sales@one.test")).toBeTruthy();
    expect(await screen.findByText("old@one.test")).toBeTruthy();
    expect(screen.queryByText("sales@two.test")).toBeNull();
    expect(screen.queryByText("legacy@two.test")).toBeNull();
  });

  it("Shared Folders lists the shares that involve this domain, not another domain's", async () => {
    serve({
      shares: [
        share("s1", "info@one.test", "boss@one.test"),
        share("s2", "info@two.test", "boss@two.test"),
        share("s3", "info@one.test", "team@two.test"),
      ],
    });
    render(<SharedFoldersTab domainId="d1" />, { wrapper });

    expect(await screen.findByText("boss@one.test")).toBeTruthy();
    expect(await screen.findByText("team@two.test")).toBeTruthy(); // shared from one.test
    expect(screen.queryByText("boss@two.test")).toBeNull();
  });

  it("useForwarders reads every page, not only the API's first 50 rows", async () => {
    const many = Array.from({ length: 260 }, (_, i) => fwd(`f${i}`, "d1", `a${i}`, "info"));
    serve({ forwarders: many });
    const { result } = renderHook(() => useForwarders(), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toHaveLength(260);
  });
});
