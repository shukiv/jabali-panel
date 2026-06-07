// DomainList — admin domains grid. Post-M21 the row action strip
// stays the same (DNS/Redirects/Index/Settings/Toggle/Edit/Delete);
// only the hook and the two Refine action buttons change.
import { useState } from "react";
import { Button, Card, Dropdown, Modal, Space, Table, Tag, Typography, notification } from "antd";
import {
  DeleteOutlined,
  DownOutlined,
  EditOutlined,
  FileTextOutlined,
  GlobalOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  SettingOutlined,
  SwapOutlined,
} from "@icons";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { SorterResult } from "antd/es/table/interface";

import { apiClient } from "../../../apiClient";
import { columnSearchProps } from "../../../components/columnSearch";
import { RowActionButton } from "../../../components/RowActionButton";
import { humanBytes } from "../../../utils/bytes";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { DomainSettingsButton } from "../../DomainSettingsButton";
import { DomainRedirectsButton } from "../../DomainRedirectsButton";
import { DomainIndexButton } from "../../DomainIndexButton";

type ActiveModal = { domain: Domain; type: "redirects" | "index" | "settings" } | null;

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
    <Typography.Text type="secondary">{stripHomePrefix(docRoot)}</Typography.Text>
  </div>
);

export type SSLBadge = {
  status: string;
  issuer?: string | null;
  issued_at?: string | null;
  expires_at?: string | null;
};

const renderSSL = (ssl: SSLBadge | null | undefined) => {
  if (!ssl) return <Tag>Off</Tag>;
  switch (ssl.status) {
    case "issued":
      return <Tag color="green">{ssl.issuer || "Let's Encrypt"}</Tag>;
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
  ssl?: SSLBadge | null;
  bytes_30d?: number;
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
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [activeModal, setActiveModal] = useState<ActiveModal>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const query = useTableURL<Domain>({
    resource: "domains",
    defaultSort: "name",
    defaultOrder: "asc",
  });
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
        <Typography.Title level={3} style={{ margin: 0 }}>
          <GlobalOutlined /> Domains
        </Typography.Title>
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
                description="No domains yet"
                ctaLabel="Create domain"
                onCta={() => navigate("create")}
              />
            ),
          }}
        >
          <Table.Column<Domain>
            dataIndex="name"
            title="Domain"
            key="name"
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            {...columnSearchProps<Domain>({
              placeholder: "Search by domain name",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
            render={(name: string, record: Domain) =>
              renderDomainCell(name, record.doc_root, record.is_panel_primary, record.is_quota_suspended)
            }
          />
          <Table.Column<Domain>
            dataIndex="username"
            title="User"
            key="username"
            sorter={{ multiple: 1 }}
            render={(username: string | null | undefined, record: Domain) =>
              username ?? <Typography.Text type="secondary">{record.user_id.substring(0, 8)}</Typography.Text>
            }
          />
          <Table.Column<Domain>
            dataIndex="is_enabled"
            title="Status"
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
            title="SSL"
            render={(ssl: SSLBadge | null | undefined) => renderSSL(ssl)}
          />
          <Table.Column<Domain>
            dataIndex="bytes_30d"
            title="BW (30d)"
            render={(v: number | undefined) => humanBytes(v ?? 0)}
          />
          <Table.Column<Domain>
            title="Actions"
            dataIndex="actions"
            render={(_, r) => (
              <Space>
                <RowActionButton
                  icon={<GlobalOutlined />}
                  onClick={() => navigate(`/jabali-admin/domains/${r.id}/dns`)}
                >
                  DNS
                </RowActionButton>
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
                  <RowActionButton icon={<DownOutlined />} color="default">
                    More
                  </RowActionButton>
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
              </Space>
            )}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
