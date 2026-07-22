// StalwartWebadminCard — GH #243 (ADR-0142). Opt-in toggle that exposes
// Stalwart's WebAdmin UI through an nginx reverse-proxy (TLS + optional IP
// allowlist + Stalwart's own admin login). Stalwart itself stays loopback-bound;
// this is the only door. Default off. Lives on the Server Settings → Email tab.
import { useTranslation } from "react-i18next";
import { App, Alert, Button, Card, Input, Modal, Space, Switch, Typography } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

interface WebadminSettings {
  stalwart_webadmin_enabled: boolean;
  stalwart_webadmin_allow_cidrs: string;
  hostname: string;
}

interface AdminCredential {
  user: string;
  password: string;
  rotated: boolean;
  url: string;
}

export function StalwartWebadminCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [enabled, setEnabled] = useState(false);
  const [cidrs, setCidrs] = useState("");
  const [hostname, setHostname] = useState("");
  const [busy, setBusy] = useState(false);
  const [adminCred, setAdminCred] = useState<AdminCredential | null>(null);
  const [adminBusy, setAdminBusy] = useState(false);

  const refresh = async () => {
    try {
      const r = await apiClient.get<WebadminSettings>("/admin/settings");
      setEnabled(!!r.data.stalwart_webadmin_enabled);
      setCidrs(r.data.stalwart_webadmin_allow_cidrs || "");
      setHostname(r.data.hostname || "");
    } catch (err) {
      message.error(`Could not load WebAdmin setting: ${err instanceof Error ? err.message : String(err)}`);
    }
  };
  useEffect(() => {
    void refresh();
  }, []);

  const patch = async (body: Record<string, unknown>) => {
    await apiClient.patch("/admin/settings", body);
  };

  const doEnable = async () => {
    setBusy(true);
    try {
      await patch({ stalwart_webadmin_enabled: true, stalwart_webadmin_allow_cidrs: cidrs });
      setEnabled(true);
      message.success("Stalwart WebAdmin exposed");
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      message.error(e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to enable");
    } finally {
      setBusy(false);
    }
  };

  const onToggle = (val: boolean) => {
    if (val) {
      Modal.confirm({
        title: "Expose the Stalwart WebAdmin to the internet?",
        okText: "Enable",
        okButtonProps: { danger: true },
        content:
          "This publishes the full mail-server admin UI at " +
          (hostname ? `${hostname}:8449` : "<panel-hostname>:8449") +
          " behind TLS, the optional IP allowlist, and Stalwart's own admin login. It is the highest-value target on the box — anyone who reaches it can read all mail and send as any domain. Set the IP allowlist, and turn this off when you are done.",
        onOk: doEnable,
      });
    } else {
      setBusy(true);
      patch({ stalwart_webadmin_enabled: false })
        .then(() => {
          setEnabled(false);
          message.warning("Stalwart WebAdmin proxy removed");
        })
        .catch((err) => message.error(err instanceof Error ? err.message : "Failed to disable"))
        .finally(() => setBusy(false));
    }
  };

  const saveAllowlist = async () => {
    setBusy(true);
    try {
      await patch({ stalwart_webadmin_allow_cidrs: cidrs });
      message.success("Allowlist saved");
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      message.error(e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to save allowlist");
    } finally {
      setBusy(false);
    }
  };

  const manageAdminCred = async (action: "reveal" | "rotate") => {
    setAdminBusy(true);
    try {
      const r = await apiClient.post<AdminCredential>("/admin/settings/stalwart-admin-credential", {
        action,
      });
      setAdminCred(r.data);
      if (action === "rotate") {
        message.success("Stalwart admin password rotated");
      }
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      message.error(e.response?.data?.detail ?? e.response?.data?.error ?? `Failed to ${action}`);
    } finally {
      setAdminBusy(false);
    }
  };

  const confirmRotateAdmin = () =>
    Modal.confirm({
      title: "Rotate the Stalwart admin password?",
      okText: "Rotate",
      okButtonProps: { danger: true },
      content:
        "This is also the panel's own management credential. Rotating it restarts " +
        "the mail server and the panel API (a few-second blip) and signs you out of " +
        "any open WebAdmin session. The new password is shown once.",
      onOk: () => manageAdminCred("rotate"),
    });

  return (
    <Card title={t("stalwartwebadmincard.stalwart_webadmin_advanced")} size="small" style={{ marginTop: 16 }}>
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Typography.Paragraph type="secondary" style={{ margin: 0 }}>
          Exposes Stalwart&apos;s native admin UI (queues, tracing, fine-grained
          settings the panel doesn&apos;t surface) through nginx. Off by default —
          Stalwart stays bound to localhost; this is the only externally reachable
          door, behind TLS, the IP allowlist, and Stalwart&apos;s own admin login.
        </Typography.Paragraph>

        <Space>
          <Switch checked={enabled} loading={busy} onChange={onToggle} />
          <Typography.Text strong>{enabled ? "Exposed" : "Off"}</Typography.Text>
        </Space>

        <div>
          <Typography.Text>Source IP allowlist (recommended)</Typography.Text>
          <Space.Compact style={{ width: "100%" }}>
            <Input
              placeholder="e.g. 203.0.113.0/24, 198.51.100.5  (empty = any IP reaches Stalwart’s login)"
              value={cidrs}
              onChange={(e) => setCidrs(e.target.value)}
              disabled={busy}
            />
            <Button onClick={saveAllowlist} loading={busy} disabled={!enabled}>
              Save
            </Button>
          </Space.Compact>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Comma/space-separated IPs or CIDRs. Empty = any IP can reach Stalwart’s login — set this.
          </Typography.Text>
        </div>

        {enabled && (
          <>
            <Alert
              type="warning"
              showIcon
              message={`Live at https://${hostname}:8449/`}
              description={t("stalwartwebadmincard.the_full_mail_server_admin_is_reachable_behi")}
            />
            <div>
              <Typography.Text strong>Stalwart admin login</Typography.Text>
              <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: "2px 0 8px" }}>
                Stalwart&apos;s own sign-in for the WebAdmin. This is also
                the panel&apos;s management credential — rotating it restarts the mail server and
                panel API briefly.
              </Typography.Paragraph>
              <Space>
                <Button onClick={() => manageAdminCred("reveal")} loading={adminBusy}>
                  Reveal admin login
                </Button>
                <Button danger onClick={confirmRotateAdmin} loading={adminBusy}>
                  Rotate admin password
                </Button>
              </Space>
            </div>
          </>
        )}

        <Modal
          open={!!adminCred}
          title={adminCred?.rotated ? "New Stalwart admin password" : "Stalwart admin login"}
          onOk={() => setAdminCred(null)}
          onCancel={() => setAdminCred(null)}
          okText={t("stalwartwebadmincard.done")}
          cancelButtonProps={{ style: { display: "none" } }}
        >
          <Typography.Paragraph>
            Use this at Stalwart&apos;s own sign-in for the WebAdmin.
            {adminCred?.rotated && (
              <>
                {" "}
                <strong>It won&apos;t be shown again</strong> — the mail server and panel API are
                restarting to apply it.
              </>
            )}
          </Typography.Paragraph>
          <Typography.Paragraph>
            User: <Typography.Text copyable>{adminCred?.user}</Typography.Text>
            <br />
            Password: <Typography.Text copyable code>{adminCred?.password}</Typography.Text>
          </Typography.Paragraph>
        </Modal>
      </Space>
    </Card>
  );
}
