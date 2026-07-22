// ExecDrawer — admin-only one-shot exec into the app's container.
// Not a TTY/shell; pipe one command at a time. Bidi shell over
// websocket is queued as a follow-up.
import { useTranslation } from "react-i18next";
import { Alert, App, Button, Drawer, Input, Space, Typography } from "antd";
import { useState } from "react";

import { execCmd, type ExecResponse } from "./api";

interface Props {
  open: boolean;
  appId: string | null;
  onClose: () => void;
}

export const ExecDrawer = ({ open, appId, onClose }: Props) => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [command, setCommand] = useState("");
  const [busy, setBusy] = useState(false);
  const [last, setLast] = useState<ExecResponse | null>(null);

  const handleRun = async () => {
    if (!appId || !command.trim()) return;
    setBusy(true);
    try {
      const r = await execCmd(appId, command);
      setLast(r);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Exec failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={t("execdrawer.exec_command")}
      width="min(100%, 720px)"
      destroyOnClose
    >
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Alert
          type="warning"
          showIcon
          message={t("execdrawer.admin_only")}
          description={t("execdrawer.runs_sh_c_command_in_the_first_service_of_th")}
        />
        <Input.TextArea
          rows={3}
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          placeholder="ls -la /data"
          autoFocus
        />
        <Button type="primary" loading={busy} onClick={handleRun} disabled={!command.trim()}>
          Run
        </Button>

        {last && (
          <Space direction="vertical" style={{ width: "100%" }} size="small">
            <Typography.Text type="secondary">
              Exit code: <b>{last.exit_code}</b>
            </Typography.Text>
            {last.stdout && (
              <>
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  stdout
                </Typography.Text>
                <pre
                  style={{
                    background: "var(--ant-color-bg-elevated, #0d1117)",
                    color: "var(--ant-color-text-base, #c9d1d9)",
                    padding: 12,
                    borderRadius: 6,
                    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
                    fontSize: 12,
                    margin: 0,
                    overflow: "auto",
                    maxHeight: 240,
                  }}
                >
                  {last.stdout}
                </pre>
              </>
            )}
            {last.stderr && (
              <>
                <Typography.Text type="secondary" style={{ fontSize: 12, color: "#ff7875" }}>
                  stderr
                </Typography.Text>
                <pre
                  style={{
                    background: "#2a1212",
                    color: "#ffb3b3",
                    padding: 12,
                    borderRadius: 6,
                    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
                    fontSize: 12,
                    margin: 0,
                    overflow: "auto",
                    maxHeight: 200,
                  }}
                >
                  {last.stderr}
                </pre>
              </>
            )}
          </Space>
        )}
      </Space>
    </Drawer>
  );
};
