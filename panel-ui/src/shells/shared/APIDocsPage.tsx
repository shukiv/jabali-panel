// API Docs page — renders /api/v1/_meta/openapi.json with a minimal,
// theme-aware AntD layout. Mounted by both admin + tenant shells so
// each audience can browse the same canonical spec. Per-endpoint
// authentication requirements are surfaced from the spec's
// `security` definitions.
//
// Why not Redoc / Swagger UI: each is a 1-2 MB add to the bundle for
// a docs page nobody loads in production traffic, and styling them
// to match Jabali's AntD + dark-mode design would be more work than
// rendering the JSON ourselves.

import { useEffect, useMemo, useState } from "react";
import {
  Alert,
  Anchor,
  Card,
  Collapse,
  Layout,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
  Grid,
  notification,
} from "antd";
import { LinkOutlined, DownloadOutlined, LockOutlined } from "@ant-design/icons";
import { apiClient } from "../../apiClient";
import { MiniMarkdown } from "../../components/MiniMarkdown";

// Minimal OpenAPI 3 shape — we only consume what we render.
interface OpenAPISpec {
  info: { title: string; version: string; description?: string };
  servers?: { url: string; description?: string }[];
  tags?: { name: string; description?: string }[];
  paths: Record<string, Record<string, OpenAPIOperation>>;
  components?: { securitySchemes?: Record<string, { type: string; description?: string }> };
}

interface OpenAPIOperation {
  tags?: string[];
  summary?: string;
  description?: string;
  parameters?: OpenAPIParam[];
  requestBody?: { required?: boolean; content?: Record<string, { schema?: unknown }> };
  responses?: Record<string, { description?: string; content?: Record<string, { schema?: unknown }> }>;
  security?: Record<string, string[]>[];
}

interface OpenAPIParam {
  in: string;
  name: string;
  required?: boolean;
  schema?: { type?: string; example?: unknown; enum?: string[] };
  description?: string;
}

const METHODS = ["get", "post", "put", "patch", "delete"] as const;

// methodColor — match the conventions ops engineers expect from
// Postman / Swagger so the visual language is familiar.
function methodColor(m: string): string {
  switch (m.toLowerCase()) {
    case "get": return "blue";
    case "post": return "green";
    case "put": return "orange";
    case "patch": return "gold";
    case "delete": return "red";
    default: return "default";
  }
}

// slugify produces a stable anchor id for a given (method, path) pair.
function slugify(method: string, path: string): string {
  return `${method}-${path.replace(/[^a-zA-Z0-9]+/g, "-").replace(/^-|-$/g, "")}`;
}

export function APIDocsPage(): JSX.Element {
  const [spec, setSpec] = useState<OpenAPISpec | null>(null);
  const [loading, setLoading] = useState(true);
  const screens = Grid.useBreakpoint();
  const showSider = !!screens.lg;

  useEffect(() => {
    (async () => {
      try {
        const resp = await apiClient.get<OpenAPISpec>("/_meta/openapi.json");
        setSpec(resp.data);
      } catch (err) {
        const e = err as { message?: string };
        notification.error({
          message: "Failed to load API documentation",
          description: e.message ?? "Unknown error",
        });
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  // Group endpoints by tag. An endpoint with no tag falls into
  // "Other" so it still appears (good failsafe while the spec grows).
  const grouped = useMemo(() => {
    if (!spec) return new Map<string, { method: string; path: string; op: OpenAPIOperation }[]>();
    const out = new Map<string, { method: string; path: string; op: OpenAPIOperation }[]>();
    for (const [path, methods] of Object.entries(spec.paths)) {
      for (const method of METHODS) {
        const op = methods[method];
        if (!op) continue;
        const tags = op.tags?.length ? op.tags : ["Other"];
        for (const tag of tags) {
          if (!out.has(tag)) out.set(tag, []);
          out.get(tag)!.push({ method, path, op });
        }
      }
    }
    return out;
  }, [spec]);

  // tagDescriptions: lookup the spec's tag definitions so the section
  // header carries the operator-facing prose, not just the bare name.
  const tagDesc = useMemo(() => {
    const m = new Map<string, string>();
    for (const t of spec?.tags ?? []) {
      if (t.description) m.set(t.name, t.description);
    }
    return m;
  }, [spec]);

  if (loading) {
    return (
      <Layout style={{ padding: 24, minHeight: "60vh" }}>
        <Spin />
      </Layout>
    );
  }
  if (!spec) {
    return (
      <Alert
        type="error"
        message="Could not load /_meta/openapi.json"
        description="Server may be on an older version that doesn't ship this endpoint yet."
      />
    );
  }

  return (
    <Layout style={{ background: "transparent" }}>
      {showSider && (
        <Layout.Sider
          width={240}
          theme="light"
          style={{ background: "transparent", paddingTop: 8 }}
        >
          <Anchor
            affix
            targetOffset={64}
            items={Array.from(grouped.keys()).map((tag) => ({
              key: tag,
              href: `#tag-${slugify("", tag)}`,
              title: tag,
            }))}
          />
        </Layout.Sider>
      )}

      <Layout.Content style={{ padding: "0 16px 32px", minWidth: 0 }}>
        <Card style={{ marginBottom: 16 }}>
          <Typography.Title level={3} style={{ marginTop: 0 }}>
            {spec.info.title}{" "}
            <Tag color="blue">{spec.info.version}</Tag>
          </Typography.Title>
          {spec.info.description && <MiniMarkdown text={spec.info.description} />}
          <Space size="middle" wrap>
            <Typography.Link href="/api/v1/_meta/openapi.yaml" target="_blank">
              <DownloadOutlined /> Raw OpenAPI YAML
            </Typography.Link>
            <Typography.Link href="/api/v1/_meta/openapi.json" target="_blank">
              <DownloadOutlined /> Raw OpenAPI JSON
            </Typography.Link>
          </Space>
        </Card>

        {Array.from(grouped.entries()).map(([tag, ops]) => (
          <Card
            key={tag}
            id={`tag-${slugify("", tag)}`}
            title={tag}
            style={{ marginBottom: 16 }}
          >
            {tagDesc.has(tag) && (
              <div style={{ marginTop: 0 }}>
                <MiniMarkdown text={tagDesc.get(tag) ?? ""} />
              </div>
            )}
            <Collapse
              accordion={false}
              items={ops.map(({ method, path, op }) => ({
                key: slugify(method, path),
                label: (
                  <Space size="small" wrap style={{ rowGap: 4 }}>
                    <Tag color={methodColor(method)} style={{ minWidth: 64, textAlign: "center" }}>
                      {method.toUpperCase()}
                    </Tag>
                    <Typography.Text code style={{ background: "transparent", wordBreak: "break-all" }}>
                      {path}
                    </Typography.Text>
                    {op.summary && (
                      <Typography.Text type="secondary">— {op.summary}</Typography.Text>
                    )}
                    {(op.security?.length ?? 0) > 0 && (
                      <Tooltip
                        title={(op.security ?? [])
                          .map((s) => Object.keys(s).join(" + "))
                          .join(" OR ")}
                      >
                        <LockOutlined style={{ color: "#999" }} />
                      </Tooltip>
                    )}
                  </Space>
                ),
                children: <OperationDetail op={op} method={method} path={path} />,
              }))}
            />
          </Card>
        ))}
      </Layout.Content>
    </Layout>
  );
}

function OperationDetail(props: {
  op: OpenAPIOperation;
  method: string;
  path: string;
}): JSX.Element {
  const { op, method, path } = props;
  return (
    <Space direction="vertical" size="middle" style={{ display: "flex" }}>
      {op.description && <MiniMarkdown text={op.description} />}

      {op.parameters && op.parameters.length > 0 && (
        <div>
          <Typography.Text strong>Parameters</Typography.Text>
          <ul style={{ marginTop: 4 }}>
            {op.parameters.map((p) => (
              <li key={`${p.in}-${p.name}`}>
                <Typography.Text code>{p.name}</Typography.Text>
                <Tag style={{ marginLeft: 8 }}>{p.in}</Tag>
                {p.required && <Tag color="red">required</Tag>}
                {p.schema?.type && <Tag>{p.schema.type}</Tag>}
                {p.description && (
                  <Typography.Text type="secondary"> — {p.description}</Typography.Text>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      {op.requestBody && (
        <div>
          <Typography.Text strong>
            Request body {op.requestBody.required && <Tag color="red">required</Tag>}
          </Typography.Text>
          <SchemaBlock content={op.requestBody.content} />
        </div>
      )}

      <div>
        <Typography.Text strong>Responses</Typography.Text>
        <ul style={{ marginTop: 4 }}>
          {Object.entries(op.responses ?? {}).map(([code, r]) => (
            <li key={code}>
              <Tag color={code.startsWith("2") ? "green" : code.startsWith("4") ? "orange" : "red"}>
                {code}
              </Tag>
              {r.description}
              <SchemaBlock content={r.content} />
            </li>
          ))}
        </ul>
      </div>

      <ExampleCurl method={method} path={path} op={op} />
    </Space>
  );
}

function SchemaBlock(props: { content?: Record<string, { schema?: unknown }> }): JSX.Element | null {
  const { content } = props;
  if (!content) return null;
  const json = content["application/json"]?.schema;
  if (!json) return null;
  return (
    <pre
      style={{
        background: "rgba(0,0,0,0.04)",
        padding: 8,
        borderRadius: 4,
        marginTop: 4,
        fontSize: 12,
        maxHeight: 240,
        overflow: "auto",
      }}
    >
      {JSON.stringify(json, null, 2)}
    </pre>
  );
}

function ExampleCurl(props: {
  method: string;
  path: string;
  op: OpenAPIOperation;
}): JSX.Element {
  const { method, path } = props;
  const m = method.toUpperCase();
  const isDDNS = path.startsWith("/nic/update");
  const cmd = isDDNS
    ? `curl -u anything:jat_REPLACE_ME 'https://panel.example.com:8443${path}?hostname=vpn.example.com&myip=203.0.113.5'`
    : `curl -H 'Authorization: Bearer jat_REPLACE_ME' \\
     -X ${m} 'https://panel.example.com:8443/api/v1${path}'`;
  return (
    <div>
      <Typography.Text strong>Example</Typography.Text>{" "}
      <Tooltip title="Replace jat_REPLACE_ME with your token from the Personal API Tokens page. Replace panel.example.com with your panel hostname.">
        <LinkOutlined style={{ color: "#999" }} />
      </Tooltip>
      <pre
        style={{
          background: "rgba(0,0,0,0.04)",
          padding: 8,
          borderRadius: 4,
          marginTop: 4,
          fontSize: 12,
          overflow: "auto",
        }}
      >
        {cmd}
      </pre>
    </div>
  );
}
