// M30.1 follow-up — surface the restic master encryption key for
// off-host backup. The password lives at
// /etc/jabali-panel/restic-repo.password (root:root 0600); losing it
// = losing every snapshot in every restic repo (local + remote).
//
// Reveal is gated behind a click-to-show button; the page never auto-
// loads the secret. Copy + Download buttons let the operator stash
// it in a password manager / printed sheet / off-host vault.
import { useTranslation } from "react-i18next";
import {
  Alert,
  Button,
  Input,
  Space,
  Typography,
  message,
} from "antd";
import {
  CopyOutlined,
  DownloadOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  KeyOutlined,
} from "@icons";
import { useState } from "react";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

interface RevealResponse {
  status: string;
  path: string;
  password: string;
  algorithm: string;
  note: string;
}

export function EncryptionKeyCard() {
  const { t } = useTranslation();
  const [revealed, setRevealed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState<RevealResponse | null>(null);

  const handleReveal = async () => {
    if (revealed) {
      setRevealed(false);
      return;
    }
    setBusy(true);
    try {
      const resp = await apiClient.get<RevealResponse>(
        "/admin/backup-encryption-key",
      );
      setSecret(resp.data);
      setRevealed(true);
    } catch (err) {
      message.error(extractApiError(err, "reveal failed"));
    } finally {
      setBusy(false);
    }
  };

  const handleCopy = async () => {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret.password);
      message.success("password copied to clipboard");
    } catch {
      message.error("clipboard access denied — copy manually from the field");
    }
  };

  const handleDownload = () => {
    if (!secret) return;
    const blob = new Blob([secret.password + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "jabali-restic-repo.password";
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  return (
    <>
      <Space style={{ marginBottom: 12 }}>
        <KeyOutlined />
        <Typography.Text strong>Master encryption key</Typography.Text>
      </Space>
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 16 }}
        message={t("encryptionkeycard.losing_this_key_loses_every_snapshot")}
        description={
          <>
            Restic encrypts every backup with AES-256-GCM using the password at{" "}
            <code>/etc/jabali-panel/restic-repo.password</code>. The panel does
            NOT auto-rotate this password (rotation invalidates every existing
            snapshot). Back it up to a password manager, printed sheet, or
            other off-host vault before relying on the backup pipeline for DR.
          </>
        }
      />
      <Space direction="vertical" style={{ width: "100%" }}>
        <Space wrap>
          <Button
            type="primary"
            icon={revealed ? <EyeInvisibleOutlined /> : <EyeOutlined />}
            loading={busy}
            onClick={handleReveal}
          >
            {revealed ? "Hide" : "Reveal password"}
          </Button>
          {revealed && secret && (
            <>
              <Button icon={<CopyOutlined />} onClick={handleCopy}>
                Copy
              </Button>
              <Button icon={<DownloadOutlined />} onClick={handleDownload}>
                Download
              </Button>
            </>
          )}
        </Space>
        {revealed && secret && (
          <>
            <Input.Password
              value={secret.password}
              readOnly
              visibilityToggle
              style={{ fontFamily: "monospace", maxWidth: 600 }}
            />
            <Typography.Text type="secondary">
              Path: <code>{secret.path}</code> · Algorithm: {secret.algorithm}
            </Typography.Text>
          </>
        )}
        <Typography.Title level={5} style={{ marginTop: 16, marginBottom: 4 }}>
          Restore from scratch
        </Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          On a fresh OS install, restore manually with:
        </Typography.Paragraph>
        <Input.TextArea
          readOnly
          rows={4}
          value={`# 1. write the password back to disk
echo '<paste-password-here>' > /etc/jabali-panel/restic-repo.password
chmod 0600 /etc/jabali-panel/restic-repo.password

# 2. point restic at the local repo (or remote URL)
export RESTIC_REPOSITORY=/var/lib/jabali-backups/repo
export RESTIC_PASSWORD_FILE=/etc/jabali-panel/restic-repo.password

# 3. list snapshots / restore
restic snapshots
restic restore <snapshot_id> --target /tmp/recovery`}
          style={{ fontFamily: "monospace", fontSize: 12 }}
        />
      </Space>
    </>
  );
}
