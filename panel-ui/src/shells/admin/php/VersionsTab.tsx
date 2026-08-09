import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Alert, Spin, Table, Tag, Tooltip } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  CheckCircleOutlined,
  DeleteOutlined,
  DownloadOutlined,
  ReloadOutlined,
} from "@icons";
import { RowActionButton } from "../../../components/RowActionButton";
import { RowActions } from "../../../components/RowActions";
import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";
import { isPHPEOL } from "../../../utils/phpEol";

interface PHPVersionStatus {
  version: string;
  installed: boolean;
  fpm_running: boolean;
  workers_running?: number;
  workers_total?: number;
}

interface PHPVersionStatusResponse {
  default_version: string;
  versions: PHPVersionStatus[];
}

export const VersionsTab = () => {
  const { t } = useTranslation();
  const [statusData, setStatusData] = useState<PHPVersionStatusResponse | null>(
    null
  );
  const [loading, setLoading] = useState(true);
  const [installingVersion, setInstallingVersion] = useState<string | null>(
    null
  );
  const [reloadingVersion, setReloadingVersion] = useState<string | null>(null);
  const [uninstallingVersion, setUninstallingVersion] = useState<string | null>(null);
  const [settingDefaultVersion, setSettingDefaultVersion] = useState<string | null>(null);
  const [dismissedWarning, setDismissedWarning] = useState(false);

  useEffect(() => {
    fetchStatus();
  }, []);

  const fetchStatus = async () => {
    setLoading(true);
    try {
      const response = await apiClient.get<PHPVersionStatusResponse>(
        "/admin/php/versions/status"
      );
      setStatusData(response.data);
    } catch (error) {
      feedback.notification.error({
        message: "Failed to fetch PHP versions",
        description: extractApiError(error, "Unknown error occurred"),
        duration: 5,
      });
    } finally {
      setLoading(false);
    }
  };

  const handleInstall = async (version: string) => {
    setInstallingVersion(version);
    try {
      // The backend detaches the apt install and returns 202 immediately
      // (GH #293) — a slow install no longer holds the request open past
      // nginx's 300s timeout (which surfaced as a 502). Poll the status
      // endpoint until the version reports installed.
      await apiClient.post(`/admin/php/versions/${version}/install`);

      const deadline = Date.now() + 20 * 60 * 1000; // generous: slow hosts
      while (true) {
        await new Promise((r) => setTimeout(r, 5000));
        if (Date.now() > deadline) {
          feedback.notification.warning({
            message: `PHP ${version} is still installing`,
            description:
              "The install is taking longer than expected; it will continue in the background. Refresh to check.",
            duration: 6,
          });
          return;
        }
        let st;
        try {
          st = await apiClient.get<PHPVersionStatusResponse>("/admin/php/versions/status");
        } catch {
          continue; // transient — keep polling
        }
        const v = st.data.versions.find((x) => x.version === version);
        if (v?.installed) {
          setStatusData(st.data);
          feedback.notification.success({
            message: `PHP ${version} installed successfully`,
            duration: 3,
          });
          return;
        }
      }
    } catch (error) {
      feedback.notification.error({
        message: `Failed to install PHP ${version}`,
        description: extractApiError(error, "Installation failed"),
        duration: 5,
      });
    } finally {
      setInstallingVersion(null);
    }
  };

  const handleUninstall = async (version: string) => {
    setUninstallingVersion(version);
    try {
      // apt-get purge --auto-remove on php<v>-* can take 30-120s on a
      // host with many extensions. Match install's 5-min ceiling.
      await apiClient.delete(`/admin/php/versions/${version}`, {
        timeout: 5 * 60 * 1000,
      });
      feedback.notification.success({
        message: `PHP ${version} uninstalled`,
        duration: 3,
      });
      await fetchStatus();
    } catch (error) {
      feedback.notification.error({
        message: `Failed to uninstall PHP ${version}`,
        description: extractApiError(error, "Uninstall failed"),
        duration: 5,
      });
    } finally {
      setUninstallingVersion(null);
    }
  };

  const handleReload = async (version: string) => {
    setReloadingVersion(version);
    try {
      await apiClient.post(`/admin/php/versions/${version}/reload`);
      feedback.notification.success({
        message: `PHP ${version} reloaded successfully`,
        duration: 3,
      });
    } catch (error) {
      const errorMsg =
        error instanceof Error ? error.message : "Reload failed";
      feedback.notification.error({
        message: `Failed to reload PHP ${version}`,
        description: errorMsg,
        duration: 5,
      });
    } finally {
      setReloadingVersion(null);
    }
  };

  const handleSetDefault = async (version: string) => {
    setSettingDefaultVersion(version);
    try {
      await apiClient.post(`/admin/php/versions/${version}/default`);
      feedback.notification.success({
        message: `PHP ${version} is now the default`,
        duration: 3,
      });
      setStatusData((prev) =>
        prev ? { ...prev, default_version: version } : prev,
      );
    } catch (error) {
      const errorMsg =
        error instanceof Error ? error.message : "Request failed";
      feedback.notification.error({
        message: `Could not set PHP ${version} as default`,
        description: errorMsg,
        duration: 5,
      });
    } finally {
      setSettingDefaultVersion(null);
    }
  };

  if (loading && !statusData) {
    return (
      <div style={{ textAlign: "center" }}>
        <Spin size="large" />
      </div>
    );
  }

  // Show newest-first (8.5 at top, 7.4 at bottom). Semver-aware sort:
  // split on "." and compare numerically so 8.10 > 8.9 if that ever matters.
  const tableData = [...(statusData?.versions || [])].sort((a, b) => {
    const pa = a.version.split(".").map(Number);
    const pb = b.version.split(".").map(Number);
    for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
      const diff = (pb[i] ?? 0) - (pa[i] ?? 0);
      if (diff !== 0) return diff;
    }
    return 0;
  });

  return (
    <>
      {!dismissedWarning && (
        <Alert
          type="warning"
          showIcon
          title={t("versionstab.modifying_php_versions_can_cause_server_down")}
          description={t("versionstab.uninstalling_php_versions_may_break_websites")}
          closable
          onClose={() => setDismissedWarning(true)}
          style={{ marginBottom: 16 }}
        />
      )}

      <Table<PHPVersionStatus>
        dataSource={tableData}
        rowKey="version"
        loading={loading}
        pagination={false}
        scroll={{ x: "max-content" }}
      >
        <Table.Column<PHPVersionStatus>
          dataIndex="version"
          title={t("versionstab.php_version")}
          render={(version: string) => (
            <span>
              PHP {version}
              {isPHPEOL(version) && (
                <Tooltip title={t("versionstab.end_of_life_no_upstream_security_patches_run")}>
                  <Tag color="red" style={{ marginLeft: 8 }}>
                    EOL
                  </Tag>
                </Tooltip>
              )}
            </span>
          )}
        />
        <Table.Column<PHPVersionStatus>
          dataIndex="installed"
          title={t("versionstab.status")}
          width={150}
          render={(installed: boolean) =>
            installed ? (
              <Tag color="green">Installed</Tag>
            ) : (
              <Tag>Not Installed</Tag>
            )
          }
        />
        <Table.Column<PHPVersionStatus>
          title={t("versionstab.default")}
          width={140}
          render={(_: any, record: PHPVersionStatus) => {
            if (record.version === statusData?.default_version && record.installed) {
              return <CheckCircleOutlined />;
            }
            // Button only shows for installed + FPM-running versions — the
            // API rejects setting a default that isn't both. Avoid the
            // failed-request UX entirely by hiding the button.
            if (record.installed && record.fpm_running) {
              return (
                <RowActionButton
                  icon={<CheckCircleOutlined />}
                  loading={settingDefaultVersion === record.version}
                  onClick={() => handleSetDefault(record.version)}
                >
                  Set default
                </RowActionButton>
              );
            }
            return "—";
          }}
        />
        <Table.Column<PHPVersionStatus>
          dataIndex="fpm_running"
          title={t("versionstab.fpm_workers")}
          width={170}
          render={(_fpmRunning: boolean, record: PHPVersionStatus) => {
            if (!record.installed) return "—";
            const total = record.workers_total ?? 0;
            const running = record.workers_running ?? 0;
            // No pools pinned to this version — installed but idle.
            if (total === 0) return <Tag>No pools</Tag>;
            const allUp = running === total;
            return (
              <Tag color={running > 0 ? (allUp ? "green" : "orange") : "red"}>
                {running}/{total} running
              </Tag>
            );
          }}
        />
        <Table.Column<PHPVersionStatus>
          title={t("versionstab.actions")}
          width={220}
          render={(_: any, record: PHPVersionStatus) => {
            const isInstalling = installingVersion === record.version;
            const isReloading = reloadingVersion === record.version;
            const isUninstalling = uninstallingVersion === record.version;
            const isDefault = statusData?.default_version === record.version;

            if (record.installed) {
              return (
                <RowActions
                  actions={[
                    { key: "reload", label: "Reload", icon: <ReloadOutlined />, onClick: () => handleReload(record.version), loading: isReloading, disabled: isReloading || isUninstalling },
                    {
                      key: "uninstall",
                      label: "Uninstall",
                      icon: <DeleteOutlined />,
                      danger: true,
                      hidden: isDefault,
                      loading: isUninstalling,
                      disabled: isUninstalling || isReloading,
                      onClick: () => handleUninstall(record.version),
                      confirm: { title: `Uninstall PHP ${record.version}?`, description: "apt-get purge --auto-remove. Sites pinned to this version will break until you change their pool.", okText: "Uninstall" },
                    },
                  ]}
                />
              );
            } else {
              return (
                <RowActionButton
                  icon={<DownloadOutlined />}
                  onClick={() => handleInstall(record.version)}
                  loading={isInstalling}
                  disabled={isInstalling}
                >
                  Install
                </RowActionButton>
              );
            }
          }}
        />
      </Table>
    </>
  );
};
