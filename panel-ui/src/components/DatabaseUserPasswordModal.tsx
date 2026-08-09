// DatabaseUserPasswordModal — reveal-once password display.
//
// Phase 3 API returns the generated password exactly once (see
// ADR-0021). This modal surfaces it to the operator with an obvious
// "save it now" framing, a copy-to-clipboard action, and a masked
// fallback so the password isn't casually left on screen.
import { useState } from "react";
import { CopyOutlined, EyeInvisibleOutlined, EyeOutlined } from "@icons";
import { Alert, Button, Input, Modal, Space, Typography } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts

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
  const [revealed, setRevealed] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(password);
      feedback.message.success("Password copied to clipboard");
    } catch {
      feedback.message.error("Copy failed — select the field and copy manually");
    }
  };

  const close = () => {
    setRevealed(false);
    onClose();
  };

  return (
    <Modal
      title={title}
      open={open}
      onCancel={close}
      footer={[
        <Button key="done" type="primary" onClick={close}>
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
          <Input.Group compact style={{ display: "flex", marginTop: 4 }}>
            <Input
              value={revealed ? password : "•".repeat(Math.min(password.length, 32))}
              readOnly
              style={{ fontFamily: "monospace", flex: 1 }}
              onFocus={(e) => e.currentTarget.select()}
            />
            <Button
              icon={revealed ? <EyeInvisibleOutlined /> : <EyeOutlined />}
              onClick={() => setRevealed((r) => !r)}
              title={revealed ? "Hide" : "Reveal"}
            />
            <Button icon={<CopyOutlined />} onClick={copy} title="Copy to clipboard" />
          </Input.Group>
        </div>
      </Space>
    </Modal>
  );
}
