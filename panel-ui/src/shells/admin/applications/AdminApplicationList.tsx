// Admin-shell cross-user applications list — read-only view of every
// installed application (WordPress today, more as M19 lands them).
// Admins modify installs by opening the relevant user's panel directly
// in their own browser tab — no in-panel impersonation after M20.
//
// The install row, status badge, domain cell, transitional-poll rule, login
// capability, and delete + invalidation are shared with the tenant list via
// components/applications (JAB-334) — a neutral module, so neither shell
// depends on the other. This adapter keeps the admin deltas: the owner column,
// the created_at-desc default sort, and a read-only action set (login + delete).
import { useTranslation } from "react-i18next";
import { shortDateTime } from "../../../utils/datetime";
import { Card, Input, Space, Table, Typography } from "antd";
import { AppstoreOutlined, SearchOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";
import { sorterToParams } from "../../../utils/tableSorter";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useTableURL } from "../../../hooks/useTableURL";
import { useMagicLink } from "../../../hooks/useMagicLink";
import { ApplicationDomainCell } from "../../../components/applications/ApplicationDomainCell";
import { ApplicationStatusTag } from "../../../components/applications/ApplicationStatusTag";
import { buildApplicationActions } from "../../../components/applications/buildApplicationActions";
import { useTransitionalPoll } from "../../../components/applications/useTransitionalPoll";
import { useDeleteApplication } from "../../../components/applications/useDeleteApplication";
import {
  canApplicationLogin,
  openApplicationLogin,
  type ApplicationInstall,
} from "../../../components/applications/applicationInventory";

interface AdminActionsCellProps {
  record: ApplicationInstall;
  canLogin: boolean;
}

const AdminActionsCell = ({ record, canLogin }: AdminActionsCellProps) => {
  const { mint, loading: loginLoading, error: loginError } = useMagicLink(record.id);
  const { deletingId, deleteApplication } = useDeleteApplication();

  return (
    <RowActions
      actions={buildApplicationActions({
        canLogin,
        onLogin: () => openApplicationLogin(mint, loginError),
        loginLoading,
        onDelete: () => deleteApplication(record),
        deleting: deletingId === record.id,
        deleteDescription: `Permanently removes ${record.domain_name || record.domain_id} and its data. This cannot be undone.`,
      })}
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

  // Poll while any row is transitional — same rule as the tenant list.
  useTransitionalPoll(query.items, query.refetch);

  const handleTableChange: React.ComponentProps<
    typeof Table<ApplicationInstall>
  >["onChange"] = (pagination, _filters, sorter) => {
    const { sort, order } = sorterToParams<ApplicationInstall>(sorter);
    query.setParams({
      page: pagination.current ?? 1,
      pageSize: pagination.pageSize ?? 20,
      sort,
      order,
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
            sorter
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
            render={(_domainName: string, record) => (
              <ApplicationDomainCell record={record} />
            )}
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
            render={(status: ApplicationInstall["status"], record) => (
              <ApplicationStatusTag status={status} lastError={record.last_error} />
            )}
          />
          <Table.Column<ApplicationInstall>
            dataIndex="created_at"
            title={t("adminapplicationlist.created")}
            key="created_at"
            sorter
            defaultSortOrder="descend"
            render={(date: string) => shortDateTime(date)}
          />
          <Table.Column<ApplicationInstall>
            title={t("adminapplicationlist.actions")}
            dataIndex="actions"
            render={(_, r) => (
              <AdminActionsCell record={r} canLogin={canApplicationLogin(r)} />
            )}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
