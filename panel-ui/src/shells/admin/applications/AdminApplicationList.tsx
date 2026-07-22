// Admin-shell cross-user applications list — read-only view of every
// installed application (WordPress today, more as M19 lands them).
// Admins modify installs by opening the relevant user's panel directly
// in their own browser tab — no in-panel impersonation after M20.
//
// Status badge styling is inlined from UserApplicationList to keep
// coupling low and avoid tight dependency on that component.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { shortDateTime } from "../../../utils/datetime";
import { useQueryClient } from "@tanstack/react-query";
import {
  Card,
  Input,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import {
  AppstoreOutlined,
  LoadingOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
  DeleteOutlined,
  LoginOutlined,
  SearchOutlined,
} from "@icons";
import { RowActions } from "../../../components/RowActions";
import type { SorterResult } from "antd/es/table/interface";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useTableURL } from "../../../hooks/useTableURL";
import { useMagicLink } from "../../../hooks/useMagicLink";
import { apiClient } from "../../../apiClient";
import { CmsIcon } from "../../user/applications/CmsIcon";

type ApplicationInstall = {
  id: string;
  app_type?: string;
  domain_id: string;
  domain_name: string;
  db_id: string;
  admin_username: string;
  admin_email: string;
  owner_email: string;
  owner_username?: string;
  locale: string;
  subdirectory: string;
  status:
    | "pending"
    | "installing"
    | "cloning"
    | "deleting"
    | "ready"
    | "failed";
  version: string | null;
  last_error: string;
  created_at: string;
  updated_at: string;
};

const STATUS_META: Record<
  ApplicationInstall["status"],
  { color: string; icon: React.ReactNode; label: string; spinning: boolean }
> = {
  pending:    { color: "default",    icon: <LoadingOutlined spin />,      label: "Pending",    spinning: true  },
  installing: { color: "processing", icon: <LoadingOutlined spin />,      label: "Installing", spinning: true  },
  cloning:    { color: "processing", icon: <LoadingOutlined spin />,      label: "Cloning",    spinning: true  },
  deleting:   { color: "warning",    icon: <LoadingOutlined spin />,      label: "Deleting",   spinning: true  },
  ready:      { color: "success",    icon: <CheckCircleOutlined />,       label: "Ready",      spinning: false },
  failed:     { color: "error",      icon: <ExclamationCircleOutlined />, label: "Failed",     spinning: false },
};

const TRANSITIONAL = new Set<ApplicationInstall["status"]>([
  "pending",
  "installing",
  "cloning",
  "deleting",
]);

interface AdminActionsCellProps {
  record: ApplicationInstall;
  canLogin: boolean;
}

const AdminActionsCell = ({
  record,
  canLogin,
}: AdminActionsCellProps) => {
  const qc = useQueryClient();
  const [deleting, setDeleting] = useState(false);
  const { mint: mintMagicLink, loading: magicLinkLoading, error: magicLinkError } = useMagicLink(record.id);

  const handleDelete = async () => {
    setDeleting(true);
    try {
      await apiClient.delete(`/applications/${record.id}`);
      message.success(
        `Deleting ${record.domain_name || record.domain_id}\u2026`,
      );
      qc.invalidateQueries({ queryKey: ["list", "applications"] });
      qc.invalidateQueries({ queryKey: ["list", "databases"] });
    } catch (err) {
      message.error(
        (err as Error)?.message ?? "Failed to delete application",
      );
    } finally {
      setDeleting(false);
    }
  };

  const handleMagicLink = async () => {
    try {
      const response = await mintMagicLink();
      window.open(
        response.url,
        "_blank",
        "noopener,noreferrer"
      );
      message.success("Admin login link opened");
    } catch {
      message.error(magicLinkError || "Failed to generate admin login link");
    }
  };

  return (
    <RowActions
      actions={[
        { key: "login", label: "Log in to admin", icon: <LoginOutlined />, loading: magicLinkLoading, onClick: handleMagicLink, tooltip: "Log in to the admin dashboard", hidden: !canLogin },
        {
          key: "delete",
          label: "Delete",
          icon: <DeleteOutlined />,
          danger: true,
          loading: deleting,
          onClick: handleDelete,
          confirm: { title: "Delete this application?", description: `Permanently removes ${record.domain_name || record.domain_id} and its data. This cannot be undone.`, okText: "Delete" },
        },
      ]}
    />
  );
};

export const AdminApplicationList = () => {
  const { t } = useTranslation();
  const query = useTableURL<ApplicationInstall>({
    resource: "applications",
    defaultSort: "created_at",
    defaultOrder: "desc",
  });

  // Poll while any row is transitional — same cadence as the
  // user-shell list. Cheaper than running a second useQuery with
  // its own subscription.
  const hasTransitional = query.items.some((r) =>
    TRANSITIONAL.has(r.status),
  );
  useEffect(() => {
    if (!hasTransitional) return;
    const h = setInterval(() => query.refetch(), 5000);
    return () => clearInterval(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasTransitional]);

  const handleTableChange: React.ComponentProps<
    typeof Table<ApplicationInstall>
  >["onChange"] = (pagination, _filters, sorter) => {
    const single = Array.isArray(sorter)
      ? (sorter[0] as SorterResult<ApplicationInstall> | undefined)
      : (sorter as SorterResult<ApplicationInstall>);
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
          <AppstoreOutlined /> Applications (All Users)
        </Typography.Title>
      </Space>

      <Card>
        <SearchableTableStringQ<ApplicationInstall>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by domain or user"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
          }}
          onChange={handleTableChange}
        >
          <Table.Column<ApplicationInstall>
            dataIndex="domain_name"
            title={t("adminapplicationlist.domain")}
            key="domain_name"
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            filterIcon={() => (
              <SearchOutlined
                style={{ color: query.params.q ? "#ef4444" : undefined }}
              />
            )}
            filterDropdown={({ confirm, close }) => (
              <div style={{ padding: 8, minWidth: 240 }}>
                <Input.Search
                  placeholder={t("adminapplicationlist.search_by_domain_or_user")}
                  allowClear
                  defaultValue={query.params.q}
                  onSearch={(value) => {
                    query.setParams({ q: value.trim(), page: 1 });
                    confirm({ closeDropdown: false });
                    close();
                  }}
                />
              </div>
            )}
            render={(domainName: string, record) => {
              const base = domainName || record.domain_id;
              const path = record.subdirectory
                ? `/${record.subdirectory}/`
                : "/";
              const label = `${base}${path}`;
              const isLink = record.status === "ready" && !!domainName;
              const appKey = record.app_type || "wordpress";
              return (
                <div
                  style={{ display: "flex", alignItems: "center", gap: 8 }}
                >
                  <CmsIcon appType={appKey} />
                  {isLink ? (
                    <a
                      href={`https://${domainName}${path}`}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      {label}
                    </a>
                  ) : (
                    <span>{label}</span>
                  )}
                </div>
              );
            }}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="owner_email"
            title={t("adminapplicationlist.owner")}
            filterIcon={() => (
              <SearchOutlined
                style={{ color: query.params.q ? "#ef4444" : undefined }}
              />
            )}
            filterDropdown={({ confirm, close }) => (
              <div style={{ padding: 8, minWidth: 240 }}>
                <Input.Search
                  placeholder={t("adminapplicationlist.search_by_domain_or_user")}
                  allowClear
                  defaultValue={query.params.q}
                  onSearch={(value) => {
                    query.setParams({ q: value.trim(), page: 1 });
                    confirm({ closeDropdown: false });
                    close();
                  }}
                />
              </div>
            )}
            render={(_email: string, r) => (
              <span>{r.owner_email || r.owner_username || "—"}</span>
            )}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="version"
            title={t("adminapplicationlist.version")}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="status"
            title={t("adminapplicationlist.status")}
            render={(status: ApplicationInstall["status"], record) => {
              const meta = STATUS_META[status] ?? STATUS_META.pending;
              const tag = (
                <Tag color={meta.color} icon={meta.icon}>
                  {meta.label}
                </Tag>
              );
              if (status === "failed" && record.last_error) {
                return <Tooltip title={record.last_error}>{tag}</Tooltip>;
              }
              return tag;
            }}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="created_at"
            title={t("adminapplicationlist.created")}
            key="created_at"
            sorter={{ multiple: 2 }}
            defaultSortOrder="descend"
            render={(date: string) => shortDateTime(date)}
          />
          <Table.Column<ApplicationInstall>
            title={t("adminapplicationlist.actions")}
            dataIndex="actions"
            render={(_, r) => {
              const appType = r.app_type ?? "wordpress";
              // Admin login is implemented for WordPress, Drupal, and
              // Joomla — matches panel-api ssoAgentCommandFor.
              const canLogin =
                r.status === "ready" &&
                (appType === "wordpress" || appType === "drupal" || appType === "joomla");

              return (
                <AdminActionsCell
                  record={r}
                  canLogin={canLogin}
                />
              );
            }}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
