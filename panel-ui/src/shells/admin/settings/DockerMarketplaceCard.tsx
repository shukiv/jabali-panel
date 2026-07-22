// DockerMarketplaceCard — toggle the M48 docker app marketplace
// opt-in. Mirrors DatabasesCard (postgres opt-in) shape.
//
// Flip true: panel-api dispatches `docker.install` on the agent
//   (sources install.sh, runs install_docker_engine, starts docker).
// Flip false: panel-api dispatches `docker.disable` (stops + disables
//   docker units). Data under /var/lib/jabali/docker-apps/ stays.
import { useTranslation } from "react-i18next";
import { App, Alert, Button, Card, Checkbox, Modal, Space, Spin, Switch, Tag, Typography } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface ServerSettingsDocker {
  docker_marketplace_enabled: boolean;
  docker_apps_for_users_enabled: boolean;
  docker_tenant_apps: string;
}

interface CatalogEntry {
  slug: string;
  name: string;
  tenant_installable: boolean;
}

export function DockerMarketplaceCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [busyLabel, setBusyLabel] = useState<string | null>(null);
  const [forUsers, setForUsers] = useState(false);
  const [forUsersBusy, setForUsersBusy] = useState(false);
  const [eligible, setEligible] = useState<CatalogEntry[]>([]);
  const [exposed, setExposed] = useState<string[]>([]);
  const [appsBusy, setAppsBusy] = useState(false);

  const refresh = async () => {
    try {
      const r = await apiClient.get<ServerSettingsDocker>("/admin/settings");
      setEnabled(!!r.data.docker_marketplace_enabled);
      setForUsers(!!r.data.docker_apps_for_users_enabled);
      const csv = (r.data.docker_tenant_apps || "").trim();
      if (r.data.docker_marketplace_enabled && r.data.docker_apps_for_users_enabled) {
        try {
          const cat = await apiClient.get<{ items: CatalogEntry[] }>("/admin/docker-apps/catalog");
          const elig = (cat.data.items || []).filter((e) => e.tenant_installable);
          setEligible(elig);
          // empty CSV = all eligible exposed (the curated default)
          setExposed(csv ? csv.split(",").map((x) => x.trim()).filter(Boolean) : elig.map((e) => e.slug));
        } catch {
          /* catalog unavailable */
        }
      }
    } catch (err) {
      message.error(
        `Could not load marketplace setting: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    }
  };

  useEffect(() => {
    void refresh();
  }, []);

  const persistFlag = async (value: boolean) => {
    await apiClient.patch("/admin/settings", {
      docker_marketplace_enabled: value,
    });
    setEnabled(value);
  };

  const handleEnable = async () => {
    setBusy(true);
    setBusyLabel("Installing Docker — this may take a few minutes…");
    try {
      await persistFlag(true);
      // Backend dispatches `docker.install` as fire-and-forget so the
      // PATCH does not block. Poll docker.status every 4s until the
      // engine is installed AND the daemon is active, then drop the
      // spinner. Cap at 5 minutes; the docker.install agent verb has
      // its own 10-min ceiling.
      const deadline = Date.now() + 5 * 60 * 1000;
      while (Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 4000));
        try {
          const { data } = await apiClient.get<{ installed?: boolean; active?: boolean }>(
            "/admin/docker-apps/engine/status",
          );
          if (data?.installed && data?.active) {
            message.success("Docker engine installed + active. Marketplace is live.");
            return;
          }
        } catch {
          // transient -- agent may be unreachable mid-install. Keep polling.
        }
        setBusyLabel("Installing Docker — still in progress…");
      }
      message.warning("Install poll timed out; check Server Status -> Docker for live state.");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Failed to enable");
    } finally {
      setBusy(false);
      setBusyLabel(null);
    }
  };

  const handleDisable = async () => {
    setBusy(true);
    setBusyLabel("Disabling Docker…");
    try {
      await persistFlag(false);
      message.warning("Marketplace disabled. App data preserved under /var/lib/jabali/docker-apps/.");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Failed to disable");
    } finally {
      setBusy(false);
      setBusyLabel(null);
    }
  };

  const setForUsersFlag = async (value: boolean) => {
    setForUsersBusy(true);
    setBusyLabel(
      value ? "Enabling tenant Docker (userns-remap + daemon restart)…" : "Disabling tenant Docker…",
    );
    try {
      // The PATCH returns immediately; the host setup runs detached on the
      // agent. Poll /me/server-capabilities until docker_apps_user_enabled
      // reflects the target (the host flag is the source of truth), so we only
      // report success once tenant docker is actually live.
      await apiClient.patch("/admin/settings", { docker_apps_for_users_enabled: value });
      setForUsers(value);
      const deadline = Date.now() + (value ? 8 * 60 * 1000 : 60 * 1000);
      while (Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 4000));
        try {
          const { data } = await apiClient.get<{ docker_apps_user_enabled?: boolean }>(
            "/me/server-capabilities",
          );
          if (!!data?.docker_apps_user_enabled === value) {
            message.success(
              value ? "Docker Apps enabled for users" : "Docker Apps disabled for users",
            );
            return;
          }
        } catch {
          // transient — keep polling.
        }
        setBusyLabel(value ? "Enabling tenant Docker — still in progress…" : "Disabling…");
      }
      message.warning("Still applying; check Server Status → Docker for live state.");
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      message.error(e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to update");
    } finally {
      setForUsersBusy(false);
      setBusyLabel(null);
    }
  };

  const onToggleForUsers = (v: boolean) => {
    if (v) {
      Modal.confirm({
        title: "Enable Docker Apps for users?",
        okText: "Enable",
        okButtonProps: { danger: true },
        content:
          "This makes the host tenant-ready (daemon-wide userns-remap) and restarts " +
          "the Docker daemon — running containers briefly go down and previously pulled " +
          "images must be re-pulled. The tenant Docker Apps tab then appears for users. " +
          "This can take a few minutes.",
        onOk: () => setForUsersFlag(true),
      });
    } else {
      void setForUsersFlag(false);
    }
  };

  if (enabled === null) {
    return (
      <Card title={t("dockermarketplacecard.docker_app_marketplace_m48")}>
        <Spin />
      </Card>
    );
  }

  return (
    <Card
      title={
        <Space>
          Docker App Marketplace (M48)
          {enabled ? <Tag color="green">Enabled</Tag> : <Tag>Disabled</Tag>}
        </Space>
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Curated catalog of one-click Docker apps (Vaultwarden, Uptime Kuma, Gitea, …) with
          auto domain + nginx reverse proxy + LE TLS + restic backup. Admin-installable only.
          Disabled by default; enabling installs Docker CE (~400 MB) and adds a docker daemon
          to the host.
        </Typography.Paragraph>

        {!enabled && (
          <Alert
            type="info"
            showIcon
            message={t("dockermarketplacecard.opt_in_feature")}
            description={
              <>
                Click <b>Enable</b> below to install Docker and unlock the marketplace at{" "}
                <code>/jabali-admin/docker-apps</code>. You can disable any time; app data
                under <code>/var/lib/jabali/docker-apps/</code> survives the flip.
              </>
            }
          />
        )}

        {enabled && (
          <Alert
            type="success"
            showIcon
            message={t("dockermarketplacecard.marketplace_live")}
            description={
              <>
                Open <a href="/jabali-admin/docker-apps">/jabali-admin/docker-apps</a> to
                install apps from the catalog.
              </>
            }
          />
        )}

        <Space>
          <Switch
            checked={enabled}
            loading={busy}
            onChange={(v) => (v ? handleEnable() : handleDisable())}
          />
          <Typography.Text>
            {enabled ? "Disable marketplace" : "Enable marketplace"}
          </Typography.Text>
        </Space>

        {enabled && (
          <div>
            <Space>
              <Switch checked={forUsers} loading={forUsersBusy} onChange={onToggleForUsers} />
              <Typography.Text strong>Enable Docker Apps for users</Typography.Text>
            </Space>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: "4px 0 0" }}>
              Off by default. When on, tenants get a Docker Apps tab (loopback-only,
              quota-bounded apps). Enabling makes the host tenant-ready (userns-remap +
              daemon restart). Off hides the tab and closes the tenant Docker API.
            </Typography.Paragraph>

            {forUsers && eligible.length > 0 && (
              <div style={{ marginTop: 12 }}>
                <Typography.Text strong>Apps available to users</Typography.Text>
                <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: "2px 0 8px" }}>
                  Pick which tenant-safe Docker apps users may install. All are checked by default.
                </Typography.Paragraph>
                <Checkbox.Group
                  value={exposed}
                  onChange={(v) => setExposed(v as string[])}
                  options={eligible.map((e) => ({ label: e.name, value: e.slug }))}
                  style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(160px, 1fr))", gap: 4 }}
                />
                <div style={{ marginTop: 10 }}>
                  <Button
                    type="primary"
                    size="small"
                    loading={appsBusy}
                    onClick={async () => {
                      setAppsBusy(true);
                      try {
                        // Save all-eligible as empty CSV (the "default" sentinel).
                        const allChecked = exposed.length === eligible.length;
                        await apiClient.patch("/admin/settings", {
                          docker_tenant_apps: allChecked ? "" : exposed.join(","),
                        });
                        message.success("Tenant app list saved");
                      } catch (err) {
                        const e = err as { response?: { data?: { detail?: string; error?: string } } };
                        message.error(e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to save");
                      } finally {
                        setAppsBusy(false);
                      }
                    }}
                  >
                    Save app list
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}
        {busyLabel && <Typography.Text type="secondary">{busyLabel}</Typography.Text>}
      </Space>
    </Card>
  );
}
