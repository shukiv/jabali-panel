// DomainEnvVarsCard.test — GH #1543. In domain-fixed mode (a `domainId` prop,
// used by the Web Domain page), the card must bind to that domain: it loads
// THAT domain's env vars, issues no /domains list fetch, and renders no domain
// picker. This is the tenant-scope guarantee at the fixed call site.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { DomainEnvVarsCard } from "./DomainEnvVarsCard";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

beforeEach(() => {
  vi.clearAllMocks();
  mocked.get.mockResolvedValue({ data: { env_vars: [{ key: "APP_ENV", value: "prod" }] } });
});

describe("DomainEnvVarsCard domain-fixed mode (GH #1543)", () => {
  it("loads the given domain's env vars and never lists /domains", async () => {
    render(<DomainEnvVarsCard domainId="d1" />);
    await vi.waitFor(() =>
      expect(mocked.get).toHaveBeenCalledWith("/domains/d1/env-vars"),
    );
    // No domain-list fetch in fixed mode.
    expect(mocked.get).not.toHaveBeenCalledWith("/domains");
    // No picker rendered.
    expect(screen.queryByText("Select a domain")).not.toBeInTheDocument();
    // The loaded var is shown.
    expect(await screen.findByDisplayValue("APP_ENV")).toBeInTheDocument();
  });
});
