import { Badge } from "antd";
import type { ReactNode } from "react";

// GH #1478: render a side-nav item's label with a trailing count badge.
// Returns the bare label (no badge) when the count is undefined (still loading
// or not a badged item) or 0, so the nav stays clean until there's something to
// show. The badge is muted (gray) so it reads as a count, not an alert.
export function navLabelWithBadge(label: ReactNode, count: number | undefined): ReactNode {
  if (count == null || count <= 0) return label;
  return (
    <span
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        gap: 8,
      }}
    >
      <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{label}</span>
      <Badge
        count={count}
        overflowCount={999}
        style={{
          backgroundColor: "#e5e7eb",
          color: "#374151",
          boxShadow: "none",
          fontWeight: 500,
        }}
      />
    </span>
  );
}
