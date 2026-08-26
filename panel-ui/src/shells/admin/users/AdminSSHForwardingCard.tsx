// AdminSSHForwardingCard — admin control for a user's SSH TCP forwarding
// opt-in (GH #1229). Off by default keeps the JAB-352 lockdown; turning it on
// gives the user loopback-only forwarding, which VS Code Remote-SSH needs to
// reach its own VS Code Server. Sensitive loopback services stay firewall-
// blocked regardless, so an opted-in user can only forward to their own apps.
//
// Admin-only surface: relaxing a hardening control is an operator decision. The
// matching read-only status shows on the tenant's own SSH Keys page.
import { Alert, Card, Space, Spin, Switch, Typography } from "antd";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiOutlined } from "@icons";
import { feedback } from "../../../lib/feedback";
import { getUserSSHForwarding, setUserSSHForwarding } from "../../../apiClient";

export function AdminSSHForwardingCard({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();

  const statusQ = useQuery({
    queryKey: ["admin-user-ssh-forwarding", userId],
    queryFn: () => getUserSSHForwarding(userId),
    enabled: userId !== "",
  });

  const mutation = useMutation({
    mutationFn: (enabled: boolean) => setUserSSHForwarding(userId, enabled),
    onSuccess: (data) => {
      qc.setQueryData(["admin-user-ssh-forwarding", userId], data);
      feedback.message.success(
        data.ssh_forwarding_enabled
          ? t("adminuseroverview.ssh_fwd.enabled_toast")
          : t("adminuseroverview.ssh_fwd.disabled_toast"),
      );
    },
    onError: () => feedback.message.error(t("adminuseroverview.ssh_fwd.save_error")),
  });

  const title = (
    <span>
      <ApiOutlined /> {t("adminuseroverview.ssh_fwd.title")}
    </span>
  );

  if (statusQ.isLoading) {
    return (
      <Card size="small" title={title}>
        <Spin />
      </Card>
    );
  }
  if (statusQ.isError || !statusQ.data) {
    return (
      <Card size="small" title={title}>
        <Alert type="error" showIcon message={t("adminuseroverview.ssh_fwd.load_error")} />
      </Card>
    );
  }

  const { ssh_forwarding_enabled: enabled, ssh_enabled: sshEnabled } = statusQ.data;

  // Package grants no SSH shell — the toggle is moot. Explain instead of
  // offering a switch the admin can flip to no effect.
  if (!sshEnabled) {
    return (
      <Card size="small" title={title}>
        <Typography.Text type="secondary">
          {t("adminuseroverview.ssh_fwd.no_ssh")}
        </Typography.Text>
      </Card>
    );
  }

  // The Switch is controlled by server state (statusQ.data), so it only flips
  // after a successful save. Turning it ON confirms first (it relaxes the
  // hardening); turning it OFF tightens, so it applies immediately.
  const onChange = (next: boolean) => {
    if (next) {
      feedback.modal.confirm({
        title: t("adminuseroverview.ssh_fwd.confirm_on"),
        okText: t("adminuseroverview.ssh_fwd.confirm_ok"),
        cancelText: t("adminuseroverview.ssh_fwd.confirm_cancel"),
        onOk: () => mutation.mutate(true),
      });
    } else {
      mutation.mutate(false);
    }
  };

  return (
    <Card size="small" title={title}>
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <Space>
          <Switch checked={enabled} loading={mutation.isPending} onChange={onChange} />
          <Typography.Text strong>{t("adminuseroverview.ssh_fwd.label")}</Typography.Text>
        </Space>
        <Alert type="info" showIcon message={t("adminuseroverview.ssh_fwd.note")} />
      </Space>
    </Card>
  );
}
