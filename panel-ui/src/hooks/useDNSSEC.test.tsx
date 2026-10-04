// GH #1918: DNSSEC is flipped from the DNS zone list's row menu, whose Signed
// tag reads domains.dnssec_enabled through the ["list","dns/zones"] query. A
// flip must refresh that list (prefix match), the per-domain DNSSEC state, and
// the domain lists — and must PUT to the domain passed at call time.
import { QueryClient, QueryClientProvider, type QueryKey } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useSetDNSSEC } from "./useDNSSEC";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn() },
}));

import { apiClient } from "../apiClient";

const mocked = apiClient as unknown as { put: ReturnType<typeof vi.fn> };

const REFRESHED: QueryKey[] = [
  ["list", "dns/zones", { page: 1, pageSize: 20 }],
  ["dnssec", "d1"],
  ["domains"],
];
const UNRELATED: QueryKey[] = [["list", "databases", { page: 1 }]];

beforeEach(() => {
  mocked.put.mockReset().mockResolvedValue({ data: { domain_id: "d1", enabled: true, keys: [] } });
});

describe("useSetDNSSEC (GH #1918)", () => {
  it("PUTs the domain given at call time and refreshes the zone list", async () => {
    const qc = new QueryClient();
    for (const k of [...REFRESHED, ...UNRELATED]) qc.setQueryData(k, { seeded: true });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useSetDNSSEC(), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({ domainID: "d1", enabled: true });
    });

    expect(mocked.put).toHaveBeenCalledWith("/domains/d1/dnssec", { enabled: true });
    for (const k of REFRESHED) expect(qc.getQueryState(k)?.isInvalidated, JSON.stringify(k)).toBe(true);
    for (const k of UNRELATED) expect(qc.getQueryState(k)?.isInvalidated, JSON.stringify(k)).toBe(false);
  });
});
