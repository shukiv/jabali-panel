import { useEffect, useState } from "react";
import { Card, Space, Switch, Typography } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

// SupportChatCard — Server Settings -> General: the "Ask Jabali" AI support
// chat (a Chatwoot widget from support.jabali-panel.com), one switch for the
// admin panel and one for the client panels. Both off by default. While either
// is on, the panel's CSP allows that origin (agent nginx.panel_csp.apply); a
// failed apply turning it on reverts the switches server-side.

type ChatSettings = {
  support_chat_admin?: boolean;
  support_chat_clients?: boolean;
};

type Field = "support_chat_admin" | "support_chat_clients";

export const SupportChatCard = () => {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [values, setValues] = useState({ support_chat_admin: false, support_chat_clients: false });
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState<Field | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ChatSettings>("/admin/settings");
        if (!cancelled) {
          setValues({
            support_chat_admin: resp.data.support_chat_admin === true,
            support_chat_clients: resp.data.support_chat_clients === true,
          });
        }
      } catch {
        if (!cancelled) feedback.message.error(t("settings.support_chat.load_error", "Failed to load the support chat setting"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [t]);

  const onToggle = async (field: Field, next: boolean) => {
    setSaving(field);
    try {
      await apiClient.patch("/admin/settings", { [field]: next });
      setValues((v) => ({ ...v, [field]: next }));
      // The layouts read the switches from the capabilities; the dashboard
      // banner hides once the chat is on.
      void queryClient.invalidateQueries({ queryKey: ["server-capabilities"] });
      void queryClient.invalidateQueries({ queryKey: ["support-chat-notice"] });
      feedback.message.success(
        next
          ? t("settings.support_chat.saved_on", "Support chat turned on. It shows from the next page load.")
          : t("settings.support_chat.saved_off", "Support chat turned off."),
      );
    } catch {
      feedback.message.error(t("settings.support_chat.save_error", "Failed to update the support chat setting"));
    } finally {
      setSaving(null);
    }
  };

  return (
    <Card title={t("settings.support_chat.title", "Support chat (Ask Jabali)")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t(
          "settings.support_chat.body",
          "Adds the \"Ask Jabali\" AI support chat to the panel. It answers questions about using Jabali Panel. Off until you turn it on.",
        )}
      </Typography.Paragraph>
      <Typography.Paragraph type="secondary">
        {t(
          "settings.support_chat.privacy",
          "The chat is a widget from support.jabali-panel.com. While it is on, the panel allows that site: its script runs in the panel page, and what people type in the chat is sent there. The panel doesn't pass account details to it; like any website chat, it sees the page address and the browser.",
        )}
      </Typography.Paragraph>
      <Space direction="vertical" size={12}>
        <Space>
          <Switch
            aria-label={t("settings.support_chat.admin", "Admin panel")}
            checked={values.support_chat_admin}
            loading={saving === "support_chat_admin"}
            disabled={saving !== null}
            onChange={(next) => void onToggle("support_chat_admin", next)}
          />
          <Typography.Text>{t("settings.support_chat.admin", "Admin panel")}</Typography.Text>
        </Space>
        <Space>
          <Switch
            aria-label={t("settings.support_chat.clients", "Client panels")}
            checked={values.support_chat_clients}
            loading={saving === "support_chat_clients"}
            disabled={saving !== null}
            onChange={(next) => void onToggle("support_chat_clients", next)}
          />
          <Typography.Text>{t("settings.support_chat.clients", "Client panels")}</Typography.Text>
        </Space>
      </Space>
    </Card>
  );
};
