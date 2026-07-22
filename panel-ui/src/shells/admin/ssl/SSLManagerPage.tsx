import { useTranslation } from "react-i18next";
import { Card, Typography } from "antd";
import { ShieldCheckOutlined } from "@icons";

import { SSLManagerTable } from "../../../components/ssl/SSLManagerTable";
import { PanelSSLCard } from "../settings/PanelSSLCard";

export const SSLManagerPage = () => {
  const { t } = useTranslation();
  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <ShieldCheckOutlined /> SSL Manager
      </Typography.Title>

      {/* Panel cert lives alongside customer-domain certs so the
          operator has one place to inspect / re-issue every cert
          managed by the panel. PanelSSLCard owns its own data
          loading + state via usePanelCertificate. */}
      <div style={{ marginBottom: 16 }}>
        <PanelSSLCard />
      </div>

      <Card title={t("sslmanagerpage.domain_certificates")}>
        <SSLManagerTable
          endpoint="/admin/ssl-certificates"
          showOwner={true}
        />
      </Card>
    </div>
  );
};
