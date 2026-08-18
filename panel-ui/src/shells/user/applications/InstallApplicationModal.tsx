// Install modal — provisions an application (WordPress today; future
// apps land via the M19 registry). POSTs the generic shape to
// /applications: {app_type, domain_id, subdirectory, use_www, params}.
// The backend looks up the descriptor, validates `params` against its
// InstallParamSchema, provisions a DB if descriptor.RequiresDB, and
// dispatches the agent install via app.install with the matching
// app_type. Returns 202 with the admin password delivered exactly
// once — we show it in a reveal-once panel.
//
// The per-app form fields beneath Domain/Subdirectory are rendered
// dynamically from descriptor.install_param_schema so adding a new
// app type (e.g. DokuWiki with a "license" enum) requires zero UI
// code — the descriptor on the API side is the only source of truth.

import { useTranslation } from "react-i18next";
import { useState, useEffect, useMemo } from "react";
import { Drawer, Form, Grid, Input, Button, Select, Space, Typography, Alert, Switch } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { AppstoreOutlined, CheckCircleTwoTone, CheckOutlined, CloseOutlined } from "@icons";
import { useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { CopyableInput } from "../../../components/CopyableInput";
import { PasswordInput } from "../../../components/PasswordInput";
import {
  useAppRegistry,
  type AppDescriptor,
  type ParamSpec,
} from "./appRegistry";
import { CmsIcon } from "./CmsIcon";

type Domain = { id: string; name: string };

type Props = {
  open: boolean;
  onClose: () => void;
  onSuccess: () => void;
  defaultAdminEmail?: string;
  // presetAppType pre-targets the drawer to one app (set when opened from
  // a Catalog card). When provided, the in-drawer app picker is hidden and
  // the chosen app_type wins over the wordpress default.
  presetAppType?: string;
};

type CreatedResult = {
  appType: string;
  domainName: string;
  adminUsername: string;
  adminEmail: string;
  adminPassword: string;
  dbId: string;
  emailLogin: boolean;
};

type ApiError = {
  response?: { data?: { error?: string; detail?: string } };
  message?: string;
};

function extractError(err: unknown, fallback: string): string {
  const e = err as ApiError;
  return (
    e.response?.data?.detail ??
    e.response?.data?.error ??
    e.message ??
    fallback
  );
}

const LOCALES: { value: string; label: string }[] = [
  { value: "en_US", label: "English (US)" },
  { value: "en_GB", label: "English (UK)" },
  { value: "he_IL", label: "עברית (Hebrew)" },
  { value: "ar",    label: "العربية (Arabic)" },
  { value: "es_ES", label: "Español (Spain)" },
  { value: "fr_FR", label: "Français" },
  { value: "de_DE", label: "Deutsch" },
  { value: "it_IT", label: "Italiano" },
  { value: "pt_BR", label: "Português (Brasil)" },
  { value: "ru_RU", label: "Русский" },
  { value: "ja",    label: "日本語" },
  { value: "zh_CN", label: "简体中文" },
];

// Subdirectory validation: must start with lowercase letter or digit,
// 1-64 chars total, only lowercase letters, digits, underscore, dash.
// Matches server-side regex: ^[a-z0-9][a-z0-9_-]{0,63}$
const SUBDIRECTORY_PATTERN = /^[a-z0-9][a-z0-9_-]{0,63}$/;
const RESERVED_SUBDIRS = new Set(["wp-admin", "wp-includes", "wp-content"]);

// snake_case → "Title case" with a few hand-tweaks for the common
// fields. The fallback transform produces readable labels for any
// field a future descriptor introduces.
function humanizeFieldName(name: string): string {
  const overrides: Record<string, string> = {
    site_title: "Site title",
    admin_username: "Admin username",
    admin_email: "Admin email",
    admin_password: "Admin password",
    locale: "Locale",
    version: "Version",
    license: "License",
    site_name: "Site name",
    admin_user: "Admin user",
    language: "Language",
  };
  if (overrides[name]) return overrides[name];
  return name
    .split(/[_\s]+/)
    .map((p, i) => (i === 0 ? p[0].toUpperCase() + p.slice(1) : p))
    .join(" ");
}

// Field ordering — site title first, admin block next, then anything
// else alphabetically. Avoids needing an explicit Order field on
// ParamSpec while still giving every app a sensible top-down flow.
const FIELD_ORDER: Record<string, number> = {
  site_title: 1,
  site_name: 2,
  version: 5,
  admin_username: 10,
  admin_user: 11,
  admin_email: 12,
  admin_password: 13,
  locale: 50,
  language: 51,
  license: 60,
};
function orderedFields(schema: Record<string, ParamSpec>): string[] {
  return Object.keys(schema).sort((a, b) => {
    const pa = FIELD_ORDER[a] ?? 100;
    const pb = FIELD_ORDER[b] ?? 100;
    if (pa !== pb) return pa - pb;
    return a.localeCompare(b);
  });
}

// renderParamField produces an AntD <Form.Item> for one descriptor
// param. Unknown types render as plain Input so a future server can
// add a type without breaking older bundles, with a console hint so
// we notice the mismatch in dev.
function renderParamField(
  name: string,
  spec: ParamSpec,
  defaultValue: string | undefined,
): React.ReactNode {
  const label = humanizeFieldName(name);
  const required = !!spec.required;
  const baseRules: Record<string, unknown>[] = [];
  if (required) {
    baseRules.push({ required: true, message: `${label} is required` });
  }

  const initialValue =
    defaultValue !== undefined
      ? defaultValue
      : spec.default !== undefined && spec.default !== null
      ? spec.default
      : undefined;

  // Locale gets the curated dropdown rather than a plain text input —
  // the descriptor declares Type:"string" but a fixed-list Select is
  // a meaningful UX win. Same idea would apply to a future "language"
  // field on MediaWiki.
  if (name === "locale" || name === "language") {
    return (
      <Form.Item
        key={name}
        label={label}
        name={name}
        initialValue={initialValue ?? "en_US"}
        rules={baseRules}
        extra={spec.description}
      >
        <Select options={LOCALES} showSearch optionFilterProp="label" />
      </Form.Item>
    );
  }

  switch (spec.type) {
    case "string":
      if (spec.pattern) {
        baseRules.push({
          pattern: new RegExp(spec.pattern),
          message: "Does not match expected format",
        });
      }
      return (
        <Form.Item
          key={name}
          label={label}
          name={name}
          initialValue={initialValue}
          rules={baseRules}
          extra={spec.description}
        >
          <Input
            autoComplete="off"
            placeholder={
              name === "admin_username" && !required ? "(auto-generated)" : undefined
            }
          />
        </Form.Item>
      );
    case "email":
      baseRules.push({ type: "email", message: "Must be a valid email" });
      return (
        <Form.Item
          key={name}
          label={label}
          name={name}
          initialValue={initialValue}
          rules={baseRules}
          extra={spec.description}
        >
          <Input autoComplete="off" />
        </Form.Item>
      );
    case "password":
      return (
        <Form.Item
          key={name}
          label={label}
          name={name}
          rules={baseRules}
          extra={
            spec.description ??
            (required
              ? undefined
              : "Leave blank to have us generate a strong random password.")
          }
        >
          <PasswordInput
            autoComplete="new-password"
            placeholder={required ? undefined : "(auto-generated)"}
          />
        </Form.Item>
      );
    case "enum":
      return (
        <Form.Item
          key={name}
          label={label}
          name={name}
          initialValue={initialValue}
          rules={baseRules}
          extra={spec.description}
        >
          <Select
            options={(spec.values ?? []).map((v) => ({
              value: v,
              label: spec.value_labels?.[v] ?? v,
            }))}
            showSearch
            optionFilterProp="label"
          />
        </Form.Item>
      );
    case "bool":
      return (
        <div key={name} style={{ marginBottom: 24 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
            <Form.Item
              name={name}
              valuePropName="checked"
              initialValue={
                typeof initialValue === "boolean" ? initialValue : false
              }
              noStyle
            >
              <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
            </Form.Item>
            <Typography.Text>{label}</Typography.Text>
          </div>
          {spec.description && (
            <Typography.Text type="secondary">{spec.description}</Typography.Text>
          )}
        </div>
      );
    default:
      return (
        <Form.Item
          key={name}
          label={label}
          name={name}
          initialValue={initialValue}
          extra={spec.description ?? `Unknown param type: ${spec.type}`}
        >
          <Input autoComplete="off" />
        </Form.Item>
      );
  }
}

// Fallback schema used when the API didn't return /applications/registry
// or the selected app has no install_param_schema. Mirrors the legacy
// WordPress modal so users on a stale bundle still get a working form.
const FALLBACK_WP_SCHEMA: Record<string, ParamSpec> = {
  site_title: { type: "string", required: true, default: "My WordPress site" },
  admin_username: { type: "string", required: true, default: "admin", pattern: "^[a-zA-Z0-9_.-]{3,60}$" },
  admin_email: { type: "email", required: true },
  admin_password: { type: "password", required: false },
  locale: { type: "string", required: false, default: "en_US" },
};

export const InstallApplicationModal = ({
  open,
  onClose,
  onSuccess,
  defaultAdminEmail,
  presetAppType,
}: Props) => {
  const { t } = useTranslation();
  const [form] = Form.useForm<Record<string, unknown>>();
  const [submitting, setSubmitting] = useState(false);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [loadingDomains, setLoadingDomains] = useState(false);
  const [result, setResult] = useState<CreatedResult | null>(null);

  // Registry comes from the shared TanStack query (same cache the Catalog
  // tab reads) — fetched once, not per-drawer-open. On error we fall back
  // to a WP-only list so a stale bundle still gets a working form.
  const registry = useAppRegistry(open);
  const loadingApps = registry.isLoading;
  const apps = useMemo<AppDescriptor[]>(() => {
    if (registry.data && registry.data.length > 0) return registry.data;
    if (registry.isError) {
      return [
        {
          name: "wordpress",
          display_name: "WordPress",
          default_subdirectory: "",
          requires_db: true,
          install_param_schema: FALLBACK_WP_SCHEMA,
        },
      ];
    }
    return [];
  }, [registry.data, registry.isError]);
  const qc = useQueryClient();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);

  const selectedAppType = Form.useWatch("app_type", form) as string | undefined;
  const selectedDomainId = Form.useWatch("domain_id", form);
  const domainSelected = !!selectedDomainId;
  const selectedApp = apps.find((a) => a.name === selectedAppType);

  // The schema we render. Falls back to the legacy WP shape when:
  // (a) no app is selected yet (typical first-paint), or
  // (b) the descriptor predates install_param_schema in the API
  //     (older API build, newer UI).
  const activeSchema: Record<string, ParamSpec> = useMemo(() => {
    if (selectedApp?.install_param_schema && Object.keys(selectedApp.install_param_schema).length > 0) {
      return selectedApp.install_param_schema;
    }
    return FALLBACK_WP_SCHEMA;
  }, [selectedApp]);

  const fieldOrder = useMemo(() => orderedFields(activeSchema), [activeSchema]);

  const refreshLists = () => {
    qc.invalidateQueries({ queryKey: ["list", "applications"] });
    qc.invalidateQueries({ queryKey: ["list", "databases"] });
    qc.invalidateQueries({ queryKey: ["list", "database-users"] });
    onSuccess();
  };

  // Load the domain list when the drawer opens (registry is the shared
  // query above, so only domains need an on-open fetch).
  useEffect(() => {
    if (!open) return;
    let alive = true;
    setLoadingDomains(true);
    apiClient
      .get<{ data: Domain[] }>("/domains", { params: { page: 1, page_size: 100 } })
      .then((resp) => {
        if (!alive) return;
        setDomains(resp.data?.data ?? []);
      })
      .catch((err) => {
        if (!alive) return;
        feedback.message.error(extractError(err, "Failed to load domains"));
      })
      .finally(() => {
        if (alive) setLoadingDomains(false);
      });
    return () => {
      alive = false;
    };
  }, [open]);

  // Seed the selected app_type once the registry is available. presetAppType
  // (from a Catalog card) wins over the wordpress default; falls back to
  // wordpress, then the first descriptor. Re-runs on each open so reopening
  // resets to the preset/default. `apps` is memoised, so this fires only on
  // open / data / preset changes — not every render.
  useEffect(() => {
    if (!open || apps.length === 0) return;
    const has = (n?: string) => !!n && apps.some((a) => a.name === n);
    const wp = apps.find((a) => a.name === "wordpress");
    const defaultName = has(presetAppType)
      ? (presetAppType as string)
      : (wp?.name ?? apps[0]?.name ?? "wordpress");
    form.setFieldsValue({ app_type: defaultName });
  }, [open, apps, presetAppType, form]);

  // When the user switches App, clear every per-app field and apply
  // the new descriptor's defaults. Without this, a "site_title" left
  // over from WordPress would persist into the DokuWiki form and the
  // user would submit stale data.
  useEffect(() => {
    if (!open) return;
    if (!selectedApp) return;
    const fieldsToClear: Record<string, undefined> = {};
    Object.keys(activeSchema).forEach((k) => {
      fieldsToClear[k] = undefined;
    });
    form.setFieldsValue(fieldsToClear);
    // Apply per-field defaults — admin_email opts to the logged-in
    // user's email when the descriptor doesn't override it.
    const defaults: Record<string, string | number | boolean> = {};
    Object.entries(activeSchema).forEach(([name, spec]) => {
      if (name === "admin_email" && defaultAdminEmail) {
        defaults[name] = defaultAdminEmail;
        return;
      }
      if (spec.default !== undefined && spec.default !== null) {
        defaults[name] = spec.default;
      }
    });
    form.setFieldsValue(defaults);
  }, [selectedApp?.name, activeSchema, defaultAdminEmail, form, open]);

  const reset = () => {
    form.resetFields();
    setResult(null);
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const handleSubmit = async () => {
    try {
      await form.validateFields();
    } catch {
      return;
    }
    const vals = form.getFieldsValue();
    setSubmitting(true);
    try {
      // Build `params` from ONLY the descriptor's declared fields so a
      // form with leftover keys (e.g. after the user switched apps mid-
      // edit) doesn't send unknown keys the server would 400 on.
      const params: Record<string, unknown> = {};
      Object.keys(activeSchema).forEach((name) => {
        const v = vals[name];
        // Drop optional empty strings — the server treats absent and
        // empty as "use default", but rejects empty when Required.
        if (v === "" && !activeSchema[name].required) return;
        if (v === undefined) return;
        params[name] = v;
      });

      const resp = await apiClient.post<{
        id: string;
        app_type: string;
        domain_id: string;
        db_id: string;
        admin_username: string;
        admin_email: string;
        admin_password: string;
      }>("/applications", {
        app_type: vals.app_type,
        domain_id: vals.domain_id,
        use_www: vals.use_www || false,
        subdirectory: vals.subdirectory || "",
        params,
      });
      const domainName =
        domains.find((d) => d.id === vals.domain_id)?.name ?? String(vals.domain_id);
      setResult({
        appType: resp.data.app_type,
        domainName,
        adminUsername: resp.data.admin_username,
        adminEmail: resp.data.admin_email,
        adminPassword: resp.data.admin_password,
        dbId: resp.data.db_id,
        emailLogin: !!selectedApp?.email_login,
      });
      refreshLists();
    } catch (err) {
      const e = err as ApiError;
      if (e.response?.data?.error === "invalid_subdirectory") {
        const detail = e.response.data.detail ?? "Invalid subdirectory";
        form.setFields([{ name: "subdirectory", errors: [detail] }]);
      } else if (e.response?.data?.error === "install_exists") {
        form.setFields([
          {
            name: "subdirectory",
            errors: [
              "This domain already hosts the same application at that location — pick a different subdirectory",
            ],
          },
        ]);
      } else {
        feedback.message.error(extractError(err, "Failed to install application"));
      }
    } finally {
      setSubmitting(false);
    }
  };


  const validateSubdirectory = (_: unknown, value: string) => {
    if (!value) {
      return Promise.resolve();
    }
    if (!SUBDIRECTORY_PATTERN.test(value)) {
      return Promise.reject(
        new Error("Must start with letter/digit; 1–64 chars, letters, digits, underscore, dash")
      );
    }
    if (RESERVED_SUBDIRS.has(value.toLowerCase())) {
      return Promise.reject(
        new Error(`"${value}" is reserved; use a different subdirectory`)
      );
    }
    return Promise.resolve();
  };

  const availableDomains = domains;

  return (
    <Drawer
      title={t("installapplicationmodal.install_application")}
      open={open}
      onClose={handleClose}
      maskClosable={!submitting && !result}
      width={isDesktop ? 560 : undefined}
      placement="right"
      destroyOnClose
      extra={
        result ? (
          <Button type="primary" onClick={handleClose}>
            Done
          </Button>
        ) : (
          <Space>
            <Button onClick={handleClose} disabled={submitting}>
              Cancel
            </Button>
            <Button
              type="primary"
              loading={submitting}
              onClick={handleSubmit}
              disabled={availableDomains.length === 0 || apps.length === 0}
            >
              Install {selectedApp?.display_name ?? "application"}
            </Button>
          </Space>
        )
      }
    >
      {!result && (
        <>
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            title={t("installapplicationmodal.what_happens_next")}
            description={
              <>
                We&rsquo;ll provision the application&rsquo;s database (if
                it needs one), download the app, run its installer, and
                show the admin password once. The install runs in the
                background — the row flips from
                &ldquo;installing&rdquo; to &ldquo;ready&rdquo; when
                it&rsquo;s done (usually ~1 minute).
              </>
            }
          />
          {availableDomains.length === 0 && !loadingDomains && (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              title={t("installapplicationmodal.no_domains_yet")}
              description={t("installapplicationmodal.you_have_no_domains_create_one_first")}
            />
          )}
          <Form
            form={form}
            layout="vertical"
            disabled={submitting}
            initialValues={{
              app_type: "wordpress",
              use_www: false,
              subdirectory: "",
            }}
          >
            {presetAppType ? (
              // Opened from a Catalog card — app already chosen. Keep the
              // value submitted via a hidden field, show a read-only header
              // instead of the picker.
              <>
                <Form.Item name="app_type" hidden>
                  <Input />
                </Form.Item>
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    marginBottom: 16,
                  }}
                >
                  <CmsIcon appType={presetAppType} size={24} />
                  <Typography.Text strong style={{ fontSize: 16 }}>
                    {selectedApp?.display_name ?? presetAppType}
                  </Typography.Text>
                </div>
              </>
            ) : (
              <Form.Item
                label={t("installapplicationmodal.application")}
                name="app_type"
                rules={[{ required: true, message: "Pick an application" }]}
              >
                <Select
                  placeholder={t("installapplicationmodal.select_an_application")}
                  loading={loadingApps}
                  suffixIcon={<AppstoreOutlined />}
                  options={apps.map((a) => ({
                    value: a.name,
                    label: a.display_name,
                  }))}
                  showSearch
                  optionFilterProp="label"
                />
              </Form.Item>
            )}

            {selectedApp?.description && (
              <Form.Item style={{ marginTop: -8, marginBottom: 16 }}>
                <Typography.Text type="secondary">
                  {selectedApp.description}
                </Typography.Text>
              </Form.Item>
            )}

            {selectedApp?.install_notice && (
              <Alert
                type="warning"
                showIcon
                style={{ marginBottom: 16 }}
                message={selectedApp.install_notice}
              />
            )}

            <Form.Item
              label={t("installapplicationmodal.domain")}
              name="domain_id"
              rules={[{ required: true, message: "Pick a domain" }]}
            >
              <Select
                placeholder={t("installapplicationmodal.select_a_domain")}
                loading={loadingDomains}
                options={availableDomains.map((d) => ({
                  value: d.id,
                  label: d.name,
                }))}
                showSearch
                optionFilterProp="label"
              />
            </Form.Item>

            {domainSelected && (
              <>
                {!selectedApp?.root_only && (
                  <>
                <div style={{ marginBottom: 16 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                    <Form.Item name="use_www" valuePropName="checked" noStyle>
                      <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
                    </Form.Item>
                    <Typography.Text>Use www prefix</Typography.Text>
                  </div>
                  <Typography.Text type="secondary">
                    Install on www.domain.com instead of domain.com
                  </Typography.Text>
                </div>

                <Form.Item
                  label={t("installapplicationmodal.directory_optional")}
                  name="subdirectory"
                  rules={[{ validator: validateSubdirectory }]}
                >
                  <Input
                    placeholder={
                      selectedApp?.default_subdirectory
                        ? `Suggested: ${selectedApp.default_subdirectory}`
                        : "Leave empty to install in root"
                    }
                    autoComplete="off"
                  />
                </Form.Item>
                  </>
                )}

                {fieldOrder.map((name) =>
                  renderParamField(name, activeSchema[name], undefined),
                )}
              </>
            )}
          </Form>
        </>
      )}

      {result && (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Alert
            type="success"
            showIcon
            icon={<CheckCircleTwoTone twoToneColor="#52c41a" />}
            title={t("installapplicationmodal.install_queued")}
            description={t("installapplicationmodal.the_install_runs_in_the_background_copy_the")}
          />
          <div>
            <Typography.Text strong>Domain</Typography.Text>
            <CopyableInput value={result.domainName} />
          </div>
          {!result.emailLogin && (
            <div>
              <Typography.Text strong>Admin username</Typography.Text>
              <CopyableInput value={result.adminUsername} />
            </div>
          )}
          <div>
            <Typography.Text strong>Admin email</Typography.Text>
            <CopyableInput value={result.adminEmail} />
          </div>
          <div>
            <Typography.Text strong>Admin password</Typography.Text>
            <CopyableInput value={result.adminPassword} secret />
          </div>
        </Space>
      )}
    </Drawer>
  );
};
