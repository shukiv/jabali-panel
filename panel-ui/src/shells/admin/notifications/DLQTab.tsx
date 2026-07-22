// DLQTab — admin Notifications > Dead Letter inspector.
//
// Lists envelopes the dispatcher moved to the DLQ stream after exhausting
// every retry. Each row exposes Replay (re-publish to the main queue +
// drop from DLQ) and Drop (XDEL just that entry). Clear-all empties
// the stream via XTRIM MAXLEN 0.
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Empty,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import { useState } from "react";

import { DeleteOutlined, ReloadOutlined, RedoOutlined } from "@icons";
import { RowActions } from "../../../components/RowActions";

import { apiClient } from "../../../apiClient";

type DLQEntry = {
  id: string;
  at: string;
  values: Record<string, string>;
};

type DLQListResponse = {
  data: DLQEntry[];
  total: number;
};

const severityColor: Record<string, string> = {
  info: "blue",
  warning: "gold",
  error: "red",
  critical: "magenta",
};

function formatTs(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

export const DLQTab = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [busyID, setBusyID] = useState<string | null>(null);

  const query = useQuery<DLQListResponse>({
    queryKey: ["notifications", "dlq"],
    queryFn: async () => {
      const { data } = await apiClient.get<DLQListResponse>(
        "/admin/notifications/dlq?limit=200",
      );
      return data;
    },
    refetchInterval: 15_000,
  });

  const rows = query.data?.data ?? [];
  const total = query.data?.total ?? 0;

  const replay = async (row: DLQEntry) => {
    setBusyID(row.id);
    try {
      await apiClient.post(`/admin/notifications/dlq/${row.id}/replay`);
      message.success("Re-queued for delivery");
      qc.invalidateQueries({ queryKey: ["notifications", "dlq"] });
    } catch (err) {
      const apiMsg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      message.error(apiMsg ?? (err instanceof Error ? err.message : "Replay failed"));
    } finally {
      setBusyID(null);
    }
  };

  const drop = async (row: DLQEntry) => {
    setBusyID(row.id);
    try {
      await apiClient.delete(`/admin/notifications/dlq/${row.id}`);
      message.success("Entry dropped");
      qc.invalidateQueries({ queryKey: ["notifications", "dlq"] });
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Drop failed");
    } finally {
      setBusyID(null);
    }
  };

  const clearAll = async () => {
    try {
      await apiClient.delete("/admin/notifications/dlq");
      message.success("DLQ cleared");
      qc.invalidateQueries({ queryKey: ["notifications", "dlq"] });
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Clear failed");
    }
  };

  const [replayingAll, setReplayingAll] = useState(false);

  const replayAll = async () => {
    setReplayingAll(true);
    try {
      const { data } = await apiClient.post<{ replayed: number; skipped: number }>(
        "/admin/notifications/dlq/replay-all",
      );
      const msg =
        data.skipped > 0
          ? `Replayed ${data.replayed}, skipped ${data.skipped} legacy entries`
          : `Replayed ${data.replayed} envelopes`;
      message.success(msg);
      qc.invalidateQueries({ queryKey: ["notifications", "dlq"] });
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Replay all failed");
    } finally {
      setReplayingAll(false);
    }
  };

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Space wrap align="center" style={{ width: "100%", justifyContent: "space-between" }}>
        <Typography.Text type="secondary">
          {total === 0
            ? "No dead-letter entries — every envelope reached its destination."
            : `${total} envelope${total === 1 ? "" : "s"} in the dead-letter queue. Replay re-publishes to the main stream; Drop deletes just that entry.`}
        </Typography.Text>
        <Space wrap>
          <Button
            icon={<ReloadOutlined />}
            onClick={() => qc.invalidateQueries({ queryKey: ["notifications", "dlq"] })}
          >
            Refresh
          </Button>
          {total > 0 && (
            <>
              <Popconfirm
                title={`Replay all ${total} dead-letter entries?`}
                description={t("dlqtab.re_publishes_every_envelope_to_the_main_deli")}
                okText={t("dlqtab.replay_all")}
                onConfirm={replayAll}
              >
                <Button icon={<RedoOutlined />} loading={replayingAll}>
                  Replay all
                </Button>
              </Popconfirm>
              <Popconfirm
                title={`Clear all ${total} dead-letter entries?`}
                description={t("dlqtab.this_deletes_every_entry_in_the_stream_use_o")}
                okText={t("dlqtab.clear")}
                okButtonProps={{ danger: true }}
                onConfirm={clearAll}
              >
                <Button danger icon={<DeleteOutlined />}>
                  Clear all
                </Button>
              </Popconfirm>
            </>
          )}
        </Space>
      </Space>

      <Table<DLQEntry>
        rowKey="id"
        loading={query.isLoading}
        dataSource={rows}
        pagination={false}
        size="small"
        scroll={{ x: "max-content" }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("dlqtab.dlq_is_empty")} /> }}
        columns={[
          {
            title: "When",
            dataIndex: "at",
            width: 180,
            render: (at: string) => formatTs(at),
          },
          {
            title: "Event",
            render: (_, r) => (
              <Space direction="vertical" size={0}>
                <Typography.Text strong>{r.values.event_kind || "—"}</Typography.Text>
                {r.values.severity && (
                  <Tag color={severityColor[r.values.severity] ?? "default"}>
                    {r.values.severity}
                  </Tag>
                )}
              </Space>
            ),
          },
          {
            title: "Title",
            render: (_, r) => (
              <Tooltip title={r.values.body}>
                <Typography.Text>{r.values.title || "—"}</Typography.Text>
              </Tooltip>
            ),
          },
          {
            title: "Reason",
            dataIndex: ["values", "reason"],
            render: (reason: string | undefined) =>
              reason ? <Typography.Text type="danger">{reason}</Typography.Text> : "—",
          },
          {
            title: "Original ID",
            dataIndex: ["values", "orig_id"],
            render: (id: string | undefined) => (
              <Typography.Text type="secondary" style={{ fontFamily: "monospace", fontSize: 12 }}>
                {id ?? "—"}
              </Typography.Text>
            ),
          },
          {
            title: "Actions",
            render: (_, r) => (
              <RowActions
                actions={[
                  { key: "replay", label: "Replay", icon: <RedoOutlined />, loading: busyID === r.id, onClick: () => replay(r) },
                  { key: "drop", label: "Drop", icon: <DeleteOutlined />, danger: true, loading: busyID === r.id, onClick: () => drop(r), confirm: { title: "Drop this entry?", okText: "Drop" } },
                ]}
              />
            ),
          },
        ]}
      />
    </Space>
  );
};
