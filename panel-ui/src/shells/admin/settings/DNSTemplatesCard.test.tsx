// DNSTemplatesCard — GH #1627. Admin CRUD for custom DNS templates. These tests
// cover the list render (with the record-count tag), the create round-trip
// including a record row, and the key UX guarantee: when the API rejects a
// record, the admin sees the exact reason (the 400 `detail` from
// ValidateDNSRecord, e.g. "record 1: ..."), not a generic toast.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const errorToast = vi.hoisted(() => vi.fn());
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: vi.fn(), error: errorToast, warning: vi.fn() },
    modal: { success: vi.fn(), confirm: vi.fn() },
  },
}));

import { apiClient } from "../../../apiClient";
import { DNSTemplatesCard } from "./DNSTemplatesCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DNSTemplatesCard />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockGet.mockResolvedValue({
    data: {
      templates: [
        {
          id: "t1",
          name: "Acme SaaS",
          description: "Acme apex + mail",
          records: [{ id: "r1", name: "@", type: "MX", content: "mail.acme.test", ttl: 3600, priority: 10 }],
        },
      ],
    },
  });
});

// Fill the create modal's name, add one record row, fill its value, then Save.
async function createTemplate(content: string) {
  fireEvent.click(screen.getByRole("button", { name: /new template/i }));
  const dialog = await screen.findByRole("dialog");
  const nameInput = await within(dialog).findByPlaceholderText("e.g. Acme SaaS");
  fireEvent.change(nameInput, { target: { value: "MyApp" } });
  // Add a record row, then fill its value (type defaults to A, ttl to 3600).
  fireEvent.click(within(dialog).getByRole("button", { name: /add record/i }));
  const valueInput = await within(dialog).findByPlaceholderText(/value \(e\.g/);
  fireEvent.change(valueInput, { target: { value: content } });
  await waitFor(() => expect(within(dialog).getByDisplayValue("MyApp")).toBeInTheDocument());
  await waitFor(() => expect(within(dialog).getByDisplayValue(content)).toBeInTheDocument());
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
}

describe("DNSTemplatesCard", () => {
  it("lists templates from the API with a record-count tag", async () => {
    renderCard();
    expect(await screen.findByText("Acme SaaS")).toBeInTheDocument();
    expect(await screen.findByText("1 record")).toBeInTheDocument();
  });

  it("creates a template via POST /admin/dns-templates with its records", async () => {
    mockPost.mockResolvedValue({ data: { id: "t2", name: "MyApp", records: [] } });
    renderCard();
    await screen.findByText("Acme SaaS");
    await createTemplate("1.2.3.4");
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    expect(mockPost).toHaveBeenCalledWith(
      "/admin/dns-templates",
      expect.objectContaining({
        name: "MyApp",
        records: [expect.objectContaining({ type: "A", content: "1.2.3.4", ttl: 3600 })],
      }),
    );
  });

  it("surfaces the API's per-record rejection detail (not a generic toast)", async () => {
    mockPost.mockRejectedValue({ response: { data: { detail: "record 1: unsupported record type" } } });
    renderCard();
    await screen.findByText("Acme SaaS");
    await createTemplate("not-an-ip");
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("record 1: unsupported record type"));
  });
});
