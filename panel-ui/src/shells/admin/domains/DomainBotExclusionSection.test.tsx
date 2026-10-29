// DomainBotExclusionSection.test.tsx — GH #2328. The admin lists, adds and
// removes paths of a domain that skip the bot-detection challenge.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";
import { DomainBotExclusionSection } from "./DomainBotExclusionSection";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

const ROWS = [
  { id: "e1", domain_id: "d1", path_prefix: "/api/hook", methods: ["POST"], note: "payments", created_at: "" },
  { id: "e2", domain_id: "d1", path_prefix: "/feed", methods: [], note: "", created_at: "" },
];

beforeEach(() => {
  vi.clearAllMocks();
  mocked.get.mockResolvedValue({ data: { data: ROWS, total: 2 } });
  mocked.post.mockResolvedValue({ data: ROWS[0] });
  mocked.delete.mockResolvedValue({ data: {} });
});

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainBotExclusionSection domainId="d1" domainName="shop.example" />
      </App>
    </QueryClientProvider>,
  );
}

describe("DomainBotExclusionSection (GH #2328)", () => {
  it("lists the paths, with their methods or Any", async () => {
    renderSection();
    expect(await screen.findByText("/api/hook")).toBeTruthy();
    expect(screen.getByText("POST")).toBeTruthy();
    expect(screen.getByText("/feed")).toBeTruthy();
    expect(screen.getByText("Any")).toBeTruthy();
    expect(mocked.get).toHaveBeenCalledWith("/domains/d1/bot-challenge-exclusions");
  });

  it("adds a path trimmed, every method when none is picked", async () => {
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: /Add path/ }));
    fireEvent.change(screen.getByPlaceholderText("/api/webhook"), { target: { value: " /webhook " } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    expect(mocked.post).toHaveBeenCalledWith("/domains/d1/bot-challenge-exclusions", {
      path_prefix: "/webhook",
      methods: [],
      note: "",
    });
  });

  it("shows the API's reason when a path is refused", async () => {
    mocked.post.mockRejectedValue({ response: { data: { error: 'path "/a/../b" must not contain ..' } } });
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: /Add path/ }));
    fireEvent.change(screen.getByPlaceholderText("/api/webhook"), { target: { value: "/a/../b" } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));
    expect(await screen.findByText('path "/a/../b" must not contain ..')).toBeTruthy();
  });
});
