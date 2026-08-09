// DomainMailProviderSection (GH#181) — pick where this domain's mail is
// hosted. The choice derives email_enabled + skip_auto_san server-side and
// publishes (or prunes) the provider's DNS records. Switching re-publishes
// DNS and reissues the cert on the next reconcile.
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Alert, Button, Input, Select, Space, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";

type Props = {
  domainId: string;
  mailProvider?: string;
  m365Onmicrosoft?: string;
  googleDkim?: string;
};

const OPTIONS = [
  { value: "jabali", label: "Jabali mail (this server)" },
  { value: "none", label: "No mail" },
  { value: "m365", label: "Microsoft 365" },
  { value: "google", label: "Google Workspace" },
];

export const DomainMailProviderSection = ({
  domainId,
  mailProvider,
  m365Onmicrosoft,
  googleDkim,
}: Props) => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [provider, setProvider] = useState<string>(mailProvider ?? "jabali");
  const [m365, setM365] = useState<string>(m365Onmicrosoft ?? "");
  const [gdkim, setGdkim] = useState<string>(googleDkim ?? "");

  useEffect(() => {
    setProvider(mailProvider ?? "jabali");
    setM365(m365Onmicrosoft ?? "");
    setGdkim(googleDkim ?? "");
  }, [mailProvider, m365Onmicrosoft, googleDkim]);

  const mut = useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.patch(`/domains/${domainId}`, {
        mail_provider: provider,
        m365_onmicrosoft: provider === "m365" ? m365 : "",
        google_dkim: provider === "google" ? gdkim : "",
      });
      return data;
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["domains"] });
      feedback.message.success("Mail provider updated — DNS and cert reconcile shortly");
    },
    onError: () => feedback.message.error("Failed to update mail provider"),
  });

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="small">
      <Alert
        type="info"
        showIcon
        message={t("domainmailprovidersection.mail_provider")}
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            Where this domain's email is hosted. <b>No mail</b> and the
            external providers stop Jabali from acting as the mailserver,
            drop the mail SANs from the cert, and (for Microsoft 365 /
            Google) publish that provider's MX / SPF / autodiscover records.
            Switching re-publishes DNS and reissues the certificate on the
            next reconcile; operator-authored apex mail records are replaced.
          </Typography.Paragraph>
        }
      />
      <Select
        style={{ minWidth: 280 }}
        value={provider}
        options={OPTIONS}
        onChange={setProvider}
      />
      {provider === "m365" && (
        <Input
          style={{ maxWidth: 360 }}
          placeholder="contoso.onmicrosoft.com (optional, for DKIM)"
          value={m365}
          onChange={(e) => setM365(e.target.value)}
        />
      )}
      {provider === "google" && (
        <Input.TextArea
          style={{ maxWidth: 480 }}
          rows={2}
          placeholder="google._domainkey TXT value from Google Admin (optional)"
          value={gdkim}
          onChange={(e) => setGdkim(e.target.value)}
        />
      )}
      <Space>
        <Button type="primary" loading={mut.isPending} onClick={() => mut.mutate()}>
          Save mail provider
        </Button>
      </Space>
    </Space>
  );
};
