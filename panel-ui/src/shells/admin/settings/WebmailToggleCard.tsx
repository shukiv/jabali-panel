import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Button, Card, Input, Space, Switch, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { extractApiError } from "../../../apiErrors";

import { apiClient } from "../../../apiClient";

// WebmailToggleCard — Server Settings → Email: enable/disable the bundled
// Bulwark webmail client server-wide (GH #316). When off, the reconciler tears
// down the webmail vhosts and stops the jabali-webmail service.
export const WebmailToggleCard = () => {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ webmail_enabled?: boolean }>("/admin/settings");
        if (!cancelled) setEnabled(resp.data.webmail_enabled !== false);
      } catch {
        if (!cancelled) feedback.message.error("Failed to load webmail setting");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const onToggle = async (next: boolean) => {
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", { webmail_enabled: next });
      setEnabled(next);
      feedback.message.success(`${next ? "Webmail enabled" : "Webmail disabled"}: ${next
          ? "The webmail vhosts will be (re)served on the next reconcile."
          : "Webmail vhosts are being torn down and the webmail service stopped."}`);
    } catch {
      feedback.message.error("Failed to update webmail setting");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("webmailtogglecard.webmail")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Serve the bundled webmail client (Bulwark) at <Typography.Text code>mail.&lt;domain&gt;</Typography.Text>{" "}
        for every email-enabled domain. Turn it off to stop offering webmail
        server-wide — mail delivery (IMAP/SMTP/JMAP) is unaffected.
      </Typography.Paragraph>
      <Switch
        checked={enabled}
        loading={saving}
        onChange={onToggle}
        checkedChildren="On"
        unCheckedChildren="Off"
      />
    </Card>
  );
};

// WebmailEditorCard — Server Settings → Email: the WOPI document editor
// webmail opens office files in (GH #2501). One server-wide URL; empty turns
// editing off. The reconciler writes it to webmail's environment and restarts
// webmail only when it changed.
export const WebmailEditorCard = () => {
  const [url, setUrl] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<{ webmail_office_editor_url?: string }>("/admin/settings");
        if (!cancelled) setUrl(resp.data.webmail_office_editor_url ?? "");
      } catch {
        if (!cancelled) feedback.message.error("Failed to load the document editor setting");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const save = async () => {
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", { webmail_office_editor_url: url });
      feedback.message.success("Document editor saved. Webmail picks it up on the next reconcile.");
    } catch (e) {
      feedback.message.error(extractApiError(e, "Failed to save the document editor setting"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Document editor" style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Webmail opens office documents from its Files view in this editor, for every webmail user on the
        server. Install EuroOffice Docs from Docker apps, give it a domain with HTTPS, and enter that
        address; leave it empty to turn editing off.
      </Typography.Paragraph>
      <Space direction="vertical" size="middle" style={{ width: "100%", maxWidth: 520 }}>
        <Input
          aria-label="Editor address"
          placeholder="https://office.example.com"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
        />
        <Button type="primary" loading={saving} onClick={() => void save()}>
          Save
        </Button>
      </Space>
    </Card>
  );
};
