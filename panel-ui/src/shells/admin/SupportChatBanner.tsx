// SupportChatBanner — tells admins on the dashboard that the "Ask Jabali" AI
// support chat exists and is off. Closing it hides it for every admin
// (support_chat_notice_dismissed is server-wide), and it never shows once the
// chat is on in either panel.
import { useState } from "react";
import { Alert, Button, Space, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";

type NoticeSettings = {
  support_chat_admin?: boolean;
  support_chat_clients?: boolean;
  support_chat_notice_dismissed?: boolean;
};

export const SupportChatBanner = () => {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [closed, setClosed] = useState(false);
  const settings = useQuery({
    queryKey: ["support-chat-notice"],
    queryFn: async () => (await apiClient.get<NoticeSettings>("/admin/settings")).data,
    staleTime: 60_000,
  });

  // Silent while loading or on error: a banner that flashes in and out
  // trains admins to ignore banners.
  const s = settings.data;
  if (closed || !s || s.support_chat_notice_dismissed || s.support_chat_admin || s.support_chat_clients) {
    return null;
  }

  const dismiss = async () => {
    setClosed(true);
    try {
      await apiClient.patch("/admin/settings", { support_chat_notice_dismissed: true });
      void queryClient.invalidateQueries({ queryKey: ["support-chat-notice"] });
    } catch {
      feedback.message.error(t("dashboard.support_chat_notice.dismiss_error", "Couldn't save that the banner was closed. It will show again."));
    }
  };

  return (
    <Alert
      type="info"
      showIcon
      // An object `closable` only renders the button when it names a closeIcon
      // (true = antd's default icon).
      closable={{
        closeIcon: true,
        onClose: () => void dismiss(),
        "aria-label": t("dashboard.support_chat_notice.close", "Close"),
      }}
      style={{ marginBottom: 16 }}
      title={t("dashboard.support_chat_notice.title", "New: \"Ask Jabali\" AI support chat")}
      description={
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text>
            {t(
              "dashboard.support_chat_notice.body",
              "You can add an AI support chat to the admin panel, the client panels, or both. It answers questions about using Jabali Panel and stays off until you turn it on.",
            )}
          </Typography.Text>
          <Link to="/jabali-admin/settings">
            <Button size="small">{t("dashboard.support_chat_notice.action", "Open settings")}</Button>
          </Link>
        </Space>
      }
    />
  );
};
