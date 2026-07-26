// DemoQuickEnter — JAB-159. Two buttons on the Login page that auto-submit the
// Kratos password flow with the seeded demo identities surfaced by /info, so a
// visitor doesn't have to copy the creds out of the banner. Renders nothing
// unless /info reports demo mode.
//
// This component is demo-only: Login.tsx imports it via a dynamic import gated
// on import.meta.env.VITE_DEMO, so a production SPA build tree-shakes it out
// entirely (the `jabali-demo-quick-enter` marker below is asserted absent from
// the prod bundle by the ui-unit anti-leak CI step).
import { Alert, Button, Space } from "antd";

import { useServiceInfo } from "../useServiceInfo";

interface DemoQuickEnterProps {
  // Fills the Kratos password flow with (identifier, password) and submits it —
  // wired to Login's onFinish("password", …).
  onEnter: (identifier: string, password: string) => void;
}

export function DemoQuickEnter({ onEnter }: DemoQuickEnterProps) {
  const { data } = useServiceInfo();
  const demo = data?.demo;
  if (!demo?.enabled) return null;

  const adminReady = !!(demo.admin_email && demo.admin_password);
  const userReady = !!(demo.user_email && demo.user_password);
  if (!adminReady && !userReady) return null;

  return (
    <Alert
      type="info"
      showIcon
      data-marker="jabali-demo-quick-enter"
      style={{ marginBottom: 16 }}
      message="Demo mode"
      description={
        <Space direction="vertical" style={{ width: "100%" }}>
          <span>Jump straight in with a seeded account — no typing needed:</span>
          <Space wrap>
            {adminReady && (
              <Button
                type="primary"
                onClick={() => onEnter(demo.admin_email ?? "", demo.admin_password ?? "")}
              >
                Enter as admin
              </Button>
            )}
            {userReady && (
              <Button onClick={() => onEnter(demo.user_email ?? "", demo.user_password ?? "")}>
                Enter as user
              </Button>
            )}
          </Space>
        </Space>
      }
    />
  );
}
