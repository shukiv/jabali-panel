import { useTranslation } from "react-i18next";
import { useState } from "react";
import { Drawer, Button, Space, Card, Select, Spin } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  CopyOutlined,
  ReloadOutlined,
} from "@icons";
import { useQuery } from "@tanstack/react-query";
import { getCronJobLog, type CronLogResponse } from "../../apiClient";

interface CronLogDrawerProps {
  open: boolean;
  onClose: () => void;
  jobId: string;
  /** Drawer title; defaults to the cron job log title. */
  title?: string;
  /** Reads a different log (Admin → System jobs, GH #1686); defaults to the cron job log. */
  fetchLog?: (lines: number) => Promise<CronLogResponse>;
}

export const CronLogDrawer = ({
  open,
  onClose,
  jobId,
  title,
  fetchLog,
}: CronLogDrawerProps) => {
  const { t } = useTranslation();
  const [lines, setLines] = useState<number>(200);

  const {
    data: logResponse = { log: "", lines: 0 },
    isLoading,
    refetch,
  } = useQuery({
    queryKey: [fetchLog ? "system-job-log" : "cron-log", jobId, lines],
    queryFn: async () => (fetchLog ? fetchLog(lines) : getCronJobLog(jobId, lines)),
    enabled: open,
  });

  const handleCopyToClipboard = () => {
    navigator.clipboard.writeText(logResponse.log).then(() => {
      feedback.message.success("Log copied to clipboard");
    });
  };

  const handleRefresh = () => {
    refetch();
  };

  return (
    <Drawer
      title={title ?? t("cronlogdrawer.cron_job_log")}
      placement="right"
      onClose={onClose}
      open={open}
      width={700}
      extra={
        <Space>
          <Button
            type="text"
            icon={<CopyOutlined />}
            onClick={handleCopyToClipboard}
          >
            Copy
          </Button>
          <Button
            type="text"
            icon={<ReloadOutlined />}
            onClick={handleRefresh}
            loading={isLoading}
          >
            Refresh
          </Button>
        </Space>
      }
    >
      <Space direction="vertical" style={{ width: "100%", marginBottom: 16 }}>
        <Select
          style={{ width: 120 }}
          value={lines}
          onChange={setLines}
          options={[
            { label: "Last 50 lines", value: 50 },
            { label: "Last 200 lines", value: 200 },
            { label: "Last 500 lines", value: 500 },
          ]}
        />
      </Space>

      <Spin spinning={isLoading}>
        <Card
          style={{
            backgroundColor: "var(--ant-color-bg-container)",
          }}
        >
          <pre
            style={{
              fontFamily: "monospace",
              margin: 0,
              maxHeight: "600px",
              overflow: "auto",
              whiteSpace: "pre-wrap",
              wordWrap: "break-word",
            }}
          >
            {logResponse.log || "(no log content)"}
          </pre>
        </Card>
      </Spin>
    </Drawer>
  );
};
