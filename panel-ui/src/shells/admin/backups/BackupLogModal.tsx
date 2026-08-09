// BackupLogModal — fetches journalctl tail for a backup transient unit
// and shows it as a scrollable <pre>. account_backup +
// account_restore route to /admin/backups/:id/logs; system_backup +
// system_restore go to /admin/system/backups/:id/logs (same agent
// command on the other side, just different mount point).
import { Button, Modal, Spin, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { ReloadOutlined } from "@icons";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

interface BackupJobRef {
  id: string;
  kind: string;
  status: string;
}

interface LogResponse {
  unit: string;
  status: string;
  exit_code?: number;
  log_text: string;
  fetched_at: string;
}

interface BackupLogModalProps {
  job: BackupJobRef | null;
  onClose: () => void;
}

const logsPathFor = (kind: string, id: string): string => {
  if (kind === "system_backup" || kind === "system_restore") {
    return `/admin/system/backups/${id}/logs`;
  }
  return `/admin/backups/${id}/logs`;
};

export const BackupLogModal = ({ job, onClose }: BackupLogModalProps) => {
  const [data, setData] = useState<LogResponse | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchLogs = async () => {
    if (!job) return;
    setLoading(true);
    try {
      const resp = await apiClient.get<{ data: LogResponse }>(logsPathFor(job.kind, job.id));
      setData(resp.data?.data ?? null);
    } catch (err) {
      feedback.message.error(extractApiError(err, "Failed to load logs"));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (job) {
      setData(null);
      fetchLogs();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [job?.id]);

  return (
    <Modal
      open={!!job}
      onCancel={onClose}
      title={job ? `Log — ${job.id.slice(0, 12)}…` : "Log"}
      width={900}
      footer={[
        <Button key="refresh" icon={<ReloadOutlined />} onClick={fetchLogs} loading={loading}>
          Refresh
        </Button>,
        <Button key="close" onClick={onClose}>
          Close
        </Button>,
      ]}
    >
      {data && (
        <div style={{ marginBottom: 12 }}>
          <Typography.Text code copyable>
            {data.unit}
          </Typography.Text>{" "}
          <Tag color={data.status === "active" ? "blue" : data.status === "failed" ? "red" : "default"}>
            {data.status || "unknown"}
          </Tag>
          {typeof data.exit_code === "number" && (
            <Tag color={data.exit_code === 0 ? "green" : "red"}>exit={data.exit_code}</Tag>
          )}
        </div>
      )}
      <Spin spinning={loading && !data}>
        <pre
          style={{
            background: "#111",
            color: "#eee",
            padding: 12,
            borderRadius: 4,
            maxHeight: "60vh",
            overflow: "auto",
            fontSize: 12,
            fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
            margin: 0,
            whiteSpace: "pre-wrap",
            wordBreak: "break-all",
          }}
        >
          {data?.log_text || (loading ? "" : "(no log output)")}
        </pre>
      </Spin>
    </Modal>
  );
};
