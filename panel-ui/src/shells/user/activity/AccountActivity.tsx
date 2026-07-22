// AccountActivity — the user's own "Account Activity" feed
// (M49 / ADR-0106). Compromise-detection surface: the user can spot
// an SSH key / login / change they did not make.
//
// Read-only. Backed by GET /api/v1/me/activity — the subject scope is
// enforced SERVER-SIDE in the repo (never a client filter), so this
// only ever shows the caller's own rows. Same useTableURL +
// SearchableTableStringQ shape as the admin view, friendlier columns.
//
// Timestamps render in the viewer's browser timezone: the server tz
// lives behind an admin-only endpoint, and the column header names the
// tz so the rendered time is never ambiguous.
import { useTranslation } from "react-i18next";
import { Card, Space, Table, Tag, Typography } from "antd";
import { FileTextOutlined } from "@icons";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import {
  AuditActionLabel,
  AuditDetail,
  type AuditRow,
  browserTz,
  dash,
  fmtTSInTz,
  resultTag,
} from "../../../components/AuditEventDetail";
import { useTableURL } from "../../../hooks/useTableURL";

export const AccountActivity = () => {
  const { t } = useTranslation();
  const tz = browserTz();
  const query = useTableURL<AuditRow>({
    resource: "me/activity",
    defaultSort: "ts",
    defaultOrder: "desc",
  });

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{ marginBottom: 16, width: "100%", justifyContent: "space-between" }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <FileTextOutlined /> Account Activity
        </Typography.Title>
      </Space>

      <Card>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
          Recent activity on your account. If you see something you did
          not do, change your password and contact your administrator.
        </Typography.Paragraph>
        <SearchableTableStringQ<AuditRow>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search activity"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
            showSizeChanger: true,
            // GH #232: controlled pagination needs onChange or page/size
            // clicks are inert (current/pageSize are pinned to query state).
            onChange: (page, pageSize) => query.setParams({ page, pageSize }),
          }}
          scroll={{ x: "max-content" }}
          expandable={{
            expandedRowRender: (r: AuditRow) => (
              <AuditDetail row={r} tz={tz} />
            ),
          }}
        >
          <Table.Column
            dataIndex="ts"
            title={`When (${tz})`}
            key="ts"
            render={(ts: string) => <code>{fmtTSInTz(ts, tz)}</code>}
          />
          <Table.Column
            title={t("accountactivity.what")}
            key="action"
            render={(_: unknown, r: AuditRow) => (
              <AuditActionLabel
                row={r}
                prefix={
                  r.actor_kind === "admin" ? (
                    <Tag color="blue">by admin</Tag>
                  ) : null
                }
              />
            )}
          />
          <Table.Column
            title={t("accountactivity.target")}
            key="target"
            render={(_: unknown, r: AuditRow) =>
              r.target_type || r.target_id ? (
                <code>
                  {r.target_type}/{r.target_id}
                </code>
              ) : (
                dash
              )
            }
          />
          <Table.Column
            dataIndex="result"
            title={t("accountactivity.result")}
            key="result"
            render={resultTag}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
