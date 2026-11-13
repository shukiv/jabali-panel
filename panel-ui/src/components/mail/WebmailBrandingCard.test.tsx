// WebmailBrandingCard — GH #2502: the owner (package switch on) or an admin
// edits a domain's webmail branding; anyone else is told why they can't.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { WebmailBrandingCard } from "./WebmailBrandingCard";
import type { WebmailBranding } from "../../hooks/useWebmailBranding";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};

const EMPTY: WebmailBranding = {
  host: "mail.ex.test",
  can_edit: true,
  applies: false,
  set_by_admin: false,
  app_name: "",
  company_name: "",
  website_url: "",
  privacy_url: "",
  imprint_url: "",
  logo_light: "",
  logo_dark: "",
  favicon: "",
};

function renderCard(saved: Partial<WebmailBranding>) {
  mocked.get.mockResolvedValue({ data: { ...EMPTY, ...saved } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <App>
        <WebmailBrandingCard domainId="d1" />
      </App>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("WebmailBrandingCard (GH #2502)", () => {
  it("tells an owner whose package lacks it, with no form", async () => {
    renderCard({ can_edit: false });
    expect(await screen.findByText(/doesn't include webmail branding/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    expect(mocked.get).toHaveBeenCalledWith("/domains/d1/webmail-branding");
  });

  it("tells the owner when their provider branded it", async () => {
    renderCard({ can_edit: false, applies: true, set_by_admin: true, app_name: "Acme Mail" });
    expect(await screen.findByText(/hosting provider set the webmail branding/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("saves the name and links via PUT", async () => {
    renderCard({});
    const saved = { ...EMPTY, app_name: "Acme Mail", website_url: "https://acme.test", applies: true };
    mocked.put.mockResolvedValue({ data: saved });

    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Acme Mail" } });
    fireEvent.change(screen.getByLabelText("Website link"), { target: { value: "https://acme.test" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/domains/d1/webmail-branding", {
        app_name: "Acme Mail",
        company_name: "",
        website_url: "https://acme.test",
        privacy_url: "",
        imprint_url: "",
      }),
    );
  });

  it("refuses a link that isn't http(s)", async () => {
    renderCard({});
    fireEvent.change(await screen.findByLabelText("Privacy policy link"), { target: { value: "javascript:alert(1)" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText(/starts with https:\/\/ or http:\/\//i)).toBeInTheDocument();
    expect(mocked.put).not.toHaveBeenCalled();
  });

  it("uploads a picture and previews the stored one", async () => {
    renderCard({});
    mocked.post.mockResolvedValue({ data: { ...EMPTY, logo_light: "0123456789ab", applies: true } });

    await screen.findByRole("button", { name: "Upload Logo" });
    const input = document.querySelector('input[type="file"]') as HTMLInputElement;
    expect(input.accept).toBe("image/png,image/jpeg");
    const file = new File([new Uint8Array([137, 80, 78, 71])], "logo.png", { type: "image/png" });
    fireEvent.change(input, { target: { files: [file] } });

    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    const [url, body] = mocked.post.mock.calls[0];
    expect(url).toBe("/domains/d1/webmail-branding/logo-light");
    expect((body as FormData).get("file")).toBeTruthy();
    const img = await screen.findByAltText("Logo preview");
    expect(img.getAttribute("src")).toBe("/api/v1/domains/d1/webmail-branding/logo-light?v=0123456789ab");
  });

  it("warns an admin when the owner's package no longer allows the branding", async () => {
    renderCard({ app_name: "Old Mail", applies: false });
    expect(await screen.findByText("Webmail doesn't show this branding")).toBeInTheDocument();
  });

  it("removes the whole branding after a confirm", async () => {
    renderCard({ app_name: "Acme Mail", applies: true });
    mocked.delete.mockResolvedValue({ data: EMPTY });

    fireEvent.click(await screen.findByRole("button", { name: "Remove branding" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));

    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/domains/d1/webmail-branding"));
  });
});
