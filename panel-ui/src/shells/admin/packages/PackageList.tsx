// PackageList — hosting packages admin list. Sortable + searchable.
// Post-M21: useTable → useTableURL, <CreateButton>/<EditButton>/
// <DeleteButton> replaced with plain react-router <Button>s + a
// RowDeleteButton wired to useDeleteMutation.
import { useTranslation } from "react-i18next";
import { Button, Card, Input, Space, Table, Tag, Typography } from "antd";
import { EditOutlined, PackageOpenOutlined, SearchOutlined, DeleteOutlined } from "@icons";

import { RowActions } from "../../../components/RowActions";
import { useNavigate } from "react-router";
import type { SorterResult } from "antd/es/table/interface";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { shortDateTime } from "../../../utils/datetime";

type Package = {
  id: string;
  name: string;
  disk_quota_mb: number;
  bandwidth_quota_mb: number;
  max_domains: number;
  max_email_accounts: number;
  max_databases: number;
  ssh_enabled: boolean;
  cgi_enabled: boolean;
  php_exec_enabled: boolean;
  created_at: string;
  updated_at: string;
};

// "∞" for 0 quotas keeps the cell readable instead of printing 0.
const formatQuota = (value: number) => (value === 0 ? "∞" : value);

export const PackageList = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const query = useTableURL<Package>({
    resource: "packages",
    defaultSort: "name",
    defaultOrder: "asc",
  });
  const deleteMutation = useDeleteMutation({ resource: "packages" });

  const handleTableChange: React.ComponentProps<typeof Table<Package>>["onChange"] = (
    pagination,
    _filters,
    sorter,
  ) => {
    const single = Array.isArray(sorter)
      ? (sorter[0] as SorterResult<Package> | undefined)
      : (sorter as SorterResult<Package>);
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
          <PackageOpenOutlined /> Hosting Packages
        </Typography.Title>
        <Button
          type="primary"
          onClick={() => navigate("/jabali-admin/packages/create")}
        >
          Create
        </Button>
      </Space>

      <Card>
        <SearchableTableStringQ<Package>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by package name"
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
                description={t("packagelist.no_hosting_packages_yet")}
                ctaLabel="Create package"
                onCta={() => navigate("/jabali-admin/packages/create")}
              />
            ),
          }}
        >
          <Table.Column
            dataIndex="name"
            title={t("packagelist.name")}
            key="name"
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
                  placeholder={t("packagelist.search_by_package_name")}
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
          />
          <Table.Column
            dataIndex="disk_quota_mb"
            title={t("packagelist.disk_mb")}
            key="disk_quota_mb"
            sorter={{ multiple: 1 }}
            render={(value: number) => formatQuota(value)}
          />
          <Table.Column
            dataIndex="bandwidth_quota_mb"
            title={t("packagelist.bandwidth_mb")}
            key="bandwidth_quota_mb"
            sorter={{ multiple: 1 }}
            render={(value: number) => formatQuota(value)}
          />
          <Table.Column
            dataIndex="max_domains"
            title={t("packagelist.domains")}
            key="max_domains"
            sorter={{ multiple: 1 }}
            render={(value: number) => formatQuota(value)}
          />
          <Table.Column
            dataIndex="max_email_accounts"
            title={t("packagelist.email")}
            key="max_email_accounts"
            sorter={{ multiple: 1 }}
            render={(value: number) => formatQuota(value)}
          />
          <Table.Column
            dataIndex="max_databases"
            title="DB"
            key="max_databases"
            sorter={{ multiple: 1 }}
            render={(value: number) => formatQuota(value)}
          />
          <Table.Column
            dataIndex="ssh_enabled"
            title={t("packagelist.ssh")}
            key="ssh_enabled"
            sorter={{ multiple: 1 }}
            render={(enabled: boolean) =>
              enabled ? <Tag color="green">yes</Tag> : <Tag>no</Tag>
            }
          />
          <Table.Column
            dataIndex="php_exec_enabled"
            title={t("packagelist.php_exec")}
            key="php_exec_enabled"
            sorter={{ multiple: 1 }}
            render={(enabled: boolean) =>
              enabled ? <Tag color="red">on</Tag> : <Tag>off</Tag>
            }
          />
          <Table.Column
            dataIndex="created_at"
            title={t("packagelist.created")}
            key="created_at"
            sorter={{ multiple: 1 }}
            render={(ts: string) => shortDateTime(ts)}
          />
          <Table.Column
            title={t("packagelist.actions")}
            dataIndex="actions"
            render={(_: unknown, r: Package) => (
              <RowActions
                actions={[
                  { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => navigate(`/jabali-admin/packages/edit/${r.id}`) },
                  {
                    key: "delete",
                    label: "Delete",
                    icon: <DeleteOutlined />,
                    danger: true,
                    onClick: () => { void deleteMutation.mutateAsync({ id: r.id }); },
                    confirm: { title: `Delete package "${r.name}"?`, okText: "Delete" },
                  },
                ]}
              />
            )}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
