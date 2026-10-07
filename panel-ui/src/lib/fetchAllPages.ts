// fetchAllPages reads every page of a {data, total, page, page_size} list.
// A tab that groups or searches rows in the browser needs all of them: asked
// for no page size, the API answers its first 50 rows only (GH #1997).

import { apiClient } from "../apiClient";

// The largest page the list endpoints accept.
export const ALL_PAGES_PAGE_SIZE = 200;

interface Envelope<T> {
  data: T[] | null;
  total: number;
}

export async function fetchAllPages<T>(path: string, params: Record<string, string> = {}): Promise<T[]> {
  const get = async (page: number) => {
    const qs = new URLSearchParams({ ...params, page: String(page), page_size: String(ALL_PAGES_PAGE_SIZE) });
    const { data } = await apiClient.get<Envelope<T>>(`${path}?${qs.toString()}`);
    return data;
  };
  const first = await get(1);
  const rows = [...(first.data ?? [])];
  // The page count comes from the first answer's total, so a list that
  // grows meanwhile can't keep the loop going.
  const pages = Math.ceil((first.total ?? 0) / ALL_PAGES_PAGE_SIZE);
  for (let page = 2; page <= pages; page++) {
    rows.push(...((await get(page)).data ?? []));
  }
  return rows;
}
