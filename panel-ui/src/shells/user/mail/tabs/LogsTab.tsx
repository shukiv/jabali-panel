// LogsTab — M6.5 Step 7. Read-only mail log viewer.

import { useTranslation } from "react-i18next";
import { useState } from "react";
import {
  Alert,
  Button,
  DatePicker,
  Input,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { ReloadOutlined } from "@icons";
import type { Dayjs } from "dayjs";

import { useMailLogs } from "../../../../hooks/useMailLogs";
import { MailLogDetailDrawer } from "./MailLogDetailDrawer";

interface Filters {
  from?: Dayjs | null;
  to?: Dayjs | null;
  sender?: string;
  recipient?: string;
}

// GH #1387: domainName scopes the logs to one domain (the drill-down); unset =
// all the caller's domains (flat page, unchanged).
export const LogsTab = ({ domainName }: { domainName?: string } = {}) => {
  const { t } = useTranslation();
  const [filters, setFilters] = useState<Filters>({});
  const [page, setPage] = useState(1);
  const [detailQid, setDetailQid] = useState<string | null>(null);
  const pageSize = 50;

  const { data, isLoading, error, refetch, isFetching } = useMailLogs({
    from_date: filters.from ? filters.from.toISOString() : undefined,
    to_date: filters.to ? filters.to.toISOString() : undefined,
    sender: filters.sender,
    recipient: filters.recipient,
    limit: pageSize,
    offset: (page - 1) * pageSize,
    domain: domainName,
  });

  const entries = data?.data ?? [];
  const total = data?.total ?? 0;

  return (
    <div>
      <Space style={{ width: "100%", justifyContent: "flex-end", marginBottom: 12, flexWrap: "wrap", rowGap: 8 }}>
        <Space>
          <span
            role="button"
            onClick={() => refetch()}
            style={{ cursor: "pointer", color: "#1677ff", display: "inline-flex", alignItems: "center", gap: 4 }}
          >
            <ReloadOutlined spin={isFetching} /> Refresh
          </span>
        </Space>
      </Space>

      <Space wrap style={{ marginBottom: 12 }}>
        <DatePicker
          showTime
          placeholder={t("logstab.from")}
          value={filters.from}
          onChange={(v) => setFilters((f) => ({ ...f, from: v }))}
        />
        <DatePicker
          showTime
          placeholder="To"
          value={filters.to}
          onChange={(v) => setFilters((f) => ({ ...f, to: v }))}
        />
        <Input
          placeholder={t("logstab.sender_contains")}
          allowClear
          value={filters.sender ?? ""}
          onChange={(e) => setFilters((f) => ({ ...f, sender: e.target.value }))}
          style={{ width: 200 }}
        />
        <Input
          placeholder={t("logstab.recipient_contains")}
          allowClear
          value={filters.recipient ?? ""}
          onChange={(e) => setFilters((f) => ({ ...f, recipient: e.target.value }))}
          style={{ width: 200 }}
        />
      </Space>

      {error && (
        <Alert
          type="warning"
          message={t("logstab.mail_logs_unavailable")}
          description={t("logstab.the_mail_server_s_trace_log_isn_t_responding")}
          style={{ marginBottom: 12 }}
          showIcon
        />
      )}

      <Typography.Text type="secondary" style={{ display: "block", fontSize: 12, marginBottom: 8 }}>
        Click a column header to sort the loaded page.
      </Typography.Text>
      <Table
        rowKey={(r) => `${r.timestamp}-${r.from}-${r.to}`}
        dataSource={entries}
        loading={isLoading}
        pagination={{
          current: page,
          pageSize,
          total,
          showSizeChanger: false,
          onChange: (p) => setPage(p),
        }}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Timestamp",
            dataIndex: "timestamp",
            width: 200,
            // GH #1365: client-side column sort (sorts the current page).
            sorter: (a, b) => a.timestamp.localeCompare(b.timestamp),
            defaultSortOrder: "descend" as const,
            render: (v: string) => new Date(v).toLocaleString(),
          },
          {
            title: "From",
            dataIndex: "from",
            sorter: (a, b) => (a.from ?? "").localeCompare(b.from ?? ""),
            render: (v: string) => (
              <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text>
            ),
          },
          {
            title: "To",
            dataIndex: "to",
            sorter: (a, b) => (a.to ?? "").localeCompare(b.to ?? ""),
            render: (v: string) => (
              <Typography.Text style={{ fontFamily: "monospace" }}>{v}</Typography.Text>
            ),
          },
          {
            title: "Size",
            dataIndex: "size",
            width: 100,
            sorter: (a, b) => (a.size ?? 0) - (b.size ?? 0),
            render: (n: number) => `${(n / 1024).toFixed(1)} KB`,
          },
          {
            // GH #262: delivery outcome, correlated from Stalwart's
            // queue.message-queued + delivery.completed/failed events.
            title: "Status",
            dataIndex: "status",
            width: 110,
            sorter: (a, b) => (a.status ?? "").localeCompare(b.status ?? ""),
            render: (v: string | undefined) => {
              const map: Record<string, { color: string; label: string }> = {
                delivered: { color: "green", label: "Delivered" },
                failed: { color: "red", label: "Failed" },
                queued: { color: "blue", label: "Queued" },
              };
              const m = map[v ?? ""] ?? { color: "default", label: v || "—" };
              return <Tag color={m.color}>{m.label}</Tag>;
            },
          },
          {
            title: "",
            key: "trail",
            width: 80,
            render: (_: unknown, r) =>
              r.queue_id ? (
                <Button size="small" type="link" onClick={() => setDetailQid(r.queue_id!)}>
                  Trail
                </Button>
              ) : null,
          },
        ]}
      />
      <MailLogDetailDrawer
        queueId={detailQid}
        open={detailQid !== null}
        onClose={() => setDetailQid(null)}
      />
    </div>
  );
};
