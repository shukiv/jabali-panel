// NodeAppsCard — opt-in toggle for Node.js apps on the Application Manager
// (ADR-0175). Sits next to PythonAppsCard. Flipping it persists
// node_apps_enabled; nothing is installed, since Node.js 22 is already on the
// server.
import { App, Card, Space, Switch, Tag, Typography } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface ServerSettingsNodeApps {
  node_apps_enabled: boolean;
}

export function NodeAppsCard() {
  const { message } = App.useApp();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    apiClient
      .get<ServerSettingsNodeApps>("/admin/settings")
      .then((r) => setEnabled(!!r.data.node_apps_enabled))
      .catch((err) =>
        message.error(
          `Could not load Node.js Apps setting: ${err instanceof Error ? err.message : String(err)}`,
        ),
      );
  }, [message]);

  const toggle = async (value: boolean) => {
    setBusy(true);
    try {
      await apiClient.patch("/admin/settings", { node_apps_enabled: value });
      setEnabled(value);
      message.success(value ? "Node.js apps enabled" : "Node.js apps disabled");
    } catch (err) {
      message.error(`Failed to update: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title={
        <Space>
          <span>Node.js Apps</span>
          {enabled ? <Tag color="green">Enabled</Tag> : <Tag>Disabled</Tag>}
        </Space>
      }
    >
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Let users run Node.js web apps on their domains from the same page as
          Python apps. Each app runs as an isolated per-user service behind
          nginx, on the server&apos;s Node.js 22. Turning this on installs
          nothing. Node.js apps count against the package&apos;s Python app
          limit.
        </Typography.Paragraph>
        <Space>
          <Switch
            checked={!!enabled}
            loading={busy || enabled === null}
            onChange={(v) => void toggle(v)}
          />
          <Typography.Text>Enable Node.js apps</Typography.Text>
        </Space>
      </Space>
    </Card>
  );
}
