// DomainMTASTSSection — per-domain MTA-STS toggle (M47 Wave 7b,
// ADR-0109). Mirrors DomainCacheSection / DomainSSLSection: loads its
// own state, flips the Switch (DB-as-truth → handler publishes the
// two managed_by="mta-sts" DNS records and schedules a reconcile so
// the SSL reconciler picks up mta-sts.<domain> as a new SAN on the
// next renewal cycle).
//
// The vhost itself (the file the agent writes via mail.mtasts.apply)
// lands once the renewed cert covers mta-sts.<domain> — Wave 7c hooks
// the reconciler step that fires the agent call. Until then, the UI
// shows "Policy live in DNS — vhost waiting for SSL renewal" so the
// operator knows the toggle worked and the wait is intentional.
import { useTranslation } from "react-i18next";
import { useCallback, useEffect, useState } from "react";
import { Alert, Skeleton, Space, Switch, Typography, message } from "antd";
import { CheckOutlined, CloseOutlined, SafetyOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

type MTAStsState = {
  domain_id: string;
  domain_name: string;
  enabled: boolean;
  id: number;
  policy_url: string;
  status_hint: "off" | "policy_published" | "live" | "awaiting_cert_renewal";
};

type Props = { domainId: string };

export const DomainMTASTSSection = ({ domainId }: Props) => {
  const { t } = useTranslation();
  const [state, setState] = useState<MTAStsState | null>(null);
  const [loading, setLoading] = useState(true);
  const [toggling, setToggling] = useState(false);

  const fetchState = useCallback(async () => {
    setLoading(true);
    try {
      const res = await apiClient.get<MTAStsState>(`/domains/${domainId}/mta-sts`);
      setState(res.data);
    } catch {
      message.error("Failed to load MTA-STS status");
    } finally {
      setLoading(false);
    }
  }, [domainId]);

  useEffect(() => {
    fetchState();
  }, [fetchState]);

  const onFlip = async (next: boolean) => {
    setToggling(true);
    try {
      const res = await apiClient.put<MTAStsState>(`/domains/${domainId}/mta-sts`, {
        enabled: next,
      });
      setState(res.data);
      message.success(
        next
          ? "MTA-STS enabled — DNS records published"
          : "MTA-STS disabled — records removed",
      );
    } catch {
      message.error("Failed to toggle MTA-STS");
      await fetchState();
    } finally {
      setToggling(false);
    }
  };

  if (loading) return <Skeleton active paragraph={{ rows: 1 }} />;

  const enabled = !!state?.enabled;
  const hint = state?.status_hint ?? "off";

  return (
    <Space direction="vertical" style={{ width: "100%" }}>
      <Space size="middle" align="center">
        <Switch
          checkedChildren={<CheckOutlined />}
          unCheckedChildren={<CloseOutlined />}
          checked={enabled}
          loading={toggling}
          onChange={onFlip}
        />
        <span>
          <SafetyOutlined style={{ marginRight: 6 }} />
          MTA-STS (RFC 8461)
        </span>
      </Space>
      {enabled && (
        <Typography.Text type="secondary" copyable={{ text: state?.policy_url }}>
          Policy URL: {state?.policy_url}
        </Typography.Text>
      )}
      {enabled && hint === "policy_published" && (
        <Alert
          type="warning"
          showIcon
          message={t("domainmtastssection.policy_live_in_dns_vhost_waiting_for_ssl_ren")}
          description={
            <>
              The TXT and A records are published. The agent will start
              serving the policy file at the URL above once the
              domain's TLS certificate is renewed to include the
              {" "}<code>mta-sts.{state?.domain_name}</code> SAN
              (happens automatically on the next ACME cycle, typically
              within a few days).
            </>
          }
        />
      )}
      {!enabled && (
        <Alert
          type="info"
          showIcon
          message={t("domainmtastssection.mta_sts_protects_inbound_mail_from_tls_downg")}
          description={t("domainmtastssection.when_enabled_jabali_publishes_a_policy_telli")}
        />
      )}
    </Space>
  );
};
