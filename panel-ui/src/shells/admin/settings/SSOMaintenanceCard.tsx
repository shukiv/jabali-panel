// SSOMaintenanceCard — GH #575. Admin SSO maintenance for phpMyAdmin SSO.
//
// - Prune expired tokens: one-click, safe (in-process DB purge).
// - Rotate encryption key: a destructive, multi-step operator procedure
//   (re-encrypt all shadow passwords → swap the on-disk key file → SIGHUP the
//   panel). It is intentionally NOT one-click: browser-firing an identity-key
//   rotation that reloads the serving process is a foot-gun that, done wrong,
//   breaks phpMyAdmin SSO for every tenant. We surface the exact guarded
//   procedure + rollback instead (the CLI-supported branch of #575).
import { useTranslation } from "react-i18next";
import {
  Alert,
  App,
  Button,
  Card,
  Popconfirm,
  Space,
  Typography,
} from "antd";
import { useMutation } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";

const { Text, Paragraph } = Typography;

const ROTATE_PROCEDURE = `# 1. Generate a new AES-256 key
openssl rand 32 | base64 > /etc/jabali/sso_key.new

# 2. Re-encrypt all shadow passwords into the new key (transactional)
jabali sso rotate-key \\
  --current-key /etc/jabali/sso_key.txt \\
  --new-key /etc/jabali/sso_key.new

# 3. Back up the old key (for rollback), then swap in the new one
cp /etc/jabali/sso_key.txt /etc/jabali/sso_key.txt.bak
mv /etc/jabali/sso_key.new /etc/jabali/sso_key.txt

# 4. Reload the panel so it uses the new key
systemctl kill -s SIGHUP jabali-panel`;

const ROLLBACK = `# If SSO breaks after rotation, restore the previous key and re-encrypt back:
mv /etc/jabali/sso_key.txt /etc/jabali/sso_key.rotated
jabali sso rotate-key --current-key /etc/jabali/sso_key.rotated --new-key /etc/jabali/sso_key.txt.bak
mv /etc/jabali/sso_key.txt.bak /etc/jabali/sso_key.txt
systemctl kill -s SIGHUP jabali-panel`;

export function SSOMaintenanceCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();

  const prune = useMutation({
    mutationFn: async () =>
      (await apiClient.post<{ purged: number }>("/admin/sso/prune-tokens")).data,
    onSuccess: (d) => message.success(`Purged ${d.purged} expired SSO token(s)`),
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "prune failed"),
  });

  return (
    <Card title={t("ssomaintenancecard.sso_maintenance_phpmyadmin")} style={{ marginBottom: 16 }}>
      <Space direction="vertical" size="large" style={{ width: "100%" }}>
        {/* prune — safe, one-click */}
        <div>
          <Text strong>Expired token cleanup</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            The reconciler purges expired phpMyAdmin SSO tokens on its nightly
            sweep. This forces an immediate purge.
          </Paragraph>
          <Popconfirm
            title={t("ssomaintenancecard.purge_expired_sso_tokens_now")}
            description={t("ssomaintenancecard.deletes_only_already_expired_tokens_active_s")}
            okText={t("ssomaintenancecard.purge")}
            onConfirm={() => prune.mutate()}
          >
            <Button loading={prune.isPending}>Prune expired tokens</Button>
          </Popconfirm>
        </div>

        {/* rotate — guarded documented procedure */}
        <div>
          <Text strong>Encryption key rotation</Text>
          <Alert
            style={{ margin: "8px 0" }}
            type="warning"
            showIcon
            message={t("ssomaintenancecard.operator_procedure_run_on_the_host_not_from")}
            description={t("ssomaintenancecard.rotation_re_encrypts_every_stored_db_user_pa")}
          />
          <Paragraph type="secondary" style={{ marginBottom: 4 }}>
            Rotate the AES-256-GCM key (as root on the panel host):
          </Paragraph>
          <pre
            style={{
              background: "rgba(0,0,0,0.04)",
              padding: 12,
              borderRadius: 6,
              fontSize: 12,
              overflowX: "auto",
            }}
          >
            {ROTATE_PROCEDURE}
          </pre>
          <Paragraph type="secondary" style={{ margin: "8px 0 4px" }}>
            Rollback (if SSO fails after rotation):
          </Paragraph>
          <pre
            style={{
              background: "rgba(0,0,0,0.04)",
              padding: 12,
              borderRadius: 6,
              fontSize: 12,
              overflowX: "auto",
            }}
          >
            {ROLLBACK}
          </pre>
        </div>
      </Space>
    </Card>
  );
}
