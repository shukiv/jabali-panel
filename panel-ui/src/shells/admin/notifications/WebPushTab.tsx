// WebPushTab — admin Notifications > Web Push panel.
//
// Web Push subscriptions are per-browser, not per-channel: VAPID keys
// live in server settings, the actual subscription handle is bound to
// the navigator that's currently logged in. Treating it as a
// notification "channel" was misleading — every browser already needs
// its own enable click anyway. This tab exposes the same toggle the
// bell dropdown footer offers, with a bit more context around it.
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, Space, Typography, message } from "antd";
import { BellOutlined } from "@icons";

import { useWebPushSubscription } from "../../../hooks/useWebPushSubscription";

export const WebPushTab = () => {
  const { t } = useTranslation();
  const webpush = useWebPushSubscription();

  const handleEnable = async () => {
    try {
      await webpush.subscribe();
      message.success("Browser push enabled");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Subscribe failed");
    }
  };

  const handleDisable = async () => {
    try {
      await webpush.unsubscribe();
      message.success("Browser push disabled");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Unsubscribe failed");
    }
  };

  const renderControls = () => {
    if (!webpush.supported) {
      return (
        <Alert
          type="warning"
          showIcon
          message={t("webpushtab.browser_push_is_not_supported_in_this_browse")}
          description={t("webpushtab.web_push_relies_on_the_service_worker_push_a")}
        />
      );
    }
    if (webpush.permission === "denied") {
      return (
        <Alert
          type="warning"
          showIcon
          message={t("webpushtab.browser_blocked_notifications")}
          description={t("webpushtab.open_the_site_permissions_for_this_domain_in")}
        />
      );
    }
    if (webpush.subscribed) {
      return (
        <Space direction="vertical" size={12}>
          <Alert
            type="success"
            showIcon
            message={t("webpushtab.this_browser_is_receiving_jabali_push_notifi")}
            description={t("webpushtab.notifications_appear_via_your_operating_syst")}
          />
          <Button danger onClick={handleDisable} loading={webpush.loading}>
            Disable on this browser
          </Button>
        </Space>
      );
    }
    return (
      <Space direction="vertical" size={12}>
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          Web Push delivers notifications to this browser even while the panel
          tab is closed. Each browser needs its own enable click — the
          subscription is tied to the navigator, not your account.
        </Typography.Paragraph>
        {webpush.error && (
          <Alert
            type="error"
            showIcon
            message={t("webpushtab.couldn_t_enable_browser_push")}
            description={webpush.error}
          />
        )}
        <Button type="primary" onClick={handleEnable} loading={webpush.loading}>
          Enable on this browser
        </Button>
      </Space>
    );
  };

  return (
    <div>
      <Card
        title={
          <Space>
            <BellOutlined />
            <span>Web Push</span>
          </Space>
        }
      >
        {renderControls()}
      </Card>
    </div>
  );
};
