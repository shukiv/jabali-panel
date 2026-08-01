// DomainList — admin domains grid. GH #351: Edit is the primary
// always-visible row action (used far more than DNS); DNS moved into the
// "..." overflow with Info/Redirects/Index/Settings/Caching/Toggle/Delete.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { Button, Card, Dropdown, Modal, Space, Table, Tag, Tooltip, Typography, notification } from "antd";
import {
  DeleteOutlined,
  MoreOutlined,
  EditOutlined,
  FileTextOutlined,
  InfoCircleOutlined,
  GlobalOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  SettingOutlined,
  SwapOutlined,
  ThunderboltOutlined,
} from "@icons";
import { useNavigate, useSearchParams, Link } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { SorterResult } from "antd/es/table/interface";

import { apiClient } from "../../../apiClient";
import { columnSearchProps } from "../../../components/columnSearch";
import { RowActionButton } from "../../../components/RowActionButton";
import { humanBytes } from "../../../utils/bytes";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useDeleteMutation, useOneQuery } from "../../../hooks/useQueries";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { ownerResourceCrumbs, adminLinks, ownerLabel } from "../../../components/admin/entityLinks";
import { useTableURL } from "../../../hooks/useTableURL";
import { DomainSettingsButton } from "../../DomainSettingsButton";
import { DomainRedirectsButton } from "../../DomainRedirectsButton";
import { DomainCacheButton } from "../../../components/DomainCacheButton";
import { DomainIndexButton } from "../../DomainIndexButton";
import { DomainInfoButton } from "../../DomainInfoButton";

type ActiveModal = {
  domain: Domain;
  type: "redirects" | "index" | "settings" | "info" | "caching";
} | null;

const stripHomePrefix = (path: string): string => {
  if (path.startsWith("/home/")) {
    const match = path.match(/^\/home\/[^/]+\/(.*)/);
    return match ? match[1] : path;
  }
  return path;
};

const renderDomainCell = (
  name: string,
  docRoot: string,
  isPanelPrimary?: boolean,
  isQuotaSuspended?: boolean,
) => (
  <div>
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
      <GlobalOutlined />
      <Typography.Link
        href={`https://${name}/`}
        target="_blank"
        rel="noopener noreferrer"
        title={`Open https://${name}/ in a new tab`}
      >
        {name}
      </Typography.Link>
      {isPanelPrimary && <Tag color="purple">System</Tag>}
      {isQuotaSuspended && <Tag color="orange">Suspended (quota)</Tag>}
    </div>
    <Typography.Text
      type="secondary"
      ellipsis
      title={stripHomePrefix(docRoot)}
      style={{ display: "block", maxWidth: 280 }}
    >
      {stripHomePrefix(docRoot)}
    </Typography.Text>
  </div>
);

export type SSLBadge = {
  status: string;
  issuer?: string | null;
  issued_at?: string | null;
  expires_at?: string | null;
};

// renderRedirect (GH #260): a compact indicator + hover detail for a domain's
// whole-domain redirect (redirect_all_to) or per-path redirects (page_redirects).
const renderRedirect = (d: Domain) => {
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
    const lines = pr.slice(0, 8).map((r) => `${r.type} ${r.source} → ${r.destination}`).join("\n");
    return (
      <Tooltip title={<span style={{ whiteSpace: "pre-wrap" }}>{lines}{pr.length > 8 ? `\n…+${pr.length - 8} more` : ""}</span>}>
        <Tag color="geekblue">{pr.length} path{pr.length > 1 ? "s" : ""}</Tag>
      </Tooltip>
    );
  }
  return <span style={{ color: "#bbb" }}>—</span>;
};

const renderSSL = (ssl: SSLBadge | null | undefined) => {
  if (!ssl) return <Tag>Off</Tag>;
  switch (ssl.status) {
    case "issued":
      return <Tag color="green">{ssl.issuer || "Let's Encrypt"}</Tag>;
    case "none":
      return <Tag>None</Tag>;
    case "provisioning":
      return <Tag color="orange">Self-signed…</Tag>;
    case "self_signed":
      return <Tag color="orange">Self-signed</Tag>;
    case "pending":
    case "issuing":
    case "renewing":
    case "pending_acme_retry":
      return <Tag color="green">Issuing…</Tag>;
    case "failed":
      return <Tag color="red">Failed</Tag>;
    case "revoked":
      return <Tag color="red">Revoked</Tag>;
    default:
      return <Tag>Off</Tag>;
  }
};

export type Domain = {
  id: string;
  user_id: string;
  username?: string | null;
  name: string;
  doc_root: string;
  is_enabled: boolean;
  is_panel_primary?: boolean;
  is_quota_suspended?: boolean;
  ssl_enabled?: boolean;
  mail_provider?: string;
  m365_onmicrosoft?: string;
  google_dkim?: string;
  ssl?: SSLBadge | null;
  bytes_30d?: number;
  temp_url_enabled?: boolean;
  temp_url?: string | null;
  nginx_custom_directives: string;
  redirect_all_to?: string | null;
  redirect_all_type?: string | null;
  page_redirects?:
    | { source: string; destination: string; type: "301" | "302" | "307" | "308" }[]
    | null;
  index_priority?:
    | "html_first"
    | "php_first"
    | "html_only"
    | "php_only"
    | "full"
    | null;
  // M24: nullable per-family binding to a managed_ips row. NULL ⇒ use
  // server default. listen_ipv4 / listen_ipv6 are the denormalized
  // {id,address} blob the list/get handler computes server-side.
  listen_ipv4_id?: number | null;
  listen_ipv6_id?: number | null;
  listen_ipv4?: { id: number; address: string } | null;
  listen_ipv6?: { id: number; address: string } | null;
  created_at: string;
  updated_at: string;
};

export const DomainList = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [activeModal, setActiveModal] = useState<ActiveModal>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const ownerId = searchParams.get("user_id") ?? undefined;
  const query = useTableURL<Domain>({
    resource: "domains",
    defaultSort: "name",
    defaultOrder: "asc",
    extraParams: ownerId ? { user_id: ownerId } : undefined,
  });
  // Owner-scoped view (#483): fetch the owner so the breadcrumb + chip can
  // name them even when the filtered list is empty.
  const ownerQ = useOneQuery<{ id: string; username?: string | null }>({
    resource: "users",
    id: ownerId,
    enabled: !!ownerId,
  });
  const clearOwner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("user_id");
    next.delete("page");
    setSearchParams(next);
  };
  const deleteMutation = useDeleteMutation({ resource: "domains" });

  const handleToggle = async (r: Domain) => {
    setTogglingId(r.id);
    try {
      await apiClient.patch(`/domains/${r.id}`, { is_enabled: !r.is_enabled });
      notification.success({ message: r.is_enabled ? "Domain disabled" : "Domain enabled" });
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", r.id] });
    } catch (err) {
      notification.error({ message: "Failed to toggle", description: (err as Error).message });
    } finally {
      setTogglingId(null);
    }
  };

  const handleTableChange: React.ComponentProps<typeof Table<Domain>>["onChange"] = (
    pagination,
    _filters,
    sorter,
  ) => {
    const single = Array.isArray(sorter)
      ? (sorter[0] as SorterResult<Domain> | undefined)
      : (sorter as SorterResult<Domain>);
    query.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort: single?.columnKey ? String(single.columnKey) : undefined,
      order:
        single?.order === "ascend"
          ? "asc"
          : single?.order === "descend"
            ? "desc"
            : undefined,
    });
  };

  useSetBreadcrumbs(
    ownerId
      ? ownerResourceCrumbs({ id: ownerId, username: ownerQ.data?.username }, { key: "domains", label: "Domains" })
      : null,
  );

  const ownerRef = ownerId
    ? { id: ownerId, username: ownerQ.data?.username }
    : undefined;

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
        }}
      >
        <Space wrap align="center">
          <Typography.Title level={3} style={{ margin: 0 }}>
            <GlobalOutlined /> Domains
          </Typography.Title>
          {ownerRef && (
            <Tag closable onClose={clearOwner} color="blue">
              Owner: {ownerLabel(ownerRef)}
            </Tag>
          )}
        </Space>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => navigate("create")}
        >
          Create Domain
        </Button>
      </Space>

      <Card>
        <SearchableTableStringQ<Domain>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by domain name"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
          }}
          onChange={handleTableChange}
          locale={{
            emptyText: (
              <EmptyWithCTA
                description={t("domainlist.no_domains_yet")}
                ctaLabel="Create domain"
                onCta={() => navigate("create")}
              />
            ),
          }}
        >
          <Table.Column<Domain>
            dataIndex="name"
            title={t("domainlist.domain")}
            key="name"
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            {...columnSearchProps<Domain>({
              placeholder: "Search by domain name",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
            render={(name: string, record: Domain) => (
              <>
                {renderDomainCell(name, record.doc_root, record.is_panel_primary, record.is_quota_suspended)}
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
            )}
          />
          <Table.Column<Domain>
            dataIndex="username"
            title={t("domainlist.user")}
            key="username"
            sorter={{ multiple: 1 }}
            render={(username: string | null | undefined, record: Domain) => (
              <Link to={adminLinks.user(record.user_id)}>
                {username ?? record.user_id.substring(0, 8)}
              </Link>
            )}
          />
          <Table.Column<Domain>
            dataIndex="is_enabled"
            title={t("domainlist.status")}
            key="is_enabled"
            sorter={{ multiple: 1 }}
            render={(enabled: boolean) =>
              enabled ? (
                <Tag color="green">active</Tag>
              ) : (
                <Tag>disabled</Tag>
              )
            }
          />
          <Table.Column<Domain>
            dataIndex="ssl"
            title={t("domainlist.ssl")}
            render={(ssl: SSLBadge | null | undefined) => renderSSL(ssl)}
          />
          <Table.Column<Domain>
            title={t("domainlist.redirect")}
            render={(_, record) => renderRedirect(record)}
          />
          <Table.Column<Domain>
            dataIndex="bytes_30d"
            title={t("domainlist.bw_30d")}
            render={(v: number | undefined) => humanBytes(v ?? 0)}
          />
          <Table.Column<Domain>
            title={t("domainlist.actions")}
            dataIndex="actions"
            render={(_, r) => (
              <Space>
                <RowActionButton
                  icon={<EditOutlined />}
                  onClick={() => navigate(`/jabali-admin/domains/edit/${r.id}`)}
                >
                  Edit
                </RowActionButton>
                <Dropdown
                  menu={{
                    items: [
                      {
                        key: "dns",
                        icon: <GlobalOutlined />,
                        label: "DNS",
                        onClick: () => navigate(`/jabali-admin/domains/${r.id}/dns`),
                      },
                      {
                        key: "info",
                        icon: <InfoCircleOutlined />,
                        label: "Information",
                        onClick: () => setActiveModal({ domain: r, type: "info" }),
                      },
                      {
                        key: "redirects",
                        icon: <SwapOutlined />,
                        label: "Redirects",
                        onClick: () => setActiveModal({ domain: r, type: "redirects" }),
                      },
                      {
                        key: "index",
                        icon: <FileTextOutlined />,
                        label: "Index Files",
                        onClick: () => setActiveModal({ domain: r, type: "index" }),
                      },
                      {
                        key: "settings",
                        icon: <SettingOutlined />,
                        label: "Nginx Settings",
                        onClick: () => setActiveModal({ domain: r, type: "settings" }),
                      },
                      {
                        key: "caching",
                        icon: <ThunderboltOutlined />,
                        label: "Caching",
                        onClick: () => setActiveModal({ domain: r, type: "caching" }),
                      },
                      {
                        key: "toggle",
                        icon: r.is_enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />,
                        label: r.is_enabled ? "Disable" : "Enable",
                        danger: r.is_enabled,
                        disabled: togglingId === r.id,
                        onClick: () => handleToggle(r),
                      },
                      ...(r.is_panel_primary
                        ? []
                        : [
                            { type: "divider" as const },
                            {
                              key: "delete",
                              icon: <DeleteOutlined />,
                              label: "Delete",
                              danger: true,
                              onClick: () =>
                                Modal.confirm({
                                  title: `Delete domain "${r.name}"?`,
                                  okText: "Delete",
                                  okButtonProps: { danger: true },
                                  onOk: async () => {
                                    await deleteMutation.mutateAsync({ id: r.id });
                                  },
                                }),
                            },
                          ]),
                    ],
                  }}
                >
                  <RowActionButton icon={<MoreOutlined />} color="default" aria-label={t("domainlist.more_actions")} />
                </Dropdown>
                {activeModal?.domain.id === r.id && activeModal.type === "redirects" && (
                  <DomainRedirectsButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domain.id === r.id && activeModal.type === "index" && (
                  <DomainIndexButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domain.id === r.id && activeModal.type === "settings" && (
                  <DomainSettingsButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domain.id === r.id && activeModal.type === "info" && (
                  <DomainInfoButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domain.id === r.id && activeModal.type === "caching" && (
                  <DomainCacheButton
                    domainId={r.id}
                    domainName={r.name}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
              </Space>
            )}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
