// UserDomainList — tenant view of domains they own. Same row-action
// strip as the admin list (DNS/Redirects/Index/Settings/Toggle/Delete)
// minus the Edit button (users edit per-domain config via the row
// buttons rather than a full edit page).
import { useTranslation } from "react-i18next";
import {
  PlusSquareOutlined,
  EyeOutlined,
  GlobalOutlined,
  MoreOutlined,
  SwapOutlined,
  FileTextOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  DeleteOutlined,
  LockOutlined,
  ThunderboltOutlined,
  ToolOutlined,
  FolderOutlined,
} from "@icons";
import { Button, Card, Dropdown, Space, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useState } from "react";
import { useNavigate } from "react-router";
import type { SorterResult } from "antd/es/table/interface";
import { useQueryClient } from "@tanstack/react-query";

import { DomainDocRootModal } from "./DomainDocRootModal";
import { apiClient } from "../../../apiClient";
import { columnSearchProps } from "../../../components/columnSearch";
import { RowActionButton } from "../../../components/RowActionButton";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { humanBytes } from "../../../utils/bytes";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { DomainDirectoryPrivacyModal } from "../../../components/DomainDirectoryPrivacyModal";
import { DomainRedirectsButton } from "../../DomainRedirectsButton";
import { TenantNginxRulesButton } from "../../DomainSettingsButton";
import { DomainCacheButton } from "../../../components/DomainCacheButton";
import { DomainIndexButton } from "../../DomainIndexButton";
import { UserDomainDrawer } from "./UserDomainDrawer";
import { DomainNginxOptionsModal } from "../../../components/DomainNginxOptionsModal";
import { useServerCapabilities } from "../../../hooks/useServerCapabilities";

const stripHomePrefix = (path: string): string => {
  if (path.startsWith("/home/")) {
    const match = path.match(/^\/home\/[^/]+\/(.*)/);
    return match ? match[1] : path;
  }
  return path;
};

const renderDomainCell = (name: string, docRoot: string) => (
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
    </div>
    <Typography.Text type="secondary">{stripHomePrefix(docRoot)}</Typography.Text>
  </div>
);

// SSL state values come from panel-api/internal/repository/
// domain_repository.go computeSSLState: "active_le" (valid LE
// cert), "self_signed", "pending", "issuing", "renewing",
// "pending_acme_retry", "failed", "revoked", "off".
//
// Mirrors admin DomainList renderSSL (DomainList.tsx 66-80) so
// user + admin shells render identically.
const getSSLTagColor = (state?: string): string => {
  switch (state) {
    case "active_le":
      return "gold"; // Let's Encrypt rendered yellow per operator request
    case "active":
      return "green";
    case "provisioning":
      return "orange";
    case "self_signed":
      return "orange";
    case "pending":
    case "issuing":
    case "renewing":
    case "pending_acme_retry":
      return "green";
    case "failed":
    case "error":
    case "revoked":
      return "red";
    default:
      return "default";
  }
};

const getSSLTagLabel = (state?: string): string => {
  switch (state) {
    case "active_le":
      return "Let's Encrypt";
    case "active":
      return "Active";
    case "none":
      return "None";
    case "provisioning":
      return "Self-signed…";
    case "self_signed":
      return "Self-signed";
    case "pending":
    case "issuing":
    case "renewing":
    case "pending_acme_retry":
      return "Issuing…";
    case "failed":
    case "error":
      return "Failed";
    case "revoked":
      return "Revoked";
    case "":
    case undefined:
      return "Off";
    default:
      return state;
  }
};

const renderRedirect = (d: { redirect_all_to?: string | null; redirect_all_type?: string | null; page_redirects?: { source: string; destination: string; type: string }[] | null }) => {
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

export type Domain = {
  id: string;
  user_id: string;
  username?: string | null;
  name: string;
  doc_root: string;
  is_enabled: boolean;
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
  ssl_state?: string;
  email_enabled?: boolean;
  bytes_30d?: number;
  created_at: string;
  updated_at: string;
};

type ActiveModal = { domainId: string; type: "redirects" | "index" | "directory-privacy" | "caching" | "nginx-options" | "rewrite-rules" | "document-root" } | null;

export const UserDomainList = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [activeModal, setActiveModal] = useState<ActiveModal>(null);
  const { data: caps } = useServerCapabilities();
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const query = useTableURL<Domain>({
    resource: "domains",
    defaultSort: "name",
    defaultOrder: "asc",
  });
  const deleteMutation = useDeleteMutation({ resource: "domains" });

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
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
          flexWrap: "wrap",
          rowGap: 8,
        }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <GlobalOutlined /> Domains
        </Typography.Title>
        <Button
          type="primary"
          icon={<PlusSquareOutlined />}
          onClick={() => setDrawerOpen(true)}
        >
          Add Domain
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
        >
          <Table.Column<Domain>
            dataIndex="name"
            title={t("userdomainlist.domain")}
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
                {renderDomainCell(name, record.doc_root)}
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
            dataIndex="is_enabled"
            title={t("userdomainlist.status")}
            render={(enabled: boolean) =>
              enabled ? (
                <Tag color="green">active</Tag>
              ) : (
                <Tag>disabled</Tag>
              )
            }
          />
          <Table.Column<Domain>
            dataIndex="ssl_state"
            title={t("userdomainlist.ssl")}
            render={(state?: string) => (
              <Tag color={getSSLTagColor(state)}>{getSSLTagLabel(state)}</Tag>
            )}
          />
          <Table.Column<Domain>
            title={t("userdomainlist.redirect")}
            render={(_, record) => renderRedirect(record)}
          />
          <Table.Column<Domain>
            dataIndex="bytes_30d"
            title={t("userdomainlist.bw_30d")}
            render={(v: number | undefined) => humanBytes(v ?? 0)}
          />
          <Table.Column<Domain>
            title={t("userdomainlist.actions")}
            dataIndex="actions"
            render={(_, r) => (
              <>
                <Space>
                {/* GH #833: single Actions entry point, mirroring the admin
                    Domains list — same dropdown shape on both views. */}
                <Dropdown
                  trigger={["click"]}
                  menu={{
                    items: [
                      {
                        key: "dns",
                        label: "DNS",
                        icon: <GlobalOutlined />,
                        onClick: () => navigate(`/jabali-panel/domains/${r.id}/dns`),
                      },
                      {
                        key: "redirects",
                        label: "Redirects",
                        icon: <SwapOutlined />,
                        onClick: () => setActiveModal({ domainId: r.id, type: "redirects" }),
                      },
                      {
                        key: "index",
                        label: "Index",
                        icon: <FileTextOutlined />,
                        onClick: () => setActiveModal({ domainId: r.id, type: "index" }),
                      },
                      {
                        key: "directory-privacy",
                        label: "Directory Privacy",
                        icon: <LockOutlined />,
                        onClick: () => setActiveModal({ domainId: r.id, type: "directory-privacy" }),
                      },
                      {
                        key: "caching",
                        label: "Caching",
                        icon: <ThunderboltOutlined />,
                        onClick: () => setActiveModal({ domainId: r.id, type: "caching" }),
                      },
                      ...(caps?.tenant_domain_options_enabled
                        ? [
                            {
                              key: "nginx-options",
                              label: "Domain options",
                              icon: <ThunderboltOutlined />,
                              onClick: () => setActiveModal({ domainId: r.id, type: "nginx-options" }),
                            },
                            {
                              key: "rewrite-rules",
                              label: "Rewrite rules",
                              icon: <ToolOutlined />,
                              onClick: () => setActiveModal({ domainId: r.id, type: "rewrite-rules" }),
                            },
                          ]
                        : []),
                      ...(caps?.tenant_docroot_editable
                        ? [
                            {
                              key: "document-root",
                              label: "Document root",
                              icon: <FolderOutlined />,
                              onClick: () => setActiveModal({ domainId: r.id, type: "document-root" }),
                            },
                          ]
                        : []),
                      {
                        key: "preview-url",
                        label: r.temp_url_enabled ? "Disable preview URL" : "Enable preview URL",
                        icon: <EyeOutlined />,
                        onClick: async () => {
                          try {
                            await apiClient.patch(`/domains/${r.id}`, {
                              temp_url_enabled: !r.temp_url_enabled,
                            });
                            feedback.notification.success({
                              message: r.temp_url_enabled
                                ? "Preview URL disabled"
                                : "Preview URL enabled — live within a minute",
                            });
                            qc.invalidateQueries({ queryKey: ["list", "domains"] });
                          } catch (err) {
                            const e = err as { response?: { data?: { detail?: string; error?: string } } };
                            feedback.notification.error({
                              message: "Failed to toggle preview URL",
                              description: e.response?.data?.detail ?? e.response?.data?.error ?? (err as Error).message,
                            });
                          }
                        },
                      },
                      {
                        key: "toggle",
                        label: r.is_enabled ? "Disable" : "Enable",
                        icon: r.is_enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />,
                        disabled: togglingId === r.id,
                        onClick: async () => {
                          setTogglingId(r.id);
                          try {
                            await apiClient.patch(`/domains/${r.id}`, {
                              is_enabled: !r.is_enabled,
                            });
                            feedback.notification.success({
                              message: r.is_enabled ? "Domain disabled" : "Domain enabled",
                            });
                            qc.invalidateQueries({ queryKey: ["list", "domains"] });
                            qc.invalidateQueries({ queryKey: ["one", "domains", r.id] });
                          } catch (err) {
                            feedback.notification.error({
                              message: "Failed to toggle",
                              description: (err as Error).message,
                            });
                          } finally {
                            setTogglingId(null);
                          }
                        },
                      },
                      { type: "divider" },
                      {
                        key: "delete",
                        label: "Delete",
                        icon: <DeleteOutlined />,
                        danger: true,
                        onClick: () =>
                          feedback.modal.confirm({
                            title: `Delete domain "${r.name}"?`,
                            content: "This cannot be undone.",
                            okText: "Delete",
                            okType: "danger",
                            onOk: async () => {
                              await deleteMutation.mutateAsync({ id: r.id });
                            },
                          }),
                      },
                    ],
                  }}
                >
                  <RowActionButton icon={<MoreOutlined />} aria-label={t("userdomainlist.more_actions")}>
                    Actions
                  </RowActionButton>
                </Dropdown>
                </Space>
                {activeModal?.domainId === r.id && activeModal.type === "redirects" && (
                  <DomainRedirectsButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domainId === r.id && activeModal.type === "index" && (
                  <DomainIndexButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domainId === r.id && activeModal.type === "directory-privacy" && (
                  <DomainDirectoryPrivacyModal
                    open={true}
                    domainId={r.id}
                    domainName={r.name}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domainId === r.id && activeModal.type === "caching" && (
                  <DomainCacheButton
                    open={true}
                    domainId={r.id}
                    domainName={r.name}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domainId === r.id && activeModal.type === "nginx-options" && (
                  <DomainNginxOptionsModal
                    domainId={r.id}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domainId === r.id && activeModal.type === "rewrite-rules" && (
                  <TenantNginxRulesButton
                    domain={r}
                    open={true}
                    onClose={() => setActiveModal(null)}
                  />
                )}
                {activeModal?.domainId === r.id && activeModal.type === "document-root" && (
                  <DomainDocRootModal
                    domainId={r.id}
                    domainName={r.name}
                    currentDocRoot={r.doc_root}
                    onClose={() => setActiveModal(null)}
                  />
                )}
              </>
            )}
          />
        </SearchableTableStringQ>
      </Card>
      <UserDomainDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} />
    </div>
  );
};
