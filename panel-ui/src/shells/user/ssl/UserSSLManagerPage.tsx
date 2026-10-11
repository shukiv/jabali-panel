import { Card, Typography } from "antd";
import { ShieldCheckOutlined } from "@icons";
import { SSLManagerTable } from "../../../components/ssl/SSLManagerTable";
import { UserCloudflareTokenCard } from "./UserCloudflareTokenCard";

export const UserSSLManagerPage = () => {
  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <ShieldCheckOutlined /> SSL Manager
      </Typography.Title>

      <Card style={{ marginBottom: 16 }}>
        <SSLManagerTable endpoint="/ssl-certificates" showOwner={false} adminActions={false} />
      </Card>

      {/* GH #2032: the user's own Cloudflare token for proxied domains. */}
      <UserCloudflareTokenCard />
    </div>
  );
};
