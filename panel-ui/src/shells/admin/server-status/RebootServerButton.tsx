// RebootServerButton — GH #1330. A gated "Reboot server" action.
//
// The action is admin-only and requires recent-auth step-up server-side; a stale
// session returns 403 stepup_required, which apiClient handles globally by
// redirecting to a Kratos refresh login. Here we just confirm, POST, and report.
import { App, Button, Typography } from "antd";
import type { ReactNode } from "react";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

export const RebootServerButton = ({
  block,
  children,
  type,
}: {
  block?: boolean;
  children?: ReactNode;
  type?: "primary" | "default";
}) => {
  const { modal } = App.useApp();

  const confirm = () => {
    modal.confirm({
      title: "Reboot server?",
      okText: "Reboot now",
      okButtonProps: { danger: true },
      cancelText: "Cancel",
      content: (
        <Typography.Paragraph style={{ marginBottom: 0 }}>
          The server will reboot in a few seconds and be briefly unavailable while
          it restarts. Every site and service on the host goes down until it's
          back, and this panel connection will drop and reconnect. Only do this
          when a reboot is actually required (e.g. after kernel/security updates).
        </Typography.Paragraph>
      ),
      onOk: async () => {
        try {
          await apiClient.post("/system/reboot");
          feedback.message.success(
            "Reboot scheduled — the server will go down shortly and come back on its own.",
          );
        } catch (e) {
          const err = e as { response?: { data?: { error?: string; detail?: string } } };
          // stepup_required is handled globally (redirect to re-auth) — don't
          // double-report it here.
          if (err.response?.data?.error !== "stepup_required") {
            feedback.message.error(
              err.response?.data?.detail ??
                err.response?.data?.error ??
                "Failed to schedule reboot",
            );
          }
          throw e; // keep the modal's OK spinner from resolving to success
        }
      },
    });
  };

  return (
    <Button danger block={block} type={type} onClick={confirm}>
      {children ?? "Reboot server"}
    </Button>
  );
};
