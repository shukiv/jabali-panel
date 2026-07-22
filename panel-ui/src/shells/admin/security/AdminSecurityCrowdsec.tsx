// AdminSecurityCrowdsec — M26 Step 7. Four cards: metrics, active
// decisions (with add Drawer + delete Popconfirm), bouncers (read-only),
// hub items (read-only). Polls metrics + status every 30s.
//
// Conventions: per docs/CONVENTIONS.md the "create" affordance is a
// Drawer (not a Modal), Tables consume <Table.Column> children (not a
// columns prop), and Statistic rows lay out via Row gutter rather than
// inline marginLeft. Hooks stay direct useQuery (not useTableURL) —
// these endpoints are not the standard {data,total,page,page_size}
// list shape; they're agent passthroughs.
import { useTranslation } from "react-i18next";
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Drawer,
  Empty,
  Form,
  Grid,
  Input,
  InputNumber,
  message,
  Popconfirm,
  Radio,
  Segmented,
  Row,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  Flex,
} from "antd";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { useSearchParams } from "react-router";
import {
  ApiOutlined,
  SafetyOutlined,
  ThunderboltOutlined,
  CheckOutlined,
  DeleteOutlined,
  ReloadOutlined,
} from "@icons";
import { RowActions } from "../../../components/RowActions";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { ISO3166_COUNTRIES } from "../../../data/iso3166";
import { CrowdsecTestIPCard } from "./CrowdsecTestIPCard";
import { Sparkline } from "../../../components/Sparkline";

import {
  useAddCrowdsecAllowlist,
  useAddCrowdsecDecision,
  useAppSecGeoblock,
  useCrowdsecAlert,
  useCrowdsecAlerts,
  useCrowdsecAllowlists,
  useCrowdsecBlocklists,
  useRefreshCrowdsecBlocklists,
  useCrowdsecBouncers,
  useCrowdsecDecisions,
  useCrowdsecHub,
  useInstallCrowdsecHubItem,
  useRemoveCrowdsecHubItem,
  useCrowdsecMetrics,
  useCrowdsecCaptcha,
  useCrowdsecProfiles,
  useCrowdsecStatus,
  useDeleteCrowdsecDecision,
  useRemoveCrowdsecAllowlist,
  useUpdateAppSecGeoblock,
  useUpdateCrowdsecCaptcha,
  useUpdateCrowdsecProfiles,
  type AppSecGeoblockMode,
  type CrowdsecAlert,
  type CrowdsecAllowlistEntry,
  type CrowdsecCaptchaProvider,
  type CrowdsecDecision,
  type CrowdsecProfileOverride,
  type CrowdsecScenarioItem,
  type CrowdsecScope,
} from "../../../hooks/useSecurityCrowdsec";

const SCOPE_OPTIONS: Array<{ value: CrowdsecScope | "all"; label: string }> = [
  { value: "all", label: "All scopes" },
  { value: "ip", label: "IP" },
  { value: "range", label: "Range (CIDR)" },
  { value: "country", label: "Country" },
  { value: "as", label: "AS" },
];

// Per-scope value-field validation. Server is authoritative (agent
// does net.ParseIP / net.ParseCIDR / 2-letter country / ASN digits) —
// these client-side patterns exist only to reject typos before a
// round-trip. All four scopes ship with their own placeholder + help.
const IP_OR_CIDR = /^[0-9a-fA-F:.]+(\/\d{1,3})?$/;
const COUNTRY_CODE = /^[A-Za-z]{2}$/;
const ASN_RE = /^(AS|as)?\d+$/;
// CrowdSec accepts Go time.ParseDuration: 4h, 1h30m, 30m, 1d (custom).
const DURATION = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h|d))+$/;

const ADD_SCOPE_OPTIONS: Array<{ value: CrowdsecScope; label: string }> = [
  { value: "ip", label: "IP address" },
  { value: "range", label: "Range (CIDR)" },
  { value: "country", label: "Country (ISO 3166-1)" },
  { value: "as", label: "AS (ASN)" },
];

type AddDecisionFormValues = {
  scope: CrowdsecScope;
  value: string;
  duration: string;
  reason: string;
};

const fmtTime = (s?: string): string => (s ? new Date(s).toLocaleString() : "—");


export const AdminSecurityCrowdsec = () => {
  const { t } = useTranslation();
  const [scope, setScope] = useState<CrowdsecScope | "all">("all");
  const decisions = useCrowdsecDecisions(scope === "all" ? undefined : scope);
  const hub = useCrowdsecHub();
  const addDecision = useAddCrowdsecDecision();
  const deleteDecision = useDeleteCrowdsecDecision();

  const [addOpen, setAddOpen] = useState(false);
  const [detailDecision, setDetailDecision] = useState<CrowdsecDecision | null>(null); // GH #716
  const alertsLink = useCrowdsecAlerts(); // GH #716: link decisions -> alerts by IP
  const [addForm] = Form.useForm<AddDecisionFormValues>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const submitAdd = async (values: AddDecisionFormValues) => {
    try {
      await addDecision.mutateAsync(values);
      message.success(`Decision added: ${values.scope}=${values.value}`);
      setAddOpen(false);
      addForm.resetFields();
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to add decision");
    }
  };

  const onDeleteDecision = async (row: CrowdsecDecision) => {
    try {
      await deleteDecision.mutateAsync(row.id);
      message.success(`Removed ban on ${row.ip}`);
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to remove decision");
    }
  };

  const addAllowlist = useAddCrowdsecAllowlist();
  const onWhitelist = async (row: CrowdsecDecision) => {
    try {
      await addAllowlist.mutateAsync({
        value: row.ip,
        reason: `Whitelisted from active decisions (${row.scenario})`.slice(0, 200),
      });
      await deleteDecision.mutateAsync(row.id);
      message.success(`Whitelisted ${row.ip} and removed the ban`);
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to whitelist IP");
    }
  };

  // Sub-tabs (one card per tab). URL-driven via ?sub= so a direct link
  // deep-links to a specific sub-tab. Keep the Add-decision Drawer
  // OUTSIDE the Tabs so it stays open across tab switches (rare, but
  // the Drawer should not unmount mid-form-fill).
  const [sp, setSp] = useSearchParams();
  const subTabs = [
    "overview",
    "decisions",
    "allowlist",
    "alerts",
    "captcha",
    "appsec",
    "settings",
    "blocklists",
    "hub",
  ] as const;
  type SubTab = (typeof subTabs)[number];
  const activeSub: SubTab = ((): SubTab => {
    const s = sp.get("sub");
    return (subTabs as readonly string[]).includes(s ?? "") ? (s as SubTab) : "overview";
  })();
  const onSubChange = (key: string) => {
    setSp((prev) => {
      const next = new URLSearchParams(prev);
      next.set("sub", key);
      return next;
    });
  };

  const overviewPanel = (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <RecentChangesCard />
      <EngineIdentityCard />
      <RemediationComponentsCard />
      <Alert
        type="info"
        showIcon
        message={t("adminsecuritycrowdsec.what_is_crowdsec")}
        description={t("adminsecuritycrowdsec.behaviour_based_intrusion_prevention_tails_s")}
      />
      <TopSourcesCard />
      <AlertsOverTimeCard />
      <CrowdsecTestIPCard />
    </Space>
  );

  const decisionsPanel = (
    <Card
      size="small"
      title={t("adminsecuritycrowdsec.active_decisions")}
      extra={
        <Space wrap>
          <Select
            size="small"
            value={scope}
            style={{ minWidth: 140 }}
            options={SCOPE_OPTIONS}
            onChange={(v) => setScope(v)}
          />
          <Button type="primary" size="small" onClick={() => setAddOpen(true)}>
            Add decision
          </Button>
        </Space>
      }
    >
      <Table<CrowdsecDecision>
        rowKey="id"
        dataSource={decisions.data ?? []}
        loading={decisions.isLoading}
        pagination={{ pageSize: 20, showSizeChanger: false }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecuritycrowdsec.no_active_decisions")} /> }}
        scroll={{ x: "max-content" }}
      >
        <Table.Column<CrowdsecDecision>
          dataIndex="ip"
          title="IP"
          key="ip"
          render={(v: string, row) => (
            <Button type="link" style={{ padding: 0 }} onClick={() => setDetailDecision(row)}>
              {v}
            </Button>
          )}
        />
        <Table.Column<CrowdsecDecision> dataIndex="scenario" title={t("adminsecuritycrowdsec.scenario")} key="scenario" />
        <Table.Column<CrowdsecDecision> dataIndex="reason" title={t("adminsecuritycrowdsec.reason")} key="reason" />
        <Table.Column<CrowdsecDecision>
          dataIndex="until"
          title={t("adminsecuritycrowdsec.until")}
          key="until"
          render={(s: string) => fmtTime(s)}
        />
        <Table.Column<CrowdsecDecision>
          title=""
          key="actions"
          width={200}
          render={(_, row) => (
            <RowActions
              actions={[
                { key: "whitelist", label: "Whitelist", icon: <CheckOutlined />, onClick: () => onWhitelist(row), confirm: { title: "Whitelist IP", description: `Add ${row.ip} to the allowlist and remove this ban? CrowdSec will stop blocking it.`, okText: "Whitelist" } },
                { key: "delete", label: "Delete", icon: <DeleteOutlined />, danger: true, onClick: () => onDeleteDecision(row), confirm: { title: "Remove ban", description: `Remove the ban on ${row.ip}? Traffic will resume immediately.`, okText: "Remove" } },
              ]}
            />
          )}
        />
      </Table>
      <Drawer
        title={detailDecision ? `Decision — ${detailDecision.ip}` : ""}
        width={560}
        open={!!detailDecision}
        onClose={() => setDetailDecision(null)}
      >
        {detailDecision ? (
          <>
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label={t("adminsecuritycrowdsec.ip_value")}>{detailDecision.ip}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritycrowdsec.why_reason")}>{detailDecision.reason || "—"}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritycrowdsec.scenario_fired")}>{detailDecision.scenario || "—"}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritycrowdsec.ban_duration")}>{detailDecision.duration || "—"}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritycrowdsec.active_until")}>{detailDecision.until || "—"}</Descriptions.Item>
            </Descriptions>
            <Alert
              style={{ marginTop: 12 }}
              type="info"
              showIcon
              message={t("adminsecuritycrowdsec.how_this_is_enforced")}
              description={t("adminsecuritycrowdsec.this_ip_was_banned_because_the_scenario_abov")}
            />
            {(() => {
              // GH #716: alert -> decision linkage. Show the alerts whose source
              // IP matches this decision, so the operator sees WHAT fired it.
              const matches = (alertsLink.data ?? []).filter((a) => a.source_ip === detailDecision.ip);
              return (
                <>
                  <Typography.Title level={5} style={{ marginTop: 16 }}>
                    Matching alerts ({matches.length})
                  </Typography.Title>
                  <Table<CrowdsecAlert>
                    size="small"
                    pagination={false}
                    scroll={{ x: "max-content" }}
                    rowKey="id"
                    loading={alertsLink.isLoading}
                    dataSource={matches}
                    locale={{ emptyText: "No matching alerts in the loaded window" }}
                    columns={[
                      { title: "Scenario", dataIndex: "scenario", ellipsis: true },
                      { title: "When", dataIndex: "created_at", width: 180, render: (v: string) => v || "—" },
                    ]}
                  />
                </>
              );
            })()}
          </>
        ) : null}
      </Drawer>
    </Card>
  );

  const hubPanel = <RecommendedHubCard hub={hub} />;

  return (
    <>
      <Tabs
        activeKey={activeSub}
        onChange={onSubChange}
        // Force horizontal scroll on overflow instead of letting the bar
        // stretch beyond the card. Combined with the outer Security
        // page collapsing top-tab labels to icons under md, 390px
        // mobile shows the subtab bar scrollable without breaking
        // labels char-per-line.
        tabBarStyle={{ marginBottom: 12 }}
        size={isDesktop ? "middle" : "small"}
        items={[
          { key: "overview", label: "Overview", children: overviewPanel },
          { key: "hub", label: "Hub", children: hubPanel },
          { key: "decisions", label: "Active decisions", children: decisionsPanel },
          { key: "allowlist", label: "Allowlist", children: <AllowlistsCard /> },
          { key: "alerts", label: "Alerts", children: <AlertsCard /> },
          { key: "captcha", label: "Captcha", children: <CaptchaPanel /> },
          { key: "appsec", label: "Block Country", children: <AppSecGeoblockCard /> },
          { key: "settings", label: "Settings", children: <SettingsPanel /> },
          { key: "blocklists", label: "Blocklists", children: <BlocklistsCard /> },
        ]}
      />

      <Drawer
        title={t("adminsecuritycrowdsec.add_crowdsec_decision_manual_ban")}
        open={addOpen}
        onClose={() => setAddOpen(false)}
        width={isDesktop ? 520 : undefined}
        placement="right"
        destroyOnClose
        extra={
          <Space>
            <Button onClick={() => setAddOpen(false)}>Cancel</Button>
            <Button
              type="primary"
              danger
              loading={addDecision.isPending}
              onClick={() => addForm.submit()}
            >
              Add ban
            </Button>
          </Space>
        }
      >
        <Form<AddDecisionFormValues>
          form={addForm}
          layout="vertical"
          onFinish={submitAdd}
          initialValues={{ scope: "ip" }}
        >
          <Form.Item
            name="scope"
            label={t("adminsecuritycrowdsec.scope")}
            rules={[{ required: true, message: "Scope required" }]}
            tooltip={t("adminsecuritycrowdsec.country_bans_rely_on_the_geoip_enricher_as_b")}
          >
            <Select options={ADD_SCOPE_OPTIONS} />
          </Form.Item>
          <Form.Item
            noStyle
            shouldUpdate={(prev, next) => prev.scope !== next.scope}
          >
            {({ getFieldValue }) => {
              const s: CrowdsecScope = getFieldValue("scope") ?? "ip";
              const config: Record<
                CrowdsecScope,
                { label: string; placeholder: string; help: string; pattern: RegExp; msg: string }
              > = {
                ip: {
                  label: "IP address",
                  placeholder: "203.0.113.7",
                  help: "Single IPv4 or IPv6 address.",
                  pattern: IP_OR_CIDR,
                  msg: "Must be a valid IP address",
                },
                range: {
                  label: "CIDR range",
                  placeholder: "203.0.113.0/24",
                  help: "CIDR block. /24 is 256 addresses; /32 matches a single IP.",
                  pattern: IP_OR_CIDR,
                  msg: "Must be a valid CIDR (e.g. 203.0.113.0/24)",
                },
                country: {
                  label: "Country code",
                  placeholder: "IL",
                  help: "Two-letter ISO 3166-1 alpha-2 code (RU, CN, IR, …). Requires GeoIP enricher.",
                  pattern: COUNTRY_CODE,
                  msg: "Two-letter country code",
                },
                as: {
                  label: "ASN",
                  placeholder: "AS64500",
                  help: "Autonomous System number, with or without the AS prefix.",
                  pattern: ASN_RE,
                  msg: "ASN number (e.g. 64500 or AS64500)",
                },
              };
              const c = config[s];
              return (
                <Form.Item
                  name="value"
                  label={c.label}
                  extra={c.help}
                  rules={[
                    { required: true, message: `${c.label} required` },
                    { pattern: c.pattern, message: c.msg },
                  ]}
                >
                  <Input placeholder={c.placeholder} autoComplete="off" />
                </Form.Item>
              );
            }}
          </Form.Item>
          <Form.Item
            name="duration"
            label={t("adminsecuritycrowdsec.duration")}
            initialValue="4h"
            rules={[
              { required: true, message: "Duration required" },
              { pattern: DURATION, message: 'Use Go duration syntax: "30m", "4h", "1h30m"' },
            ]}
          >
            <Input placeholder="4h" autoComplete="off" />
          </Form.Item>
          <Form.Item
            name="reason"
            label={t("adminsecuritycrowdsec.reason")}
            rules={[
              { required: true, message: "Reason required" },
              { min: 3, max: 200, message: "3..200 characters" },
            ]}
          >
            <Input placeholder="manual ban — repeated login abuse" autoComplete="off" />
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
};

// AppSecGeoblockCard — server-wide L7 country allow/deny list applied
// by CrowdSec AppSec's pre-evaluation hook (GeoIPEnrich(...).IsoCode).
// See https://doc.crowdsec.net/docs/next/appsec/rules_examples/#5-geoblocking.
// Unlike decisions (L3/L4 firewall-bouncer), this operates on HTTP
// requests reaching nginx + gets a 403 with a DropRequest("Forbidden
// Country") reason. Operator must wire nginx to CrowdSec's AppSec
// endpoint for enforcement — see plans/m26-security-tab-runbook.md.
const AppSecGeoblockCard = () => {
  const { t } = useTranslation();
  const geoblock = useAppSecGeoblock();
  const updateGeoblock = useUpdateAppSecGeoblock();

  const [mode, setMode] = useState<AppSecGeoblockMode>("off");
  const [countries, setCountries] = useState<string[]>([]);

  // Pre-built option set for the country Select. Memoised once at module
  // load — ISO3166_COUNTRIES is a frozen literal so the .map is cheap
  // either way, but useMemo here keeps Select.options stable across
  // renders (helps AntD virtualisation cache).
  const countryOptions = useMemo(
    () =>
      ISO3166_COUNTRIES.map((c) => ({
        value: c.code,
        label: `${c.flag}  ${c.name} (${c.code})`,
        searchKey: `${c.name} ${c.code}`.toLowerCase(),
      })),
    [],
  );

  useEffect(() => {
    if (geoblock.data) {
      setMode(geoblock.data.mode);
      setCountries(geoblock.data.countries);
    }
  }, [geoblock.data]);

  const dirty =
    geoblock.data !== undefined &&
    (mode !== geoblock.data.mode ||
      countries.join(",") !== geoblock.data.countries.join(","));

  const apply = async () => {
    try {
      await updateGeoblock.mutateAsync({ mode, countries });
      message.success("AppSec geoblock updated and crowdsec reloaded");
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to apply geoblock");
    }
  };

  return (
    <Card size="small" title={t("adminsecuritycrowdsec.appsec_geoblock_server_wide")} loading={geoblock.isLoading}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          HTTP-layer country filter applied by CrowdSec AppSec. Blocks with
          403 at the nginx edge; complementary to IP/range bans (L3). Needs
          the GeoIP enricher and nginx → AppSec wiring — see runbook.
        </Typography.Paragraph>
        <div>
          <Typography.Text strong>Mode: </Typography.Text>
          <Segmented
            value={mode}
            onChange={(v) => setMode(v as "off" | "allow" | "deny")}
            options={[
              { label: "Off", value: "off" },
              { label: "Allow-list", value: "allow" },
              { label: "Deny-list", value: "deny" },
            ]}
          />
        </div>
        {mode !== "off" && (
          <div>
            <Typography.Text strong>Countries: </Typography.Text>
            <Select<string[]>
              mode="multiple"
              style={{ width: "100%", maxWidth: 720 }}
              placeholder={t("adminsecuritycrowdsec.type_a_country_name_or_code_or_pick_from_the")}
              value={countries}
              onChange={(next) =>
                setCountries(
                  next
                    .map((c) => c.toUpperCase().trim())
                    .filter((c) => /^[A-Z]{2}$/.test(c)),
                )
              }
              options={countryOptions}
              showSearch
              optionFilterProp="searchKey"
              filterOption={(input, opt) =>
                (opt?.searchKey ?? "").includes(input.toLowerCase())
              }
              maxTagCount="responsive"
              allowClear
            />
          </div>
        )}
        {mode === "allow" && countries.length === 0 && (
          <Alert
            type="warning"
            showIcon
            message={t("adminsecuritycrowdsec.allow_list_with_no_countries_blocks_every_re")}
          />
        )}
        {mode === "deny" && countries.length === 0 && (
          <Alert
            type="warning"
            showIcon
            message={t("adminsecuritycrowdsec.deny_list_with_no_countries_has_no_effect_ad")}
          />
        )}
        <Space>
          <Popconfirm
            title={t("adminsecuritycrowdsec.apply_appsec_geoblock")}
            description={
              mode === "off"
                ? "Disables the server-wide country filter. Requests from any country pass AppSec."
                : `${mode === "allow" ? "Allow-list" : "Deny-list"} mode with ${countries.length} ${
                    countries.length === 1 ? "country" : "countries"
                  }. CrowdSec is reloaded (SIGHUP) — no traffic drops.`
            }
            okText={t("adminsecuritycrowdsec.apply")}
            onConfirm={apply}
            disabled={!dirty || updateGeoblock.isPending}
          >
            <Button
              type="primary"
              disabled={!dirty}
              loading={updateGeoblock.isPending}
            >
              Apply
            </Button>
          </Popconfirm>
          {dirty && (
            <Button
              onClick={() => {
                if (geoblock.data) {
                  setMode(geoblock.data.mode);
                  setCountries(geoblock.data.countries);
                }
              }}
            >
              Reset
            </Button>
          )}
        </Space>
      </Space>
    </Card>
  );
};

// AllowlistsCard — server-wide IP/CIDR never-ban list (M27 Step 2,
// ADR-0061). LAPI is truth; jabali shells to cscli via the agent. Table
// + Drawer-for-add follows docs/CONVENTIONS.md — Drawer not Modal, `Table.Column`
// children, `destroyOnClose`.
type AllowlistFormValues = {
  value: string;
  reason: string;
};

const ALLOWLIST_IP_OR_CIDR = /^[0-9a-fA-F:.]+(\/\d{1,3})?$/;

// BlocklistsCard — community blocklists currently contributing active
// decisions to this engine. Subscriptions live at app.crowdsec.net.
const BlocklistsCard = () => {
  const { t } = useTranslation();
  const q = useCrowdsecBlocklists();
  const refresh = useRefreshCrowdsecBlocklists();
  const data = q.data?.blocklists ?? [];
  const total = q.data?.total ?? 0;

  return (
    <Card
      title={t("adminsecuritycrowdsec.active_decision_sources")}
      extra={
        <Space size={12}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            Total blocked: {total.toLocaleString()}
          </Typography.Text>
          <Button
            size="small"
            icon={<ReloadOutlined />}
            loading={refresh.isPending}
            onClick={() => refresh.mutate()}
          >
            Refresh
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        message={t("adminsecuritycrowdsec.aggregates_every_active_decision_by_origin_s")}
        style={{ marginBottom: 12 }}
      />
      <Table<{ name: string; count: number; latest_end: string }>
        size="small"
        loading={q.isPending}
        rowKey="name"
        dataSource={data}
        pagination={false}
        locale={{ emptyText: "No active decisions on this engine yet." }}
        scroll={{ x: "max-content" }}
        columns={[
          {
            title: "Blocklist",
            dataIndex: "name",
            key: "name",
            render: (v: string) => <Typography.Text code>{v}</Typography.Text>,
          },
          {
            title: "Active decisions",
            dataIndex: "count",
            key: "count",
            align: "right",
            render: (v: number) => v.toLocaleString(),
          },
          {
            title: "Latest expiry",
            dataIndex: "latest_end",
            key: "latest_end",
            render: (v: string) => (v ? new Date(v).toLocaleString() : "—"),
          },
        ]}
      />
    </Card>
  );
};

const AllowlistsCard = () => {
  const { t } = useTranslation();
  const allowlists = useCrowdsecAllowlists();
  const [allowlistQuery, setAllowlistQuery] = useState("");
  const addEntry = useAddCrowdsecAllowlist();
  const removeEntry = useRemoveCrowdsecAllowlist();
  const [addOpen, setAddOpen] = useState(false);
  const [form] = Form.useForm<AllowlistFormValues>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const onSubmit = async (values: AllowlistFormValues) => {
    try {
      await addEntry.mutateAsync(values);
      message.success(`Allowlisted ${values.value}`);
      setAddOpen(false);
      form.resetFields();
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to allowlist");
    }
  };

  const onRemove = async (row: CrowdsecAllowlistEntry) => {
    try {
      await removeEntry.mutateAsync(row.value);
      message.success(`Removed ${row.value} from allowlist`);
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Failed to remove");
    }
  };

  return (
    <>
      <Card
        size="small"
        title={t("adminsecuritycrowdsec.allowlist_never_ban")}
        extra={
          <Button type="primary" size="small" onClick={() => setAddOpen(true)}>
            Add to allowlist
          </Button>
        }
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("adminsecuritycrowdsec.allowlisted_ips_bypass_every_scenario_decisi")}
        />
        <SearchableTableStringQ<CrowdsecAllowlistEntry>
          onSearchChange={setAllowlistQuery}
          searchPlaceholder="Search IP or reason…"
          rowKey="value"
          dataSource={(allowlists.data ?? []).filter(
            (e) =>
              !allowlistQuery ||
              e.value.toLowerCase().includes(allowlistQuery.toLowerCase()) ||
              (e.reason ?? "").toLowerCase().includes(allowlistQuery.toLowerCase()),
          )}
          loading={allowlists.isLoading}
          pagination={{ pageSize: 10, showSizeChanger: false }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecuritycrowdsec.no_allowlist_entries")} /> }}
        >
          <Table.Column<CrowdsecAllowlistEntry>
            dataIndex="value"
            title={t("adminsecuritycrowdsec.ip_or_cidr")}
            key="value"
            render={(v: string) => <Typography.Text code>{v}</Typography.Text>}
          />
          <Table.Column<CrowdsecAllowlistEntry>
            dataIndex="reason"
            title={t("adminsecuritycrowdsec.reason")}
            key="reason"
          />
          <Table.Column<CrowdsecAllowlistEntry>
            dataIndex="created_at"
            title={t("adminsecuritycrowdsec.added")}
            key="created_at"
            render={(s: string) => fmtTime(s)}
          />
          <Table.Column<CrowdsecAllowlistEntry>
            title=""
            key="delete"
            width={90}
            render={(_, row) => (
              <Popconfirm
                title={t("adminsecuritycrowdsec.remove_from_allowlist")}
                description={`${row.value} will be subject to scenarios and decisions again.`}
                okText={t("adminsecuritycrowdsec.remove")}
                okButtonProps={{ danger: true }}
                cancelText={t("adminsecuritycrowdsec.cancel")}
                onConfirm={() => onRemove(row)}
              >
                <Button danger type="text" size="small">
                  Remove
                </Button>
              </Popconfirm>
            )}
          />
        </SearchableTableStringQ>
      </Card>

      <Drawer
        title={t("adminsecuritycrowdsec.add_to_allowlist")}
        open={addOpen}
        onClose={() => setAddOpen(false)}
        width={isDesktop ? 520 : undefined}
        placement="right"
        destroyOnClose
        extra={
          <Space>
            <Button onClick={() => setAddOpen(false)}>Cancel</Button>
            <Button type="primary" loading={addEntry.isPending} onClick={() => form.submit()}>
              Add
            </Button>
          </Space>
        }
      >
        <Form<AllowlistFormValues> form={form} layout="vertical" onFinish={onSubmit}>
          <Form.Item
            name="value"
            label={t("adminsecuritycrowdsec.ip_or_cidr")}
            rules={[
              { required: true, message: "Value required" },
              { pattern: ALLOWLIST_IP_OR_CIDR, message: "Must be a valid IP or CIDR" },
            ]}
            tooltip={t("adminsecuritycrowdsec.single_ipv4_ipv6_address_or_cidr_block_e_g_1")}
          >
            <Input placeholder="192.0.2.1 or 10.0.0.0/24" />
          </Form.Item>
          <Form.Item
            name="reason"
            label={t("adminsecuritycrowdsec.reason")}
            rules={[
              { required: true, message: "Reason required" },
              { min: 3, max: 200, message: "Reason must be 3..200 chars" },
            ]}
          >
            <Input placeholder="e.g. office LAN, CI runner" />
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
};

// AlertsCard — read-only list of CrowdSec scenario fires. Row click
// opens a Drawer with the full alert detail (events + decisions). No
// mutations; upstream caps to 100/24h server-side (M27 Step 3).
type AlertDetail = {
  id?: number;
  scenario?: string;
  source?: { ip?: string; scope?: string; value?: string; cn?: string; as_name?: string };
  events?: Array<{ timestamp?: string; meta?: Array<{ key: string; value: string }> }>;
  decisions?: Array<{ type?: string; value?: string; duration?: string; scenario?: string }>;
  start_at?: string;
  stop_at?: string;
  events_count?: number;
  machine_id?: string;
};

const AlertsCard = () => {
  const { t } = useTranslation();
  const alerts = useCrowdsecAlerts();
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const detail = useCrowdsecAlert(selectedId);
  const alert = detail.data as AlertDetail | undefined;

  return (
    <>
      <Card size="small" title={t("adminsecuritycrowdsec.alerts_last_24h")}>
        <Table<CrowdsecAlert>
          rowKey="id"
          dataSource={alerts.data ?? []}
          loading={alerts.isLoading}
          pagination={{ pageSize: 20, showSizeChanger: false }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecuritycrowdsec.no_alerts_in_the_last_24h")} /> }}
          scroll={{ x: "max-content" }}
          onRow={(row) => ({
            onClick: () => setSelectedId(row.id),
            style: { cursor: "pointer" },
          })}
        >
          <Table.Column<CrowdsecAlert> dataIndex="scenario" title={t("adminsecuritycrowdsec.scenario")} key="scenario" />
          <Table.Column<CrowdsecAlert>
            title={t("adminsecuritycrowdsec.source")}
            key="source"
            render={(_, row) => (
              <Space size="small">
                <Typography.Text code>{row.source_ip || row.source_value || "—"}</Typography.Text>
                {row.source_scope && <Tag>{row.source_scope}</Tag>}
              </Space>
            )}
          />
          <Table.Column<CrowdsecAlert>
            dataIndex="events_count"
            title={t("adminsecuritycrowdsec.events")}
            key="events_count"
            width={100}
          />
          <Table.Column<CrowdsecAlert>
            dataIndex="decisions_count"
            title={t("adminsecuritycrowdsec.decisions")}
            key="decisions_count"
            width={110}
          />
          <Table.Column<CrowdsecAlert>
            dataIndex="started_at"
            title={t("adminsecuritycrowdsec.started")}
            key="started_at"
            render={(s: string) => fmtTime(s)}
          />
          <Table.Column<CrowdsecAlert>
            dataIndex="machine_id"
            title={t("adminsecuritycrowdsec.machine")}
            key="machine_id"
            render={(s: string) => <Typography.Text type="secondary">{s || "—"}</Typography.Text>}
          />
        </Table>
      </Card>

      <Drawer
        title={alert?.scenario ? `Alert: ${alert.scenario}` : "Alert detail"}
        open={selectedId !== null}
        onClose={() => setSelectedId(null)}
        width={720}
        placement="right"
        destroyOnClose
      >
        {detail.isLoading ? (
          <Typography.Text type="secondary">Loading…</Typography.Text>
        ) : alert ? (
          <Space direction="vertical" size="large" style={{ width: "100%" }}>
            <Descriptions
              column={1}
              size="small"
              items={[
                { key: "scenario", label: "Scenario", children: alert.scenario ?? "—" },
                {
                  key: "source",
                  label: "Source",
                  children: (
                    <Space size="small" wrap>
                      <Typography.Text code>{alert.source?.ip ?? alert.source?.value ?? "—"}</Typography.Text>
                      {alert.source?.scope && <Tag>{alert.source.scope}</Tag>}
                      {alert.source?.cn && <Tag color="blue">{alert.source.cn}</Tag>}
                    </Space>
                  ),
                },
                { key: "events", label: "Events count", children: String(alert.events_count ?? 0) },
                { key: "start", label: "Started", children: fmtTime(alert.start_at) },
                { key: "stop", label: "Stopped", children: fmtTime(alert.stop_at) },
                { key: "machine", label: "Machine", children: alert.machine_id ?? "—" },
              ]}
            />

            <Card size="small" title={t("adminsecuritycrowdsec.decisions_issued")}>
              {(alert.decisions ?? []).length === 0 ? (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecuritycrowdsec.no_decisions_issued")} />
              ) : (
                <Table
                  rowKey={(_, idx) => `d-${idx}`}
                  dataSource={alert.decisions}
                  pagination={false}
                  size="small"
                  scroll={{ x: "max-content" }}
                >
                  <Table.Column dataIndex="type" title={t("adminsecuritycrowdsec.type")} key="type" />
                  <Table.Column dataIndex="value" title={t("adminsecuritycrowdsec.value")} key="value" />
                  <Table.Column dataIndex="duration" title={t("adminsecuritycrowdsec.duration")} key="duration" />
                </Table>
              )}
            </Card>
          </Space>
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecuritycrowdsec.alert_not_found")} />
        )}
      </Drawer>
    </>
  );
};


// CaptchaRemediationCard — hCaptcha / reCAPTCHA / Turnstile credentials
// for crowdsec-nginx-bouncer (M27 Step 5). Secret is write-only —
// GET never returns it; empty secret on PUT means "keep existing."
type CaptchaFormValues = {
  enabled: boolean;
  provider: CrowdsecCaptchaProvider;
  site_key: string;
  secret_key: string;
};

const PROVIDER_OPTIONS = [
  { value: "hcaptcha", label: "hCaptcha" },
  { value: "recaptcha", label: "reCAPTCHA v2" },
  { value: "turnstile", label: "Cloudflare Turnstile" },
];

const CaptchaRemediationCard = () => {
  const { t } = useTranslation();
  const captcha = useCrowdsecCaptcha();
  const update = useUpdateCrowdsecCaptcha();
  const [form] = Form.useForm<CaptchaFormValues>();

  useEffect(() => {
    if (captcha.data) {
      form.setFieldsValue({
        enabled: captcha.data.enabled,
        provider: captcha.data.provider || "hcaptcha",
        site_key: captcha.data.site_key,
        secret_key: "",
      });
    }
  }, [captcha.data, form]);

  const onSubmit = async (values: CaptchaFormValues) => {
    try {
      await update.mutateAsync({
        enabled: values.enabled,
        provider: values.enabled ? values.provider : "",
        site_key: values.site_key,
        secret_key: values.secret_key, // "" = keep existing
      });
      form.setFieldValue("secret_key", "");
      message.success("Captcha settings saved");
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Save failed");
    }
  };

  return (
    <Card
      size="small"
      title={t("adminsecuritycrowdsec.captcha_remediation")}
      loading={captcha.isLoading}
      extra={
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          Requires nginx-bouncer (installed with CrowdSec)
        </Typography.Text>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message={t("adminsecuritycrowdsec.when_enabled_crowdsec_scenarios_flagged_for")}
        description={t("adminsecuritycrowdsec.create_an_hcaptcha_recaptcha_turnstile_site")}
      />
      <Form<CaptchaFormValues>
        form={form}
        layout="vertical"
        onFinish={onSubmit}
        initialValues={{ enabled: false, provider: "hcaptcha", site_key: "", secret_key: "" }}
      >
        <Form.Item
          name="enabled"
          label={t("adminsecuritycrowdsec.enabled")}
          getValueProps={(v) => ({ value: v ? "on" : "off" })}
          normalize={(v) => v === "on"}
        >
          <Segmented
            options={[
              { label: "Off", value: "off" },
              { label: "On", value: "on" },
            ]}
          />
        </Form.Item>
        <Form.Item
          noStyle
          shouldUpdate={(prev, next) => prev.enabled !== next.enabled}
        >
          {({ getFieldValue }) => {
            const enabled = getFieldValue("enabled") as boolean;
            return (
              <>
                <Form.Item
                  name="provider"
                  label={t("adminsecuritycrowdsec.provider")}
                  rules={enabled ? [{ required: true, message: "Provider required" }] : []}
                >
                  <Select disabled={!enabled} options={PROVIDER_OPTIONS} />
                </Form.Item>
                <Form.Item
                  name="site_key"
                  label={t("adminsecuritycrowdsec.site_key_public")}
                  rules={
                    enabled
                      ? [{ required: true, message: "Site key required" }, { max: 512 }]
                      : []
                  }
                >
                  <Input disabled={!enabled} placeholder="publishable site key" />
                </Form.Item>
                <Form.Item
                  name="secret_key"
                  label={t("adminsecuritycrowdsec.secret_key_write_only")}
                  tooltip={t("adminsecuritycrowdsec.leave_blank_to_keep_the_stored_secret_unchan")}
                  rules={[{ max: 512 }]}
                >
                  <Input.Password
                    disabled={!enabled}
                    placeholder={captcha.data?.enabled ? "(unchanged)" : "secret key"}
                    autoComplete="off"
                    visibilityToggle={false}
                  />
                </Form.Item>
              </>
            );
          }}
        </Form.Item>
        <Space>
          <Popconfirm
            title={t("adminsecuritycrowdsec.apply_captcha_settings")}
            description={t("adminsecuritycrowdsec.this_rewrites_etc_crowdsec_bouncers_crowdsec")}
            okText={t("adminsecuritycrowdsec.apply")}
            cancelText={t("adminsecuritycrowdsec.cancel")}
            onConfirm={() => form.submit()}
          >
            <Button type="primary" loading={update.isPending}>
              Save
            </Button>
          </Popconfirm>
        </Space>
      </Form>
    </Card>
  );
};

// ProfilesCard — per-scenario remediation override (M27 Step 6, ADR-0063).
// Row-per-scenario; inline Select for default/captcha/off. Captcha option
// greyed out when captcha_enabled=false (requires Step 5 configured).
type ProfileRow = CrowdsecScenarioItem & {
  override: "default" | "captcha" | "off";
};

const ProfilesCard = () => {
  const { t } = useTranslation();
  const profiles = useCrowdsecProfiles();
  const update = useUpdateCrowdsecProfiles();
  const [draft, setDraft] = useState<Record<string, "default" | "captcha" | "off">>({});

  const rows: ProfileRow[] = (profiles.data?.scenarios ?? []).map((s) => {
    const existing = (profiles.data?.overrides ?? []).find((o) => o.scenario === s.name);
    const fromServer = (existing?.action ?? "default") as ProfileRow["override"];
    const override = draft[s.name] ?? fromServer;
    return { ...s, override };
  });

  const dirty = rows.some((r) => {
    const existing = (profiles.data?.overrides ?? []).find((o) => o.scenario === r.name);
    const fromServer = (existing?.action ?? "default") as ProfileRow["override"];
    return r.override !== fromServer;
  });

  const captchaEnabled = profiles.data?.captcha_enabled ?? false;

  const onApply = async () => {
    const overrides: CrowdsecProfileOverride[] = rows
      .filter((r) => r.override !== "default")
      .map((r) => ({ scenario: r.name, action: r.override as "captcha" | "off" }));
    try {
      await update.mutateAsync(overrides);
      setDraft({});
      message.success("Profiles saved — crowdsec reloaded");
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "Save failed");
    }
  };

  return (
    <Card
      size="small"
      title={t("adminsecuritycrowdsec.per_scenario_remediation_override")}
      loading={profiles.isLoading}
      extra={
        dirty && (
          <Space>
            <Button onClick={() => setDraft({})}>Reset</Button>
            <Popconfirm
              title={t("adminsecuritycrowdsec.apply_overrides")}
              description={t("adminsecuritycrowdsec.rewrites_etc_crowdsec_profiles_yaml_marker_b")}
              okText={t("adminsecuritycrowdsec.apply")}
              cancelText={t("adminsecuritycrowdsec.cancel")}
              onConfirm={onApply}
            >
              <Button type="primary" loading={update.isPending}>
                Apply
              </Button>
            </Popconfirm>
          </Space>
        )
      }
    >
      {!captchaEnabled && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("adminsecuritycrowdsec.captcha_action_requires_the_captcha_remediat")}
        />
      )}
      <Table<ProfileRow>
        rowKey="name"
        dataSource={rows}
        pagination={{ pageSize: 20, showSizeChanger: false }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("adminsecuritycrowdsec.no_scenarios_installed")} /> }}
        tableLayout="fixed"
      >
        <Table.Column<ProfileRow>
          dataIndex="name"
          title={t("adminsecuritycrowdsec.scenario")}
          key="name"
          width={320}
          render={(v: string) => <Typography.Text code>{v}</Typography.Text>}
        />
        <Table.Column<ProfileRow>
          dataIndex="description"
          title={t("adminsecuritycrowdsec.description")}
          key="description"
          ellipsis={{ showTitle: false }}
          render={(v: string) => (
            <Tooltip title={v} placement="topLeft">
              <span>{v}</span>
            </Tooltip>
          )}
        />
        <Table.Column<ProfileRow>
          title={t("adminsecuritycrowdsec.override")}
          key="override"
          width={200}
          render={(_, row) => (
            <Select
              size="small"
              style={{ width: 170 }}
              value={row.override}
              onChange={(v) => setDraft((d) => ({ ...d, [row.name]: v }))}
              options={[
                { value: "default", label: "Default (ban)" },
                { value: "captcha", label: "Captcha", disabled: !captchaEnabled },
                { value: "off", label: "Off (bypass)" },
              ]}
            />
          )}
        />
      </Table>
    </Card>
  );
};

// RecommendedHubCard — curated picker of well-known free CrowdSec Hub
// items. Each entry maps to `cscli <type> install <name>`. Catalog is
// hand-maintained because cscli has no "free vs premium" filter and the
// upstream catalog (Premium/Enterprise blocklists) requires a
// signed-in account; everything below works on a fresh install with no
// enrollment.
type RecommendedItem = {
  type: "collections" | "scenarios" | "parsers" | "appsec-rules";
  name: string;
  title: string;
  description: string;
  category: "core" | "web" | "appsec" | "intel";
};

const RECOMMENDED_HUB_ITEMS: RecommendedItem[] = [
  {
    type: "collections",
    name: "crowdsecurity/linux",
    title: "Linux base",
    description: "syslog, sshd, journald — required base for almost every other collection",
    category: "core",
  },
  {
    type: "collections",
    name: "crowdsecurity/sshd",
    title: "SSH brute-force",
    description: "Detects sshd password brute-force, key rejection floods, slow-rate attacks",
    category: "core",
  },
  {
    type: "collections",
    name: "crowdsecurity/nginx",
    title: "nginx (web access)",
    description: "Parsers + scenarios for nginx access/error logs (HTTP scans, bad-bots, 4xx floods)",
    category: "web",
  },
  {
    type: "collections",
    name: "crowdsecurity/base-http-scenarios",
    title: "Generic HTTP scenarios",
    description: "Crawl detection, path traversal probes, generic HTTP exploits (works with any web server)",
    category: "web",
  },
  {
    type: "collections",
    name: "crowdsecurity/http-cve",
    title: "HTTP CVE detection",
    description: "Known-CVE exploit fingerprints (Log4Shell, Spring4Shell, CVE-2023-* WordPress CVEs)",
    category: "web",
  },
  {
    type: "collections",
    name: "crowdsecurity/wordpress",
    title: "WordPress",
    description: "wp-login brute force, xmlrpc abuse, plugin/theme CVE exploits",
    category: "web",
  },
  {
    type: "collections",
    name: "crowdsecurity/whitelist-good-actors",
    title: "Good-actor whitelist",
    description: "Skip bans for googlebot/bingbot/cloudflare/AWS health probes — reduces false positives",
    category: "intel",
  },
  {
    type: "collections",
    name: "crowdsecurity/appsec-virtual-patching",
    title: "AppSec virtual patching",
    description: "Pre-eval AppSec rules for unpatched CVEs (blocks the request, not the IP). Already shipped by jabali — install adds upstream updates",
    category: "appsec",
  },
  {
    type: "collections",
    name: "crowdsecurity/appsec-generic-rules",
    title: "AppSec generic rules",
    description: "Generic CRS-style patterns (XSS, SQLi, RCE) for nginx-bouncer in-band filtering",
    category: "appsec",
  },
];

const CATEGORY_COLOR: Record<RecommendedItem["category"], string> = {
  core: "geekblue",
  web: "blue",
  appsec: "magenta",
  intel: "green",
};

const RecommendedHubCard = ({
  hub,
}: {
  hub: ReturnType<typeof useCrowdsecHub>;
}) => {
  const { t } = useTranslation();
  const install = useInstallCrowdsecHubItem();
  const remove = useRemoveCrowdsecHubItem();
  const [pending, setPending] = useState<string | null>(null);

  // Index installed items by `<type>:<name>` for O(1) lookup.
  const installedKey = useMemo(() => {
    const set = new Set<string>();
    (hub.data ?? []).forEach((it) => {
      if (it.installed) set.add(`${it.type}:${it.name}`);
    });
    return set;
  }, [hub.data]);

  const onInstall = async (item: RecommendedItem) => {
    setPending(`${item.type}:${item.name}`);
    try {
      await install.mutateAsync({ type: item.type, name: item.name });
      message.success(`Installed ${item.name}`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Install failed");
    } finally {
      setPending(null);
    }
  };

  const onRemove = async (item: RecommendedItem) => {
    setPending(`${item.type}:${item.name}`);
    try {
      await remove.mutateAsync({ type: item.type, name: item.name });
      message.success(`Removed ${item.name}`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Remove failed");
    } finally {
      setPending(null);
    }
  };

  return (
    <Card
      size="small"
      title={t("adminsecuritycrowdsec.recommended_free_blocklists_scenarios")}
      extra={
        <Typography.Link
          href="https://hub.crowdsec.net/"
          target="_blank"
          rel="noopener noreferrer"
        >
          hub.crowdsec.net
        </Typography.Link>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message={t("adminsecuritycrowdsec.one_click_install_of_upstream_crowdsec_hub_i")}
        description={
          <>
            Curated free items from the public Hub. Install runs{" "}
            <Typography.Text code>cscli &lt;type&gt; install &lt;name&gt;</Typography.Text> and reloads
            crowdsec. Premium/Enterprise blocklists (firehol, dshield, etc.) need an account and are
            managed via cscli on the host.
          </>
        }
      />
      <Table<RecommendedItem>
        rowKey={(r) => `${r.type}:${r.name}`}
        dataSource={RECOMMENDED_HUB_ITEMS}
        pagination={false}
        size="small"
        tableLayout="fixed"
      >
        <Table.Column<RecommendedItem>
          title={t("adminsecuritycrowdsec.item")}
          key="title"
          width={260}
          render={(_, row) => (
            <Space direction="vertical" size={0}>
              <Space size={6} wrap>
                <Typography.Text strong>{row.title}</Typography.Text>
                <Tag color={CATEGORY_COLOR[row.category]}>{row.category}</Tag>
                {installedKey.has(`${row.type}:${row.name}`) && (
                  <Tag color="green">installed</Tag>
                )}
              </Space>
              <Typography.Text code style={{ fontSize: 12 }}>
                {row.name}
              </Typography.Text>
            </Space>
          )}
        />
        <Table.Column<RecommendedItem>
          title={t("adminsecuritycrowdsec.description")}
          dataIndex="description"
          key="description"
          ellipsis={{ showTitle: false }}
          render={(v: string) => (
            <Tooltip title={v} placement="topLeft">
              <Typography.Text type="secondary">{v}</Typography.Text>
            </Tooltip>
          )}
        />
        <Table.Column<RecommendedItem>
          title=""
          key="action"
          width={130}
          align="right"
          render={(_, row) => {
            const key = `${row.type}:${row.name}`;
            const isInstalled = installedKey.has(key);
            const busy = pending === key;
            return isInstalled ? (
              <Popconfirm
                title={`Remove ${row.name}?`}
                okText={t("adminsecuritycrowdsec.remove")}
                okButtonProps={{ danger: true }}
                onConfirm={() => onRemove(row)}
              >
                <Button size="small" danger loading={busy}>
                  Remove
                </Button>
              </Popconfirm>
            ) : (
              <Button size="small" type="primary" loading={busy} onClick={() => onInstall(row)}>
                Install
              </Button>
            );
          }}
        />
      </Table>
    </Card>
  );
};


// RemediationComponentsCard — rich per-bouncer panel for the engine
// Overview tab. Merges the standalone Bouncers tab into Overview to
// mirror CrowdSec Console's "Remediation components" block: one row
// per bouncer with type icon, name, version (when surfaced by cscli),
// status freshness colour-coded by last_pull age, and a one-line
// description of what the bouncer enforces.
//
// Reads from /admin/security/crowdsec/bouncers via useCrowdsecBouncers.
// firewall = nft set drops (L3/L4); nginx = AppSec HTTP body inspect
// (L7). AppSec-only bouncers never call /v1/decisions/stream so they
// have no last_pull — those render with a Tag instead of a timestamp.
const bouncerTypeMeta = (b: { name: string; type: string }) => {
  const lower = (b.name + " " + b.type).toLowerCase();
  if (lower.includes("nginx") || lower.includes("appsec")) {
    return {
      kind: "appsec" as const,
      label: "nginx + AppSec",
      icon: <SafetyOutlined />,
      detail:
        "Inspects every HTTP request body via 127.0.0.1:7422 (CRS + vpatch rules). Returns 403 on match; doesn't pull /v1/decisions/stream because L7 decisions live in-process.",
    };
  }
  if (lower.includes("firewall") || lower.includes("nft")) {
    return {
      kind: "firewall" as const,
      label: "nftables firewall",
      icon: <ThunderboltOutlined />,
      detail:
        "Pulls /v1/decisions/stream every 60s and writes IP bans into the crowdsec / crowdsec6 nft sets. Drops at PREROUTING priority -10 (before INPUT).",
    };
  }
  return {
    kind: "other" as const,
    label: b.type || "remediation",
    icon: <ApiOutlined />,
    detail: "Custom bouncer registered via `cscli bouncers add`.",
  };
};

const lastPullStatus = (lastPull: string | undefined, kind: string) => {
  if (kind === "appsec") {
    return { tint: "#1677ff", text: "L7 inline", explain: "AppSec bouncers don't poll — every nginx request is forwarded to the engine inline." };
  }
  if (!lastPull) {
    return { tint: "#cf1322", text: "never", explain: "No successful pull recorded since registration. Check api_url + api_key in bouncer config." };
  }
  const ageMs = Date.now() - new Date(lastPull).getTime();
  if (ageMs < 5 * 60_000) return { tint: "#52c41a", text: "healthy", explain: "Last LAPI pull within 5 min — bouncer is keeping the firewall set fresh." };
  if (ageMs < 60 * 60_000) return { tint: "#faad14", text: "lagging", explain: "Pulled >5min ago. Expected within `update_frequency` (60s default after PR #155)." };
  return { tint: "#cf1322", text: "stale", explain: "Pulled >1h ago. Bouncer may be down or LAPI unreachable." };
};

const RemediationComponentsCard = () => {
  const { t } = useTranslation();
  const bouncers = useCrowdsecBouncers();
  const rows = bouncers.data ?? [];
  return (
    <Card
      size="small"
      title={
        <Space>
          <SafetyOutlined />
          <span>Remediation components</span>
        </Space>
      }
      loading={bouncers.isLoading}
      extra={
        <Typography.Text type="secondary">
          {rows.length} {rows.length === 1 ? "bouncer" : "bouncers"}
        </Typography.Text>
      }
    >
      {rows.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={t("adminsecuritycrowdsec.no_bouncers_registered_cscli_bouncers_add")}
        />
      ) : (
        <Row gutter={[12, 12]}>
          {rows.map((b) => {
            const meta = bouncerTypeMeta(b);
            const status = lastPullStatus(b.last_pull, meta.kind);
            return (
              <Col xs={24} md={12} key={b.name}>
                <Card
                  size="small"
                  style={{
                    borderLeft: `3px solid ${status.tint}`,
                  }}
                  styles={{ body: { padding: 12 } }}
                >
                  <Space
                    align="start"
                    style={{ width: "100%", justifyContent: "space-between" }}
                  >
                    <Space align="start">
                      <span style={{ fontSize: 20, color: status.tint }}>
                        {meta.icon}
                      </span>
                      <Space direction="vertical" size={0}>
                        <Typography.Text strong style={{ fontSize: 13 }}>
                          {b.name}
                        </Typography.Text>
                        <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                          {meta.label}
                        </Typography.Text>
                      </Space>
                    </Space>
                    <Space direction="vertical" size={0} align="end">
                      {b.revoked ? (
                        <Tag color="red">revoked</Tag>
                      ) : (
                        <Tooltip title={status.explain}>
                          <Tag color={status.tint === "#52c41a" ? "green" : status.tint === "#faad14" ? "orange" : status.tint === "#1677ff" ? "blue" : "red"}>
                            {status.text}
                          </Tag>
                        </Tooltip>
                      )}
                      <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                        {b.last_pull ? fmtTime(b.last_pull) : "—"}
                      </Typography.Text>
                    </Space>
                  </Space>
                  <Typography.Paragraph
                    type="secondary"
                    style={{ marginTop: 8, marginBottom: 0, fontSize: 12 }}
                  >
                    {meta.detail}
                  </Typography.Paragraph>
                </Card>
              </Col>
            );
          })}
        </Row>
      )}
    </Card>
  );
};

// EngineIdentityCard — engine-fingerprint header for the CrowdSec
// Overview tab. Mirrors the "Security engine «hostname»" identity
// block from the CrowdSec Console screenshot: hostname, OS, IP,
// machine ID, last activity. Reads from /admin/security/crowdsec/
// status (extended PR #160 with hostname/os/started/machine_id/
// last_heartbeat fields) and server-settings public IP.
// GH #716: recent CrowdSec config changes from the audit trail (hub install/
// remove, captcha/geoblock/allowlist/blocklist/profile edits — everything on
// the /admin/security/crowdsec/* routes is recorded by the audit middleware).
type SecAuditEvent = {
  id: string;
  ts: string;
  action: string;
  target_id: string;
  actor_kind: string;
  result: string;
};
const RecentChangesCard = () => {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["security", "crowdsec", "recent-changes"],
    queryFn: async () =>
      (
        await apiClient.get<{ data: SecAuditEvent[] }>(
          "/admin/audit?q=crowdsec&page_size=15",
        )
      ).data.data,
  });
  return (
    <Card size="small" title={t("adminsecuritycrowdsec.recent_changes_audit")}>
      <Table<SecAuditEvent>
        size="small"
        pagination={false}
        scroll={{ x: "max-content" }}
        rowKey="id"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        locale={{ emptyText: "No recent CrowdSec config changes recorded" }}
        columns={[
          { title: "When", dataIndex: "ts", width: 170, render: (v: string) => new Date(v).toLocaleString() },
          { title: "Action", dataIndex: "action", ellipsis: true },
          { title: "Target", dataIndex: "target_id", ellipsis: true, render: (v: string) => v || "—" },
          { title: "By", dataIndex: "actor_kind", width: 90 },
          { title: "Result", dataIndex: "result", width: 90, render: (v: string) => <Tag color={v === "ok" ? "green" : v === "denied" ? "red" : "orange"}>{v}</Tag> },
        ]}
      />
    </Card>
  );
};

// GH #716: at-a-glance health cards — pass/fail per enforcement layer.
const EngineIdentityCard = () => {
  const { t } = useTranslation();
  const status = useCrowdsecStatus();
  const metrics = useCrowdsecMetrics();
  const settings = useQuery({
    queryKey: ["admin-settings-public-ip"],
    queryFn: async () => {
      const r = await apiClient.get<{ public_ipv4?: string; hostname?: string }>(
        "/admin/settings",
      );
      return r.data;
    },
  });
  const hostname = status.data?.hostname ?? settings.data?.hostname ?? "—";
  const version = status.data?.version ?? "—";
  const startedAt = status.data?.started_at;
  const lastHeartbeat = status.data?.last_heartbeat;
  const healthy =
    !!status.data?.running && !!status.data?.lapi_reachable;

  return (
    <Card size="small" loading={status.isLoading}>
      <Space direction="vertical" size="small" style={{ width: "100%" }}>
        <Flex gap="middle" align="flex-start" wrap>
          <SafetyOutlined style={{ fontSize: 28, color: healthy ? "#52c41a" : "#cf1322" }} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <Typography.Title level={4} style={{ margin: 0, wordBreak: "break-word" }}>
              Security engine «{hostname}»
            </Typography.Title>
            <Space size="small" wrap>
              {version !== "—" && <Tag color="blue">{version}</Tag>}
              <Tag color={status.data?.running ? "green" : "red"}>
                {status.data?.running ? "running" : "down"}
              </Tag>
              <Tag color={status.data?.lapi_reachable ? "green" : "red"}>
                LAPI {status.data?.lapi_reachable ? "ok" : "down"}
              </Tag>
              <Tag color={status.data?.capi_reachable ? "green" : "default"}>
                CAPI {status.data?.capi_reachable ? "ok" : "offline"}
              </Tag>
              <Tag color={status.data?.config_valid ? "green" : "red"}>
                config {status.data?.config_valid ? "valid" : "invalid"}
              </Tag>
              <Tag color={(status.data?.bouncer_count ?? 0) > 0 ? "green" : "orange"}>
                {status.data?.bouncer_count ?? 0} bouncer
                {(status.data?.bouncer_count ?? 0) === 1 ? "" : "s"}
              </Tag>
            </Space>
          </div>
        </Flex>

        {status.data && status.data.config_valid === false && status.data.config_valid_detail ? (
          <Alert
            type="error"
            showIcon
            message={t("adminsecuritycrowdsec.config_validation_failed_crowdsec_t")}
            description={
              <pre style={{ whiteSpace: "pre-wrap", margin: 0 }}>
                {status.data.config_valid_detail}
              </pre>
            }
          />
        ) : null}

        <Row gutter={[16, 8]}>
          <Col xs={24} md={12} lg={8}>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>
              Last heartbeat
            </Typography.Text>
            <div style={{ fontSize: 13 }}>
              {lastHeartbeat ? fmtTime(lastHeartbeat) : "—"}
            </div>
          </Col>
          {startedAt && (
            <Col xs={24} md={12} lg={16}>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                Daemon uptime since
              </Typography.Text>
              <div style={{ fontSize: 13 }}>{startedAt}</div>
            </Col>
          )}
        </Row>

        <Row gutter={[16, 8]} style={{ marginTop: 4 }}>
          <Col xs={12} md={8} lg={4}>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>
              Parsed events
            </Typography.Text>
            <div style={{ fontSize: 16, fontWeight: 600 }}>
              {(metrics.data?.parsed ?? 0).toLocaleString()}
            </div>
          </Col>
          <Col xs={12} md={8} lg={4}>
            <Tooltip title={t("adminsecuritycrowdsec.lines_no_parser_matched_gaps_in_coverage")}>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                Unparsed
              </Typography.Text>
            </Tooltip>
            <div
              style={{
                fontSize: 16,
                fontWeight: 600,
                color: (metrics.data?.unparsed ?? 0) > 0 ? "#faad14" : undefined,
              }}
            >
              {(metrics.data?.unparsed ?? 0).toLocaleString()}
            </div>
          </Col>
          <Col xs={12} md={8} lg={4}>
            <Tooltip title={t("adminsecuritycrowdsec.scenario_thresholds_tripped_suspicious_patte")}>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                Buckets fired
              </Typography.Text>
            </Tooltip>
            <div style={{ fontSize: 16, fontWeight: 600, color: "#722ed1" }}>
              {(metrics.data?.buckets ?? 0).toLocaleString()}
            </div>
          </Col>
          <Col xs={12} md={8} lg={4}>
            <Tooltip title={t("adminsecuritycrowdsec.ips_currently_banned_under_captcha")}>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                Active decisions
              </Typography.Text>
            </Tooltip>
            <div
              style={{
                fontSize: 16,
                fontWeight: 600,
                color: (metrics.data?.decisions_active ?? 0) > 0 ? "#cf1322" : undefined,
              }}
            >
              {(metrics.data?.decisions_active ?? 0).toLocaleString()}
            </div>
          </Col>
          <Col xs={12} md={8} lg={4}>
            <Tooltip title={t("adminsecuritycrowdsec.all_time_alerts_since_crowdsec_started")}>
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                Total alerts
              </Typography.Text>
            </Tooltip>
            <div style={{ fontSize: 16, fontWeight: 600, color: "#13c2c2" }}>
              {(metrics.data?.alerts_total ?? 0).toLocaleString()}
            </div>
          </Col>
        </Row>
      </Space>
    </Card>
  );
};

// TopSourcesCard — top N source IPs (or country/AS for non-IP scopes)
// by alert count over a 24h/7d/30d window. Mirrors the "Top attackers"
// view from CrowdSec Console. Reads from /admin/security/crowdsec/
// alerts/top-sources which the agent aggregates server-side so the
// payload is bounded regardless of alert volume.
type TopSourceRow = {
  value: string;
  scope: string;
  count: number;
  scenarios: string[];
};

const TopSourcesCard = () => {
  const [since, setSince] = useState<"24h" | "7d" | "30d">("24h");
  const q = useQuery({
    queryKey: ["cs-alerts-top-sources", since],
    queryFn: async () => {
      const r = await apiClient.get<{
        items: TopSourceRow[];
        since: string;
        limit: number;
      }>(`/admin/security/crowdsec/alerts/top-sources?since=${since}&limit=10`);
      return r.data;
    },
    refetchInterval: 60_000,
  });
  const rows = q.data?.items ?? [];
  const maxCount = rows.reduce((acc, r) => (r.count > acc ? r.count : acc), 0);

  return (
    <Card
      size="small"
      title={
        <Space>
          <SafetyOutlined />
          <span>Top source IPs</span>
        </Space>
      }
      extra={
        <Segmented
          size="small"
          value={since}
          onChange={(v) => setSince(v as "24h" | "7d" | "30d")}
          options={[
            { label: "24h", value: "24h" },
            { label: "7d", value: "7d" },
            { label: "30d", value: "30d" },
          ]}
        />
      }
      loading={q.isLoading}
    >
      {rows.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={`No alerts in last ${since}`}
        />
      ) : (
        <Space direction="vertical" size={6} style={{ width: "100%" }}>
          {rows.map((r, i) => {
            const pct = maxCount > 0 ? Math.round((r.count / maxCount) * 100) : 0;
            return (
              <div key={`${r.scope}:${r.value}`}>
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 8,
                    fontSize: 13,
                  }}
                >
                  <Typography.Text type="secondary" style={{ width: 24 }}>
                    #{i + 1}
                  </Typography.Text>
                  <Typography.Text
                    strong
                    style={{ flex: "1 1 auto", fontFamily: "monospace" }}
                    copyable={{ text: r.value }}
                  >
                    {r.value}
                  </Typography.Text>
                  <Tag style={{ marginRight: 0 }}>{r.scope}</Tag>
                  <Tooltip
                    title={
                      r.scenarios.length > 0 ? (
                        <ul style={{ paddingLeft: 16, margin: 0 }}>
                          {r.scenarios.map((sc) => (
                            <li key={sc}>{sc}</li>
                          ))}
                        </ul>
                      ) : (
                        "no scenario context"
                      )
                    }
                  >
                    <Tag color="blue">{r.count}</Tag>
                  </Tooltip>
                </div>
                <div
                  style={{
                    height: 4,
                    background: "var(--ant-color-fill-tertiary, rgba(0,0,0,0.06))",
                    borderRadius: 2,
                    overflow: "hidden",
                    marginLeft: 32,
                  }}
                >
                  <div
                    style={{
                      height: "100%",
                      width: `${pct}%`,
                      background: "var(--ant-color-primary, #1677ff)",
                      transition: "width 200ms ease",
                    }}
                  />
                </div>
              </div>
            );
          })}
        </Space>
      )}
    </Card>
  );
};

// AlertsOverTimeCard — engine-dashboard chart matching CrowdSec
// Console's "Alerts over time" panel. Reads from
// /admin/security/crowdsec/alerts/timeseries which the agent
// bucket-counts (hour buckets for 24h window, day buckets for 7d/30d).
// Uses inline-SVG Sparkline (zero chart-lib dep).
const AlertsOverTimeCard = () => {
  const [since, setSince] = useState<"24h" | "7d" | "30d">("7d");
  const q = useQuery({
    queryKey: ["cs-alerts-timeseries", since],
    queryFn: async () => {
      const r = await apiClient.get<{
        buckets: { ts: string; count: number }[];
        bucket_size: string;
        since: string;
      }>(`/admin/security/crowdsec/alerts/timeseries?since=${since}`);
      return r.data;
    },
    refetchInterval: 60_000,
  });
  const points = (q.data?.buckets ?? []).map((b) => ({ x: b.ts, y: b.count }));
  const total = points.reduce((acc, p) => acc + p.y, 0);
  const max = points.reduce((acc, p) => (p.y > acc ? p.y : acc), 0);
  return (
    <Card
      size="small"
      title={
        <Space>
          <ThunderboltOutlined />
          <span>Alerts over time</span>
        </Space>
      }
      extra={
        <Segmented
          size="small"
          value={since}
          onChange={(v) => setSince(v as "24h" | "7d" | "30d")}
          options={[
            { label: "24h", value: "24h" },
            { label: "7d", value: "7d" },
            { label: "30d", value: "30d" },
          ]}
        />
      }
      loading={q.isLoading}
    >
      <Space direction="vertical" size="small" style={{ width: "100%" }}>
        <Space size="large">
          <Typography.Text type="secondary">
            Total: <Typography.Text strong>{total}</Typography.Text>
          </Typography.Text>
          <Typography.Text type="secondary">
            Peak / bucket: <Typography.Text strong>{max}</Typography.Text>
          </Typography.Text>
          <Typography.Text type="secondary">
            Bucket: {q.data?.bucket_size ?? "—"}
          </Typography.Text>
        </Space>
        <div style={{ width: "100%", overflowX: "auto" }}>
          <Sparkline data={points} width={920} height={160} filled />
        </div>
      </Space>
    </Card>
  );
};

// SettingsPanel — operator-facing CrowdSec configuration tab.
// Bundles the sensitivity preset (server-wide ssh-bf + AppSec anomaly
// + ban-duration knobs). Captcha config moved to its own tab
// (co-located with per-scenario picker) because the two are
// procedurally linked.
const SettingsPanel = () => (
  <Space direction="vertical" size="large" style={{ width: "100%" }}>
    <SensitivityCard />
    <BouncerModeCard />
    <LoginAllowlistCard />
  </Space>
);

// CaptchaPanel — stacks the captcha credentials card on top of the
// per-scenario remediation override picker. Co-located because the
// per-scenario "Captcha" action is grey-disabled until the
// credentials card has a provider + site/secret key configured;
// keeping them on separate tabs forced operators to bounce between
// Settings and Per-scenario when wiring captcha for the first time.
const CaptchaPanel = () => (
  <Space direction="vertical" size="large" style={{ width: "100%" }}>
    <CaptchaRemediationCard />
    <ProfilesCard />
  </Space>
);

// SensitivityCard — server-wide CrowdSec sensitivity preset. Writes
// /etc/crowdsec/{scenarios,appsec-rules,profiles.d}/jabali-*.yaml via
// the agent's security.crowdsec.sensitivity.apply verb when the admin
// saves. Three presets collapse all three knobs (ssh-bf threshold,
// AppSec anomaly score, ban duration) into one choice.
const SensitivityCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["admin-settings"],
    queryFn: async () => {
      const r = await apiClient.get<{ crowdsec_sensitivity: string }>("/admin/settings");
      return r.data;
    },
  });
  const [level, setLevel] = useState<string>("balanced");
  useEffect(() => {
    if (settings.data?.crowdsec_sensitivity) setLevel(settings.data.crowdsec_sensitivity);
  }, [settings.data]);

  const save = useMutation({
    mutationFn: async (newLevel: string) => {
      await apiClient.patch("/admin/settings", { crowdsec_sensitivity: newLevel });
    },
    onSuccess: () => {
      message.success("Sensitivity preset applied — CrowdSec reloaded");
      qc.invalidateQueries({ queryKey: ["admin-settings"] });
    },
    onError: (e: unknown) => {
      message.error(e instanceof Error ? e.message : "Failed to apply");
    },
  });

  return (
    <Card size="small" title={t("adminsecuritycrowdsec.sensitivity_preset_server_wide")} loading={settings.isLoading}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Single dial that tunes three CrowdSec knobs at once: SSH brute-
          force threshold, AppSec inbound-anomaly score threshold, and
          default ban duration. Pick the posture that matches your traffic.
        </Typography.Paragraph>

        <Radio.Group value={level} onChange={(e) => setLevel(e.target.value)}>
          <Space direction="vertical">
            <Radio value="relaxed">
              <Typography.Text strong>Relaxed</Typography.Text>{" "}
              <Typography.Text type="secondary">
                — SSH brute-force 15 fails / 60s, AppSec anomaly threshold 10, ban 30m. Good for admins doing legitimate noisy work from changing IPs.
              </Typography.Text>
            </Radio>
            <Radio value="balanced">
              <Typography.Text strong>Balanced (default)</Typography.Text>{" "}
              <Typography.Text type="secondary">
                — CrowdSec + CRS upstream defaults (ssh-bf 5/30s, anomaly 5, ban 4h). Recommended for most servers.
              </Typography.Text>
            </Radio>
            <Radio value="strict">
              <Typography.Text strong>Strict</Typography.Text>{" "}
              <Typography.Text type="secondary">
                — ssh-bf 3/30s, anomaly 5, ban 24h. For paranoid posture or known-targeted hosts.
              </Typography.Text>
            </Radio>
          </Space>
        </Radio.Group>

        <Button
          type="primary"
          loading={save.isPending}
          disabled={level === settings.data?.crowdsec_sensitivity}
          onClick={() => save.mutate(level)}
        >
          Apply
        </Button>
      </Space>
    </Card>
  );
};

// BouncerModeCard — server-wide nginx-bouncer MODE toggle. Writes
// /etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf via the agent's
// security.crowdsec.bouncer.mode.apply verb. Two postures:
//
//   live   per-request LAPI lookup; instant ban. Constant SQLite cgo
//          load (~23% one core on a small VM with CAPI loaded).
//   stream bouncer caches all decisions in Lua shared_dict, polls
//          LAPI every 60s. ~10% sustained CPU. Up-to-60s L7 ban lag;
//          firewall bouncer is also 60s so net L3 exposure is the
//          same.
const BouncerModeCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["admin-settings"],
    queryFn: async () => {
      const r = await apiClient.get<{ crowdsec_bouncer_mode: string }>("/admin/settings");
      return r.data;
    },
  });
  const [mode, setMode] = useState<string>("stream");
  useEffect(() => {
    if (settings.data?.crowdsec_bouncer_mode) setMode(settings.data.crowdsec_bouncer_mode);
  }, [settings.data]);

  const save = useMutation({
    mutationFn: async (newMode: string) => {
      await apiClient.patch("/admin/settings", { crowdsec_bouncer_mode: newMode });
    },
    onSuccess: () => {
      message.success("Bouncer mode applied — nginx reloaded");
      qc.invalidateQueries({ queryKey: ["admin-settings"] });
    },
    onError: (e: unknown) => {
      message.error(e instanceof Error ? e.message : "Failed to apply");
    },
  });

  return (
    <Card size="small" title={t("adminsecuritycrowdsec.bouncer_mode_server_wide")} loading={settings.isLoading}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          How the nginx bouncer evaluates each request. Stream mode caches
          decisions in nginx memory and polls CrowdSec every 60 seconds —
          drastically lower CPU but newly-issued L7 bans take up to a minute
          to apply. Live mode hits the local CrowdSec API per request — instant
          but ~2x the CPU on busy hosts.
        </Typography.Paragraph>

        <Radio.Group value={mode} onChange={(e) => setMode(e.target.value)}>
          <Space direction="vertical">
            <Radio value="stream">
              <Typography.Text strong>Stream (default)</Typography.Text>{" "}
              <Typography.Text type="secondary">
                — bouncer caches decisions, polls LAPI every 60s. ~10% CrowdSec CPU.
                Up-to-60s lag for new L7 bans. Recommended for almost every host.
              </Typography.Text>
            </Radio>
            <Radio value="live">
              <Typography.Text strong>Live</Typography.Text>{" "}
              <Typography.Text type="secondary">
                — per-request LAPI lookup. Instant block. ~23% CrowdSec CPU on a
                small VM with CAPI loaded. Use only when the 60s lag is unacceptable.
              </Typography.Text>
            </Radio>
          </Space>
        </Radio.Group>

        <Button
          type="primary"
          loading={save.isPending}
          disabled={mode === settings.data?.crowdsec_bouncer_mode}
          onClick={() => save.mutate(mode)}
        >
          Apply
        </Button>
      </Space>
    </Card>
  );
};

// LoginAllowlistCard — GH #598. Auto-allowlist successful panel + SSH login
// source IPs in CrowdSec, time-boxed, so a logged-in admin/user is never
// bounced from the IP they're working from. Writes crowdsec_login_allowlist_*
// on /admin/settings; the panel middleware reads it directly and the agent SSH
// watcher gets it pushed via security.crowdsec.login_allowlist.apply.
const LoginAllowlistCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["admin-settings"],
    queryFn: async () => {
      const r = await apiClient.get<{
        crowdsec_login_allowlist_enabled: boolean;
        crowdsec_login_allowlist_ttl_hours: number;
      }>("/admin/settings");
      return r.data;
    },
  });
  const [enabled, setEnabled] = useState<boolean>(true);
  const [ttlHours, setTtlHours] = useState<number>(168);
  useEffect(() => {
    if (settings.data) {
      setEnabled(settings.data.crowdsec_login_allowlist_enabled);
      if (settings.data.crowdsec_login_allowlist_ttl_hours > 0) {
        setTtlHours(settings.data.crowdsec_login_allowlist_ttl_hours);
      }
    }
  }, [settings.data]);

  const save = useMutation({
    mutationFn: async (vals: { enabled: boolean; ttlHours: number }) => {
      await apiClient.patch("/admin/settings", {
        crowdsec_login_allowlist_enabled: vals.enabled,
        crowdsec_login_allowlist_ttl_hours: vals.ttlHours,
      });
    },
    onSuccess: () => {
      message.success("Login allowlist policy applied");
      qc.invalidateQueries({ queryKey: ["admin-settings"] });
    },
    onError: (e: unknown) => {
      message.error(e instanceof Error ? e.message : "Failed to apply");
    },
  });

  const dirty =
    enabled !== settings.data?.crowdsec_login_allowlist_enabled ||
    ttlHours !== settings.data?.crowdsec_login_allowlist_ttl_hours;

  return (
    <Card size="small" title={t("adminsecuritycrowdsec.auto_allowlist_login_ips_server_wide")} loading={settings.isLoading}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          After a successful panel or SSH login, add the source IP to the CrowdSec
          allowlist for the configured window (refreshed on activity), so a
          legitimate operator is never locked out by a later false positive.
          Private/loopback addresses are always skipped.
        </Typography.Paragraph>
        <Alert
          type="warning"
          showIcon
          message={t("adminsecuritycrowdsec.security_trade_off")}
          description={t("adminsecuritycrowdsec.this_is_a_broad_crowdsec_exemption_a_success")}
        />
        <Space align="center">
          <Switch checked={enabled} onChange={setEnabled} />
          <Typography.Text>{enabled ? "Enabled" : "Disabled"}</Typography.Text>
        </Space>
        <Space align="center">
          <Typography.Text>Allowlist TTL (hours):</Typography.Text>
          <InputNumber
            min={1}
            max={8760}
            value={ttlHours}
            disabled={!enabled}
            onChange={(v) => setTtlHours(typeof v === "number" ? v : 168)}
          />
          <Typography.Text type="secondary">1–8760 (default 168 = 7 days)</Typography.Text>
        </Space>
        <Button
          type="primary"
          loading={save.isPending}
          disabled={!dirty}
          onClick={() => save.mutate({ enabled, ttlHours })}
        >
          Apply
        </Button>
      </Space>
    </Card>
  );
};
