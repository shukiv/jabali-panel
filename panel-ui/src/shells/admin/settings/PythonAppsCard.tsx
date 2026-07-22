// PythonAppsCard — opt-in toggle for the Python Application Manager
// (ADR-0131 / GH #203). Mirrors DockerMarketplaceCard. Flipping it on
// persists python_apps_enabled; the host installs the Python runtime
// prerequisites (python3-venv + build toolchain) via install.sh on the
// next `jabali update` (or already, on a fresh install with the flag set).
import { useTranslation } from "react-i18next";
import { App, Alert, Card, Space, Spin, Switch, Tag, Typography } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface ServerSettingsPyApps {
  python_apps_enabled: boolean;
}

export function PythonAppsCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [busyLabel, setBusyLabel] = useState<string | null>(null);

  const refresh = async () => {
    try {
      const r = await apiClient.get<ServerSettingsPyApps>("/admin/settings");
      setEnabled(!!r.data.python_apps_enabled);
    } catch (err) {
      message.error(
        `Could not load Python Apps setting: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    }
  };

  useEffect(() => {
    void refresh();
  }, []);

  const toggle = async (value: boolean) => {
    setBusy(true);
    setBusyLabel(value ? "Installing Python runtime — this may take a minute…" : null);
    try {
      await apiClient.patch("/admin/settings", { python_apps_enabled: value });
      setEnabled(value);
      if (value) {
        // Backend dispatches app.python.install_runtime in the background.
        // Poll the runtime status until the prerequisites are present, then
        // drop the spinner. Cap at 6 minutes (apt + build toolchain).
        const deadline = Date.now() + 6 * 60 * 1000;
        let installed = false;
        while (Date.now() < deadline) {
          await new Promise((r) => setTimeout(r, 4000));
          try {
            const r = await apiClient.get<{ installed: boolean }>(
              "/admin/settings/python-runtime",
            );
            if (r.data.installed) {
              installed = true;
              break;
            }
          } catch {
            // keep polling
          }
        }
        message.success(
          installed
            ? "Python Application Manager enabled — runtime installed"
            : "Enabled — runtime still installing in the background; run jabali update if a venv build fails",
        );
      } else {
        message.success("Python Application Manager disabled");
      }
    } catch (err) {
      message.error(
        `Failed to update: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      setBusy(false);
      setBusyLabel(null);
    }
  };

  return (
    <Card
      title={
        <Space>
          <span>Python Application Manager</span>
          {enabled ? <Tag color="green">Enabled</Tag> : <Tag>Disabled</Tag>}
        </Space>
      }
    >
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Let users run native Python web apps (Django/Flask/FastAPI) on their
          domains — each app runs as an isolated per-user service behind nginx,
          like cPanel&apos;s Application Manager. PHP hosting is unaffected.
        </Typography.Paragraph>
        <Space>
          <Switch
            checked={!!enabled}
            loading={busy || enabled === null}
            onChange={(v) => void toggle(v)}
          />
          <Typography.Text>Enable Python apps</Typography.Text>
        </Space>
        {busy && busyLabel && (
          <Space>
            <Spin size="small" />
            <Typography.Text type="secondary">{busyLabel}</Typography.Text>
          </Space>
        )}
        {enabled && (
          <Alert
            type="info"
            showIcon
            message={t("pythonappscard.runtime_prerequisites")}
            description={t("pythonappscard.apps_need_python3_venv_and_a_build_toolchain")}
          />
        )}
      </Space>
    </Card>
  );
}
