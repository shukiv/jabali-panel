// Databases tab — Server Settings card for opt-in PostgreSQL
// (M37 Phase 4). Mirrors the EmailCard / PanelSSLCard pattern: own
// data fetch, own apiClient calls, no coupling to the parent page's
// form. The parent's ServerSettings type also carries postgres_enabled
// so a global PATCH still round-trips that field — but the user-facing
// UX here is "click Install → wait for the server to install
// postgresql.service" with a status badge that polls
// `/admin/databases/postgres/status`.
//
// Install   → backend dispatches db.postgres.install (apt + initdb +
//              systemctl enable+start). Up to ~60 s on a fresh host.
// Toggle ON  → starts the existing service (if previously stopped).
// Toggle OFF → stops + disables (data preserved).
// Uninstall  → separate destructive button. apt purge + rm -rf data.
//              Two-step confirm dialog.

import {
  DatabaseOutlined,
  DownloadOutlined,
  ExclamationCircleOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { Alert, Button, Card, Skeleton, Space, Switch, Tag, Typography, message } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface PostgresStatus {
  installed: boolean;
  active: boolean;
  version?: string;
}

interface ServerSettingsPostgres {
  postgres_enabled: boolean;
}

export function DatabasesCard() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [status, setStatus] = useState<PostgresStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [busyLabel, setBusyLabel] = useState<string | null>(null);

  const refresh = async () => {
    setStatusLoading(true);
    try {
      const settings = await apiClient.get<ServerSettingsPostgres>(
        "/admin/settings",
      );
      setEnabled(settings.data.postgres_enabled);
      const st = await apiClient.get<PostgresStatus>(
        "/admin/databases/postgres/status",
      );
      setStatus(st.data);
    } catch (err) {
      message.error(
        `Could not load PostgreSQL status: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    } finally {
      setStatusLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, []);

  const persistFlag = async (value: boolean) => {
    await apiClient.patch("/admin/settings", { postgres_enabled: value });
    setEnabled(value);
  };

  const handleInstall = async () => {
    setBusy(true);
    setBusyLabel("Installing PostgreSQL — this may take up to a minute…");
    try {
      await persistFlag(true);
      // Background dispatch kicks off install_postgres on the agent.
      // Poll status every 6 s for up to 90 s before giving up.
      for (let i = 0; i < 15; i++) {
        await new Promise((r) => setTimeout(r, 6000));
        try {
          const st = await apiClient.get<PostgresStatus>(
            "/admin/databases/postgres/status",
          );
          setStatus(st.data);
          if (st.data.installed && st.data.active) break;
        } catch {
          // keep polling — install may still be running
        }
      }
      message.success("PostgreSQL installed.");
    } catch (err) {
      message.error(
        `Install failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      setBusy(false);
      setBusyLabel(null);
    }
  };

  const handleToggle = async (next: boolean) => {
    if (next && !status?.installed) {
      // First-time enable: route through the install flow.
      await handleInstall();
      return;
    }
    setBusy(true);
    setBusyLabel(next ? "Starting PostgreSQL…" : "Stopping PostgreSQL…");
    try {
      await persistFlag(next);
      await new Promise((r) => setTimeout(r, 2500));
      await refresh();
      message.success(
        next
          ? "PostgreSQL enabled."
          : "PostgreSQL disabled (data preserved).",
      );
    } catch (err) {
      message.error(
        `Toggle failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      setBusy(false);
      setBusyLabel(null);
    }
  };

  const handleUninstall = () => {
    feedback.modal.confirm({
      title: "Uninstall PostgreSQL?",
      icon: <ExclamationCircleOutlined />,
      content:
        "This permanently removes the PostgreSQL packages AND deletes /var/lib/postgresql. All Postgres databases on this server will be lost. This cannot be undone.",
      okText: "Uninstall and delete data",
      okType: "danger",
      cancelText: "Cancel",
      onOk: async () => {
        setBusy(true);
        setBusyLabel("Uninstalling PostgreSQL…");
        try {
          await apiClient.post("/admin/databases/postgres/uninstall", {});
          await persistFlag(false);
          message.success("PostgreSQL uninstalled and data removed.");
          await refresh();
        } catch (err) {
          message.error(
            `Uninstall failed: ${
              err instanceof Error ? err.message : String(err)
            }`,
          );
        } finally {
          setBusy(false);
          setBusyLabel(null);
        }
      },
    });
  };

  const renderStatusBadge = () => {
    if (statusLoading || status == null) return <Tag>checking…</Tag>;
    if (!status.installed) return <Tag>Not installed</Tag>;
    if (status.active) return <Tag color="green">Running</Tag>;
    return <Tag color="orange">Installed (stopped)</Tag>;
  };

  return (
    <Card
      title={
        <Space>
          <DatabaseOutlined />
          Databases
        </Space>
      }
      extra={
        <Button
          type="text"
          icon={<ReloadOutlined />}
          onClick={() => void refresh()}
          loading={statusLoading}
        >
          Refresh
        </Button>
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        MariaDB is always installed. PostgreSQL is opt-in — install it
        below to let users create PostgreSQL databases. Disabling stops
        the service without deleting data; uninstalling drops the data
        permanently.
      </Typography.Paragraph>

      {busyLabel && (
        <Alert
          type="info"
          showIcon
          message={busyLabel}
          style={{ marginBottom: 12 }}
        />
      )}

      {enabled == null ? (
        <Skeleton active paragraph={{ rows: 2 }} />
      ) : (
        <>
          <Space size="large" style={{ marginBottom: 12 }} wrap>
            {!status?.installed ? (
              <Button
                type="primary"
                icon={<DownloadOutlined />}
                loading={busy}
                onClick={handleInstall}
              >
                Install PostgreSQL
              </Button>
            ) : (
              <Space>
                <Switch
                  checked={enabled}
                  loading={busy}
                  onChange={handleToggle}
                />
                <Typography.Text strong>PostgreSQL</Typography.Text>
              </Space>
            )}
            {renderStatusBadge()}
            {status?.version && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {status.version}
              </Typography.Text>
            )}
          </Space>

          {status?.installed && (
            <div>
              <Typography.Paragraph
                type="secondary"
                style={{ marginBottom: 8 }}
              >
                Removing PostgreSQL also drops every database created
                from the user databases page. Make sure backups are
                taken.
              </Typography.Paragraph>
              <Button danger onClick={handleUninstall} loading={busy}>
                Uninstall PostgreSQL
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  );
}
