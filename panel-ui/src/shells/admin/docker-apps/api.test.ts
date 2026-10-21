// api.test.ts — the admin catalog list leaves retired entries out unless the
// caller asks for them with true.
import { beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("../../../apiClient", () => ({ apiClient: { get } }));

import { listCatalog } from "./api";

describe("listCatalog", () => {
  beforeEach(() => {
    get.mockReset();
    get.mockResolvedValue({ data: { items: [] } });
  });

  it("asks for retired entries only when told to", async () => {
    await listCatalog();
    expect(get).toHaveBeenLastCalledWith("/admin/docker-apps/catalog", { params: undefined });
    await listCatalog(true);
    expect(get).toHaveBeenLastCalledWith("/admin/docker-apps/catalog", { params: { include_retired: "1" } });
  });

  // queryFn: listCatalog passes React Query's context as the argument.
  it("does not take a query context for true", async () => {
    await listCatalog({ queryKey: ["docker-apps-catalog"], signal: new AbortController().signal } as unknown as boolean);
    expect(get).toHaveBeenLastCalledWith("/admin/docker-apps/catalog", { params: undefined });
  });
});
