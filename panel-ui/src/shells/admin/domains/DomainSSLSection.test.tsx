// DomainSSLSection.test.tsx — GH #738 regression: the admin-email lookup that
// gates the "Let's Encrypt" option must hit GET /admin/settings (flat
// admin_email), not the non-existent /system/settings (which 404'd → adminEmail
// always undefined → LE permanently greyed even when an admin email was set).
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";

import { DomainSSLSection } from "./DomainSSLSection";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

import { apiClient } from "../../../apiClient";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

beforeEach(() => {
  mocked.get.mockImplementation((url: string) => {
    if (url === "/admin/settings") return Promise.resolve({ data: { admin_email: "admin@example.com" } });
    if (url.endsWith("/ssl")) return Promise.resolve({ data: { status: "none" } });
    return Promise.resolve({ data: { ssl_mode: "none" } }); // /domains/:id
  });
});

it("reads admin_email from /admin/settings (not the 404 /system/settings) — GH #738", async () => {
  render(
    <DomainSSLSection domainId="dom1" domainName="example.com" sslEnabled={false} onToggled={() => {}} />,
  );
  await waitFor(() => {
    expect(mocked.get).toHaveBeenCalledWith("/admin/settings");
  });
  const urls = mocked.get.mock.calls.map((c) => c[0] as string);
  expect(urls).not.toContain("/system/settings");
});

// GH #2200: a domain waiting for proof of ownership is self-signed because
// Let's Encrypt is not asked yet, not "while issuance retries". The alert says
// so, and there is no Retry (the server refuses it for an unproven name).
it("ownership pending: explains the wait instead of a retry — GH #2200", async () => {
  mocked.get.mockImplementation((url: string) => {
    if (url === "/admin/settings") return Promise.resolve({ data: { admin_email: "admin@example.com" } });
    if (url.endsWith("/ssl")) {
      return Promise.resolve({ data: { ssl: { status: "pending_acme_retry", retry_count: 1, renewal_count: 0, staging: false } } });
    }
    return Promise.resolve({ data: { ssl_mode: "le" } });
  });
  render(
    <DomainSSLSection domainId="dom1" domainName="example.com" sslEnabled ownershipPending onToggled={() => {}} />,
  );
  expect(await screen.findByText("Waiting for proof of ownership")).toBeInTheDocument();
  expect(screen.queryByText("Using self-signed certificate")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Retry now/ })).not.toBeInTheDocument();
});

it("proven domain: keeps the self-signed retry alert", async () => {
  mocked.get.mockImplementation((url: string) => {
    if (url === "/admin/settings") return Promise.resolve({ data: { admin_email: "admin@example.com" } });
    if (url.endsWith("/ssl")) {
      return Promise.resolve({ data: { ssl: { status: "pending_acme_retry", retry_count: 1, renewal_count: 0, staging: false } } });
    }
    return Promise.resolve({ data: { ssl_mode: "le" } });
  });
  render(<DomainSSLSection domainId="dom1" domainName="example.com" sslEnabled onToggled={() => {}} />);
  expect(await screen.findByText("Using self-signed certificate")).toBeInTheDocument();
  expect(screen.queryByText("Waiting for proof of ownership")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: /Retry now/ })).toBeInTheDocument();
});
