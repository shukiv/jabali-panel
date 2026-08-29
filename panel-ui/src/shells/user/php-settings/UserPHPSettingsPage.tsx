import { useTranslation } from "react-i18next";
import { Tabs, Alert, Button, Card, Form, Row, Col, Select, Space, Spin, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { CodeOutlined } from "@icons";
import { UserPHPPerformanceCard } from "./UserPHPPerformanceCard";
import { DomainEnvVarsCard } from "./DomainEnvVarsCard";
import { PHPExtensionsCard } from "./PHPExtensionsCard";
import { PHPXdebugCard } from "./PHPXdebugCard";
import { apiClient } from "../../../apiClient";
import { getIdentity, type Identity } from "../../../identity";
import { isPHPEOL } from "../../../utils/phpEol";

type Domain = {
  id: string;
  name: string;
  user_id: string;
};

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
  domain_id: string;
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

// A curated set of common IANA zones. The server validates any value against
// the tz database, so this is a convenience list, not the limit.
const COMMON_TIMEZONES = [
  "UTC",
  "Africa/Johannesburg",
  "America/Chicago",
  "America/Los_Angeles",
  "America/New_York",
  "America/Sao_Paulo",
  "Asia/Dubai",
  "Asia/Jerusalem",
  "Asia/Kolkata",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Europe/Berlin",
  "Europe/Istanbul",
  "Europe/London",
  "Europe/Madrid",
  "Europe/Moscow",
  "Europe/Paris",
  "Pacific/Auckland",
];
const TIMEZONE_OPTIONS = [
  { label: "Use pool default", value: null as string | null },
  ...COMMON_TIMEZONES.map((z) => ({ label: z, value: z as string | null })),
];

export function UserPHPSettingsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [opcacheResetting, setOpcacheResetting] = useState(false);
  const [, setMe] = useState<Identity | null>(null);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [selectedDomain, setSelectedDomain] = useState<string | null>(null);
  const [phpSettings, setPhpSettings] = useState<DomainPHPSettings | null>(null);
  const [availableVersions, setAvailableVersions] = useState<string[]>([]);
  const [versionSaving, setVersionSaving] = useState(false);
  const [cliVersion, setCliVersion] = useState<string>(""); // "" = auto
  const [cliSaving, setCliSaving] = useState(false);
  const [composerChannel, setComposerChannel] = useState<string>("latest");
  const [composerSaving, setComposerSaving] = useState(false);
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [form] = Form.useForm<PHPSettingsFormData>();

  // Load identity, domains, and installed PHP versions on mount
  useEffect(() => {
    (async () => {
      const identity = await getIdentity();
      setMe(identity);

      try {
        const resp = await apiClient.get<{ data: Domain[]; total: number }>(
          "/domains",
        );
        setDomains(resp.data?.data ?? []);
      } catch (err) {
        feedback.message.error("Failed to load domains");
      }

      try {
        const resp = await apiClient.get<{ versions: string[] }>(
          "/php/versions",
        );
        setAvailableVersions(resp.data?.versions ?? []);
      } catch (err) {
        // Non-fatal: PHP version selector falls back to "Default only".
      }

      try {
        const resp = await apiClient.get<{ version: string }>(
          "/me/php-cli-version",
        );
        setCliVersion(resp.data?.version ?? "");
      } catch {
        // Non-fatal: account may have no shell user.
      }

      try {
        const resp = await apiClient.get<{ channel: string }>(
          "/me/composer-channel",
        );
        setComposerChannel(resp.data?.channel ?? "latest");
      } catch {
        // Non-fatal: account may have no shell user.
      }
    })();
  }, []);

  const onChangeComposer = async (channel: string) => {
    setComposerSaving(true);
    try {
      await apiClient.put("/me/composer-channel", { channel });
      setComposerChannel(channel);
      feedback.message.success(
        channel === "lts"
          ? "Composer set to the 2.2 LTS channel"
          : "Composer set to the latest channel",
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to set Composer version",
      );
    } finally {
      setComposerSaving(false);
    }
  };

  const onChangeCliVersion = async (version: string) => {
    setCliSaving(true);
    try {
      await apiClient.put("/me/php-cli-version", { version });
      setCliVersion(version);
      feedback.message.success(
        version
          ? `CLI default set to PHP ${version}`
          : "CLI default reverted to automatic",
      );
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to set CLI PHP version",
      );
    } finally {
      setCliSaving(false);
    }
  };

  const onChangePHPVersion = async (version: string | null) => {
    if (!selectedDomain) return;
    setVersionSaving(true);
    try {
      if (version === null) {
        await apiClient.delete(`/domains/${selectedDomain}/php-pool`);
      } else {
        await apiClient.post(`/domains/${selectedDomain}/php-pool`, {
          php_version: version,
        });
      }
      feedback.message.success(
        version
          ? `Switched to PHP ${version}`
          : "Reverted to server default PHP version",
      );
      const resp = await apiClient.get<DomainPHPSettings>(
        `/domains/${selectedDomain}/php-settings`,
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

  // Load PHP settings when domain is selected
  useEffect(() => {
    if (!selectedDomain) {
      setPhpSettings(null);
      form.resetFields();
      return;
    }

    (async () => {
      setLoading(true);
      try {
        const resp = await apiClient.get<DomainPHPSettings>(
          `/domains/${selectedDomain}/php-settings`,
        );
        setPhpSettings(resp.data);
        form.setFieldsValue({
          domain_id: selectedDomain,
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
      } catch (err) {
        feedback.message.error("Failed to load PHP settings");
      } finally {
        setLoading(false);
      }
    })();
  }, [selectedDomain, form]);

  const onSave = async (values: PHPSettingsFormData) => {
    if (!selectedDomain) return;

    setSubmitting(true);
    try {
      await apiClient.patch(`/domains/${selectedDomain}/php-settings`, {
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
      // Reload settings to confirm
      if (selectedDomain) {
        const resp = await apiClient.get<DomainPHPSettings>(
          `/domains/${selectedDomain}/php-settings`,
        );
        setPhpSettings(resp.data);
      }
    } catch (err) {
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

  // GH #1332 item 10: reset OPcache for the selected domain's PHP version.
  // OPcache is shared per FPM pool (per version), so this affects every site
  // the account runs on that version — stated in the confirm copy.
  const onResetOpcache = async () => {
    setOpcacheResetting(true);
    try {
      await apiClient.post("/me/php-opcache/reset", {
        php_version: phpSettings?.php_version ?? undefined,
      });
      feedback.message.success("OPcache reset (FPM restarted for this PHP version)");
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(
        e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to reset OPcache",
      );
    } finally {
      setOpcacheResetting(false);
    }
  };

  return (
    // GH #1332 item 1: the page was clamped to 800px and centred, unlike every
    // other settings page. Use the shell's full responsive width like its peers.
    <div>
      <Space direction="vertical" size="large" style={{ width: "100%" }}>
        <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
          <CodeOutlined /> PHP Settings
        </Typography.Title>

        <Alert
          title={t("userphpsettingspage.caution")}
          description={t("userphpsettingspage.changing_php_settings_can_affect_your_websit")}
          type="warning"
          showIcon
        />

        <Tabs
          defaultActiveKey="settings"
          items={[
            {
              key: "settings",
              label: "Version & Domains",
              children: (
                <Space direction="vertical" size="large" style={{ width: "100%" }}>
        <Card title={t("userphpsettingspage.cli_terminal_default_php_version")} size="small">
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            Sets which PHP version a bare <code>php</code> (and composer / wp-cli)
            uses in your SSH/terminal sessions. This is separate from each
            domain&apos;s web PHP version. You can always pick a specific version
            per command with <code>php8.3</code>, <code>php8.4</code>, etc.
          </Typography.Paragraph>
          <Select
            style={{ minWidth: 280 }}
            value={cliVersion}
            loading={cliSaving}
            onChange={onChangeCliVersion}
            options={[
              { value: "", label: "Automatic (follow domain pool)" },
              ...availableVersions.map((v) => ({ value: v, label: `PHP ${v}` })),
            ]}
          />
          {/* GH #1332 item 13: Composer version channel. */}
          <Typography.Paragraph
            type="secondary"
            style={{ marginTop: 16, marginBottom: 4 }}
          >
            Composer version for your shell <code>composer</code>.
          </Typography.Paragraph>
          <Select
            style={{ minWidth: 280 }}
            value={composerChannel}
            loading={composerSaving}
            onChange={onChangeComposer}
            options={[
              { value: "latest", label: "Composer (latest)" },
              { value: "lts", label: "Composer 2.2 LTS (older PHP compatibility)" },
            ]}
          />
        </Card>

        <Card>
          <Form<PHPSettingsFormData>
            form={form}
            layout="vertical"
            onFinish={onSave}
          >
            <Form.Item
              label={t("userphpsettingspage.domain")}
              name="domain_id"
              rules={[{ required: true, message: "Please select a domain" }]}
            >
              <Select
                placeholder={t("userphpsettingspage.select_a_domain")}
                onChange={setSelectedDomain}
                options={domains.map((d) => ({
                  label: d.name,
                  value: d.id,
                }))}
              />
            </Form.Item>

            <Spin spinning={loading}>
              {selectedDomain && phpSettings && (
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
                              <Typography.Text type="danger">
                                (EOL)
                              </Typography.Text>
                            </span>
                          ) : (
                            `PHP ${v}`
                          ),
                          value: v,
                        })),
                      ]}
                    />
                  </Form.Item>

                  {/* GH #1332 items 7, 10, 15: quick actions for the selected domain. */}
                  <Space wrap style={{ marginBottom: 8 }}>
                    <Button
                      loading={opcacheResetting}
                      onClick={onResetOpcache}
                    >
                      Reset OPcache
                    </Button>
                    <Button
                      type="link"
                      style={{ paddingInline: 0 }}
                      onClick={() =>
                        navigate(`/jabali-panel/logs?domain=${selectedDomain}`)
                      }
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
                  <Typography.Paragraph
                    type="secondary"
                    style={{ fontSize: 12, marginTop: -4 }}
                  >
                    Resetting OPcache restarts PHP-FPM for this version and
                    affects all your sites running it — handy after a deploy.
                  </Typography.Paragraph>

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
                  <Typography.Paragraph
                    type="secondary"
                    style={{ marginTop: -4 }}
                  >
                    These apply to this domain across all its PHP versions.
                    Turn <strong>Display errors</strong> on only for
                    development — it prints PHP errors to visitors.
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
                      const hasChanges =
                        !!selectedDomain &&
                        form.isFieldsTouched(dirtyFields);
                      return (
                        <Form.Item
                          style={{ marginBottom: 0, marginTop: 24 }}
                        >
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
        </Card>
                </Space>
              ),
            },
            {
              key: "perf",
              label: "Performance",
              children: <UserPHPPerformanceCard />,
            },
            {
              key: "env",
              label: "Environment",
              children: <DomainEnvVarsCard />,
            },
            {
              key: "extensions",
              label: "Extensions",
              children: <PHPExtensionsCard />,
            },
            {
              key: "xdebug",
              label: "Xdebug",
              children: <PHPXdebugCard />,
            },
          ]}
        />
      </Space>
    </div>
  );
}
