// Shared catalog masonry grid (JAB-167). As many >=320px columns as the
// viewport fits (1 mobile, 2 tablet, 3+ desktop) with no media queries — the
// admin Docker-Apps layout, now the one design for every marketplace. Each
// CatalogCard wraps itself in a break-inside-avoid item.
import type { ReactNode } from "react";

export function CatalogGrid({ children }: { children: ReactNode }) {
  return <div style={{ columns: "320px", columnGap: 16 }}>{children}</div>;
}
