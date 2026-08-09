// Tenant Redis access card (GH #1016). Jabali locks Redis behind ACL auth, so a
// migrated app that connected unauthenticated now gets "NOAUTH Authentication
// required". This card hands the tenant a scoped, ready-to-use credential.
//
// Reveal-on-click: GET /me/redis-access provisions the tenant's ACL user
// (idempotent) and returns the credential — so we don't fetch a secret until
// the user asks for it.
import { useState } from "react";
import { Card, Button, Descriptions, Typography, Alert, message } from "antd";
import { apiClient } from "../../../apiClient";

const { Text, Paragraph } = Typography;

interface RedisAccess {
  socket: string;
  host: string;
  port: number;
  username: string;
  password: string;
  database: number;
  key_prefix: string;
  allowed_commands: string[];
  note: string;
}

export const RedisAccessCard = () => {
  const [creds, setCreds] = useState<RedisAccess | null>(null);
  const [loading, setLoading] = useState(false);

  const reveal = async () => {
    setLoading(true);
    try {
      const resp = await apiClient.get<RedisAccess>("/me/redis-access");
      setCreds(resp.data);
    } catch {
      message.error("Could not load your Redis credentials.");
    } finally {
      setLoading(false);
    }
  };

  return (
    <Card
      title="Redis access"
      extra={
        !creds && (
          <Button type="primary" loading={loading} onClick={reveal}>
            Show my Redis credentials
          </Button>
        )
      }
    >
      <Paragraph type="secondary" style={{ marginBottom: 16 }}>
        Redis on this server requires authentication. Use the credentials below
        to connect from your applications — for example{" "}
        <Text code>$redis-&gt;connect('/run/redis/redis.sock')</Text> then{" "}
        <Text code>$redis-&gt;auth([$username, $password])</Text>.
      </Paragraph>

      {creds && (
        <>
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label="Socket">
              <Text copyable code>
                {creds.socket}
              </Text>
            </Descriptions.Item>
            <Descriptions.Item label="TCP">
              <Text type="secondary">
                Not available — Redis listens on the unix socket only.
              </Text>
            </Descriptions.Item>
            <Descriptions.Item label="Username">
              <Text copyable code>
                {creds.username}
              </Text>
            </Descriptions.Item>
            <Descriptions.Item label="Password">
              <Text copyable code>
                {creds.password}
              </Text>
            </Descriptions.Item>
            <Descriptions.Item label="Database">{creds.database}</Descriptions.Item>
            <Descriptions.Item label="Required key prefix">
              <Text copyable code>
                {creds.key_prefix}
              </Text>
            </Descriptions.Item>
          </Descriptions>

          <Alert
            type="info"
            showIcon
            style={{ marginTop: 16 }}
            message="Set your client's key prefix"
            description={
              <>
                Your credential can only read and write keys under{" "}
                <Text code>{creds.key_prefix}</Text>. Set your Redis client's key
                prefix to that value — e.g. phpredis{" "}
                <Text code>
                  setOption(Redis::OPT_PREFIX, '{creds.key_prefix}')
                </Text>{" "}
                or Laravel <Text code>REDIS_PREFIX</Text>. Keyspace-scanning and
                admin commands (<Text code>KEYS</Text>, <Text code>SCAN</Text>,{" "}
                <Text code>FLUSHDB</Text>, <Text code>CONFIG</Text>, …) are not
                permitted.
              </>
            }
          />
        </>
      )}
    </Card>
  );
};
