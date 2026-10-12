import { useTranslation } from "react-i18next";
import { Modal, Alert, Card, Space } from "antd";

interface RunNowResultModalProps {
  open: boolean;
  onClose: () => void;
  result: {
    exit_code: number;
    stdout: string;
    stderr: string;
    output?: string;
    running?: boolean;
  } | null;
}

const outputStyle = {
  fontFamily: "monospace",
  margin: 0,
  maxHeight: "300px",
  overflow: "auto",
  backgroundColor: "var(--ant-color-bg-container)",
  padding: "12px",
  borderRadius: "4px",
  whiteSpace: "pre-wrap",
  wordWrap: "break-word",
} as const;

export const RunNowResultModal = ({
  open,
  onClose,
  result,
}: RunNowResultModalProps) => {
  const { t } = useTranslation();
  if (!result) {
    return null;
  }

  const isSuccess = result.exit_code === 0;

  return (
    <Modal
      title={t("runnowresultmodal.cron_job_execution_result")}
      open={open}
      onCancel={onClose}
      footer={null}
      width={700}
    >
      <Space direction="vertical" style={{ width: "100%" }}>
        {result.running ? (
          // GH #2122: the run outlived Run now's wait and keeps running in the
          // background; there is no exit code yet.
          <Alert
            title={t("runnowresultmodal.still_running")}
            description={t("runnowresultmodal.still_running_detail")}
            type="info"
            showIcon
          />
        ) : (
          <Alert
            title={isSuccess ? "Execution Successful" : "Execution Failed"}
            description={`Exit code: ${result.exit_code}`}
            type={isSuccess ? "success" : "error"}
            showIcon
          />
        )}

        {result.output && (
          <Card title={t("runnowresultmodal.output")}>
            <pre style={outputStyle}>{result.output}</pre>
          </Card>
        )}

        {result.stdout && (
          <Card title={t("runnowresultmodal.standard_output")}>
            <pre style={outputStyle}>{result.stdout}</pre>
          </Card>
        )}

        {result.stderr && (
          <Card
            title={t("runnowresultmodal.standard_error")}
            style={{ borderColor: "var(--ant-color-error)" }}
          >
            <pre
              style={{
                fontFamily: "monospace",
                margin: 0,
                maxHeight: "300px",
                overflow: "auto",
                backgroundColor: "var(--ant-color-error-bg)",
                padding: "12px",
                borderRadius: "4px",
                whiteSpace: "pre-wrap",
                wordWrap: "break-word",
                color: "var(--ant-color-error)",
              }}
            >
              {result.stderr}
            </pre>
          </Card>
        )}
      </Space>
    </Modal>
  );
};
