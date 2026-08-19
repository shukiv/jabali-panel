// AdminFileManagerCard — opt-in toggle for the GH #1184 admin File Manager, a
// whole-filesystem browser/editor on the admin side. OFF by default and kept
// deliberately loud: it exposes root-owned paths + edits through the panel UI
// (a hard deny-list protects credentials, SSH, and jabali's own control plane),
// so enabling it is a conscious decision.
import { App, Alert, Card, Space, Switch, Tag, Typography } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface ServerSettingsAdminFM {
  admin_file_manager_enabled: boolean;
}

export function AdminFileManagerCard() {
  const { message } = App.useApp();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    try {
      const r = await apiClient.get<ServerSettingsAdminFM>("/admin/settings");
      setEnabled(!!r.data.admin_file_manager_enabled);
    } catch (err) {
      message.error(`Could not load the Admin File Manager setting: ${err instanceof Error ? err.message : String(err)}`);
    }
  };

  useEffect(() => { void refresh(); }, []);

  const toggle = async (value: boolean) => {
    setBusy(true);
    try {
      await apiClient.patch("/admin/settings", { admin_file_manager_enabled: value });
      setEnabled(value);
      message.success(value ? "Admin File Manager enabled" : "Admin File Manager disabled");
    } catch (err) {
      message.error(`Could not update the setting: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title={
        <Space>
          <Typography.Text strong>Admin File Manager</Typography.Text>
          <Tag color="red">whole filesystem</Tag>
        </Space>
      }
    >
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Adds a File Manager on the admin side that can browse and edit files anywhere on the
          server (starting from <code>/</code>) — useful for editing service configs, clearing
          logs, or cleaning caches without opening an SFTP client.
        </Typography.Paragraph>
        <Alert
          type="warning"
          showIcon
          message="Powerful — leave off unless you need it"
          description={
            <span>
              This gives the admin UI root-level read/write across the filesystem. A hard deny-list
              blocks credential stores (<code>/etc/shadow</code>, <code>/etc/ssh</code>), Let's Encrypt
              keys, and jabali's own config/binaries/sockets, but everything else is reachable. Anyone
              with an admin session — or a bug in the file paths — inherits that reach. Enable it only
              when you actively need it, then turn it back off.
            </span>
          }
        />
        <Space>
          <Switch checked={!!enabled} loading={busy || enabled === null} onChange={(v) => void toggle(v)} />
          <Typography.Text>{enabled ? "Enabled" : "Disabled"}</Typography.Text>
        </Space>
      </Space>
    </Card>
  );
}

export default AdminFileManagerCard;
