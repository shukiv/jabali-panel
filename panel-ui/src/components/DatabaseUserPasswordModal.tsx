// DatabaseUserPasswordModal — reveal-once password display.
//
// Phase 3 API returns the generated password exactly once (see
// ADR-0021). This modal surfaces it to the operator with an obvious
// "save it now" framing, a copy-to-clipboard action, and a masked
// fallback so the password isn't casually left on screen.
import { Alert, Button, Modal, Space, Typography } from "antd";
import { CopyableInput } from "./CopyableInput";

interface DatabaseUserPasswordModalProps {
  open: boolean;
  username: string;
  password: string;
  title?: string;
  onClose: () => void;
}

export function DatabaseUserPasswordModal({
  open,
  username,
  password,
  title = "Database user password",
  onClose,
}: DatabaseUserPasswordModalProps) {
  return (
    <Modal
      title={title}
      open={open}
      onCancel={onClose}
      footer={[
        <Button key="done" type="primary" onClick={onClose}>
          I have saved the password
        </Button>,
      ]}
      maskClosable={false}
      destroyOnClose
    >
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        <Alert
          type="warning"
          showIcon
          title="This password will never be shown again."
          description="Copy it now. We only store a bcrypt hash — we can't retrieve the plaintext later. If lost, you'll need to rotate the password."
        />

        <div>
          <Typography.Text type="secondary">User</Typography.Text>
          <div style={{ fontFamily: "monospace" }}>{username}</div>
        </div>

        <div>
          <Typography.Text type="secondary">Password</Typography.Text>
          <CopyableInput value={password} secret style={{ fontFamily: "monospace", marginTop: 4 }} />
        </div>
      </Space>
    </Modal>
  );
}
