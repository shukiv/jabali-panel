// UserSSLManagerPage.test.tsx — GH #2032: the SSL page carries the user's
// Cloudflare token card next to the certificate list.
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../components/ssl/SSLManagerTable", () => ({
  SSLManagerTable: () => <div>certificate list</div>,
}));
vi.mock("./UserCloudflareTokenCard", () => ({
  UserCloudflareTokenCard: () => <div>cloudflare token card</div>,
}));

import { UserSSLManagerPage } from "./UserSSLManagerPage";

describe("UserSSLManagerPage", () => {
  it("shows the certificate list and the Cloudflare token card", () => {
    render(<UserSSLManagerPage />);
    expect(screen.getByText("certificate list")).toBeInTheDocument();
    expect(screen.getByText("cloudflare token card")).toBeInTheDocument();
  });
});
