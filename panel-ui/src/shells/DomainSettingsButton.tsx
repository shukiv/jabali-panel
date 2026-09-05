// Shared settings modal for nginx custom directives used by both admin and user domain lists.
// Opens a modal with tabs: "Rule Builder" and "Raw Directives" (functional textarea).
// The Rule Builder tab allows building 6 types of typed nginx rules with drag-reorder.
import { useEffect, useState } from "react";
import {
  SettingOutlined,
  ToolOutlined,
  CodeOutlined,
  CheckOutlined,
  CloseOutlined,
  WarningOutlined,
  PlusOutlined,
  ImportOutlined,
  DownOutlined,
  DeleteOutlined,
  MenuOutlined,
  UpOutlined,
} from "@icons";
import { Button, Modal, Alert, Tabs, Input, Typography, Card, Select, Switch, Row, Col, Dropdown, Tag } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts
import { useQueryClient } from "@tanstack/react-query";
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import { apiClient } from "../apiClient";

// Nginx rule type definitions
export type NginxRule =
  | { type: "custom_header"; name: string; value: string; always?: boolean }
  | {
      type: "rewrite";
      pattern: string;
      replacement: string;
      flag?: "last" | "break" | "redirect" | "permanent";
    }
  | {
      type: "proxy_pass";
      path: string;
      target: string;
      websocket?: boolean;
      read_timeout?: string;
    }
  | {
      type: "ip_access";
      path: string;
      mode: "allow_list" | "deny_list";
      ips: string[];
    }
  | { type: "php_setting"; name: string; value: string }
  | { type: "max_upload_size"; size: string };

// Minimal shape — admin and user shells have slightly different Domain
// records but this button only cares about these fields.
export type DomainSettingsTarget = {
  id: string;
  name: string;
  user_id?: string;
  php_pool_id?: string | null;
  nginx_custom_directives?: string | null;
  nginx_rules?: NginxRule[] | null;
};

// compileRules mirrors panel-api/internal/nginxrules/Compile (Go) so the
// Raw Directives tab can show a live preview of what the Rule Builder
// will emit on the backend. Keep the two implementations byte-identical;
// drift means the operator sees one thing in the UI and another in nginx.
const compileRules = (rules: NginxRule[]): string => {
  if (!rules || rules.length === 0) return "";
  const quote = (v: string): string => `"${v.replace(/\\/g, "\\\\").replace(/"/g, "\\\"")}"`;
  const quoteLoc = (v: string): string =>
    /[\s"'\\]/.test(v) ? quote(v) : v;
  const out: string[] = [];
  for (const r of rules) {
    switch (r.type) {
      case "custom_header": {
        const always = r.always ? " always" : "";
        out.push(`    add_header ${r.name} ${quote(r.value)}${always};`);
        break;
      }
      case "rewrite": {
        const flag = r.flag || "last";
        out.push(`    rewrite ${r.pattern} ${quote(r.replacement)} ${flag};`);
        break;
      }
      case "proxy_pass": {
        out.push(`    location ${quoteLoc(r.path)} {`);
        out.push(`        proxy_pass ${r.target};`);
        out.push("        proxy_set_header Host $host;");
        out.push("        proxy_set_header X-Real-IP $remote_addr;");
        out.push("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;");
        out.push("        proxy_set_header X-Forwarded-Proto $scheme;");
        if (r.websocket) {
          out.push("        proxy_http_version 1.1;");
          out.push("        proxy_set_header Upgrade $http_upgrade;");
          out.push('        proxy_set_header Connection "upgrade";');
        }
        if (r.read_timeout) {
          out.push(`        proxy_read_timeout ${r.read_timeout};`);
        }
        out.push("    }");
        break;
      }
      case "ip_access": {
        out.push(`    location ${quoteLoc(r.path)} {`);
        if (r.mode === "deny_list") {
          for (const ip of r.ips) out.push(`        deny ${ip};`);
          out.push("        allow all;");
        } else {
          for (const ip of r.ips) out.push(`        allow ${ip};`);
          out.push("        deny all;");
        }
        out.push("    }");
        break;
      }
      case "php_setting": {
        out.push(`    fastcgi_param PHP_VALUE ${quote(`${r.name}=${r.value}`)};`);
        break;
      }
      case "max_upload_size": {
        out.push(`    client_max_body_size ${r.size};`);
        break;
      }
    }
  }
  return out.join("\n");
};

// Raw Directives editor component. Shows two stacked sections:
//   1. Read-only preview of the directives compiled from the Rule Builder
//      tab (regenerates on every render).
//   2. Editable textarea for raw operator-authored directives that get
//      appended after the compiled rules in the vhost.
const RawDirectivesEditor = ({
  value,
  onChange,
  rules,
}: {
  value: string;
  onChange: (v: string) => void;
  rules: NginxRule[];
}) => {
  const compiled = compileRules(rules);
  return (
    <div>
      {compiled && (
        <div style={{ marginBottom: 16 }}>
          <div style={{ marginBottom: 8 }}>
            <Typography.Text strong>From Rule Builder (read-only)</Typography.Text>
          </div>
          <Input.TextArea
            rows={Math.min(12, Math.max(3, compiled.split("\n").length))}
            value={compiled}
            readOnly
            style={{ fontFamily: "monospace", background: "rgba(0,0,0,0.03)" }}
          />
          <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
            Edit these on the Rule Builder tab. They are auto-prepended to the raw directives below in the vhost.
          </Typography.Text>
        </div>
      )}
      <div style={{ marginBottom: 8 }}>
        <Typography.Text strong>Raw directives</Typography.Text>
      </div>
      <Input.TextArea
        rows={compiled ? 8 : 14}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={`# Example:
rewrite ^/old$ /new permanent;
add_header X-Frame-Options "DENY" always;`}
        style={{ fontFamily: "monospace" }}
      />
      <Typography.Text
        type="secondary"
        style={{ display: "block", marginTop: 8 }}
      >
        Restricted to safe directives (rewrite, add_header, proxy_pass, etc.).
        Dangerous directives are blocked.
      </Typography.Text>
    </div>
  );
};

// Type-specific form renderers
const renderCustomHeaderBody = (
  rule: Extract<NginxRule, { type: "custom_header" }>,
  onUpdate: (field: string, value: unknown) => void
) => (
  <>
    <Row gutter={16} style={{ marginBottom: 12 }}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Header Name <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="X-Frame-Options"
          value={rule.name}
          onChange={(e) => onUpdate("name", e.target.value)}
        />
      </Col>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Value <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="DENY"
          value={rule.value}
          onChange={(e) => onUpdate("value", e.target.value)}
        />
      </Col>
    </Row>
    <Row gutter={16}>
      <Col span={24}>
        <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />}
          checked={rule.always ?? true}
          onChange={(v) => onUpdate("always", v)}
          style={{ marginRight: 8 }}
        />
        <Typography.Text>Send header on error responses</Typography.Text>
      </Col>
    </Row>
  </>
);

const renderRewriteBody = (
  rule: Extract<NginxRule, { type: "rewrite" }>,
  onUpdate: (field: string, value: unknown) => void
) => (
  <>
    <Row gutter={16} style={{ marginBottom: 12 }}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Pattern <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="^/old$"
          value={rule.pattern}
          onChange={(e) => onUpdate("pattern", e.target.value)}
        />
        <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
          Regex matched against the URI
        </Typography.Text>
      </Col>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Replacement <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="/new"
          value={rule.replacement}
          onChange={(e) => onUpdate("replacement", e.target.value)}
        />
      </Col>
    </Row>
    <Row gutter={16}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>Flag</Typography.Text>
        </div>
        <Select
          value={rule.flag ?? "last"}
          onChange={(v) => onUpdate("flag", v)}
          options={[
            { value: "last", label: "last" },
            { value: "break", label: "break" },
            { value: "redirect", label: "redirect" },
            { value: "permanent", label: "permanent" },
          ]}
          style={{ width: "100%" }}
        />
      </Col>
    </Row>
  </>
);

const renderProxyPassBody = (
  rule: Extract<NginxRule, { type: "proxy_pass" }>,
  onUpdate: (field: string, value: unknown) => void
) => (
  <>
    <Row gutter={16} style={{ marginBottom: 12 }}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Path <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="/api/"
          value={rule.path}
          onChange={(e) => onUpdate("path", e.target.value)}
        />
        <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
          Location prefix
        </Typography.Text>
      </Col>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Target URL <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="http://localhost:3000"
          value={rule.target}
          onChange={(e) => onUpdate("target", e.target.value)}
        />
        <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
          Upstream service URL
        </Typography.Text>
      </Col>
    </Row>
    <Row gutter={16}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>WebSocket support</Typography.Text>
        </div>
        <Switch
          checked={!!rule.websocket}
          onChange={(v) => onUpdate("websocket", v)}
        />
        <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
          Adds proxy_http_version 1.1 + Upgrade/Connection headers
        </Typography.Text>
      </Col>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>Read timeout</Typography.Text>
        </div>
        <Input
          placeholder="86400s"
          value={rule.read_timeout ?? ""}
          onChange={(e) => onUpdate("read_timeout", e.target.value)}
        />
        <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
          nginx duration (e.g. 60s, 24h). Empty = nginx default (60s).
        </Typography.Text>
      </Col>
    </Row>
  </>
);

const renderIpAccessBody = (
  rule: Extract<NginxRule, { type: "ip_access" }>,
  onUpdate: (field: string, value: unknown) => void
) => (
  <>
    <Row gutter={16} style={{ marginBottom: 12 }}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Path <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="/admin/"
          value={rule.path}
          onChange={(e) => onUpdate("path", e.target.value)}
        />
      </Col>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Mode <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Select
          value={rule.mode}
          onChange={(v) => onUpdate("mode", v)}
          options={[
            { value: "allow_list", label: "Allow listed IPs" },
            { value: "deny_list", label: "Deny listed IPs" },
          ]}
          style={{ width: "100%" }}
        />
      </Col>
    </Row>
    <Row gutter={16}>
      <Col span={24}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            IP Addresses <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Select
          mode="tags"
          placeholder="192.168.1.1, 10.0.0.0/8"
          value={rule.ips}
          onChange={(v) => onUpdate("ips", v)}
          tokenSeparators={[",", " ", "\n"]}
          style={{ width: "100%" }}
        />
      </Col>
    </Row>
  </>
);

const renderPhpSettingBody = (
  rule: Extract<NginxRule, { type: "php_setting" }>,
  onUpdate: (field: string, value: unknown) => void
) => (
  <>
    <Row gutter={16}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            PHP Directive <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="memory_limit"
          value={rule.name}
          onChange={(e) => onUpdate("name", e.target.value)}
        />
      </Col>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Value <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="512M"
          value={rule.value}
          onChange={(e) => onUpdate("value", e.target.value)}
        />
      </Col>
    </Row>
  </>
);

const renderMaxUploadSizeBody = (
  rule: Extract<NginxRule, { type: "max_upload_size" }>,
  onUpdate: (field: string, value: unknown) => void
) => (
  <>
    <Row gutter={16}>
      <Col span={12}>
        <div style={{ marginBottom: 8 }}>
          <Typography.Text>
            Size <Typography.Text type="danger">*</Typography.Text>
          </Typography.Text>
        </div>
        <Input
          placeholder="100M"
          value={rule.size}
          onChange={(e) => onUpdate("size", e.target.value)}
        />
        <Typography.Text type="secondary" style={{ display: "block", marginTop: 4 }}>
          e.g. 10M, 1G
        </Typography.Text>
      </Col>
    </Row>
  </>
);

// Sortable rule card
interface SortableRuleCardProps {
  idx: number;
  rule: NginxRule;
  isExpanded: boolean;
  onToggleExpanded: (idx: number) => void;
  onRemove: (idx: number) => void;
  onUpdate: (idx: number, field: string, value: unknown) => void;
}

const SortableRuleCard = ({
  idx,
  rule,
  isExpanded,
  onToggleExpanded,
  onRemove,
  onUpdate,
}: SortableRuleCardProps) => {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: idx,
  });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : 1,
  };

  const getRuleTypeLabel = (type: NginxRule["type"]) => {
    const labels: Record<NginxRule["type"], string> = {
      custom_header: "Custom Header",
      rewrite: "Rewrite",
      proxy_pass: "Proxy Pass",
      ip_access: "IP Access",
      php_setting: "PHP Setting",
      max_upload_size: "Max Upload Size",
    };
    return labels[type];
  };

  const getRuleSummary = (rule: NginxRule): string => {
    switch (rule.type) {
      case "custom_header":
        return `${rule.name}: ${rule.value}`;
      case "rewrite":
        return `${rule.pattern} → ${rule.replacement}`;
      case "proxy_pass":
        return `${rule.path} → ${rule.target}`;
      case "ip_access":
        return `${rule.path} (${rule.mode})`;
      case "php_setting":
        return `${rule.name} = ${rule.value}`;
      case "max_upload_size":
        return `Max: ${rule.size}`;
    }
  };

  return (
    <div ref={setNodeRef} style={style}>
      <Card style={{ marginBottom: 12 }} bodyStyle={{ padding: 12 }}>
        <div style={{ display: "flex", alignItems: "center", marginBottom: isExpanded ? 12 : 0, gap: 8 }}>
          <button
            {...attributes}
            {...listeners}
            style={{
              cursor: "grab",
              background: "none",
              border: "none",
              padding: 4,
              display: "flex",
              alignItems: "center",
            }}
          >
            <MenuOutlined />
          </button>

          <Typography.Text strong>
            {getRuleTypeLabel(rule.type)}
          </Typography.Text>

          <Typography.Text type="secondary">
            {getRuleSummary(rule)}
          </Typography.Text>

          <div style={{ flex: 1 }} />

          <Button
            type="text"
            icon={isExpanded ? <UpOutlined /> : <DownOutlined />}
            onClick={() => onToggleExpanded(idx)}
            style={{ padding: 4 }}
          />
          <Button
            danger
            icon={<DeleteOutlined />}
            type="text"
            onClick={() => onRemove(idx)}
          />
        </div>

        {isExpanded && (
          <div style={{ paddingTop: 8 }}>
            {rule.type === "custom_header" &&
              renderCustomHeaderBody(
                rule as Extract<NginxRule, { type: "custom_header" }>,
                (field, value) => onUpdate(idx, field, value)
              )}
            {rule.type === "rewrite" &&
              renderRewriteBody(
                rule as Extract<NginxRule, { type: "rewrite" }>,
                (field, value) => onUpdate(idx, field, value)
              )}
            {rule.type === "proxy_pass" &&
              renderProxyPassBody(
                rule as Extract<NginxRule, { type: "proxy_pass" }>,
                (field, value) => onUpdate(idx, field, value)
              )}
            {rule.type === "ip_access" &&
              renderIpAccessBody(
                rule as Extract<NginxRule, { type: "ip_access" }>,
                (field, value) => onUpdate(idx, field, value)
              )}
            {rule.type === "php_setting" &&
              renderPhpSettingBody(
                rule as Extract<NginxRule, { type: "php_setting" }>,
                (field, value) => onUpdate(idx, field, value)
              )}
            {rule.type === "max_upload_size" &&
              renderMaxUploadSizeBody(
                rule as Extract<NginxRule, { type: "max_upload_size" }>,
                (field, value) => onUpdate(idx, field, value)
              )}
          </div>
        )}
      </Card>
    </div>
  );
};

// Rule Builder component
const RuleBuilder = ({
  rules,
  onRulesChange,
  allowedTypes,
}: {
  rules: NginxRule[];
  onRulesChange: (rules: NginxRule[]) => void;
  // When set, the "Add Rule" menu is limited to these rule types (GH #307
  // tenant subset). Omitted = all types (admin).
  allowedTypes?: NginxRule["type"][];
}) => {
  const [expandedCards, setExpandedCards] = useState<Set<number>>(new Set());

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;

    if (over && active.id !== over.id) {
      const oldIndex = Number(active.id);
      const newIndex = Number(over.id);
      onRulesChange(arrayMove(rules, oldIndex, newIndex));
    }
  };

  const addRule = (type: NginxRule["type"]) => {
    let newRule: NginxRule;

    switch (type) {
      case "custom_header":
        newRule = { type: "custom_header", name: "", value: "", always: true };
        break;
      case "rewrite":
        newRule = { type: "rewrite", pattern: "", replacement: "", flag: "last" };
        break;
      case "proxy_pass":
        newRule = { type: "proxy_pass", path: "/", target: "" };
        break;
      case "ip_access":
        newRule = { type: "ip_access", path: "/", mode: "allow_list", ips: [] };
        break;
      case "php_setting":
        newRule = { type: "php_setting", name: "", value: "" };
        break;
      case "max_upload_size":
        newRule = { type: "max_upload_size", size: "" };
        break;
    }

    const newRules = [...rules, newRule];
    onRulesChange(newRules);
    // Auto-expand the new card
    setExpandedCards(new Set(expandedCards).add(rules.length));
  };

  const removeRule = (idx: number) => {
    onRulesChange(rules.filter((_, i) => i !== idx));
  };

  const updateRule = (idx: number, field: string, value: unknown) => {
    const updated = [...rules];
    updated[idx] = { ...updated[idx], [field]: value };
    onRulesChange(updated);
  };

  const addMenuItems = [
    { key: "custom_header", label: "Custom Header", icon: <PlusOutlined /> },
    { key: "rewrite", label: "Rewrite", icon: <PlusOutlined /> },
    { key: "proxy_pass", label: "Proxy Pass", icon: <PlusOutlined /> },
    { key: "ip_access", label: "IP Access", icon: <PlusOutlined /> },
    { key: "php_setting", label: "PHP Setting", icon: <PlusOutlined /> },
    { key: "max_upload_size", label: "Max Upload Size", icon: <PlusOutlined /> },
  ].filter(
    (it) => !allowedTypes || allowedTypes.includes(it.key as NginxRule["type"]),
  );

  return (
    <div>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
        Add rules using the form below. They will be converted to nginx directives automatically.
      </Typography.Paragraph>

      {rules.length === 0 ? (
        <div
          style={{
            padding: "32px 24px",
            textAlign: "center",
          }}
        >
          <Typography.Text type="secondary">
            No rules yet. Click Add Rule to get started.
          </Typography.Text>
        </div>
      ) : (
        <div style={{ marginBottom: 16 }}>
          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={handleDragEnd}
          >
            <SortableContext
              items={rules.map((_, idx) => idx)}
              strategy={verticalListSortingStrategy}
            >
              {rules.map((rule, idx) => (
                <SortableRuleCard
                  key={idx}
                  idx={idx}
                  rule={rule}
                  isExpanded={expandedCards.has(idx)}
                  onToggleExpanded={(i) => {
                    const newSet = new Set(expandedCards);
                    if (newSet.has(i)) {
                      newSet.delete(i);
                    } else {
                      newSet.add(i);
                    }
                    setExpandedCards(newSet);
                  }}
                  onRemove={removeRule}
                  onUpdate={updateRule}
                />
              ))}
            </SortableContext>
          </DndContext>
        </div>
      )}

      <div style={{ textAlign: "center", marginBottom: 16 }}>
        <Dropdown
          menu={{
            items: addMenuItems.map((item) => ({
              ...item,
              onClick: () => addRule(item.key as NginxRule["type"]),
            })),
          }}
        >
          <Button icon={<PlusOutlined />}>
            Add Rule <DownOutlined />
          </Button>
        </Dropdown>
      </div>
    </div>
  );
};

// HtaccessImport — paste an Apache .htaccess and convert it into typed Rule
// Builder entries via POST /domains/:id/htaccess/preview (ADR-0130). The
// converted rules are MERGED into the current rules list (the operator still
// reviews + Saves on the Rule Builder tab); warnings/notes are shown so
// nothing is silently dropped. Security-relevant warnings are flagged red.
type PreviewWarning = {
  line: number;
  source: string;
  reason: string;
  security?: boolean;
};
type PreviewResponse = {
  rules: NginxRule[];
  warnings: PreviewWarning[];
  notes: string[];
  invalid?: string[];
};

const HtaccessImport = ({
  domainId,
  rules,
  onRulesChange,
}: {
  domainId: string;
  rules: NginxRule[];
  onRulesChange: (rules: NginxRule[]) => void;
}) => {
  const [content, setContent] = useState("");
  const [basePath, setBasePath] = useState("/");
  const [preview, setPreview] = useState<PreviewResponse | null>(null);
  const [loading, setLoading] = useState(false);

  const handleConvert = async () => {
    setLoading(true);
    setPreview(null);
    try {
      const res = await apiClient.post<PreviewResponse>(
        `/domains/${domainId}/htaccess/preview`,
        { content, base_path: basePath || "/" },
      );
      // Normalize: a no-rule conversion may arrive as null fields; coerce to
      // arrays so the render below can use .length/.filter safely.
      setPreview({
        rules: res.data.rules ?? [],
        warnings: res.data.warnings ?? [],
        notes: res.data.notes ?? [],
        invalid: res.data.invalid ?? [],
      });
    } catch (err) {
      const e = err as {
        response?: { data?: { error?: string } };
        message?: string;
      };
      feedback.message.error(`Could not convert .htaccess: ${e.response?.data?.error ?? e.message ?? "Unknown error"}`);
    } finally {
      setLoading(false);
    }
  };

  const handleAdd = () => {
    if (!preview || preview.rules.length === 0) return;
    onRulesChange([...rules, ...preview.rules]);
    feedback.message.success(
      `Added ${preview.rules.length} rule(s) to the Rule Builder: review them on the Rule Builder tab, then Save / Apply.`,
    );
    setPreview(null);
    setContent("");
  };

  const securityWarnings = preview?.warnings.filter((w) => w.security) ?? [];
  const otherWarnings = preview?.warnings.filter((w) => !w.security) ?? [];

  return (
    <div>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
        Paste an Apache <code>.htaccess</code> to convert its redirects,
        rewrites, headers, PHP settings and access rules into typed Rule
        Builder entries. The front-controller block used by WordPress / Laravel
        is recognized and skipped (the default routing already handles it).
        Anything that can&apos;t be safely converted is listed below — never
        applied silently.
      </Typography.Paragraph>
      <Input.TextArea
        value={content}
        onChange={(e) => setContent(e.target.value)}
        placeholder={"# paste .htaccess here\nRewriteEngine On\n..."}
        rows={10}
        style={{ fontFamily: "monospace", marginBottom: 8 }}
      />
      <Row gutter={8} style={{ marginBottom: 12 }} align="middle">
        <Col flex="none">
          <Typography.Text type="secondary">Base path</Typography.Text>
        </Col>
        <Col flex="120px">
          <Input
            value={basePath}
            onChange={(e) => setBasePath(e.target.value)}
            placeholder="/"
          />
        </Col>
        <Col flex="auto">
          <Button
            type="primary"
            onClick={handleConvert}
            loading={loading}
            disabled={content.trim() === ""}
          >
            Convert
          </Button>
        </Col>
      </Row>

      {preview && (
        <div>
          <Alert
            type={preview.rules.length > 0 ? "success" : "info"}
            showIcon
            style={{ marginBottom: 12 }}
            message={
              preview.rules.length > 0
                ? `${preview.rules.length} rule(s) ready to import`
                : "No convertible rules found"
            }
            action={
              preview.rules.length > 0 ? (
                <Button size="small" type="primary" onClick={handleAdd}>
                  Add to Rule Builder
                </Button>
              ) : undefined
            }
          />

          {securityWarnings.length > 0 && (
            <Alert
              type="error"
              showIcon
              style={{ marginBottom: 12 }}
              message="Not converted — review manually (security relevant)"
              description={
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {securityWarnings.map((w, i) => (
                    <li key={i}>
                      <Tag color="red">line {w.line}</Tag> {w.reason}
                      <br />
                      <Typography.Text code>{w.source}</Typography.Text>
                    </li>
                  ))}
                </ul>
              }
            />
          )}

          {otherWarnings.length > 0 && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 12 }}
              message={`${otherWarnings.length} line(s) not converted`}
              description={
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {otherWarnings.map((w, i) => (
                    <li key={i}>
                      <Typography.Text type="secondary">
                        line {w.line}:
                      </Typography.Text>{" "}
                      {w.reason}
                    </li>
                  ))}
                </ul>
              }
            />
          )}

          {preview.notes.length > 0 && (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 12 }}
              message="Notes"
              description={
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {preview.notes.map((n, i) => (
                    <li key={i}>{n}</li>
                  ))}
                </ul>
              }
            />
          )}
        </div>
      )}
    </div>
  );
};

export const DomainSettingsButton = ({
  domain,
  open: controlledOpen,
  onClose,
}: {
  domain: DomainSettingsTarget;
  open?: boolean;
  onClose?: () => void;
}) => {
  const [isModalOpen, setIsModalOpen] = useState(false);
  const effectiveOpen = controlledOpen ?? isModalOpen;

  const handleCloseModal = () => {
    if (onClose) {
      onClose();
    } else {
      setIsModalOpen(false);
    }
  };

  const [directivesValue, setDirectivesValue] = useState(
    domain.nginx_custom_directives ?? ""
  );
  const [rules, setRules] = useState<NginxRule[]>(domain.nginx_rules ?? []);
  const [isSaving, setIsSaving] = useState(false);
  const qc = useQueryClient();

  useEffect(() => {
    if (controlledOpen) {
      setDirectivesValue(domain.nginx_custom_directives ?? "");
      setRules(domain.nginx_rules ?? []);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [controlledOpen]);

  const handleOpenModal = async () => {
    // Re-sync from prop in case the values were updated elsewhere
    setDirectivesValue(domain.nginx_custom_directives ?? "");
    setRules(domain.nginx_rules ?? []);
    setIsModalOpen(true);
  };


  const handleApply = async () => {
    setIsSaving(true);
    try {
      await apiClient.patch(`/domains/${domain.id}`, {
        nginx_custom_directives: directivesValue,
        nginx_rules: rules,
      });
      feedback.message.success("Nginx config saved");
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
      handleCloseModal();
    } catch (err) {
      const e = err as {
        response?: { data?: { detail?: string } };
        message?: string;
      };
      feedback.message.error(`Failed to save: ${e.response?.data?.detail ?? e.message ?? "Unknown error"}`);
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <>
      {controlledOpen === undefined && (
        <Button
          type="text"
          icon={<SettingOutlined />}
          onClick={handleOpenModal}
        >
          Nginx Directives
        </Button>
      )}

      <Modal
        title={`Nginx Directives for ${domain.name}`}
        open={effectiveOpen}
        onCancel={handleCloseModal}
        width={720}
        footer={[
          <Button key="cancel" onClick={handleCloseModal}>
            Cancel
          </Button>,
          <Button
            key="apply"
            type="primary"
            icon={<CheckOutlined />}
            onClick={handleApply}
            loading={isSaving}
          >
            Apply
          </Button>,
        ]}
      >
        <Alert
          type="warning"
          icon={<WarningOutlined />}
          title="Use with caution"
          description="Incorrect directives can break your website. Changes are tested with nginx before applying, but you are responsible for ensuring your configuration is correct."
          showIcon
          style={{ marginBottom: 24 }}
        />

        <Tabs
          defaultActiveKey="builder"
          items={[
            {
              key: "builder",
              label: (
                <span>
                  <ToolOutlined /> Rule Builder
                </span>
              ),
              children: <RuleBuilder rules={rules} onRulesChange={setRules} />,
            },
            {
              key: "raw",
              label: (
                <span>
                  <CodeOutlined /> Raw Directives
                </span>
              ),
              children: (
                <RawDirectivesEditor
                  value={directivesValue}
                  onChange={setDirectivesValue}
                  rules={rules}
                />
              ),
            },
            {
              key: "htaccess",
              label: (
                <span>
                  <ImportOutlined /> Import .htaccess
                </span>
              ),
              children: (
                <HtaccessImport
                  domainId={domain.id}
                  rules={rules}
                  onRulesChange={setRules}
                />
              ),
            },
          ]}
        />
      </Modal>
    </>
  );
};

// DomainNginxSection — inline, tab-friendly variant of DomainSettingsButton.
// Renders the same Rule Builder + Raw Directives editor but in-place (no
// Modal wrapper), with a single Save button. Used by DomainEdit's "Nginx"
// tab so operators can see the editor as a peer of General / SSL / Caching
// instead of as a button-launched modal.
export const DomainNginxSection = ({ domain }: { domain: DomainSettingsTarget }) => {
  const [directivesValue, setDirectivesValue] = useState(
    domain.nginx_custom_directives ?? "",
  );
  const [rules, setRules] = useState<NginxRule[]>(domain.nginx_rules ?? []);
  const [isSaving, setIsSaving] = useState(false);
  const qc = useQueryClient();

  // Re-sync from prop when the domain reloads (e.g. after a save
  // round-trip invalidates the one-domain query upstream).
  useEffect(() => {
    setDirectivesValue(domain.nginx_custom_directives ?? "");
    setRules(domain.nginx_rules ?? []);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [domain.id, domain.nginx_custom_directives, JSON.stringify(domain.nginx_rules)]);

  const handleSave = async () => {
    setIsSaving(true);
    try {
      await apiClient.patch(`/domains/${domain.id}`, {
        nginx_custom_directives: directivesValue,
        nginx_rules: rules,
      });
      feedback.message.success("Nginx config saved");
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
    } catch (err) {
      const e = err as {
        response?: { data?: { detail?: string } };
        message?: string;
      };
      feedback.message.error(`Failed to save: ${e.response?.data?.detail ?? e.message ?? "Unknown error"}`);
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <div>
      <Alert
        type="warning"
        icon={<WarningOutlined />}
        message="Use with caution"
        description="Incorrect directives can break your website. Changes are tested with nginx before applying, but you are responsible for ensuring your configuration is correct."
        showIcon
        style={{ marginBottom: 16 }}
      />
      <Tabs
        defaultActiveKey="builder"
        items={[
          {
            key: "builder",
            label: (
              <span>
                <ToolOutlined /> Rule Builder
              </span>
            ),
            children: <RuleBuilder rules={rules} onRulesChange={setRules} />,
          },
          {
            key: "raw",
            label: (
              <span>
                <CodeOutlined /> Raw Directives
              </span>
            ),
            children: (
              <RawDirectivesEditor
                value={directivesValue}
                onChange={setDirectivesValue}
                rules={rules}
              />
            ),
          },
          {
            key: "htaccess",
            label: (
              <span>
                <ImportOutlined /> Import .htaccess
              </span>
            ),
            children: (
              <HtaccessImport
                domainId={domain.id}
                rules={rules}
                onRulesChange={setRules}
              />
            ),
          },
        ]}
      />
      <div style={{ marginTop: 16 }}>
        <Button
          type="primary"
          icon={<CheckOutlined />}
          onClick={handleSave}
          loading={isSaving}
        >
          Save
        </Button>
      </div>
    </div>
  );
};


// TenantNginxRulesButton — GH #307. A pared-down Rule Builder for tenants:
// rewrite + custom_header only (no Raw Directives tab, no proxy_pass/ip_access).
// Mounted on the user domain list ONLY when the admin has opted in
// (caps.tenant_domain_options_enabled); the backend re-enforces the safe subset
// (validateTenantNginxRules) so the type filter here is UX, not the security
// boundary.
// TenantNginxRulesPanel — GH #1543. The Rule Builder body, rendered both inline
// (a tab on the tenant Web Domain page) and inside the row-menu Modal, which now
// delegates here. Re-syncs from domain.nginx_rules when it changes underneath
// so an inline pane that stays mounted doesn't clobber a concurrent save.
export const TenantNginxRulesPanel = ({
  domain,
  onSaved,
}: {
  domain: DomainSettingsTarget;
  onSaved?: () => void;
}) => {
  const [rules, setRules] = useState<NginxRule[]>(domain.nginx_rules ?? []);
  const [saving, setSaving] = useState(false);
  const qc = useQueryClient();

  useEffect(() => {
    setRules(domain.nginx_rules ?? []);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [domain.id, JSON.stringify(domain.nginx_rules)]);

  const handleSave = async () => {
    setSaving(true);
    try {
      // Send ONLY nginx_rules — never nginx_custom_directives (admin-only).
      await apiClient.patch(`/domains/${domain.id}`, { nginx_rules: rules });
      feedback.message.success("Rewrite rules saved — applied on the next reconcile");
      qc.invalidateQueries({ queryKey: ["list", "domains"] });
      qc.invalidateQueries({ queryKey: ["one", "domains", domain.id] });
      onSaved?.();
    } catch (err) {
      const e = err as {
        response?: { data?: { detail?: string } };
        message?: string;
      };
      feedback.message.error(`Failed to save rules: ${e.response?.data?.detail ?? e.message ?? "Unknown error"}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Typography.Paragraph type="secondary">
        Add rewrite rules and custom response headers for this domain. Rewrites
        must point to a local path on your own site (no external URLs or
        proxying).
      </Typography.Paragraph>
      <RuleBuilder rules={rules} onRulesChange={setRules} allowedTypes={["rewrite", "custom_header"]} />
      <div style={{ marginTop: 16 }}>
        <Button type="primary" loading={saving} onClick={handleSave}>
          Save
        </Button>
      </div>
    </div>
  );
};

export const TenantNginxRulesButton = ({
  domain,
  open: controlledOpen,
  onClose,
}: {
  domain: DomainSettingsTarget;
  open?: boolean;
  onClose?: () => void;
}) => {
  const [internalOpen, setInternalOpen] = useState(false);
  const open = controlledOpen ?? internalOpen;
  const close = () => (onClose ? onClose() : setInternalOpen(false));

  return (
    <>
      {controlledOpen === undefined && (
        <Button icon={<ToolOutlined />} onClick={() => setInternalOpen(true)}>
          Rewrite rules
        </Button>
      )}
      <Modal
        open={open}
        title="Rewrite & header rules"
        onCancel={close}
        footer={null}
        width={720}
        destroyOnClose
      >
        <TenantNginxRulesPanel domain={domain} onSaved={close} />
      </Modal>
    </>
  );
};
