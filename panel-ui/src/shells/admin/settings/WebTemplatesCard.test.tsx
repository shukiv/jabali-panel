// WebTemplatesCard — GH #1624 / ADR-0169 Phase 3b. Admin CRUD for web (nginx)
// templates. These tests cover the list render, the create round-trip, and the
// key UX guarantee: when the API rejects a directive, the admin sees the exact
// reason (the 400 `detail` from ValidateNginxDirectivesAdmin), not a generic
// toast.
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
import { WebTemplatesCard } from "./WebTemplatesCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <WebTemplatesCard />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockGet.mockResolvedValue({
    data: {
      templates: [
        { id: "t1", name: "WordPress", description: "WP rewrites", nginx_directives: "add_header X-A b;" },
      ],
    },
  });
});

// Fill the create modal's required fields, then click Save.
async function createTemplate(directives: string) {
  fireEvent.click(screen.getByRole("button", { name: /new template/i }));
  const dialog = await screen.findByRole("dialog");
  const nameInput = await within(dialog).findByPlaceholderText("e.g. WordPress");
  fireEvent.change(nameInput, { target: { value: "MyApp" } });
  // The directives textarea placeholder carries a `proxy_pass` example.
  const directivesInput = within(dialog).getByPlaceholderText(/proxy_pass/);
  fireEvent.change(directivesInput, { target: { value: directives } });
  // Let antd's Form commit both field values before validateFields runs on Save.
  await waitFor(() => expect(within(dialog).getByDisplayValue("MyApp")).toBeInTheDocument());
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
}

describe("WebTemplatesCard", () => {
  it("lists templates from the API", async () => {
    renderCard();
    expect(await screen.findByText("WordPress")).toBeInTheDocument();
  });

  it("creates a template via POST /admin/web-templates", async () => {
    mockPost.mockResolvedValue({ data: { id: "t2", name: "MyApp" } });
    renderCard();
    await screen.findByText("WordPress");
    await createTemplate("add_header X-B c;");
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    expect(mockPost).toHaveBeenCalledWith(
      "/admin/web-templates",
      expect.objectContaining({ name: "MyApp", nginx_directives: "add_header X-B c;" }),
    );
  });

  it("surfaces the API's rejection detail (not a generic toast)", async () => {
    mockPost.mockRejectedValue({ response: { data: { detail: "forbidden directive: root" } } });
    renderCard();
    await screen.findByText("WordPress");
    await createTemplate("root /etc/jabali-panel/;");
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("forbidden directive: root"));
  });
});
