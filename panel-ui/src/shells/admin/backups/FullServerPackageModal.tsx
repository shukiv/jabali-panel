// FullServerPackageModal — GH #1408 / #502. Package a whole backup RUN (the
// system leg + every account) into ONE downloadable container. Packaging
// materializes every account, so it runs as a background job the modal polls;
// when it's ready the admin downloads a single file — the basis for moving or
// recovering a full server in one archive.
import { useEffect, useRef, useState } from "react";
import { Alert, Button, Modal, Progress, Space, Typography } from "antd";
import { feedback } from "../../../lib/feedback";
import { apiClient } from "../../../apiClient";
import { downloadUrl } from "../../../utils/download";
import { extractApiError } from "../../../apiErrors";

interface Props {
  runId: string | null;
  open: boolean;
  onClose: () => void;
}

type Phase = "warn" | "packaging" | "ready" | "failed";
interface Status {
  status: "packaging" | "done" | "failed";
  bytes?: number;
  packed?: string[];
  skipped?: string[];
  error?: string;
}

export function FullServerPackageModal({ runId, open, onClose }: Props) {
  const [phase, setPhase] = useState<Phase>("warn");
  const [status, setStatus] = useState<Status | null>(null);
  const cancelled = useRef(false);

  useEffect(() => {
    if (open) {
      setPhase("warn");
      setStatus(null);
      cancelled.current = false;
    }
    return () => {
      cancelled.current = true;
    };
  }, [open, runId]);

  const startPackaging = async () => {
    if (!runId) return;
    setPhase("packaging");
    try {
      await apiClient.post(`/admin/system/full-backup/${runId}/package`);
    } catch (err) {
      feedback.message.error(extractApiError(err, "Could not start packaging"));
      setPhase("warn");
      return;
    }
    // Poll the packaging job until it seals.
    const deadline = Date.now() + 60 * 60 * 1000;
    for (;;) {
      await new Promise((r) => setTimeout(r, 3000));
      if (cancelled.current) return;
      try {
        const { data } = await apiClient.get<Status>(
          `/admin/system/full-backup/${runId}/package-status`,
        );
        setStatus(data);
        if (data.status === "done") {
          setPhase("ready");
          return;
        }
        if (data.status === "failed") {
          setPhase("failed");
          return;
        }
      } catch {
        // transient — keep polling until the deadline
      }
      if (Date.now() > deadline) {
        setPhase("failed");
        setStatus({ status: "failed", error: "packaging timed out" });
        return;
      }
    }
  };

  const download = () => {
    if (!runId) return;
    downloadUrl(`/api/v1/admin/system/full-backup/${runId}/download`);
  };

  return (
    <Modal
      title="Full server backup — package & download"
      open={open}
      onCancel={onClose}
      footer={null}
      maskClosable={phase !== "packaging"}
      closable={phase !== "packaging"}
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Bundles this run — the system backup and every account backup — into one
          archive you can download and move to another server.
        </Typography.Paragraph>

        <Alert
          type="warning"
          showIcon
          message="Disk space"
          description="Packaging temporarily writes the accounts to disk before compressing them — plan for up to about 2× this server's account data in free space while it runs."
        />

        {phase === "warn" && (
          <Button type="primary" onClick={startPackaging}>
            Package for download
          </Button>
        )}

        {phase === "packaging" && (
          <Space direction="vertical" style={{ width: "100%" }}>
            <Progress percent={100} status="active" showInfo={false} />
            <Typography.Text type="secondary">
              Packaging in the background… this can take several minutes for a full
              server. You can leave this open; the archive is prepared on the server.
            </Typography.Text>
          </Space>
        )}

        {phase === "ready" && status && (
          <>
            <Alert
              type="success"
              showIcon
              message="Archive ready"
              description={
                <Space direction="vertical" size={0}>
                  <span>{(status.packed?.length ?? 0)} backup(s) packaged.</span>
                  {(status.skipped?.length ?? 0) > 0 && (
                    <Typography.Text type="warning">
                      Skipped (no snapshot): {status.skipped?.join(", ")}
                    </Typography.Text>
                  )}
                </Space>
              }
            />
            <Button type="primary" icon={null} onClick={download}>
              Download full server backup
            </Button>
          </>
        )}

        {phase === "failed" && (
          <Alert
            type="error"
            showIcon
            message="Packaging failed"
            description={status?.error || "The full server backup could not be packaged."}
          />
        )}
      </Space>
    </Modal>
  );
}
