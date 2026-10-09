// Tenant Redis access card (GH #1016). Jabali locks Redis behind ACL auth, so a
// migrated app that connected unauthenticated now gets "NOAUTH Authentication
// required". This card hands the tenant a scoped, ready-to-use credential.
//
// Reveal-on-click: GET /me/redis-access provisions the tenant's ACL user
// (idempotent) and returns the credential — so we don't fetch a secret until
// the user asks for it.
import { useState } from "react";
import { Card, Button, Descriptions, Typography, Alert, Popconfirm } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { apiClient } from "../../../apiClient";

const { Text, Paragraph } = Typography;

interface RedisFlushResult {
  deleted: number;
  complete: boolean;
}

// GH #2003: what the flush toast says. A flush that hit its time limit
// (complete=false) asks for another run.
export function redisFlushMessage(r: RedisFlushResult): string {
  const n = `${r.deleted} key${r.deleted === 1 ? "" : "s"}`;
  return r.complete ? `Deleted ${n}.` : `Deleted ${n} so far. Click Flush again to delete the rest.`;
}

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
  const [flushing, setFlushing] = useState(false);

  // GH #2003: the tenant's Redis user can't run FLUSHALL/FLUSHDB. The panel
  // deletes the keys under the tenant's prefix instead.
  const flush = async () => {
    setFlushing(true);
    try {
      const resp = await apiClient.post<RedisFlushResult>("/me/redis-access/flush");
      feedback.message.success(redisFlushMessage(resp.data));
    } catch {
      feedback.message.error("Could not flush your Redis keys.");
    } finally {
      setFlushing(false);
    }
  };

  const reveal = async () => {
    setLoading(true);
    try {
      const resp = await apiClient.get<RedisAccess>("/me/redis-access");
      setCreds(resp.data);
    } catch {
      feedback.message.error("Could not load your Redis credentials.");
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

      <Typography.Title level={5} style={{ marginTop: 24 }}>
        Flush your keys
      </Typography.Title>
      <Paragraph type="secondary">
        <Text code>FLUSHALL</Text> and <Text code>FLUSHDB</Text> would delete every
        user&apos;s keys, so your Redis user can&apos;t run them. Flush deletes only
        the keys under your key prefix, in every database. An app can do the same
        with <Text code>POST /api/v1/me/redis-access/flush</Text> and an API token
        that has the Redis flush permission.
      </Paragraph>
      <Popconfirm
        title="Delete all your Redis keys?"
        description="Deletes every key under your key prefix. Other users' keys are not touched."
        okText="Flush"
        okButtonProps={{ danger: true }}
        onConfirm={flush}
      >
        <Button danger loading={flushing}>
          Flush my Redis keys
        </Button>
      </Popconfirm>
    </Card>
  );
};
