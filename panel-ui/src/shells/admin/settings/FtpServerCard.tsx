import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Alert, Card, Form, Input, InputNumber, Space, Switch, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import { apiClient } from "../../../apiClient";

// FtpServerCard — Server Settings: the GH #1053 FTP module opt-in.
// STRICTLY off by default. Enabling installs vsftpd (module
// install-on-enable) and opens 21/tcp + the passive range; disabling masks
// the daemon and closes the ports on the next `jabali update` sweep.
// SFTP subaccounts work regardless of this toggle.
export const FtpServerCard = () => {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(false);
  const [allowPlaintext, setAllowPlaintext] = useState(false);
  const [pasvAddress, setPasvAddress] = useState("");
  const [maxClients, setMaxClients] = useState<number>(50);
  const [maxPerIP, setMaxPerIP] = useState<number>(8);
  const [maxRateKBs, setMaxRateKBs] = useState<number>(0);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  // JAB-259/260 phase C: observed-vs-desired. The host converges asynchronously,
  // so a toggle can leave a transient gap; a persistent gap (a fail-open disable
  // or a silently-failed plaintext tighten) is a real security drift the operator
  // must see rather than trust a false "off"/"secure". Polled so a healed drift
  // clears itself and a stuck one stays visible.
  const [drift, setDrift] = useState<{ exposure: boolean; tls: boolean; ports: boolean }>({
    exposure: false,
    tls: false,
    ports: false,
  });

  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      try {
        const resp = await apiClient.get<{
          drift?: { exposure?: boolean; tls?: boolean; ports?: boolean };
        }>("/admin/settings/modules/ftp/status");
        if (!cancelled) {
          setDrift({
            exposure: !!resp.data.drift?.exposure,
            tls: !!resp.data.drift?.tls,
            ports: !!resp.data.drift?.ports,
          });
        }
      } catch {
        // Status is best-effort; never block the card on a probe hiccup.
      }
    };
    poll();
    const id = window.setInterval(poll, 8000);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{
          ftp_enabled?: boolean;
          ftp_allow_plaintext?: boolean;
          ftp_pasv_address?: string;
          ftp_max_clients?: number;
          ftp_max_per_ip?: number;
          ftp_local_max_rate_kbs?: number;
        }>("/admin/settings");
        if (!cancelled) {
          setEnabled(!!resp.data.ftp_enabled);
          setAllowPlaintext(!!resp.data.ftp_allow_plaintext);
          setPasvAddress(resp.data.ftp_pasv_address ?? "");
          setMaxClients(resp.data.ftp_max_clients ?? 50);
          setMaxPerIP(resp.data.ftp_max_per_ip ?? 8);
          setMaxRateKBs(resp.data.ftp_local_max_rate_kbs ?? 0);
        }
      } catch {
        if (!cancelled) feedback.message.error(t("ftpservercard.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [t]);

  const patch = async (body: Record<string, unknown>, onOk: () => void) => {
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", body);
      onOk();
    } catch {
      feedback.message.error(t("ftpservercard.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("ftpservercard.title")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("ftpservercard.description")}
      </Typography.Paragraph>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        {drift.exposure && (
          <Alert
            type="error"
            showIcon
            message={t("ftpservercard.drift_exposure_title")}
            description={t("ftpservercard.drift_exposure_desc")}
          />
        )}
        {drift.tls && (
          <Alert
            type="error"
            showIcon
            message={t("ftpservercard.drift_tls_title")}
            description={t("ftpservercard.drift_tls_desc")}
          />
        )}
        {drift.ports && (
          <Alert
            type="warning"
            showIcon
            message={t("ftpservercard.drift_ports_title")}
            description={t("ftpservercard.drift_ports_desc")}
          />
        )}
        <Form.Item label={t("ftpservercard.enable_label")} style={{ marginBottom: 0 }}>
          <Switch
            checked={enabled}
            loading={saving}
            onChange={(next) =>
              patch({ ftp_enabled: next }, () => {
                setEnabled(next);
                feedback.message.success(
                  next ? t("ftpservercard.enabled_msg") : t("ftpservercard.disabled_msg"),
                );
              })
            }
            checkedChildren="On"
            unCheckedChildren="Off"
          />
        </Form.Item>

        {enabled && (
          <>
            <Form.Item
              label={t("ftpservercard.plaintext_label")}
              style={{ marginBottom: 0 }}
              tooltip={t("ftpservercard.plaintext_tooltip")}
            >
              <Switch
                checked={allowPlaintext}
                loading={saving}
                onChange={(next) =>
                  patch({ ftp_allow_plaintext: next }, () => {
                    setAllowPlaintext(next);
                    feedback.message.success(t("ftpservercard.saved"));
                  })
                }
                checkedChildren="On"
                unCheckedChildren="Off"
              />
            </Form.Item>
            {allowPlaintext && (
              <Alert type="warning" showIcon message={t("ftpservercard.plaintext_warning")} />
            )}
            {/* Numbers commit on BLUR, not per keystroke — every PATCH
                re-dispatches the module install, which restarts vsftpd. */}
            <Space wrap size="middle">
              <Form.Item
                label={t("ftpservercard.max_clients_label")}
                tooltip={t("ftpservercard.zero_unlimited")}
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  min={0}
                  max={100000}
                  value={maxClients}
                  disabled={saving}
                  onChange={(v) => setMaxClients(v ?? 0)}
                  onBlur={() =>
                    patch({ ftp_max_clients: maxClients }, () =>
                      feedback.message.success(t("ftpservercard.saved")),
                    )
                  }
                />
              </Form.Item>
              <Form.Item
                label={t("ftpservercard.max_per_ip_label")}
                tooltip={t("ftpservercard.zero_unlimited")}
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  min={0}
                  max={10000}
                  value={maxPerIP}
                  disabled={saving}
                  onChange={(v) => setMaxPerIP(v ?? 0)}
                  onBlur={() =>
                    patch({ ftp_max_per_ip: maxPerIP }, () =>
                      feedback.message.success(t("ftpservercard.saved")),
                    )
                  }
                />
              </Form.Item>
              <Form.Item
                label={t("ftpservercard.max_rate_label")}
                tooltip={t("ftpservercard.max_rate_tooltip")}
                style={{ marginBottom: 0 }}
              >
                <InputNumber
                  min={0}
                  max={10000000}
                  value={maxRateKBs}
                  disabled={saving}
                  addonAfter="KB/s"
                  onChange={(v) => setMaxRateKBs(v ?? 0)}
                  onBlur={() =>
                    patch({ ftp_local_max_rate_kbs: maxRateKBs }, () =>
                      feedback.message.success(t("ftpservercard.saved")),
                    )
                  }
                />
              </Form.Item>
            </Space>
            <Form.Item
              label={t("ftpservercard.pasv_label")}
              tooltip={t("ftpservercard.pasv_tooltip")}
              style={{ marginBottom: 0 }}
            >
              <Input.Search
                defaultValue={pasvAddress}
                placeholder="203.0.113.10"
                enterButton={t("ftpservercard.save")}
                loading={saving}
                onSearch={(v) =>
                  patch({ ftp_pasv_address: v.trim() }, () => {
                    setPasvAddress(v.trim());
                    feedback.message.success(t("ftpservercard.saved"));
                  })
                }
                style={{ maxWidth: 360 }}
              />
            </Form.Item>
          </>
        )}
      </Space>
    </Card>
  );
};
