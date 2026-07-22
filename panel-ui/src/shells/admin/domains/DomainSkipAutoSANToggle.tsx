// Per-domain "skip auto-added SAN" toggle. Off (default): panel adds
// mail.<domain> + autoconfig.<domain> (+ mta-sts.<domain>) to the LE
// cert SAN list per ADR-0070. On: panel issues a cert for ONLY
// <domain> + www.<domain>. Use when the tenant's mail runs elsewhere
// or the helper subdomains aren't DNS-resolvable.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Alert, Skeleton, Space, Switch, Typography, message } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

type Resp = { domain_id: string; domain_name: string; enabled: boolean };

type Props = { domainId: string };

export const DomainSkipAutoSANToggle = ({ domainId }: Props) => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["domain-skip-auto-san", domainId],
    queryFn: async () => {
      const { data } = await apiClient.get<Resp>(
        `/domains/${domainId}/skip-auto-san`,
      );
      return data;
    },
    enabled: !!domainId,
  });

  const [enabled, setEnabled] = useState<boolean>(false);
  useEffect(() => {
    if (data) setEnabled(data.enabled);
  }, [data]);

  const mut = useMutation({
    mutationFn: async (next: boolean) => {
      const { data } = await apiClient.put<Resp>(
        `/domains/${domainId}/skip-auto-san`,
        { enabled: next },
      );
      return data;
    },
    onSuccess: async (resp) => {
      setEnabled(resp.enabled);
      await qc.invalidateQueries({
        queryKey: ["domain-skip-auto-san", domainId],
      });
      message.success(
        resp.enabled
          ? "Auto-SAN disabled — cert will cover only the base domain"
          : "Auto-SAN enabled — cert will include mail / autoconfig",
      );
    },
    onError: () => message.error("Failed to update"),
  });

  if (isLoading) return <Skeleton active paragraph={{ rows: 1 }} />;

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="small">
      <Alert
        type="info"
        showIcon
        message={t("domainskipautosantoggle.skip_auto_added_san_names")}
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            By default the panel adds <code>mail.&lt;domain&gt;</code> and{" "}
            <code>autoconfig.&lt;domain&gt;</code> to this cert (and{" "}
            <code>mta-sts.&lt;domain&gt;</code> when MTA-STS is on). If
            those subdomains aren't DNS-resolvable to this server, Let's
            Encrypt fails the WHOLE cert. Turn this on to issue a cert for
            only the base domain + www.
          </Typography.Paragraph>
        }
      />
      <Space>
        <Switch
          checked={enabled}
          loading={mut.isPending}
          onChange={(next) => mut.mutate(next)}
        />
        <Typography.Text>
          {enabled ? "Auto-SAN disabled" : "Auto-SAN enabled (default)"}
        </Typography.Text>
      </Space>
    </Space>
  );
};
