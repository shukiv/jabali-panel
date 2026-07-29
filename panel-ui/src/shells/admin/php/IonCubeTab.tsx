import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Popconfirm,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
  notification,
} from "antd";
import { DownloadOutlined, DeleteOutlined } from "@icons";
import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

// Mirrors the GET /admin/php/ioncube response (php_ioncube_admin.go).
interface IonCubeStatus {
  installed_versions: string[];
  enabled: Record<string, string[]>;
  supported_versions: string[];
  loader_version: string;
}

interface VersionRow {
  version: string;
  installed: boolean;
  enabledPools: number;
}

export const IonCubeTab = () => {
  const [status, setStatus] = useState<IonCubeStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      const { data } = await apiClient.get<IonCubeStatus>("/admin/php/ioncube");
      setStatus(data);
    } catch (err) {
      notification.error({
        message: "Failed to load ionCube status",
        description: extractApiError(err, "Unknown error"),
      });
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const install = async (version: string) => {
    setBusy(version);
    try {
      await apiClient.post(`/admin/php/ioncube/versions/${version}/install`);
      notification.success({ message: `ionCube installed for PHP ${version}` });
      await load();
    } catch (err) {
      notification.error({
        message: `Install failed for PHP ${version}`,
        description: extractApiError(err, "Unknown error"),
      });
    } finally {
      setBusy(null);
    }
  };

  const uninstall = async (version: string) => {
    setBusy(version);
    try {
      await apiClient.delete(`/admin/php/ioncube/versions/${version}`);
      notification.success({ message: `ionCube uninstalled for PHP ${version}` });
      await load();
    } catch (err) {
      notification.error({
        message: `Uninstall failed for PHP ${version}`,
        description: extractApiError(err, "Unknown error"),
      });
    } finally {
      setBusy(null);
    }
  };

  if (loading) {
    return (
      <div style={{ textAlign: "center", padding: 40 }}>
        <Spin />
      </div>
    );
  }

  const installed = new Set(status?.installed_versions ?? []);
  const enabledByVersion = (v: string) =>
    Object.values(status?.enabled ?? {}).filter((vers) => vers.includes(v)).length;
  const rows: VersionRow[] = (status?.supported_versions ?? []).map((version) => ({
    version,
    installed: installed.has(version),
    enabledPools: enabledByVersion(version),
  }));

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Alert
        type="info"
        showIcon
        message="ionCube Loader"
        description={
          <Typography.Text type="secondary">
            Install the ionCube Loader per PHP version so encoded applications can
            run. Installing here only places the loader on the server — each site
            enables it individually under its PHP settings (per-pool, not
            server-wide). Loader {status?.loader_version ? `v${status.loader_version}` : ""}.
          </Typography.Text>
        }
      />
      <Table<VersionRow>
        rowKey="version"
        dataSource={rows}
        pagination={false}
        columns={[
          {
            title: "PHP Version",
            dataIndex: "version",
            render: (v: string) => <Typography.Text strong>{v}</Typography.Text>,
          },
          {
            title: "Loader",
            dataIndex: "installed",
            render: (inst: boolean) =>
              inst ? <Tag color="green">Installed</Tag> : <Tag>Not installed</Tag>,
          },
          {
            title: "Sites using it",
            dataIndex: "enabledPools",
            render: (n: number) => (n > 0 ? <Tag color="blue">{n}</Tag> : <span>—</span>),
          },
          {
            title: "Action",
            key: "action",
            render: (_: unknown, row: VersionRow) =>
              row.installed ? (
                <Popconfirm
                  title={`Uninstall ionCube for PHP ${row.version}?`}
                  description={
                    row.enabledPools > 0
                      ? `${row.enabledPools} site(s) still use it — disable them first.`
                      : "The loader file will be removed."
                  }
                  okText="Uninstall"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => uninstall(row.version)}
                >
                  <Button
                    danger
                    size="small"
                    icon={<DeleteOutlined />}
                    loading={busy === row.version}
                  >
                    Uninstall
                  </Button>
                </Popconfirm>
              ) : (
                <Button
                  type="primary"
                  size="small"
                  icon={<DownloadOutlined />}
                  loading={busy === row.version}
                  onClick={() => install(row.version)}
                >
                  Install
                </Button>
              ),
          },
        ]}
      />
    </Space>
  );
};
