// HistoryTab — admin Notifications > History table (M14 follow-up).
//
// Paginated table of notification_history rows. Admins see both their
// own per-user deliveries AND system-wide broadcast rows (user_id IS
// NULL) via ListForAdminInbox on the backend. Click a row to mark it
// read and (if a deeplink exists) navigate there.
import { useTranslation } from "react-i18next";
import { Button, Descriptions, Modal, Popconfirm, Space, Table, Tag, Tooltip, Typography, message } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";

import { CheckOutlined, DeleteOutlined, EyeOutlined } from "@icons";
import { RowActionButton } from "../../../components/RowActionButton";

import { apiClient } from "../../../apiClient";

type NotificationHistoryRow = {
  id: string;
  event_kind: string;
  severity: "info" | "warning" | "error" | "critical";
  title: string;
  body: string;
  deeplink?: string;
  outcome: "pending" | "sent" | "failed" | "skipped";
  channel_id?: string | null;
  envelope_id?: string | null;
  error_message?: string;
  retry_count: number;
  read_at?: string | null;
  created_at: string;
  is_dead_letter?: boolean;
};

type InboxListResponse = {
  data: NotificationHistoryRow[];
  total: number;
  page: number;
  page_size: number;
  unread: number;
};

const severityColor: Record<NotificationHistoryRow["severity"], string> = {
  info: "blue",
  warning: "gold",
  error: "red",
  critical: "magenta",
};

const outcomeColor: Record<NotificationHistoryRow["outcome"], string> = {
  pending: "default",
  sent: "green",
  failed: "red",
  skipped: "orange",
};

function formatTs(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

export const HistoryTab = () => {
  const { t } = useTranslation();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [info, setInfo] = useState<NotificationHistoryRow | null>(null);
  const navigate = useNavigate();
  const qc = useQueryClient();

  // GH #726: the notification bell links here with ?focus=<id> for alerts that
  // have no dedicated destination. Scroll that row into view and highlight it.
  const [searchParams] = useSearchParams();
  const focusId = searchParams.get("focus");

  const listKey = ["notifications", "history", page, pageSize] as const;

  const query = useQuery<InboxListResponse>({
    queryKey: listKey,
    queryFn: async () => {
      const { data } = await apiClient.get<InboxListResponse>(
        `/notifications/inbox?page=${page}&page_size=${pageSize}`,
      );
      return data;
    },
    refetchInterval: 30_000,
    staleTime: 10_000,
  });

  const rows = query.data?.data ?? [];
  const total = query.data?.total ?? 0;
  const unread = query.data?.unread ?? 0;

  // Once the focused row is on the page (antd stamps data-row-key on each <tr>),
  // scroll it into view. Recent alerts land on page 1, so this usually hits.
  useEffect(() => {
    if (!focusId || rows.length === 0) return;
    const el = document.querySelector(`[data-row-key="${CSS.escape(focusId)}"]`);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focusId, rows.length]);

  const markAllRead = async () => {
    try {
      await apiClient.post("/notifications/inbox/read-all");
      qc.invalidateQueries({ queryKey: ["notifications"] });
      message.success("Marked all as read");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Mark-all failed");
    }
  };

  const clearAll = async () => {
    try {
      await apiClient.delete("/notifications/inbox");
      qc.invalidateQueries({ queryKey: ["notifications"] });
      message.success("All notifications cleared");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Clear failed");
    }
  };

  const markRead = async (row: NotificationHistoryRow) => {
    if (row.read_at) return;
    try {
      await apiClient.post(`/notifications/inbox/${row.id}/read`);
      qc.invalidateQueries({ queryKey: ["notifications"] });
    } catch {
      // Non-blocking.
    }
  };

  const handleRowClick = (row: NotificationHistoryRow) => {
    void markRead(row);
    if (row.deeplink) {
      navigate(row.deeplink);
    }
  };

  return (
    <div>
      <Space
        wrap
        style={{
          marginBottom: 16,
          width: "100%",
          justifyContent: "space-between",
          rowGap: 8,
        }}
      >
        <Typography.Text type="secondary">
          {total} total · {unread} unread
        </Typography.Text>
        <Space wrap>
          <Button
            size="small"
            icon={<CheckOutlined />}
            onClick={markAllRead}
            disabled={unread === 0}
          >
            Mark all as read
          </Button>
          <Popconfirm
            title={t("historytab.clear_all_notifications")}
            description={t("historytab.this_permanently_deletes_every_row_in_your_i")}
            onConfirm={clearAll}
            okText={t("historytab.clear")}
            okButtonProps={{ danger: true }}
          >
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              disabled={total === 0}
            >
              Clear all
            </Button>
          </Popconfirm>
        </Space>
      </Space>

      <Table<NotificationHistoryRow>
        rowKey="id"
        loading={query.isLoading}
        dataSource={rows}
        pagination={{
          current: page,
          pageSize,
          total,
          showSizeChanger: true,
          pageSizeOptions: ["10", "25", "50", "100"],
          onChange: (p, s) => {
            setPage(p);
            setPageSize(s);
          },
        }}
        scroll={{ x: "max-content" }}
        onRow={(row) => ({
          onClick: () => handleRowClick(row),
          style: {
            cursor: row.deeplink ? "pointer" : "default",
            // GH #726: the notification the bell sent us to (?focus=<id>) gets a
            // stronger amber tint so it stands out from the unread blue wash.
            background:
              row.id === focusId
                ? "rgba(250,173,20,0.18)"
                : row.read_at
                  ? undefined
                  : "rgba(24,144,255,0.04)",
          },
        })}
      >
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.time")}
          dataIndex="created_at"
          width={180}
          render={(v: string) => formatTs(v)}
        />
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.severity")}
          dataIndex="severity"
          width={110}
          render={(s: NotificationHistoryRow["severity"]) => (
            <Tag color={severityColor[s] ?? "default"}>{s}</Tag>
          )}
        />
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.event")}
          dataIndex="event_kind"
          width={200}
          render={(k: string) => <code style={{ fontSize: 12 }}>{k}</code>}
        />
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.title")}
          dataIndex="title"
          render={(v: string) => <Typography.Text strong>{v}</Typography.Text>}
        />
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.body")}
          dataIndex="body"
          width={420}
          render={(v: string) => (
            <Typography.Paragraph
              style={{ margin: 0, fontSize: 12, whiteSpace: "pre-wrap" }}
              ellipsis={{ rows: 2, expandable: true, symbol: "more" }}
            >
              {v}
            </Typography.Paragraph>
          )}
        />
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.outcome")}
          dataIndex="outcome"
          width={160}
          render={(o: NotificationHistoryRow["outcome"], row) => {
            const tag = <Tag color={outcomeColor[o] ?? "default"}>{o}</Tag>;
            const wrapped =
              o === "failed" && row.error_message ? (
                <Tooltip title={row.error_message}>{tag}</Tooltip>
              ) : (
                tag
              );
            if (row.is_dead_letter) {
              return (
                <Space size={4} wrap>
                  {wrapped}
                  <Tooltip title={t("historytab.this_envelope_was_moved_to_the_dead_letter_q")}>
                    <Tag color="volcano">dead letter</Tag>
                  </Tooltip>
                </Space>
              );
            }
            return wrapped;
          }}
        />
        <Table.Column<NotificationHistoryRow>
          title={t("historytab.read")}
          dataIndex="read_at"
          width={80}
          render={(v: string | null | undefined) =>
            v ? <Tag color="default">read</Tag> : <Tag color="blue">new</Tag>
          }
        />
        <Table.Column<NotificationHistoryRow>
          title=""
          width={64}
          render={(_v: unknown, row) => (
            <RowActionButton
              size="small"
              icon={<EyeOutlined />}
              onClick={(e) => {
                e.stopPropagation();
                setInfo(row);
                void markRead(row);
              }}
              aria-label={t("historytab.show_details")}
            >
              View
            </RowActionButton>
          )}
        />
      </Table>

      <Modal
        open={info !== null}
        onCancel={() => setInfo(null)}
        title={info ? `${info.event_kind} · ${info.severity}` : ""}
        footer={[
          info?.deeplink ? (
            <Button
              key="open"
              type="primary"
              onClick={() => {
                if (info?.deeplink) {
                  navigate(info.deeplink);
                  setInfo(null);
                }
              }}
            >
              Open
            </Button>
          ) : null,
          <Button key="close" onClick={() => setInfo(null)}>
            Close
          </Button>,
        ]}
        width={720}
      >
        {info && (
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t("historytab.time")}>{formatTs(info.created_at)}</Descriptions.Item>
            <Descriptions.Item label={t("historytab.severity")}>
              <Tag color={severityColor[info.severity] ?? "default"}>{info.severity}</Tag>
            </Descriptions.Item>
            <Descriptions.Item label={t("historytab.event")}>
              <code style={{ fontSize: 12 }}>{info.event_kind}</code>
            </Descriptions.Item>
            <Descriptions.Item label={t("historytab.title")}>
              <Typography.Text strong>{info.title}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label={t("historytab.body")}>
              <Typography.Paragraph
                style={{ margin: 0, fontSize: 13, whiteSpace: "pre-wrap" }}
                copyable={{ text: info.body }}
              >
                {info.body}
              </Typography.Paragraph>
            </Descriptions.Item>
            <Descriptions.Item label={t("historytab.outcome")}>
              <Space size={4} wrap>
                <Tag color={outcomeColor[info.outcome] ?? "default"}>{info.outcome}</Tag>
                {info.is_dead_letter && <Tag color="volcano">dead letter</Tag>}
              </Space>
              {info.error_message && (
                <Typography.Paragraph
                  type="danger"
                  style={{ margin: "8px 0 0", fontSize: 12, whiteSpace: "pre-wrap" }}
                >
                  {info.error_message}
                </Typography.Paragraph>
              )}
            </Descriptions.Item>
            {info.channel_id && (
              <Descriptions.Item label={t("historytab.channel")}>
                <code style={{ fontSize: 12 }}>{info.channel_id}</code>
              </Descriptions.Item>
            )}
            {info.retry_count > 0 && (
              <Descriptions.Item label={t("historytab.retries")}>{info.retry_count}</Descriptions.Item>
            )}
            {info.deeplink && (
              <Descriptions.Item label={t("historytab.deeplink")}>
                <code style={{ fontSize: 12 }}>{info.deeplink}</code>
              </Descriptions.Item>
            )}
          </Descriptions>
        )}
      </Modal>
    </div>
  );
};
