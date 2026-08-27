// UpdatesLogTab — the panel/system update run history (from
// /admin/updates/history). Surfaced as a tab on the Logs & Statistics page
// so update logs live alongside the other log surfaces. The Updates page
// keeps a "Recent Update History" timeline; this is the full table view.
import { Card, Table, Tag, Typography } from "antd";
import dayjs from "dayjs";

import {
  useUpdateHistory,
  type UpdateHistoryRow,
} from "../../../hooks/useSystemUpdates";

const { Text } = Typography;

function statusTag(status: string) {
  const color = status === "success" ? "green" : status === "failed" ? "red" : "blue";
  return <Tag color={color}>{status}</Tag>;
}

export const UpdatesLogTab = () => {
  const { data, isLoading } = useUpdateHistory(100);
  const rows = data?.items ?? [];

  return (
    <Card>
      <Table<UpdateHistoryRow>
        rowKey="id"
        size="middle"
        loading={isLoading}
        dataSource={rows}
        scroll={{ x: "max-content" }}
        pagination={{ defaultPageSize: 20, showTotal: (t) => `${t} runs` }}
        columns={[
          {
            title: "Time",
            dataIndex: "started_at",
            render: (v: string) => dayjs(v).format("MMM D, YYYY HH:mm"),
            sorter: (a, b) => a.started_at.localeCompare(b.started_at),
            defaultSortOrder: "descend",
          },
          {
            title: "Type",
            dataIndex: "kind",
            render: (k: string) => (k === "jabali" ? "Jabali panel" : "System packages"),
            filters: [
              { text: "Jabali panel", value: "jabali" },
              { text: "System packages", value: "apt" },
            ],
            onFilter: (val, r) => r.kind === val,
          },
          { title: "Action", dataIndex: "action" },
          {
            title: "Status",
            dataIndex: "status",
            render: (s: string) => statusTag(s),
          },
          {
            title: "Summary",
            dataIndex: "summary",
            render: (s: string) => <Text type="secondary">{s || "—"}</Text>,
          },
        ]}
      />
    </Card>
  );
};
