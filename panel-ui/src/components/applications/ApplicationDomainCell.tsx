// ApplicationDomainCell — the CMS icon + domain/path link both application
// lists render. A ready row with a domain links to the live site; anything
// else is plain text. When `status` is supplied (the tenant list) it stacks
// under the link; when omitted (the admin list, which keeps status in its own
// column) the cell is a single inline row. One domain renderer for both lists
// (JAB-334).
import type { ReactNode } from "react";
import { CmsIcon } from "../../shells/user/applications/CmsIcon";
import type { ApplicationInstall } from "./applicationInventory";

type DomainCellRow = Pick<
  ApplicationInstall,
  "domain_name" | "domain_id" | "subdirectory" | "status" | "app_type"
>;

export function ApplicationDomainCell({
  record,
  status,
}: {
  record: DomainCellRow;
  status?: ReactNode;
}) {
  const base = record.domain_name || record.domain_id;
  const path = record.subdirectory ? `/${record.subdirectory}/` : "/";
  const label = `${base}${path}`;
  const isLink = record.status === "ready" && !!record.domain_name;
  const appKey = record.app_type || "wordpress";

  const link = isLink ? (
    <a href={`https://${record.domain_name}${path}`} target="_blank" rel="noopener noreferrer">
      {label}
    </a>
  ) : (
    <span>{label}</span>
  );

  if (status === undefined) {
    return (
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <CmsIcon appType={appKey} />
        {link}
      </div>
    );
  }

  return (
    <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
      <CmsIcon appType={appKey} />
      <div style={{ display: "flex", flexDirection: "column", gap: 4, alignItems: "flex-start" }}>
        {link}
        {status}
      </div>
    </div>
  );
}
