// MCPSetupGuide — a companion to the API Tokens page that turns a token into a
// working jabali-mcp setup: it lets the tenant assemble one or more Jabali
// instances (this panel is pre-filled), generates the panels.json for the
// jabali-mcp server, and shows the one-line registration for the major MCP
// clients. No new backend — it reuses the token minted on the Tokens tab.
//
// Security: tokens entered here are assembled into the config IN THE BROWSER and
// are never sent to or stored by the panel. The generated config is a secret —
// treat the copied text / downloaded file like a password.
import { useMemo, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Input,
  Segmented,
  Space,
  Typography,
} from "antd";
import {
  ApiOutlined,
  CloudServerOutlined,
  DeleteOutlined,
  DownloadOutlined,
  PlusOutlined,
} from "@icons";

const { Paragraph, Text, Link } = Typography;

interface Instance {
  name: string;
  url: string;
  token: string;
}

const DEFAULT_PATH = "~/.config/jabali-mcp/panels.json";

const CodeBlock = ({ text }: { text: string }): JSX.Element => (
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

type ClientKey =
  | "claude-code"
  | "claude-desktop"
  | "codex"
  | "opencode"
  | "kimi";

type AccessMode = "read" | "write";

// clientSnippet returns the registration for a client that reads the default
// panels.json (so no env is needed for config). If access is "write", the
// snippet also sets JABALI_MCP_ALLOW_WRITE=1 to enable mutating tools.
function clientSnippet(client: ClientKey, access: AccessMode): string {
  const write = access === "write";
  switch (client) {
    case "claude-code":
      return `claude mcp add jabali${write ? " --env JABALI_MCP_ALLOW_WRITE=1" : ""} -- jabali-mcp`;
    case "claude-desktop":
      return [
        "// claude_desktop_config.json",
        "{",
        '  "mcpServers": {',
        write
          ? '    "jabali": { "command": "jabali-mcp", "env": { "JABALI_MCP_ALLOW_WRITE": "1" } }'
          : '    "jabali": { "command": "jabali-mcp" }',
        "  }",
        "}",
      ].join("\n");
    case "codex":
      return [
        "# ~/.codex/config.toml",
        "[mcp_servers.jabali]",
        'command = "jabali-mcp"',
        ...(write ? ["", "[mcp_servers.jabali.env]", 'JABALI_MCP_ALLOW_WRITE = "1"'] : []),
      ].join("\n");
    case "opencode":
      return [
        "// opencode.json",
        "{",
        '  "mcp": {',
        write
          ? '    "jabali": { "type": "local", "command": ["jabali-mcp"], "environment": { "JABALI_MCP_ALLOW_WRITE": "1" }, "enabled": true }'
          : '    "jabali": { "type": "local", "command": ["jabali-mcp"], "enabled": true }',
        "  }",
        "}",
      ].join("\n");
    case "kimi":
      return `kimi mcp add --transport stdio jabali${write ? " -e JABALI_MCP_ALLOW_WRITE=1" : ""} -- jabali-mcp`;
  }
}

export function MCPSetupGuide(): JSX.Element {
  const origin =
    typeof window !== "undefined" ? window.location.origin : "https://<panel-host>";
  const [instances, setInstances] = useState<Instance[]>([
    { name: "default", url: `${origin}/api/v1`, token: "" },
  ]);
  const [client, setClient] = useState<ClientKey>("claude-code");
  const [access, setAccess] = useState<AccessMode>("read");

  const panelsJson = useMemo(
    () =>
      JSON.stringify(
        instances.map((i) => ({
          name: i.name.trim() || "default",
          url: i.url.trim(),
          token: i.token.trim(),
        })),
        null,
        2,
      ) + "\n",
    [instances],
  );

  const patchAt = (idx: number, patch: Partial<Instance>): void =>
    setInstances((prev) => prev.map((it, i) => (i === idx ? { ...it, ...patch } : it)));
  const addRow = (): void =>
    setInstances((prev) => [
      ...prev,
      { name: `panel-${prev.length + 1}`, url: "", token: "" },
    ]);
  const removeRow = (idx: number): void =>
    setInstances((prev) => prev.filter((_, i) => i !== idx));

  const download = (): void => {
    const blob = new Blob([panelsJson], { type: "application/json" });
    const href = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = href;
    a.download = "panels.json";
    a.click();
    URL.revokeObjectURL(href);
  };

  const ready = instances.every((i) => i.url.trim() && i.token.trim());

  return (
    <Card
      title="Use this panel from an AI assistant (MCP)"
      size="small"
      style={{ marginTop: 24 }}
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Alert
          type="info"
          showIcon
          icon={<CloudServerOutlined />}
          message="jabali-mcp exposes this panel's API as tools an AI assistant can use."
          description={
            <>
              It authenticates with an API token and inherits your account's
              isolation — it can only touch resources you own. It is{" "}
              <Text strong>read-only by default</Text>; mutating tools are opt-in
              and destructive ones ask for confirmation. Install it from{" "}
              <Link href="https://github.com/shukiv/jabali-mcp" target="_blank" rel="noreferrer">
                github.com/shukiv/jabali-mcp
              </Link>
              .
            </>
          }
        />

        <div>
          <Text strong>1. Get a token</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            Create one on the <Text strong>Tokens</Text> tab (a read-only token is
            safest — Custom → the read permissions you want), then paste it below.
          </Paragraph>
        </div>

        <div>
          <Text strong>2. Your instances</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            This panel is pre-filled. Add more Jabali panels to manage a fleet from
            one place — each needs its own token from that panel.
          </Paragraph>
          <Space direction="vertical" size="small" style={{ width: "100%" }}>
            {instances.map((it, idx) => (
              <Space.Compact key={idx} style={{ width: "100%" }} block>
                <Input
                  style={{ width: 150 }}
                  placeholder="name"
                  value={it.name}
                  onChange={(e) => patchAt(idx, { name: e.target.value })}
                />
                <Input
                  placeholder="https://panel:8443/api/v1"
                  value={it.url}
                  onChange={(e) => patchAt(idx, { url: e.target.value })}
                />
                <Input.Password
                  placeholder="jat_… token"
                  value={it.token}
                  onChange={(e) => patchAt(idx, { token: e.target.value })}
                />
                <Button
                  icon={<DeleteOutlined />}
                  danger
                  disabled={instances.length === 1}
                  onClick={() => removeRow(idx)}
                  aria-label="Remove instance"
                />
              </Space.Compact>
            ))}
            <Button icon={<PlusOutlined />} onClick={addRow}>
              Add instance
            </Button>
          </Space>
        </div>

        <div>
          <Text strong>3. Save the config</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            Save this as <Text code>{DEFAULT_PATH}</Text> (mode 600) on the machine
            that runs the assistant. jabali-mcp finds it there automatically.
          </Paragraph>
          {!ready && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 8 }}
              message="Fill in a URL and token for every instance to complete the config."
            />
          )}
          <CodeBlock text={panelsJson} />
          <Button
            icon={<DownloadOutlined />}
            onClick={download}
            style={{ marginTop: 8 }}
          >
            Download panels.json
          </Button>
        </div>

        <div>
          <Text strong>4. Register with your client</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            Because the config is at the default path, no environment variables are
            needed for it. (Saved elsewhere? Add{" "}
            <Text code>--env JABALI_PANELS_FILE=/your/path</Text>.)
          </Paragraph>

          <div style={{ marginBottom: 8 }}>
            <Text type="secondary" style={{ marginRight: 8 }}>
              Access:
            </Text>
            <Segmented<AccessMode>
              value={access}
              onChange={(v) => setAccess(v)}
              options={[
                { label: "Read-only", value: "read" },
                { label: "Read-write", value: "write" },
              ]}
            />
          </div>
          {access === "write" ? (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 8 }}
              message="Read-write enables mutating tools (create/update)."
              description={
                <>
                  Destructive tools (delete, restore, password) still require an
                  explicit <Text code>confirm: true</Text> per call. To preview
                  every write before it runs, also add{" "}
                  <Text code>JABALI_MCP_DRY_RUN=1</Text>.
                </>
              }
            />
          ) : (
            <Paragraph type="secondary" style={{ marginBottom: 8 }}>
              Read-only exposes only list/get tools — the assistant can see your
              account but change nothing.
            </Paragraph>
          )}

          <Segmented<ClientKey>
            value={client}
            onChange={(v) => setClient(v)}
            style={{ marginBottom: 8 }}
            options={[
              { label: "Claude Code", value: "claude-code" },
              { label: "Claude Desktop", value: "claude-desktop" },
              { label: "Codex", value: "codex" },
              { label: "OpenCode", value: "opencode" },
              { label: "Kimi", value: "kimi" },
            ]}
          />
          <CodeBlock text={clientSnippet(client, access)} />
        </div>

        <Alert
          type="warning"
          showIcon
          icon={<ApiOutlined />}
          message="The generated config contains your tokens."
          description="It is assembled here in your browser and never sent to the panel. Treat the copied text and the downloaded file like a password."
        />
      </Space>
    </Card>
  );
}
