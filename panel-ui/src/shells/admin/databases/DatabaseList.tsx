// DatabaseList — admin view. Currently unrouted (no /jabali-admin/
// databases mount in App.tsx) but kept Refine-free so a future route
// wire-up doesn't have to touch this file again.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button, Card, Space, Table, Tag, Typography } from "antd";
import { shortDateTime } from "../../../utils/datetime";
import { useNavigate } from "react-router";
import { sorterToParams } from "../../../utils/tableSorter";

import { columnSearchProps } from "../../../components/columnSearch";
import { RowDeleteButton } from "../../../components/RowDeleteButton";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { EmptyWithCTA } from "../../../components/EmptyWithCTA";
import { useDeleteMutation } from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { DatabaseChownAction } from "./DatabaseChownAction";

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
  // GH #1609: admin reassign-owner modal, controlled by the row whose owner is
  // being changed (null = closed).
  const [chownTarget, setChownTarget] = useState<Database | null>(null);

  const handleTableChange: React.ComponentProps<
    typeof Table<Database>
  >["onChange"] = (pagination, _filters, sorter) => {
    const { sort, order } = sorterToParams<Database>(sorter);
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
            sorter
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
            sorter
            render={(date: string) => shortDateTime(date)}
          />
          <Table.Column<Database>
            title={t("databaselist.actions")}
            dataIndex="actions"
            render={(_, r) => (
              <Space>
                <Button size="small" onClick={() => setChownTarget(r)}>
                  {t("databaselist.change_owner")}
                </Button>
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

      {chownTarget && (
        <DatabaseChownAction
          database={chownTarget}
          open={chownTarget != null}
          onClose={() => setChownTarget(null)}
        />
      )}
    </div>
  );
};
