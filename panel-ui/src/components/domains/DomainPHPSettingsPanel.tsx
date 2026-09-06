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
import { Button, Col, Form, Row, Select, Space, Spin, Tag, Typography } from "antd";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import { apiClient } from "../../apiClient";
import { isPHPEOL } from "../../utils/phpEol";
import { IANA_TIMEZONES } from "../../data/timezones";

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
  const [phpSettings, setPhpSettings] = useState<DomainPHPSettings | null>(null);
  const [availableVersions, setAvailableVersions] = useState<string[]>([]);
  const [versionSaving, setVersionSaving] = useState(false);
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
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
        form.setFieldsValue({
          php_memory_limit: resp.data.php_memory_limit,
          php_upload_max_filesize: resp.data.php_upload_max_filesize,
          php_post_max_size: resp.data.php_post_max_size,
          php_max_input_vars: resp.data.php_max_input_vars,
          php_max_execution_time: resp.data.php_max_execution_time,
          php_max_input_time: resp.data.php_max_input_time,
          php_display_errors: resp.data.php_display_errors,
          php_error_reporting: resp.data.php_error_reporting,
          php_timezone: resp.data.php_timezone,
        });
      } catch {
        feedback.message.error("Failed to load PHP settings");
      } finally {
        setLoading(false);
      }
    })();
  }, [domainId, form]);

  const onSave = async (values: PHPSettingsFormData) => {
    setSubmitting(true);
    try {
      await apiClient.patch(`/domains/${domainId}/php-settings`, {
        php_memory_limit: values.php_memory_limit,
        php_upload_max_filesize: values.php_upload_max_filesize,
        php_post_max_size: values.php_post_max_size,
        php_max_input_vars: values.php_max_input_vars,
        php_max_execution_time: values.php_max_execution_time,
        php_max_input_time: values.php_max_input_time,
        // undefined (never set / cleared) -> null so the API clears the override.
        php_display_errors: values.php_display_errors ?? null,
        php_error_reporting: values.php_error_reporting ?? null,
        php_timezone: values.php_timezone ?? null,
      });
      feedback.message.success("PHP settings updated successfully");
      // Reload settings to confirm.
      const resp = await apiClient.get<DomainPHPSettings>(
        `/domains/${domainId}/php-settings`,
      );
      setPhpSettings(resp.data);
    } catch {
      feedback.message.error("Failed to update PHP settings");
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
  ];

  // GH #1332 item 6: a small tag on each field showing whether it is a custom
  // override or falls back to the pool default. Reflects the last-saved state
  // (phpSettings), refreshed after every save.
  const fieldSet = (v: unknown) => v !== null && v !== undefined;
  const overrideLabel = (text: string, overridden: boolean) => (
    <Space size={6}>
      {text}
      {overridden ? (
        <Tag color="blue" style={{ marginInlineEnd: 0 }}>
          Custom
        </Tag>
      ) : (
        <Tag style={{ marginInlineEnd: 0 }}>Pool default</Tag>
      )}
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

              {/* GH #1332 items 7, 15: quick actions for this domain. Reset
                  OPcache lives on the OPcache & JIT tab (per-version / shared-
                  pool, not per-domain), so it is not duplicated here. */}
              <Space wrap style={{ marginBottom: 8 }}>
                <Button
                  type="link"
                  style={{ paddingInline: 0 }}
                  onClick={() => navigate(`/jabali-panel/logs?domain=${domainId}`)}
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
                    )}
                    name="php_memory_limit"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={MEMORY_LIMIT_OPTIONS}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.upload_max_file_size"),
                      fieldSet(phpSettings?.php_upload_max_filesize),
                    )}
                    name="php_upload_max_filesize"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={UPLOAD_MAX_OPTIONS}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.post_max_size"),
                      fieldSet(phpSettings?.php_post_max_size),
                    )}
                    name="php_post_max_size"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={POST_MAX_OPTIONS}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.max_input_variables"),
                      fieldSet(phpSettings?.php_max_input_vars),
                    )}
                    name="php_max_input_vars"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={MAX_INPUT_VARS_OPTIONS}
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
                    )}
                    name="php_max_execution_time"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={MAX_EXECUTION_TIME_OPTIONS}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      t("userphpsettingspage.max_input_time"),
                      fieldSet(phpSettings?.php_max_input_time),
                    )}
                    name="php_max_input_time"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={MAX_INPUT_TIME_OPTIONS}
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
                    )}
                    name="php_display_errors"
                    extra="Shows PHP errors in the page output. Keep off on public/production sites."
                  >
                    <Select
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
                    )}
                    name="php_error_reporting"
                  >
                    <Select
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      options={ERROR_REPORTING_OPTIONS}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    label={overrideLabel(
                      "Timezone",
                      fieldSet(phpSettings?.php_timezone),
                    )}
                    name="php_timezone"
                    extra="date.timezone for this domain's PHP."
                  >
                    <Select
                      showSearch
                      placeholder={t("userphpsettingspage.use_pool_default")}
                      allowClear
                      optionFilterProp="label"
                      options={TIMEZONE_OPTIONS}
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
    </Form>
  );
}
