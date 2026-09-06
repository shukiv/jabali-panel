// JAB-300: the shared data columns for the domain inventory grid (name,
// owner, status, SSL, redirect, bandwidth). The admin and tenant screens
// rendered near-identical columns from two copies that had already drifted
// (admin ellipsised the docroot, tenant didn't; admin showed System/Suspended
// tags, tenant a service badge). This is the single definition; the audience
// discriminant carries the deltas. The Actions column is appended by
// DomainInventory because it owns the row-modal state.
import { Link } from "react-router";
import { Tag, Tooltip, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { GlobalOutlined, ExportOutlined } from "@icons";
import { columnSearchProps } from "../columnSearch";
import { humanBytes } from "../../utils/bytes";
import { getSSLTag } from "../../utils/sslState";
import { adminLinks } from "../admin/entityLinks";
import { serviceBadge, type Domain, type DomainApplicationSummary } from "./types";
import { DomainApplicationCell } from "./DomainApplicationCell";

// A discriminated union — audience policy stays internal to the module rather
// than being rebuilt as caller-supplied column/callback bags (JAB-300 AC).
export type DomainInventoryAudience =
  | { kind: "admin"; ownerId?: string }
  | { kind: "tenant" };

const stripHomePrefix = (path: string): string => {
  if (path.startsWith("/home/")) {
    const match = path.match(/^\/home\/[^/]+\/(.*)/);
    return match ? match[1] : path;
  }
  return path;
};

// The name/docroot cell. The unified docroot line uses the admin list's
// ellipsis + maxWidth so a long path can't stretch the column on either screen.
// GH #1543: on the tenant list the name opens the domain's own Web Domain page
// (an internal Link), with a separate launch icon still opening the live site;
// the admin name keeps opening the live site directly, since admin edits a
// domain from its own Edit page via the row menu.
const renderDomainCell = (record: Domain, audience: DomainInventoryAudience) => (
  <div>
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
      <GlobalOutlined />
      {audience.kind === "tenant" ? (
        <>
          <Link to={`/jabali-panel/domains/${record.id}`}>{record.name}</Link>
          <Typography.Link
            href={`https://${record.name}/`}
            target="_blank"
            rel="noopener noreferrer"
            title={`Open https://${record.name}/ in a new tab`}
            aria-label={`Open https://${record.name}/ in a new tab`}
          >
            <ExportOutlined />
          </Typography.Link>
        </>
      ) : (
        <Typography.Link
          href={`https://${record.name}/`}
          target="_blank"
          rel="noopener noreferrer"
          title={`Open https://${record.name}/ in a new tab`}
        >
          {record.name}
        </Typography.Link>
      )}
      {audience.kind === "admin" && record.is_panel_primary && <Tag color="purple">System</Tag>}
      {audience.kind === "admin" && record.is_quota_suspended && (
        <Tag color="orange">Suspended (quota)</Tag>
      )}
    </div>
    <Typography.Text
      type="secondary"
      ellipsis
      title={stripHomePrefix(record.doc_root)}
      style={{ display: "block", maxWidth: 280 }}
    >
      {stripHomePrefix(record.doc_root)}
    </Typography.Text>
  </div>
);

const renderRedirect = (
  d: Pick<Domain, "redirect_all_to" | "redirect_all_type" | "page_redirects">,
) => {
  if (d.redirect_all_to) {
    const t = d.redirect_all_type || "301";
    return (
      <Tooltip title={`${t} → ${d.redirect_all_to}`}>
        <Tag color="purple">Redirect {t}</Tag>
      </Tooltip>
    );
  }
  const pr = d.page_redirects ?? [];
  if (pr.length > 0) {
    const lines = pr
      .slice(0, 8)
      .map((r) => `${r.type} ${r.source} → ${r.destination}`)
      .join("\n");
    return (
      <Tooltip
        title={
          <span style={{ whiteSpace: "pre-wrap" }}>
            {lines}
            {pr.length > 8 ? `\n…+${pr.length - 8} more` : ""}
          </span>
        }
      >
        <Tag color="geekblue">
          {pr.length} path{pr.length > 1 ? "s" : ""}
        </Tag>
      </Tooltip>
    );
  }
  return <span style={{ color: "#bbb" }}>—</span>;
};

// The SSL badge folds both wire shapes (admin nested `ssl`, tenant flat
// `ssl_state`) through the one getSSLTag matrix so the two screens render
// identically (JAB-300, matrix shipped in #1494).
const renderSSL = (record: Domain, audience: DomainInventoryAudience) => {
  const { color, label } = getSSLTag(audience.kind === "admin" ? record.ssl : record.ssl_state);
  return <Tag color={color}>{label}</Tag>;
};

// GH #1543 D2: the tenant Application column's inline mutations. The cell owns
// no state — it calls back into DomainInventory, which hosts the install modal,
// the delete confirm, and the transitional poll. Optional (admin never supplies
// it, and the column it drives is tenant-only).
export type DomainColumnAppActions = {
  onInstall: (r: Domain) => void;
  onDelete: (app: DomainApplicationSummary, r: Domain) => void;
  deletingId: string | null;
};

type ColumnCtx = {
  t: (key: string) => string;
  query: { params: { q?: string }; setParams: (p: { q: string; page: number }) => void };
  appActions?: DomainColumnAppActions;
};

export const buildDomainDataColumns = (
  audience: DomainInventoryAudience,
  { t, query, appActions }: ColumnCtx,
): ColumnsType<Domain> => {
  // Each screen keeps its own i18n namespace so no header text shifts.
  const title = (key: string) =>
    audience.kind === "admin" ? t(`domainlist.${key}`) : t(`userdomainlist.${key}`);

  const columns: ColumnsType<Domain> = [
    {
      dataIndex: "name",
      title: title("domain"),
      key: "name",
      sorter: true,
      defaultSortOrder: "ascend",
      ...columnSearchProps<Domain>({
        placeholder: "Search by domain name",
        currentQ: query.params.q,
        onSearch: (v) => query.setParams({ q: v, page: 1 }),
      }),
      render: (_name: string, record: Domain) => {
        const svc = audience.kind === "tenant" ? serviceBadge(record) : null;
        return (
          <>
            {renderDomainCell(record, audience)}
            {svc && (
              <div>
                <Tag color={svc.color} style={{ fontSize: 12 }}>
                  {svc.label}
                </Tag>
              </div>
            )}
            {audience.kind === "tenant" && record.reverse_proxy_port ? (
              <div>
                <Tooltip
                  title={`Reverse proxy — run your app on 127.0.0.1:${record.reverse_proxy_port}`}
                >
                  <Tag color="cyan" style={{ fontSize: 12 }}>
                    proxy → :{record.reverse_proxy_port}
                  </Tag>
                </Tooltip>
              </div>
            ) : null}
            {record.temp_url && (
              <div>
                <Typography.Link
                  href={record.temp_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  copyable={{ text: record.temp_url }}
                  style={{ fontSize: 12 }}
                >
                  {record.temp_url.replace(/^https?:\/\//, "")}
                </Typography.Link>
              </div>
            )}
          </>
        );
      },
    },
  ];

  if (audience.kind === "admin") {
    columns.push({
      dataIndex: "username",
      title: title("user"),
      key: "username",
      sorter: true,
      render: (username: string | null | undefined, record: Domain) => (
        <Link to={adminLinks.user(record.user_id)}>
          {username ?? record.user_id.substring(0, 8)}
        </Link>
      ),
    });
  }

  columns.push(
    {
      dataIndex: "is_enabled",
      title: title("status"),
      key: "is_enabled",
      sorter: true,
      render: (enabled: boolean) =>
        enabled ? <Tag color="green">active</Tag> : <Tag>disabled</Tag>,
    },
    {
      key: "ssl",
      title: title("ssl"),
      render: (_: unknown, record: Domain) => renderSSL(record, audience),
    },
    {
      key: "redirect",
      title: title("redirect"),
      render: (_: unknown, record: Domain) => renderRedirect(record),
    },
  );

  // GH #1543: the tenant Web Domains list gains an Application column showing
  // this domain's primary One-Click install (badge + version/status + Login).
  // Tenant-only: admin edits a domain from its Edit page and has its own
  // Applications list. The literal header avoids adding an i18n key (no unused-
  // key gate; the tenant namespace has no "application" entry).
  if (audience.kind === "tenant") {
    columns.push({
      key: "application",
      title: "Application",
      responsive: ["md"],
      render: (_: unknown, record: Domain) => (
        <DomainApplicationCell
          applications={record.applications}
          onInstall={appActions ? () => appActions.onInstall(record) : undefined}
          onDelete={appActions ? (app) => appActions.onDelete(app, record) : undefined}
          deletingId={appActions?.deletingId ?? null}
        />
      ),
    });
  }

  columns.push({
    dataIndex: "bytes_30d",
    title: title("bw_30d"),
    key: "bytes_30d",
    render: (v: number | undefined) => humanBytes(v ?? 0),
  });

  return columns;
};
