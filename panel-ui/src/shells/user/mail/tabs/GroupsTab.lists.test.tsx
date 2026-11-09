// GH #1833: distribution lists have their own Mail tab, Groups keeps the
// Shared Workspace groups, and a list's members editor takes outside
// addresses up to the admin's cap.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../../apiClient";
import { GroupsTab, MembersModal } from "./GroupsTab";
import type { MailGroup } from "../../../../hooks/useMailGroups";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
};

const group = (id: string, local: string, kind: string, external = 0): MailGroup => ({
  id,
  domain_id: "d1",
  local_part: local,
  email: `${local}@one.test`,
  display_name: "",
  description: "",
  group_kind: kind,
  has_mailbox: true,
  has_calendar: true,
  has_addressbook: true,
  has_files: true,
  internal_only: false,
  member_count: 1,
  external_member_count: external,
  created_at: "",
  updated_at: "",
});

const LIST = group("g1", "sales", "distribution", 2);
const WORKSPACE = group("g2", "team", "resource");

function serve(detail: { external_members?: string[]; external_member_cap?: number; group?: MailGroup } = {}) {
  mocked.get.mockImplementation((url: string) => {
    if (url.startsWith("/domains?")) {
      return Promise.resolve({ data: { data: [{ id: "d1", name: "one.test", email_enabled: true }], total: 1 } });
    }
    if (url === "/domains/d1/mailgroups") return Promise.resolve({ data: { data: [LIST, WORKSPACE] } });
    if (url.startsWith("/domains/d1/mailboxes")) {
      return Promise.resolve({ data: { data: [{ id: "mb1", email: "info@one.test", domain_id: "d1" }], total: 1 } });
    }
    const one = url.match(/^\/mailgroups\/(g\d)$/);
    if (one) {
      const g = detail.group ?? LIST;
      return Promise.resolve({
        data: {
          ...g,
          members: [{ mailbox_id: "mb1", email: "info@one.test" }],
          external_members: detail.external_members ?? [],
          external_member_cap: detail.external_member_cap ?? 20,
        },
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  mocked.put.mockResolvedValue({ data: { data: {} } });
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
  mocked.put.mockReset();
});

describe("Distribution lists and Groups tabs (GH #1833)", () => {
  it("the Distribution lists tab shows lists only, with their outside addresses", async () => {
    serve();
    render(<GroupsTab domainId="d1" kind="distribution" />, { wrapper });

    expect(await screen.findByText("sales@one.test")).toBeTruthy();
    expect(screen.queryByText("team@one.test")).toBeNull();
    expect(screen.getByText("+2 outside")).toBeTruthy();
    expect(screen.getByRole("button", { name: /New distribution list/ })).toBeTruthy();
  });

  it("the Groups tab shows Shared Workspace groups only", async () => {
    serve();
    render(<GroupsTab domainId="d1" kind="resource" />, { wrapper });

    expect(await screen.findByText("team@one.test")).toBeTruthy();
    expect(screen.queryByText("sales@one.test")).toBeNull();
  });
});

const outsideInput = () =>
  within(screen.getByLabelText("Outside addresses").closest(".ant-select") as HTMLElement).getByRole("combobox");

const typeOutside = (text: string) => fireEvent.change(outsideInput(), { target: { value: text } });

const save = () => fireEvent.click(screen.getByRole("button", { name: "Save" }));

describe("MembersModal outside addresses (GH #1833)", () => {
  it("shows a list's outside addresses and the cap", async () => {
    serve({ external_members: ["partner@gmail.com"], external_member_cap: 10 });
    render(<MembersModal group={LIST} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText("partner@gmail.com")).toBeTruthy();
    expect(screen.getByText("1 of 10 outside addresses · 2 of 20 recipients in all")).toBeTruthy();
    expect(screen.getByText("Mail from outside senders can land in spam")).toBeTruthy();
  });

  // A list of mailboxes only, open to any sender, has no recipient limit.
  it("shows no recipient limit for a list of mailboxes only", async () => {
    serve();
    render(<MembersModal group={LIST} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText("0 of 20 outside addresses")).toBeTruthy();
    expect(screen.queryByText(/recipients in all/)).toBeNull();
  });

  it("shows the recipient limit for an internal-only list", async () => {
    const internal = { ...LIST, internal_only: true };
    serve({ group: internal });
    render(<MembersModal group={internal} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText("0 of 20 outside addresses · 1 of 20 recipients in all")).toBeTruthy();
  });

  it("saves a new outside address with the list", async () => {
    serve({ external_members: ["partner@gmail.com"] });
    const onClose = vi.fn();
    render(<MembersModal group={LIST} onClose={onClose} />, { wrapper });
    await screen.findByText("partner@gmail.com");

    typeOutside("new@outlook.com,");
    await screen.findByText("new@outlook.com");
    save();

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/mailgroups/g1/external-members", {
        addresses: ["partner@gmail.com", "new@outlook.com"],
      }),
    );
    expect(mocked.put).toHaveBeenCalledWith("/mailgroups/g1/members", { mailbox_ids: ["mb1"] });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("Enter adds the typed address", async () => {
    serve();
    render(<MembersModal group={LIST} onClose={() => {}} />, { wrapper });
    await screen.findByLabelText("Outside addresses");

    typeOutside("typed@gmail.com");
    fireEvent.keyDown(outsideInput(), { key: "Enter", code: "Enter", keyCode: 13, which: 13 });
    expect(await screen.findByText("1 of 20 outside addresses · 2 of 20 recipients in all")).toBeTruthy();
  });

  it("does not save a typo", async () => {
    serve();
    render(<MembersModal group={LIST} onClose={() => {}} />, { wrapper });
    await screen.findByLabelText("Outside addresses");

    typeOutside("not-an-address,");
    await screen.findByText(/not an email address: not-an-address/);
    save();

    await new Promise((r) => setTimeout(r, 50));
    expect(mocked.put).not.toHaveBeenCalled();
  });

  it("leaves outside addresses alone when only mailboxes change", async () => {
    serve({ external_members: ["partner@gmail.com"] });
    render(<MembersModal group={LIST} onClose={() => {}} />, { wrapper });
    await screen.findByText("partner@gmail.com");

    save();
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith("/mailgroups/g1/members", { mailbox_ids: ["mb1"] }));
    expect(mocked.put).not.toHaveBeenCalledWith("/mailgroups/g1/external-members", expect.anything());
  });

  it("says so when the administrator allows none", async () => {
    serve({ external_member_cap: 0 });
    render(<MembersModal group={LIST} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText("Your administrator does not allow outside addresses on lists.")).toBeTruthy();
    expect(screen.queryByLabelText("Outside addresses")).toBeNull();
  });

  it("a Shared Workspace group has no outside addresses", async () => {
    serve({ group: WORKSPACE });
    render(<MembersModal group={WORKSPACE} onClose={() => {}} />, { wrapper });

    expect(await screen.findByLabelText("Member mailboxes")).toBeTruthy();
    expect(screen.queryByText("Outside addresses")).toBeNull();
  });
});
