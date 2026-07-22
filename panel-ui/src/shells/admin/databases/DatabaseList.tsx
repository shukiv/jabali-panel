// DatabaseList — admin view. Currently unrouted (no /jabali-admin/
// databases mount in App.tsx) but kept Refine-free so a future route
// wire-up doesn't have to touch this file again.
import { useTranslation } from "react-i18next";
import { Button, Card, Space, Table, Tag, Typography } from "antd";
import { shortDateTime } from "../../../utils/datetime";
import { useNavigate } from "react-router";
import type { SorterResult } from "antd/es/table/interface";

import { columnSearchProps } from "../../../components/columnSearch";
import { RowDeleteButton } from "../../../components/RowDeleteButton";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";

export type Database = {
  id: string;
  user_id: string;
  name: string;
  engine: "mariadb" | "postgres";
  charset?: string;
  collation?: string;
  created_at: string;
  updated_at: string;
};

const engineColorMap: Record<string, string> = {
  mariadb: "blue",
  postgres: "green",
};

export const DatabaseList = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const query = useTableURL<Database>({
    resource: "databases",
    defaultSort: "name",
    defaultOrder: "asc",
  });
  const deleteMutation = useDeleteMutation({ resource: "databases" });

  const handleTableChange: React.ComponentProps<
    typeof Table<Database>
  >["onChange"] = (pagination, _filters, sorter) => {
    const single = Array.isArray(sorter)
      ? (sorter[0] as SorterResult<Database> | undefined)
      : (sorter as SorterResult<Database>);
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
          Databases
        </Typography.Title>
        <Button
          type="primary"
          onClick={() => navigate("/jabali-admin/databases/create")}
        >
          Create
        </Button>
      </Space>

      <Card>
        <SearchableTableStringQ<Database>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search by database name"
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
                description={t("databaselist.no_databases_yet")}
                ctaLabel="Create database"
                onCta={() => navigate("/jabali-admin/databases/create")}
              />
            ),
          }}
        >
          <Table.Column<Database>
            dataIndex="name"
            title={t("databaselist.database")}
            key="name"
            sorter={{ multiple: 1 }}
            defaultSortOrder="ascend"
            {...columnSearchProps<Database>({
              placeholder: "Search by database name",
              currentQ: query.params.q,
              onSearch: (v) => query.setParams({ q: v, page: 1 }),
            })}
          />
          <Table.Column<Database>
            dataIndex="user_id"
            title={t("databaselist.user_id")}
            render={(value: string) => value.substring(0, 8)}
          />
          <Table.Column<Database>
            dataIndex="engine"
            title={t("databaselist.engine")}
            render={(engine: string) => (
              <Tag color={engineColorMap[engine] || "default"}>{engine}</Tag>
            )}
          />
          <Table.Column<Database>
            dataIndex="charset"
            title={t("databaselist.charset")}
          />
          <Table.Column<Database>
            dataIndex="created_at"
            title={t("databaselist.created")}
            key="created_at"
            sorter={{ multiple: 2 }}
            render={(date: string) => shortDateTime(date)}
          />
          <Table.Column<Database>
            title={t("databaselist.actions")}
            dataIndex="actions"
            render={(_, r) => (
              <Space>
                <RowDeleteButton
                  confirmTitle={`Delete database "${r.name}"?`}
                  onConfirm={async () => {
                    await deleteMutation.mutateAsync({ id: r.id });
                  }}
                />
              </Space>
            )}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
