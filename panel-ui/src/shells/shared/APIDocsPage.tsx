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
//
// GH #2244: ~90 sections and ~770 endpoints, so the page shows one section
// at a time, picked from a list (a rail on wide screens, a searchable select
// on narrow ones), with a search across every endpoint — the layout of
// Notifications > Events. The list used to be an antd Anchor, which scrolls
// the window: in the admin shell the content column scrolls instead, so
// picking a section did nothing there. Like Events, it all sits in one card
// under the page's heading: the figures and the search in a strip, the rail
// and the section below, and the long introduction folded to its title.

import { useEffect, useMemo, useRef, useState } from "react";
import { Alert, Button, Card, Collapse, Empty, Input, Select, Space, Spin, Tag, Tooltip, Typography, Grid, theme } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { ApiOutlined, LinkOutlined, DownloadOutlined, LockOutlined, SearchOutlined } from "@icons";
import { apiClient } from "../../apiClient";
import { CollapsibleAlert } from "../../components/CollapsibleAlert";
import { MiniMarkdown } from "../../components/MiniMarkdown";
import { SummaryStat } from "../../components/SummaryStat";

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

interface Endpoint {
  method: string;
  path: string;
  op: OpenAPIOperation;
}

// Section — one tag of the spec and its endpoints.
interface Section {
  tag: string;
  slug: string;
  ops: Endpoint[];
}

function tagSlug(tag: string): string {
  return tag.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

// The selected section lives in the address (#tag-<slug>) so a section can be
// linked to, and a reload stays on it.
const HASH_PREFIX = "#tag-";

function slugFromHash(): string | null {
  const h = window.location.hash;
  return h.startsWith(HASH_PREFIX) ? decodeURIComponent(h.slice(HASH_PREFIX.length)) : null;
}

// buildSections groups the endpoints by tag, in alphabetical order so ~90
// sections are easy to scan (the spec declares its tags in no useful order),
// with "Other" last for an endpoint with no tag (a failsafe while the spec
// grows).
function buildSections(spec: OpenAPISpec): Section[] {
  const byTag = new Map<string, Endpoint[]>();
  for (const [path, methods] of Object.entries(spec.paths)) {
    for (const method of METHODS) {
      const op = methods[method];
      if (!op) continue;
      for (const tag of op.tags?.length ? op.tags : ["Other"]) {
        if (!byTag.has(tag)) byTag.set(tag, []);
        byTag.get(tag)!.push({ method, path, op });
      }
    }
  }
  return Array.from(byTag.entries())
    .sort(([a], [b]) =>
      a === "Other" ? 1 : b === "Other" ? -1 : a.localeCompare(b, undefined, { sensitivity: "base" }),
    )
    .map(([tag, ops]) => ({ tag, slug: tagSlug(tag), ops }));
}

// scrollParent — the nearest ancestor that scrolls, or null for the window.
// The tenant shell scrolls the window; the admin shell its content column.
function scrollParent(el: HTMLElement): HTMLElement | null {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const oy = getComputedStyle(p).overflowY;
    if ((oy === "auto" || oy === "scroll") && p.scrollHeight > p.clientHeight) return p;
  }
  return null;
}

// matchesSearch: every word of the search appears in the endpoint's method,
// path, summary or section name.
function matchesSearch(e: Endpoint, tag: string, words: string[]): boolean {
  const hay = `${e.method} ${e.path} ${e.op.summary ?? ""} ${tag}`.toLowerCase();
  return words.every((w) => hay.includes(w));
}

// APIDocsPage — the /api-docs page of either shell: its heading, then the docs
// in one card. The Personal API Tokens page shows the same docs in its
// "Automation API" tab.
export function APIDocsPage(): JSX.Element {
  return (
    <div>
      <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <ApiOutlined /> API Docs
      </Typography.Title>
      <Card>
        <APIDocsBrowser />
      </Card>
    </div>
  );
}

// APIDocsBrowser — the docs themselves, for the body of a card: the API's
// introduction (folded to its title), a strip with the figures on the left and
// the search on the right, then the rail of sections beside the selected one.
export function APIDocsBrowser(): JSX.Element {
  const [spec, setSpec] = useState<OpenAPISpec | null>(null);
  const [loading, setLoading] = useState(true);
  const screens = Grid.useBreakpoint();
  const showSider = !!screens.lg;
  const { token } = theme.useToken();
  const [selSlug, setSelSlug] = useState<string | null>(() => slugFromHash());
  const [query, setQuery] = useState("");
  const [railQuery, setRailQuery] = useState("");
  const detailRef = useRef<HTMLElement>(null);
  const railListRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    (async () => {
      try {
        const resp = await apiClient.get<OpenAPISpec>("/_meta/openapi.json");
        setSpec(resp.data);
      } catch (err) {
        const e = err as { message?: string };
        feedback.message.error(`Failed to load API documentation: ${e.message ?? "Unknown error"}`);
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  const sections = useMemo(() => (spec ? buildSections(spec) : []), [spec]);

  // An endpoint with two tags sits in two sections; count it once.
  const totalEndpoints = useMemo(
    () => new Set(sections.flatMap((s) => s.ops.map((e) => `${e.method} ${e.path}`))).size,
    [sections],
  );

  const searching = query.trim() !== "";
  const results = useMemo(() => {
    const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return sections
      .map((s) => ({ section: s, ops: s.ops.filter((e) => matchesSearch(e, s.tag, words)) }))
      .filter((r) => r.ops.length > 0);
  }, [sections, query]);
  const matchCount = new Set(results.flatMap((r) => r.ops.map((e) => `${e.method} ${e.path}`))).size;

  // The section the address names, else the first one.
  const active = sections.find((s) => s.slug === selSlug) ?? sections[0] ?? null;

  const railSections = sections.filter((s) =>
    s.tag.toLowerCase().includes(railQuery.trim().toLowerCase()),
  );

  const select = (slug: string) => {
    setSelSlug(slug);
    setQuery("");
    // Keep the router's history entry; only the hash changes.
    window.history.replaceState(
      window.history.state,
      "",
      `${window.location.pathname}${window.location.search}${HASH_PREFIX}${slug}`,
    );
    // A reader deep in a long section who picks another lands at its top,
    // whichever element scrolls this shell.
    requestAnimationFrame(() => {
      const el = detailRef.current;
      if (!el) return;
      const parent = scrollParent(el);
      const visibleTop = parent ? parent.getBoundingClientRect().top : 0;
      if (el.getBoundingClientRect().top < visibleTop - 1) el.scrollIntoView?.({ block: "start" });
    });
  };

  // The selected section shows in the rail when the page opens on it (a
  // #tag- link) or a search result opens it: ~90 sections run past the bottom
  // of the screen, so the rail scrolls itself to it. The page does not move.
  useEffect(() => {
    const list = railListRef.current;
    const item = list?.querySelector<HTMLElement>('[aria-current="true"]');
    if (!list || !item) return;
    const parent = scrollParent(list);
    const listBox = list.getBoundingClientRect();
    const bottom = Math.min(listBox.bottom, parent ? parent.getBoundingClientRect().bottom : window.innerHeight);
    const itemBox = item.getBoundingClientRect();
    // 8px clear of the edge, so it does not read as cut off.
    if (itemBox.top < listBox.top) list.scrollTop -= listBox.top - itemBox.top;
    else if (itemBox.bottom > bottom) list.scrollTop += itemBox.bottom - bottom + 8;
  }, [active?.slug, searching, showSider]);

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
      <div style={{ padding: 48, textAlign: "center" }}>
        <Spin />
      </div>
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

  // The rail, the strip and the detail header follow EventsTab's styling so
  // the two pages read as one design.
  const cardBorder = `1px solid ${token.colorBorderSecondary}`;
  const countStyle = {
    color: token.colorTextSecondary,
    fontSize: 12,
    fontVariantNumeric: "tabular-nums" as const,
  };

  const rail = (
    <nav
      aria-label="API sections"
      style={{
        // Sticks while the section beside it scrolls, below the "Viewing as"
        // banner when an admin views a tenant's panel (GH #2243). Its own
        // scroll keeps all ~90 sections reachable.
        position: "sticky",
        top: "var(--imp-banner-h, 0px)",
        maxHeight: "calc(100vh - 96px)",
        display: "flex",
        flexDirection: "column",
        gap: 2,
        borderRight: cardBorder,
        paddingRight: 12,
      }}
    >
      <Typography.Text
        type="secondary"
        style={{
          fontSize: 11,
          fontWeight: 600,
          letterSpacing: ".08em",
          textTransform: "uppercase",
          padding: "6px 10px 4px",
        }}
      >
        Sections
      </Typography.Text>
      <Input
        size="small"
        allowClear
        prefix={<SearchOutlined />}
        placeholder="Filter sections"
        aria-label="Filter sections"
        value={railQuery}
        onChange={(e) => setRailQuery(e.target.value)}
        style={{ marginBottom: 6 }}
      />
      <div
        ref={railListRef}
        style={{ overflowY: "auto", minHeight: 0, display: "flex", flexDirection: "column", gap: 2 }}
      >
        {railSections.map((s) => {
          const selected = !searching && s.slug === active?.slug;
          return (
            <button
              key={s.slug}
              type="button"
              aria-current={selected ? "true" : undefined}
              onClick={() => select(s.slug)}
              style={{
                display: "flex",
                alignItems: "center",
                gap: 10,
                width: "100%",
                cursor: "pointer",
                textAlign: "left",
                border: "1px solid transparent",
                borderRadius: 8,
                padding: "9px 11px",
                background: selected ? token.colorPrimaryBg : "transparent",
                boxShadow: selected ? `inset 3px 0 0 0 ${token.colorError}` : undefined,
                color: token.colorText,
              }}
            >
              <span style={{ flex: 1, fontSize: 13.5, fontWeight: selected ? 600 : 500 }}>{s.tag}</span>
              <span
                style={{
                  fontSize: 11.5,
                  fontVariantNumeric: "tabular-nums",
                  color: token.colorTextSecondary,
                  background: token.colorBgContainer,
                  border: cardBorder,
                  borderRadius: 999,
                  padding: "1px 8px",
                }}
              >
                {s.ops.length}
              </span>
            </button>
          );
        })}
        {railSections.length === 0 && (
          <Typography.Text type="secondary" style={{ padding: "6px 10px", display: "block" }}>
            No section matches
          </Typography.Text>
        )}
      </div>
    </nav>
  );

  const detailHeader = (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 12,
        flexWrap: "wrap",
        paddingBottom: 12,
        borderBottom: `1px solid ${token.colorSplit}`,
      }}
    >
      <Typography.Title level={5} style={{ margin: 0 }}>
        {searching ? `Results for “${query.trim()}”` : active?.tag}
      </Typography.Title>
      <Typography.Text type="secondary" style={{ fontSize: 13 }}>
        {searching
          ? `${matchCount} of ${totalEndpoints} endpoints match`
          : active
            ? `${active.ops.length} ${active.ops.length === 1 ? "endpoint" : "endpoints"}`
            : ""}
      </Typography.Text>
    </div>
  );

  return (
    <div>
      <CollapsibleAlert
        storageKey="apidocs.intro"
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title={
          <>
            {spec.info.title} <Tag color="blue">{spec.info.version}</Tag>
          </>
        }
        description={spec.info.description ? <MiniMarkdown text={spec.info.description} /> : undefined}
        action={
          <Space size="middle" wrap>
            <Typography.Link href="/api/v1/_meta/openapi.yaml" target="_blank">
              <DownloadOutlined /> Raw OpenAPI YAML
            </Typography.Link>
            <Typography.Link href="/api/v1/_meta/openapi.json" target="_blank">
              <DownloadOutlined /> Raw OpenAPI JSON
            </Typography.Link>
          </Space>
        }
      />

      {/* summary strip: the figures beside the rail, the search above the detail */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          flexWrap: "wrap",
          paddingBottom: 16,
          borderBottom: cardBorder,
          marginBottom: 16,
        }}
      >
        <SummaryStat value={totalEndpoints} label="endpoints" />
        <SummaryStat value={sections.length} label="sections" />
        <div style={{ flex: 1 }} />
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder="Search all endpoints"
          aria-label="Search all endpoints"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={{ minWidth: 220, maxWidth: 320 }}
        />
      </div>

      {/* Events turns its rail into a row of chips on a narrow screen; ~90
          sections need a searchable select instead. */}
      {!showSider && (
        <Select
          showSearch
          aria-label="API section"
          optionFilterProp="label"
          value={searching ? undefined : active?.slug}
          placeholder="Go to a section"
          onChange={(slug: string) => select(slug)}
          options={sections.map((s) => ({ value: s.slug, label: `${s.tag} (${s.ops.length})` }))}
          style={{ width: "100%", marginBottom: 16 }}
        />
      )}

      {/* master–detail */}
      <div
        style={
          showSider
            ? { display: "grid", gridTemplateColumns: "262px 1fr", gap: 20, alignItems: "start" }
            : undefined
        }
      >
        {showSider && rail}
        <section ref={detailRef} style={{ minWidth: 0, scrollMarginTop: "var(--imp-banner-h, 0px)" }}>
          {detailHeader}
          {searching ? (
            results.length === 0 ? (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description="No endpoint matches your search"
                style={{ padding: "48px 0" }}
              />
            ) : (
              results.map(({ section, ops }) => (
                <div key={section.slug} style={{ marginTop: 16 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
                    <Typography.Text strong role="heading" aria-level={6}>
                      {section.tag}
                    </Typography.Text>
                    <span style={countStyle}>{ops.length}</span>
                    <Button
                      type="link"
                      size="small"
                      onClick={() => select(section.slug)}
                      style={{ marginLeft: "auto" }}
                    >
                      Open section
                    </Button>
                  </div>
                  <EndpointList ops={ops} />
                </div>
              ))
            )
          ) : (
            active && (
              <>
                {tagDesc.has(active.tag) && (
                  <div style={{ paddingTop: 12, color: token.colorTextSecondary }}>
                    <MiniMarkdown text={tagDesc.get(active.tag) ?? ""} />
                  </div>
                )}
                <div style={{ marginTop: 12 }}>
                  <EndpointList ops={active.ops} />
                </div>
              </>
            )
          )}
        </section>
      </div>
    </div>
  );
}

// EndpointList — one collapsible row per endpoint; a row opens to its
// parameters, request and response bodies, and an example curl.
function EndpointList(props: { ops: Endpoint[] }): JSX.Element {
  return (
    <Collapse
      accordion={false}
      items={props.ops.map(({ method, path, op }) => ({
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
