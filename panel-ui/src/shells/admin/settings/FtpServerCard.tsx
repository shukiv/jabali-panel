import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Alert, Card, Form, Input, Space, Switch, Typography } from "antd";
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
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{
          ftp_enabled?: boolean;
          ftp_allow_plaintext?: boolean;
          ftp_pasv_address?: string;
        }>("/admin/settings");
        if (!cancelled) {
          setEnabled(!!resp.data.ftp_enabled);
          setAllowPlaintext(!!resp.data.ftp_allow_plaintext);
          setPasvAddress(resp.data.ftp_pasv_address ?? "");
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
