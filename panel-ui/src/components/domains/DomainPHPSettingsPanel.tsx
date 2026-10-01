// DomainPHPSettingsPanel — the per-domain PHP surface (GH #1543 / GH #1332),
// extracted from UserPHPSettingsPage's "Version & Domains" tab so it can render
// two ways: on the tenant Web Domain page as a tab (domain fixed by the route),
// and inside the standalone PHP Settings page behind a domain picker. It owns
// only the DOMAIN-scoped controls — the PHP version for this domain
// (POST/DELETE /domains/:id/php-pool) and the php.ini limit overrides
// (GET/PATCH /domains/:id/php-settings). The account/pool-level tabs on the
// standalone page (CLI/Composer, Performance, OPcache, Extensions, Xdebug) are
// per-version-pool — shared by every domain on that version — so they stay put.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button, Col, Form, Popconfirm, Row, Select, Space, Spin, Tag, Typography } from "antd";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { apiClient } from "../../apiClient";
import { isPHPEOL } from "../../utils/phpEol";
import { IANA_TIMEZONES } from "../../data/timezones";
import { LogStreamModal } from "../LogStreamModal";
import { useDomainLogStreams } from "../logs/useDomainLogStreams";

type DomainPHPSettings = {
  php_pool_id?: string | null;
  php_version?: string | null;
  php_memory_limit?: string | null;
  php_upload_max_filesize?: string | null;
  php_post_max_size?: string | null;
  php_max_input_vars?: number | null;
  php_max_execution_time?: number | null;
  php_max_input_time?: number | null;
  // GH #1332 per-domain runtime directives.
  php_display_errors?: boolean | null;
  php_error_reporting?: number | null;
  php_timezone?: string | null;
  // GH #1701 Slice 2 flags.
  php_log_errors?: boolean | null;
  php_file_uploads?: boolean | null;
  php_short_open_tag?: boolean | null;
  // GH #1543 (johnnyq): the real value this domain inherits per directive when
  // it sets no override (pool ini override → box php.ini baseline), keyed by
  // php.ini directive name. Used to label each select's inherit option with the
  // actual default, e.g. "256M (Default)". Absent → generic label.
  pool_defaults?: Record<string, string> | null;
  // GH #1701: who may set each directive on this domain (the owner's package
  // policy), and which directives the CALLER may set. Audience is data: an
  // admin gets every directive in `editable`, a tenant only what their
  // package permits. Absent (an older API) = nothing locked.
  policy?: Record<string, string> | null;
  editable?: string[] | null;
  // GH #1701: the caller may reset this domain's OPcache (an admin, or a
  // tenant whose package lets them edit FPM). Absent (an older API) = no.
  opcache_reset_allowed?: boolean;
};

type PHPSettingsFormData = {
  php_memory_limit?: string | null;
  php_upload_max_filesize?: string | null;
  php_post_max_size?: string | null;
  php_max_input_vars?: number | null;
  php_max_execution_time?: number | null;
  php_max_input_time?: number | null;
  php_display_errors?: boolean | null;
  php_error_reporting?: number | null;
  php_timezone?: string | null;
  php_log_errors?: boolean | null;
  php_file_uploads?: boolean | null;
  php_short_open_tag?: boolean | null;
};

// Form field -> php.ini directive, for the GH #1701 policy lookups.
const FIELD_DIRECTIVE: Record<keyof PHPSettingsFormData, string> = {
  php_memory_limit: "memory_limit",
  php_upload_max_filesize: "upload_max_filesize",
  php_post_max_size: "post_max_size",
  php_max_input_vars: "max_input_vars",
  php_max_execution_time: "max_execution_time",
  php_max_input_time: "max_input_time",
  php_display_errors: "display_errors",
  php_error_reporting: "error_reporting",
  php_timezone: "date.timezone",
  php_log_errors: "log_errors",
  php_file_uploads: "file_uploads",
  php_short_open_tag: "short_open_tag",
};

const MEMORY_LIMIT_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "32M", value: "32M" },
  { label: "64M", value: "64M" },
  { label: "128M", value: "128M" },
  { label: "256M", value: "256M" },
  { label: "512M", value: "512M" },
  { label: "1G", value: "1G" },
];

const UPLOAD_MAX_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "1M", value: "1M" },
  { label: "10M", value: "10M" },
  { label: "50M", value: "50M" },
  { label: "100M", value: "100M" },
  { label: "256M", value: "256M" },
  { label: "512M", value: "512M" },
];

const POST_MAX_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "1M", value: "1M" },
  { label: "10M", value: "10M" },
  { label: "50M", value: "50M" },
  { label: "100M", value: "100M" },
  { label: "256M", value: "256M" },
  { label: "512M", value: "512M" },
];

const MAX_INPUT_VARS_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "100", value: 100 },
  { label: "500", value: 500 },
  { label: "1000", value: 1000 },
  { label: "2000", value: 2000 },
  { label: "5000", value: 5000 },
  { label: "10000", value: 10000 },
];

const MAX_EXECUTION_TIME_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "10s", value: 10 },
  { label: "30s", value: 30 },
  { label: "60s", value: 60 },
  { label: "120s", value: 120 },
  { label: "300s", value: 300 },
  { label: "600s", value: 600 },
];

const MAX_INPUT_TIME_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "10s", value: 10 },
  { label: "30s", value: 30 },
  { label: "60s", value: 60 },
  { label: "120s", value: 120 },
  { label: "300s", value: 300 },
];

// GH #1332 per-domain runtime directives. display_errors is pinned Off on every
// PHP vhost by the agent, so "Use pool default" and "Off" are the same effect —
// both keep errors hidden; "On" surfaces them for this domain only.
const DISPLAY_ERRORS_OPTIONS = [
  { label: "On (show errors)", value: true },
  { label: "Off", value: false },
];

// error_reporting bitmask presets. Production (22527) = E_ALL minus notices,
// deprecations and strict; matches the php.ini-production default. All (32767)
// = E_ALL (php.ini-development).
const ERROR_REPORTING_OPTIONS = [
  { label: "Use pool default", value: null },
  { label: "None (report nothing)", value: 0 },
  { label: "Production (errors + warnings)", value: 22527 },
  { label: "All (development)", value: 32767 },
];

// GH #1701 Slice 2 flags. null = inherit: the pool's flag, else the box
// php.ini. The agent pins the resolved value on every PHP vhost, so one
// domain's choice cannot carry over to a sibling on the same PHP pool.
const FLAG_OPTIONS = [
  { label: "Use pool default", value: null as boolean | null },
  { label: "On", value: true as boolean | null },
  { label: "Off", value: false as boolean | null },
];

// The full IANA/PHP timezone list, shared with admin Server Settings so both
// selectors offer the same zones (GH #1332). Searchable in the Select below.
const TIMEZONE_OPTIONS = [
  { label: "Use pool default", value: null as string | null },
  ...IANA_TIMEZONES.map((z) => ({ label: z, value: z as string | null })),
];

export interface DomainPHPSettingsPanelProps {
  domainId: string;
}

export function DomainPHPSettingsPanel({ domainId }: DomainPHPSettingsPanelProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const logStreams = useDomainLogStreams();
  const [phpSettings, setPhpSettings] = useState<DomainPHPSettings | null>(null);
  const [availableVersions, setAvailableVersions] = useState<string[]>([]);
  const [versionSaving, setVersionSaving] = useState(false);
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [form] = Form.useForm<PHPSettingsFormData>();

  // Installed PHP versions for the version selector. Non-fatal: the selector
  // falls back to "Server default" only.
  useEffect(() => {
    (async () => {
      try {
        const resp = await apiClient.get<{ versions: string[] }>("/php/versions");
        setAvailableVersions(resp.data?.versions ?? []);
      } catch {
        /* selector falls back to Default only */
      }
    })();
  }, []);

  const onChangePHPVersion = async (version: string | null) => {
    setVersionSaving(true);
    try {
      if (version === null) {
        await apiClient.delete(`/domains/${domainId}/php-pool`);
      } else {
        await apiClient.post(`/domains/${domainId}/php-pool`, {
          php_version: version,
        });
      }
      feedback.message.success(
        version
          ? `Switched to PHP ${version}`
          : "Reverted to server default PHP version",
      );
      const resp = await apiClient.get<DomainPHPSettings>(
        `/domains/${domainId}/php-settings`,
      );
      setPhpSettings(resp.data);
      // GH #1332: switching a domain's version may have created a new
      // per-version pool — refresh the Performance card so its version list +
      // per-version values reflect it.
      qc.invalidateQueries({ queryKey: ["me-php-pool-tuning"] });
    } catch (err) {
      const e = err as {
        response?: { data?: { error?: string } };
        message?: string;
      };
      feedback.message.error(
        e.response?.data?.error ?? e.message ?? "Failed to change PHP version",
      );
    } finally {
      setVersionSaving(false);
    }
  };

  // GH #1701: OPcache belongs to the PHP pool, so the reset restarts the pool
  // serving this domain and every site on it starts with an empty cache.
  const onResetOpcache = async () => {
    setResetting(true);
    try {
      const resp = await apiClient.post<{ php_version?: string }>(
        `/domains/${domainId}/php-settings/opcache-reset`,
      );
      feedback.message.success(
        `OPcache reset (PHP ${resp.data?.php_version ?? phpSettings?.php_version ?? ""} restarted)`,
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to reset OPcache",
      );
    } finally {
      setResetting(false);
    }
  };

  // Load this domain's PHP settings on mount and whenever the domain changes
  // (the standalone page reuses one instance behind its picker).
  useEffect(() => {
    (async () => {
      setLoading(true);
      try {
        const resp = await apiClient.get<DomainPHPSettings>(
          `/domains/${domainId}/php-settings`,
        );
        setPhpSettings(resp.data);
        // resetFields BEFORE setFieldsValue: setFieldsValue does not clear the
        // touched flags, so without the reset a domain switch would leave Save
        // enabled on a stale dirty state (latent bug in the original page).
        form.resetFields();
        // GH #1705: the API omits a field the domain does not override
        // (omitempty), so it arrives undefined. An undefined Select value shows
        // the placeholder; null selects the inherit option, whose label names
        // the real default ("256M (Default)"). Map absent to null.
        form.setFieldsValue({
          php_memory_limit: resp.data.php_memory_limit ?? null,
          php_upload_max_filesize: resp.data.php_upload_max_filesize ?? null,
          php_post_max_size: resp.data.php_post_max_size ?? null,
          php_max_input_vars: resp.data.php_max_input_vars ?? null,
          php_max_execution_time: resp.data.php_max_execution_time ?? null,
          php_max_input_time: resp.data.php_max_input_time ?? null,
          php_display_errors: resp.data.php_display_errors ?? null,
          php_error_reporting: resp.data.php_error_reporting ?? null,
          php_timezone: resp.data.php_timezone ?? null,
          php_log_errors: resp.data.php_log_errors ?? null,
          php_file_uploads: resp.data.php_file_uploads ?? null,
          php_short_open_tag: resp.data.php_short_open_tag ?? null,
        });
      } catch {
        feedback.message.error("Failed to load PHP settings");
      } finally {
        setLoading(false);
      }
    })();
  }, [domainId, form]);

  // GH #1701: a directive the caller may not set is shown read-only and sent
  // back exactly as stored, so the API sees no change to it.
  const locked = (field: keyof PHPSettingsFormData): boolean => {
    const editable = phpSettings?.editable;
    return Array.isArray(editable) && !editable.includes(FIELD_DIRECTIVE[field]);
  };
  const outgoing = <K extends keyof PHPSettingsFormData>(
    field: K,
    values: PHPSettingsFormData,
  ): PHPSettingsFormData[K] | null =>
    locked(field)
      ? ((phpSettings?.[field] as PHPSettingsFormData[K] | undefined) ?? null)
      : // undefined (never set / cleared) -> null so the API clears the override.
        (values[field] ?? null);

  const onSave = async (values: PHPSettingsFormData) => {
    setSubmitting(true);
    try {
      await apiClient.patch(`/domains/${domainId}/php-settings`, {
        php_memory_limit: outgoing("php_memory_limit", values),
        php_upload_max_filesize: outgoing("php_upload_max_filesize", values),
        php_post_max_size: outgoing("php_post_max_size", values),
        php_max_input_vars: outgoing("php_max_input_vars", values),
        php_max_execution_time: outgoing("php_max_execution_time", values),
        php_max_input_time: outgoing("php_max_input_time", values),
        php_display_errors: outgoing("php_display_errors", values),
        php_error_reporting: outgoing("php_error_reporting", values),
        php_timezone: outgoing("php_timezone", values),
        php_log_errors: outgoing("php_log_errors", values),
        php_file_uploads: outgoing("php_file_uploads", values),
        php_short_open_tag: outgoing("php_short_open_tag", values),
      });
      feedback.message.success("PHP settings updated successfully");
      // Reload settings to confirm.
      const resp = await apiClient.get<DomainPHPSettings>(
        `/domains/${domainId}/php-settings`,
      );
      setPhpSettings(resp.data);
    } catch (err) {
      const e = err as { response?: { data?: { error?: string; detail?: string } } };
      feedback.message.error(
        e.response?.data?.error === "php_setting_not_permitted" && e.response.data.detail
          ? e.response.data.detail
          : "Failed to update PHP settings",
      );
    } finally {
      setSubmitting(false);
    }
  };

  // Fields the Save button cares about. AntD's form state changes don't
  // trigger parent re-renders, so we can't compute `hasChanges` inline —
  // we have to evaluate it inside a Form.Item shouldUpdate wrapper so it
  // re-runs on every form mutation. Typed literal-tuple so
  // form.isFieldsTouched's keyof-narrowed overload accepts it.
  const dirtyFields: (keyof PHPSettingsFormData)[] = [
    "php_memory_limit",
    "php_upload_max_filesize",
    "php_post_max_size",
    "php_max_input_vars",
    "php_max_execution_time",
    "php_max_input_time",
    "php_display_errors",
    "php_error_reporting",
    "php_timezone",
    "php_log_errors",
    "php_file_uploads",
    "php_short_open_tag",
  ];

  // GH #1332 item 6: a small tag on each field showing whether it is a custom
  // override or falls back to the pool default. Reflects the last-saved state
  // (phpSettings), refreshed after every save.
  // GH #1543: relabel the "Use pool default" (value null) option of a select
  // with the real inherited value — "256M (Default)" — from pool_defaults. The
  // null option is what the Select shows while a domain has no override, so this
  // surfaces the actual default without any extra auto-select logic. Falls back
  // to the generic label when the backend couldn't resolve a value.
  type Opt = { label: string; value: string | number | boolean | null };
  // A per-directive formatter turns the raw box baseline (from pool_defaults)
  // into the "(Default)" label on the null (inherit) option. Returning null —
  // or an absent key, meaning the agent resolved no baseline — keeps the
  // generic "Use pool default" label.
  type DefaultFmt = (raw: string) => string | null;
  const sizeFmt =
    (suffix = ""): DefaultFmt =>
    (raw) =>
      raw ? `${raw}${suffix}` : null;
  // error_reporting is a bitmask (GH #1332); map the presets we offer, else show
  // the raw value — a box/pool may carry any bitmask (e.g. PHP 8.4 ships 24575).
  const errorReportingFmt: DefaultFmt = (raw) => {
    if (raw === "") return null;
    const presets: Record<string, string> = {
      "0": "None",
      "22527": "Production",
      "32767": "All",
    };
    return presets[raw] ?? raw;
  };
  // A stock Debian php.ini ships date.timezone commented out → ini_get returns
  // "" and PHP's effective zone is UTC, so surface that rather than a blank.
  const timezoneFmt: DefaultFmt = (raw) => (raw === "" ? "UTC" : raw);
  // A flag reads the way ini_get reports it: "1" on; "" or "0" off.
  const flagFmt: DefaultFmt = (raw) =>
    raw === "1" || raw.toLowerCase() === "on" ? "On" : "Off";
  const defaultLabel = (
    directive: string,
    fmt: DefaultFmt = sizeFmt(),
  ): string | null => {
    const raw = phpSettings?.pool_defaults?.[directive];
    if (raw === undefined) return null;
    const shown = fmt(raw);
    return shown === null ? null : `${shown} (Default)`;
  };
  const withDefault = (
    opts: Opt[],
    directive: string,
    fmt: DefaultFmt = sizeFmt(),
  ): Opt[] => {
    const label = defaultLabel(directive, fmt);
    if (label === null) return opts;
    return opts.map((o) => (o.value === null ? { ...o, label } : o));
  };
  // The placeholder shows when the field is empty — after the clear button,
  // too — so it names the same default as the inherit option.
  const inheritPlaceholder = (directive: string, fmt: DefaultFmt = sizeFmt()) =>
    defaultLabel(directive, fmt) ?? t("userphpsettingspage.use_pool_default");

  const fieldSet = (v: unknown) => v !== null && v !== undefined;
  // GH #1701: a tenant sees "Set by your administrator" on a locked directive;
  // an admin sees "Admin only" on one the tenant cannot change.
  const policyTag = (field: keyof PHPSettingsFormData) => {
    if (locked(field)) {
      return (
        <Tag color="gold" style={{ marginInlineEnd: 0 }}>
          Set by your administrator
        </Tag>
      );
    }
    if (phpSettings?.policy?.[FIELD_DIRECTIVE[field]] === "admin_only") {
      return (
        <Tag color="purple" style={{ marginInlineEnd: 0 }}>
          Admin only
        </Tag>
      );
    }
    return null;
  };
  const overrideLabel = (text: string, overridden: boolean, field: keyof PHPSettingsFormData) => (
    <Space size={6}>
      {text}
      {overridden ? (
        <Tag color="blue" style={{ marginInlineEnd: 0 }}>
          Custom
        </Tag>
      ) : (
        <Tag style={{ marginInlineEnd: 0 }}>Pool default</Tag>
      )}
      {policyTag(field)}
    </Space>
  );

  return (
    <Form<PHPSettingsFormData> form={form} layout="vertical" onFinish={onSave}>
      <Spin spinning={loading}>
          {phpSettings && (
            <>
              <Form.Item
                label={t("userphpsettingspage.php_version")}
                extra="Applies to this domain only — each domain can run its own PHP version. EOL versions have no security patches; avoid them on public sites."
              >
                <Select
                  value={phpSettings.php_version ?? null}
                  loading={versionSaving}
                  disabled={versionSaving}
                  onChange={(v) => onChangePHPVersion(v)}
                  style={{ width: 220 }}
                  options={[
                    { label: "Server default", value: null },
                    ...availableVersions.map((v) => ({
                      label: isPHPEOL(v) ? (
                        <span>
                          PHP {v}{" "}
                          <Typography.Text type="danger">(EOL)</Typography.Text>
                        </span>
                      ) : (
                        `PHP ${v}`
                      ),
                      value: v,
                    })),
                  ]}
                />
              </Form.Item>

              {/* GH #1332 items 7, 15: quick actions for this domain. GH #1701:
                  Reset OPcache is back here at the reporter's request, next to
                  the per-version one on the OPcache & JIT tab; it resets the
                  pool serving this domain. View error log opens this domain's
                  error-log stream right here (GH #1701), the same stream as the
                  Error Log button on the domain's Logs tab. */}
              <Space wrap style={{ marginBottom: 8 }}>
                {phpSettings.opcache_reset_allowed && phpSettings.php_version && (
                  <Popconfirm
                    title="Reset OPcache?"
                    description={`This restarts PHP ${phpSettings.php_version} for every site on this domain's PHP pool.`}
                    okText="Reset"
                    onConfirm={onResetOpcache}
                  >
                    <Button type="link" style={{ paddingInline: 0 }} loading={resetting}>
                      Reset OPcache
                    </Button>
                  </Popconfirm>
                )}
                <Button
                  type="link"
                  style={{ paddingInline: 0 }}
                  onClick={() => void logStreams.openStream("error", domainId)}
                >
                  View error log
                </Button>
                <Button
                  type="link"
                  style={{ paddingInline: 0 }}
                  onClick={() => navigate("/jabali-panel/cron")}
                >
                  Scheduled tasks (Cron)
                </Button>
              </Space>

              <Typography.Title level={5} style={{ marginBottom: 0 }}>
                Resource Limits
              </Typography.Title>
              {/* GH #1332 item 3: these are DOMAIN-level php.ini overrides,
                  applied to this domain regardless of which PHP version it
                  runs — not per-version. Label it so that's clear. */}
              <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
                Applied to this domain across all PHP versions. Per-version
                worker tuning lives under Performance.
              </Typography.Paragraph>
              <Row gutter={[16, 16]}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.memory_limit"),
                      fieldSet(phpSettings?.php_memory_limit),
                      "php_memory_limit",
                    )}
                    name="php_memory_limit"
                  >
                    <Select
                      disabled={locked("php_memory_limit")}
                      placeholder={inheritPlaceholder("memory_limit")}
                      allowClear
                      options={withDefault(MEMORY_LIMIT_OPTIONS, "memory_limit")}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.upload_max_file_size"),
                      fieldSet(phpSettings?.php_upload_max_filesize),
                      "php_upload_max_filesize",
                    )}
                    name="php_upload_max_filesize"
                  >
                    <Select
                      disabled={locked("php_upload_max_filesize")}
                      placeholder={inheritPlaceholder("upload_max_filesize")}
                      allowClear
                      options={withDefault(UPLOAD_MAX_OPTIONS, "upload_max_filesize")}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.post_max_size"),
                      fieldSet(phpSettings?.php_post_max_size),
                      "php_post_max_size",
                    )}
                    name="php_post_max_size"
                  >
                    <Select
                      disabled={locked("php_post_max_size")}
                      placeholder={inheritPlaceholder("post_max_size")}
                      allowClear
                      options={withDefault(POST_MAX_OPTIONS, "post_max_size")}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.max_input_variables"),
                      fieldSet(phpSettings?.php_max_input_vars),
                      "php_max_input_vars",
                    )}
                    name="php_max_input_vars"
                  >
                    <Select
                      disabled={locked("php_max_input_vars")}
                      placeholder={inheritPlaceholder("max_input_vars")}
                      allowClear
                      options={withDefault(MAX_INPUT_VARS_OPTIONS, "max_input_vars")}
                    />
                  </Form.Item>
                </Col>
              </Row>

              <Typography.Title level={5}>Execution Limits</Typography.Title>
              <Row gutter={[16, 16]}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.max_execution_time"),
                      fieldSet(phpSettings?.php_max_execution_time),
                      "php_max_execution_time",
                    )}
                    name="php_max_execution_time"
                  >
                    <Select
                      disabled={locked("php_max_execution_time")}
                      placeholder={inheritPlaceholder("max_execution_time", sizeFmt("s"))}
                      allowClear
                      options={withDefault(MAX_EXECUTION_TIME_OPTIONS, "max_execution_time", sizeFmt("s"))}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.max_input_time"),
                      fieldSet(phpSettings?.php_max_input_time),
                      "php_max_input_time",
                    )}
                    name="php_max_input_time"
                  >
                    <Select
                      disabled={locked("php_max_input_time")}
                      placeholder={inheritPlaceholder("max_input_time", sizeFmt("s"))}
                      allowClear
                      options={withDefault(MAX_INPUT_TIME_OPTIONS, "max_input_time", sizeFmt("s"))}
                    />
                  </Form.Item>
                </Col>
              </Row>

              <Typography.Title level={5}>
                Error Handling &amp; Runtime
              </Typography.Title>
              <Typography.Paragraph type="secondary" style={{ marginTop: -4 }}>
                These apply to this domain across all its PHP versions. Turn{" "}
                <strong>Display errors</strong> on only for development — it
                prints PHP errors to visitors.
              </Typography.Paragraph>
              <Row gutter={[16, 16]}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "Display errors",
                      fieldSet(phpSettings?.php_display_errors),
                      "php_display_errors",
                    )}
                    name="php_display_errors"
                    extra="Shows PHP errors in the page output. Keep off on public/production sites."
                  >
                    <Select
                      disabled={locked("php_display_errors")}
                      placeholder="Use pool default (off)"
                      allowClear
                      options={DISPLAY_ERRORS_OPTIONS}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "Error reporting",
                      fieldSet(phpSettings?.php_error_reporting),
                      "php_error_reporting",
                    )}
                    name="php_error_reporting"
                  >
                    <Select
                      disabled={locked("php_error_reporting")}
                      placeholder={inheritPlaceholder("error_reporting", errorReportingFmt)}
                      allowClear
                      options={withDefault(ERROR_REPORTING_OPTIONS, "error_reporting", errorReportingFmt)}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "Timezone",
                      fieldSet(phpSettings?.php_timezone),
                      "php_timezone",
                    )}
                    name="php_timezone"
                    extra="date.timezone for this domain's PHP."
                  >
                    <Select
                      disabled={locked("php_timezone")}
                      showSearch
                      placeholder={inheritPlaceholder("date.timezone", timezoneFmt)}
                      allowClear
                      optionFilterProp="label"
                      options={withDefault(TIMEZONE_OPTIONS, "date.timezone", timezoneFmt)}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "Log errors",
                      fieldSet(phpSettings?.php_log_errors),
                      "php_log_errors",
                    )}
                    name="php_log_errors"
                    extra="Records PHP errors in the error log. Visitors never see logged errors."
                  >
                    <Select
                      disabled={locked("php_log_errors")}
                      placeholder={inheritPlaceholder("log_errors", flagFmt)}
                      allowClear
                      options={withDefault(FLAG_OPTIONS, "log_errors", flagFmt)}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "File uploads",
                      fieldSet(phpSettings?.php_file_uploads),
                      "php_file_uploads",
                    )}
                    name="php_file_uploads"
                    extra="Lets this domain's PHP accept uploaded files. Off breaks uploads in WordPress and most apps."
                  >
                    <Select
                      disabled={locked("php_file_uploads")}
                      placeholder={inheritPlaceholder("file_uploads", flagFmt)}
                      allowClear
                      options={withDefault(FLAG_OPTIONS, "file_uploads", flagFmt)}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "Short open tag",
                      fieldSet(phpSettings?.php_short_open_tag),
                      "php_short_open_tag",
                    )}
                    name="php_short_open_tag"
                    extra="Treats <? as a PHP opening tag. Only for old code that needs it: files that start with <?xml stop working."
                  >
                    <Select
                      disabled={locked("php_short_open_tag")}
                      placeholder={inheritPlaceholder("short_open_tag", flagFmt)}
                      allowClear
                      options={withDefault(FLAG_OPTIONS, "short_open_tag", flagFmt)}
                    />
                  </Form.Item>
                </Col>
              </Row>

              <Form.Item
                noStyle
                shouldUpdate={(prev, cur) =>
                  dirtyFields.some((f) => prev[f] !== cur[f])
                }
              >
                {() => {
                  const hasChanges = form.isFieldsTouched(dirtyFields);
                  return (
                    <Form.Item style={{ marginBottom: 0, marginTop: 24 }}>
                      <Button
                        type="primary"
                        htmlType="submit"
                        loading={submitting}
                        disabled={!hasChanges}
                      >
                        Save Changes
                      </Button>
                    </Form.Item>
                  );
                }}
              </Form.Item>
            </>
          )}
      </Spin>
      {/* Portal-rendered; holds no form fields. */}
      <LogStreamModal {...logStreams.modalProps} />
    </Form>
  );
}
