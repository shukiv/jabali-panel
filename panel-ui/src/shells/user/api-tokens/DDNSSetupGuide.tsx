// DDNSSetupGuide (Gitea #587) — a guided companion to the API Tokens page that
// turns a DDNS-scoped token into a working dynamic-DNS setup: it explains the
// token-as-Basic-auth-password model, builds/copies the /nic/update URL + a
// ddclient config for a chosen hostname, documents the DynDNS response strings,
// and runs an optional live test update. No new backend — it drives the
// existing GET /nic/update shim (api/ddns.go).
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Input,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import { KeyOutlined, ThunderboltOutlined } from "@icons";

const { Paragraph, Text } = Typography;

// DynDNS protocol response strings the /nic/update shim returns (api/ddns.go).
const RESPONSES: { code: string; color: string; meaning: string }[] = [
  { code: "good <ip>", color: "green", meaning: "Update succeeded — the record now points at <ip>." },
  { code: "nochg <ip>", color: "blue", meaning: "The record already pointed at that IP; nothing changed (still a success)." },
  { code: "badauth", color: "red", meaning: "Authorization missing or the token is invalid/lacks the ddns (or write:dns) scope. Check the password field." },
  { code: "nohost", color: "orange", meaning: "The hostname doesn't resolve to an A/AAAA record this token is allowed to update. Pick an existing record (or widen the token's record: scope)." },
  { code: "notfqdn", color: "orange", meaning: "The hostname isn't a valid fully-qualified domain name." },
  { code: "911", color: "volcano", meaning: "Server-side error — back off and retry later, don't hammer it." },
];

const CodeBlock = ({ text }: { text: string }) => (
  <Paragraph
    copyable={{ text }}
    style={{
      background: "var(--ant-color-fill-tertiary, #f5f5f5)",
      padding: "8px 12px",
      borderRadius: 6,
      fontFamily: "monospace",
      whiteSpace: "pre-wrap",
      marginBottom: 0,
    }}
  >
    {text}
  </Paragraph>
);

export function DDNSSetupGuide(): JSX.Element {
  const { t } = useTranslation();
  const [hostname, setHostname] = useState("");
  const [token, setToken] = useState("");
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<string | null>(null);

  const origin = typeof window !== "undefined" ? window.location.origin : "https://<panel-host>";
  const host = hostname.trim() || "vpn.example.com";

  const updateURL = useMemo(() => `${origin}/nic/update?hostname=${encodeURIComponent(host)}`, [origin, host]);

  const ddclientConf = useMemo(
    () =>
      [
        "# /etc/ddclient/ddclient.conf",
        "protocol=dyndns2",
        "use=web                       # let ddclient detect the public IP",
        `server=${origin.replace(/^https?:\/\//, "")}`,
        "ssl=yes",
        "login=jabali                  # username is IGNORED by the shim",
        "password='YOUR_DDNS_TOKEN'    # <- paste your ddns-scoped API token here",
        host,
      ].join("\n"),
    [origin, host],
  );

  const runTest = async () => {
    if (!token.trim()) {
      message.warning("Paste a DDNS-scoped token to test");
      return;
    }
    setTesting(true);
    setResult(null);
    try {
      // Username is ignored; the token is the Basic-auth password.
      const auth = btoa(`jabali:${token.trim()}`);
      const resp = await fetch(`/nic/update?hostname=${encodeURIComponent(host)}`, {
        headers: { Authorization: `Basic ${auth}` },
      });
      const body = (await resp.text()).trim();
      setResult(body || `(empty, HTTP ${resp.status})`);
    } catch (e) {
      setResult(`request failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setTesting(false);
    }
  };

  const resultTone = result
    ? result.startsWith("good") || result.startsWith("nochg")
      ? "success"
      : "error"
    : undefined;

  return (
    <Card title={t("ddnssetupguide.dynamic_dns_ddns_setup")} size="small" style={{ marginTop: 24 }}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Alert
          type="info"
          showIcon
          icon={<KeyOutlined />}
          message={t("ddnssetupguide.your_api_token_is_the_ddns_password")}
          description={t("ddnssetupguide.point_your_router_or_ddclient_at_the_update")}
        />

        <div>
          <Text strong>Hostname (an existing A/AAAA record you own)</Text>
          <Input
            placeholder="vpn.example.com"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            style={{ marginTop: 6, maxWidth: 360 }}
          />
        </div>

        <div>
          <Text strong>Update URL</Text>
          <CodeBlock text={updateURL} />
          <Text type="secondary">
            Your router's DDNS fields: service/server = this host, username = anything, password = your token,
            hostname = {host}.
          </Text>
        </div>

        <div>
          <Text strong>ddclient config</Text>
          <CodeBlock text={ddclientConf} />
        </div>

        <div>
          <Text strong>Test an update</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            Paste a ddns-scoped token to fire a real update against {host} and see the DynDNS response.
          </Paragraph>
          <Space.Compact style={{ width: "100%", maxWidth: 520 }}>
            <Input.Password
              placeholder="ddns-scoped API token"
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
            <Button type="primary" icon={<ThunderboltOutlined />} loading={testing} onClick={runTest}>
              Test
            </Button>
          </Space.Compact>
          {result && (
            <Alert
              style={{ marginTop: 10 }}
              type={resultTone}
              message={<Text code>{result}</Text>}
              showIcon
            />
          )}
        </div>

        <div>
          <Text strong>Response codes</Text>
          <Table
            size="small"
            style={{ marginTop: 6 }}
            pagination={false}
            rowKey="code"
            dataSource={RESPONSES}
            columns={[
              {
                title: "Response",
                dataIndex: "code",
                width: 130,
                render: (c: string, r) => <Tag color={r.color}>{c}</Tag>,
              },
              { title: "Meaning", dataIndex: "meaning" },
            ]}
          />
        </div>
      </Space>
    </Card>
  );
}
