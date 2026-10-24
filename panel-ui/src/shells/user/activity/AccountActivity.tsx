// AccountActivity — the user's own "Account Activity" feed
// (M49 / ADR-0106). Compromise-detection surface: the user can spot
// an SSH key / login / change they did not make.
//
// Read-only. Backed by GET /api/v1/me/activity — the subject scope is
// enforced SERVER-SIDE in the repo (never a client filter), so this
// only ever shows the caller's own rows. Same useTableURL +
// SearchableTableStringQ shape as the admin view, friendlier columns.
//
// Timestamps render in the server's timezone (GH #2164, from GET /me), and
// the column header names the tz so the rendered time is never ambiguous.
import { useTranslation } from "react-i18next";
import { Table, Tag, Typography } from "antd";

import { SearchableTableStringQ } from "../../../components/SearchableTable";
import {
  AuditActionLabel,
  AuditDetail,
  AuditTime,
  type AuditRow,
  dash,
  resultTag,
} from "../../../components/AuditEventDetail";
import { usePanelClock } from "../../../components/Timestamp";
import { useTableURL } from "../../../hooks/useTableURL";

export const AccountActivity = () => {
  const { t } = useTranslation();
  const tz = usePanelClock().timezone;
  const query = useTableURL<AuditRow>({
    resource: "me/activity",
    defaultSort: "ts",
    defaultOrder: "desc",
  });

  // A tab of Logs & Statistics: the tab names it and the page's card frames
  // it, so no heading or card of its own here (GH #2251).
  return (
    <div>
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
            <AuditDetail row={r} />
          ),
        }}
      >
        <Table.Column
          dataIndex="ts"
          title={`When (${tz})`}
          key="ts"
          render={(ts: string) => <AuditTime ts={ts} />}
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
    </div>
  );
};
