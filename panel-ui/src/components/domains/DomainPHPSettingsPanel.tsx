// DomainPHPSettingsPanel — the per-domain PHP surface (GH #1543 / GH #1332),
// extracted from UserPHPSettingsPage's "Version & Domains" tab so it can render
// two ways: on the tenant Web Domain page as a tab (domain fixed by the route),
// and inside the standalone PHP Settings page behind a domain picker. It owns
// only the DOMAIN-scoped controls — the PHP version for this domain
// (POST/DELETE /domains/:id/php-pool) and the php.ini limit overrides
// (GET/PATCH /domains/:id/php-settings). The account/pool-level tabs on the
// standalone page (CLI/Composer, Performance, OPcache, Extensions, Xdebug) are
// per-version-pool — shared by every domain on that version — so they stay put.
//
// GH #1701 (lxsdevcode): each setting shows whether it is Custom or inherits
// the pool default (live, as you edit), with a Reset to default link on a
// custom one. The sections collapse, and a sticky bar at the bottom counts the
// unsaved changes and holds Save and Discard. Closing or reloading the page
// with unsaved changes asks first; so does switching the domain's tab or the
// picked domain, through onDirtyChange (the app's BrowserRouter has no
// navigation blocker, so the sidebar and the Back button are not guarded).
import { useEffect, useReducer, useRef, useState } from "react";
import type { ReactElement, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  Affix,
  AutoComplete,
  Button,
  Col,
  Collapse,
  Flex,
  Form,
  Popconfirm,
  Row,
  Select,
  Space,
  Spin,
  Tag,
  Typography,
  theme,
} from "antd";
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
  // GH #1701 Slice 3 admin values. php_open_basedir is the stored token form
  // ({DOCROOT}, {WEBSPACEROOT}, {TMP} and absolute paths).
  php_open_basedir?: string | null;
  php_allow_url_fopen?: boolean | null;
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
  php_open_basedir?: string | null;
  php_allow_url_fopen?: boolean | null;
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
  php_open_basedir: "open_basedir",
  php_allow_url_fopen: "allow_url_fopen",
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

// GH #1332 per-domain runtime directives. The agent pins display_errors Off on
// every PHP vhost that sets no value of its own, whatever the pool or php.ini
// says (a pool display_errors override never reaches a domain), so the
// inherited value is always Off (GH #1701: it used to read "Use pool default"
// with no value). "On" surfaces errors for this domain only.
const DISPLAY_ERRORS_DEFAULT_LABEL = "Off (Default)";
const DISPLAY_ERRORS_OPTIONS = [
  { label: DISPLAY_ERRORS_DEFAULT_LABEL, value: null as boolean | null },
  { label: "On (show errors)", value: true as boolean | null },
  { label: "Off", value: false as boolean | null },
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

// GH #1701 Slice 3: open_basedir presets. The server checks every value: a
// tenant may list only folders inside their home, an admin may add other paths
// but never another user's home. The pool default is the home folder plus the
// temp folders; the database socket and the cache-purge folder are always
// added.
const OPEN_BASEDIR_OPTIONS = [
  { label: "This domain's folder + temp folders", value: "{DOCROOT}:{TMP}" },
  { label: "This domain's folder only (most apps then cannot upload files)", value: "{DOCROOT}" },
  { label: "Home folder + temp folders", value: "{WEBSPACEROOT}:{TMP}" },
];

// The full IANA/PHP timezone list, shared with admin Server Settings so both
// selectors offer the same zones (GH #1332). Searchable in the Select below.
const TIMEZONE_OPTIONS = [
  { label: "Use pool default", value: null as string | null },
  ...IANA_TIMEZONES.map((z) => ({ label: z, value: z as string | null })),
];

type FormField = keyof PHPSettingsFormData;
const FORM_FIELDS = Object.keys(FIELD_DIRECTIVE) as FormField[];

// The form values for stored settings. The API omits a field the domain does
// not override (omitempty); it becomes null, which selects the inherit option
// (GH #1705: undefined would show the placeholder instead).
function formValuesOf(s: DomainPHPSettings): PHPSettingsFormData {
  return Object.fromEntries(FORM_FIELDS.map((f) => [f, s[f] ?? null])) as PHPSettingsFormData;
}

// A form value as it would be saved: absent is null, and open_basedir is
// trimmed with an empty one meaning inherit (see onSave).
function savedForm(field: FormField, v: unknown): unknown {
  if (v === undefined || v === null) return null;
  if (field === "php_open_basedir" && typeof v === "string") return v.trim() || null;
  return v;
}

const fieldSet = (v: unknown) => v !== null && v !== undefined;

// The collapsible sections, in page order. The first three are open by
// default; Security opens on load only when the domain sets one of its values,
// so a collapsed section never hides an override.
const SECTIONS: { key: string; fields: FormField[] }[] = [
  {
    key: "resources",
    fields: ["php_memory_limit", "php_upload_max_filesize", "php_post_max_size", "php_max_input_vars"],
  },
  { key: "execution", fields: ["php_max_execution_time", "php_max_input_time"] },
  {
    key: "runtime",
    fields: [
      "php_display_errors",
      "php_error_reporting",
      "php_timezone",
      "php_log_errors",
      "php_file_uploads",
      "php_short_open_tag",
    ],
  },
  { key: "security", fields: ["php_open_basedir", "php_allow_url_fopen"] },
];
const OPEN_BY_DEFAULT = ["resources", "execution", "runtime"];

function openSectionsFor(s: DomainPHPSettings): string[] {
  return SECTIONS.filter(
    (sec) => OPEN_BY_DEFAULT.includes(sec.key) || sec.fields.some((f) => fieldSet(s[f])),
  ).map((sec) => sec.key);
}

// Each control sits in a row with its Reset to default link.
const CONTROL_STYLE = { flex: 1, minWidth: 0 };

export interface DomainPHPSettingsPanelProps {
  domainId: string;
  // Told whether the form holds unsaved changes, so the host can ask before
  // its own navigation unmounts the panel (a tab switch, another domain).
  onDirtyChange?: (dirty: boolean) => void;
}

export function DomainPHPSettingsPanel({ domainId, onDirtyChange }: DomainPHPSettingsPanelProps) {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const logStreams = useDomainLogStreams();
  const [phpSettings, setPhpSettings] = useState<DomainPHPSettings | null>(null);
  const [availableVersions, setAvailableVersions] = useState<string[]>([]);
  const [versionSaving, setVersionSaving] = useState(false);
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [openSections, setOpenSections] = useState<string[]>(OPEN_BY_DEFAULT);
  const [form] = Form.useForm<PHPSettingsFormData>();
  // GH #1701: the panel renders from the live form values (Custom / Unsaved
  // tags, the unsaved count, Save), read from the store. AntD does not
  // re-render the parent on a form change, so every change re-renders it here,
  // synchronously: a user edit (onValuesChange), a reset and a reseed.
  // (Form.useWatch batches its re-render, so a quick Save click could still
  // see the old state.)
  const [, rerender] = useReducer((n: number) => n + 1, 0);

  // resetFields BEFORE setFieldsValue: setFieldsValue does not clear the
  // touched flags, so a domain switch would otherwise keep a stale dirty state.
  const seedForm = (s: DomainPHPSettings) => {
    form.resetFields();
    form.setFieldsValue(formValuesOf(s));
    rerender();
  };

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
        seedForm(resp.data);
        setOpenSections(openSectionsFor(resp.data));
      } catch {
        feedback.message.error("Failed to load PHP settings");
      } finally {
        setLoading(false);
      }
    })();
    // seedForm only uses the stable form instance.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
        // An emptied open_basedir clears the override (inherit the pool's).
        php_open_basedir: outgoing("php_open_basedir", values)?.trim() || null,
        php_allow_url_fopen: outgoing("php_allow_url_fopen", values),
      });
      feedback.message.success("PHP settings updated successfully");
      // Reload settings to confirm, and show them as stored (the server may
      // tidy a value, e.g. an open_basedir path), so nothing reads as unsaved.
      const resp = await apiClient.get<DomainPHPSettings>(
        `/domains/${domainId}/php-settings`,
      );
      setPhpSettings(resp.data);
      seedForm(resp.data);
    } catch (err) {
      const e = err as { response?: { data?: { error?: string; detail?: string } } };
      const apiError = e.response?.data?.error;
      feedback.message.error(
        apiError === "php_setting_not_permitted" && e.response?.data?.detail
          ? e.response.data.detail
          : // A value the server refused names the setting and the reason,
            // e.g. an open_basedir folder outside the home directory.
            apiError?.startsWith("invalid_php_setting: ")
            ? apiError.slice("invalid_php_setting: ".length)
            : "Failed to update PHP settings",
      );
    } finally {
      setSubmitting(false);
    }
  };

  // GH #1701: the live form values (see rerender above).
  const current = form.getFieldsValue(true) as PHPSettingsFormData;
  // A field is unsaved when its value differs from the stored one, not merely
  // touched: picking another value and then the original again leaves nothing
  // to save. A locked field cannot change.
  const changed: FormField[] = phpSettings
    ? FORM_FIELDS.filter(
        (f) => !locked(f) && savedForm(f, current[f]) !== savedForm(f, phpSettings[f]),
      )
    : [];
  const dirty = changed.length > 0;
  const isCustom = (field: FormField) => fieldSet(savedForm(field, current[field]));

  const onDirtyChangeRef = useRef(onDirtyChange);
  onDirtyChangeRef.current = onDirtyChange;
  useEffect(() => {
    onDirtyChangeRef.current?.(dirty);
  }, [dirty]);
  // An unmounted panel holds nothing unsaved.
  useEffect(() => () => onDirtyChangeRef.current?.(false), []);

  // Closing or reloading the tab with unsaved changes asks first.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const onDiscard = () => {
    if (phpSettings) seedForm(phpSettings);
  };

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

  // GH #1701: a tenant sees "Set by your administrator" on a locked directive;
  // an admin sees "Admin only" on one the tenant cannot change.
  const policyTag = (field: FormField) => {
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
  // GH #1332 item 6, GH #1701: every setting is tagged Custom (the domain sets
  // it) or Pool default (it inherits), following the form as you edit, and
  // Unsaved until it is saved.
  const overrideLabel = (text: string, field: FormField) => (
    <Space size={6}>
      {text}
      {isCustom(field) ? (
        <Tag color="blue" style={{ marginInlineEnd: 0 }}>
          Custom
        </Tag>
      ) : (
        <Tag style={{ marginInlineEnd: 0 }}>Pool default</Tag>
      )}
      {changed.includes(field) && (
        <Tag color="orange" style={{ marginInlineEnd: 0 }}>
          Unsaved
        </Tag>
      )}
      {policyTag(field)}
    </Space>
  );

  // One setting: its label, its control and, while it is custom, a Reset to
  // default link (the same as picking the inherit option). The link sits
  // beside the control, outside the <label>, so it never joins the control's
  // accessible name.
  const settingItem = (
    field: FormField,
    text: string,
    control: ReactElement,
    extra?: ReactNode,
  ) => (
    <Col xs={24} sm={12} key={field}>
      <Form.Item label={overrideLabel(text, field)} htmlFor={field} extra={extra}>
        <Flex gap={8} align="center">
          <Form.Item name={field} noStyle>
            {control}
          </Form.Item>
          {!locked(field) && isCustom(field) && (
            <Button
              type="link"
              size="small"
              style={{ paddingInline: 0 }}
              aria-label={`Reset ${text} to default`}
              onClick={() => {
                form.setFieldValue(field, null);
                rerender();
              }}
            >
              Reset to default
            </Button>
          )}
        </Flex>
      </Form.Item>
    </Col>
  );

  // A collapsed section still says how many of its settings are custom or
  // unsaved.
  const sectionExtra = (key: string) => {
    const fields = SECTIONS.find((s) => s.key === key)?.fields ?? [];
    const custom = fields.filter(isCustom).length;
    const unsaved = fields.filter((f) => changed.includes(f)).length;
    return (
      <Space size={4}>
        {custom > 0 && (
          <Tag color="blue" style={{ marginInlineEnd: 0 }}>
            {custom} custom
          </Tag>
        )}
        {unsaved > 0 && (
          <Tag color="orange" style={{ marginInlineEnd: 0 }}>
            {unsaved} unsaved
          </Tag>
        )}
      </Space>
    );
  };

  const sections: { key: string; label: string; children: ReactNode }[] = [
    {
      key: "resources",
      label: "Resource Limits",
      children: (
        <>
          {/* GH #1332 item 3: these are DOMAIN-level php.ini overrides,
              applied to this domain regardless of which PHP version it runs —
              not per-version. Label it so that's clear. */}
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            Applied to this domain across all PHP versions. Per-version worker
            tuning lives under Performance.
          </Typography.Paragraph>
          <Row gutter={[16, 16]}>
            {settingItem(
              "php_memory_limit",
              t("userphpsettingspage.memory_limit"),
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_memory_limit")}
                placeholder={inheritPlaceholder("memory_limit")}
                allowClear
                options={withDefault(MEMORY_LIMIT_OPTIONS, "memory_limit")}
              />,
            )}
            {settingItem(
              "php_upload_max_filesize",
              t("userphpsettingspage.upload_max_file_size"),
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_upload_max_filesize")}
                placeholder={inheritPlaceholder("upload_max_filesize")}
                allowClear
                options={withDefault(UPLOAD_MAX_OPTIONS, "upload_max_filesize")}
              />,
            )}
            {settingItem(
              "php_post_max_size",
              t("userphpsettingspage.post_max_size"),
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_post_max_size")}
                placeholder={inheritPlaceholder("post_max_size")}
                allowClear
                options={withDefault(POST_MAX_OPTIONS, "post_max_size")}
              />,
            )}
            {settingItem(
              "php_max_input_vars",
              t("userphpsettingspage.max_input_variables"),
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_max_input_vars")}
                placeholder={inheritPlaceholder("max_input_vars")}
                allowClear
                options={withDefault(MAX_INPUT_VARS_OPTIONS, "max_input_vars")}
              />,
            )}
          </Row>
        </>
      ),
    },
    {
      key: "execution",
      label: "Execution Limits",
      children: (
        <Row gutter={[16, 16]}>
          {settingItem(
            "php_max_execution_time",
            t("userphpsettingspage.max_execution_time"),
            <Select
              style={CONTROL_STYLE}
              disabled={locked("php_max_execution_time")}
              placeholder={inheritPlaceholder("max_execution_time", sizeFmt("s"))}
              allowClear
              options={withDefault(MAX_EXECUTION_TIME_OPTIONS, "max_execution_time", sizeFmt("s"))}
            />,
          )}
          {settingItem(
            "php_max_input_time",
            t("userphpsettingspage.max_input_time"),
            <Select
              style={CONTROL_STYLE}
              disabled={locked("php_max_input_time")}
              placeholder={inheritPlaceholder("max_input_time", sizeFmt("s"))}
              allowClear
              options={withDefault(MAX_INPUT_TIME_OPTIONS, "max_input_time", sizeFmt("s"))}
            />,
          )}
        </Row>
      ),
    },
    {
      key: "runtime",
      label: "Error Handling & Runtime",
      children: (
        <>
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            These apply to this domain across all its PHP versions. Turn{" "}
            <strong>Display errors</strong> on only for development — it prints
            PHP errors to visitors.
          </Typography.Paragraph>
          <Row gutter={[16, 16]}>
            {settingItem(
              "php_display_errors",
              "Display errors",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_display_errors")}
                placeholder={DISPLAY_ERRORS_DEFAULT_LABEL}
                allowClear
                options={DISPLAY_ERRORS_OPTIONS}
              />,
              "Shows PHP errors in the page output. Keep off on public/production sites.",
            )}
            {settingItem(
              "php_error_reporting",
              "Error reporting",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_error_reporting")}
                placeholder={inheritPlaceholder("error_reporting", errorReportingFmt)}
                allowClear
                options={withDefault(ERROR_REPORTING_OPTIONS, "error_reporting", errorReportingFmt)}
              />,
            )}
            {settingItem(
              "php_timezone",
              "Timezone",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_timezone")}
                showSearch
                placeholder={inheritPlaceholder("date.timezone", timezoneFmt)}
                allowClear
                optionFilterProp="label"
                options={withDefault(TIMEZONE_OPTIONS, "date.timezone", timezoneFmt)}
              />,
              "date.timezone for this domain's PHP.",
            )}
            {settingItem(
              "php_log_errors",
              "Log errors",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_log_errors")}
                placeholder={inheritPlaceholder("log_errors", flagFmt)}
                allowClear
                options={withDefault(FLAG_OPTIONS, "log_errors", flagFmt)}
              />,
              "Records PHP errors in the error log. Visitors never see logged errors.",
            )}
            {settingItem(
              "php_file_uploads",
              "File uploads",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_file_uploads")}
                placeholder={inheritPlaceholder("file_uploads", flagFmt)}
                allowClear
                options={withDefault(FLAG_OPTIONS, "file_uploads", flagFmt)}
              />,
              "Lets this domain's PHP accept uploaded files. Off breaks uploads in WordPress and most apps.",
            )}
            {settingItem(
              "php_short_open_tag",
              "Short open tag",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_short_open_tag")}
                placeholder={inheritPlaceholder("short_open_tag", flagFmt)}
                allowClear
                options={withDefault(FLAG_OPTIONS, "short_open_tag", flagFmt)}
              />,
              "Treats <? as a PHP opening tag. Only for old code that needs it: files that start with <?xml stop working.",
            )}
          </Row>
        </>
      ),
    },
    {
      // GH #1701 Slice 3: security-sensitive, admin only unless the owner's
      // package opts the tenant in.
      key: "security",
      label: "Security",
      children: (
        <>
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            These limit what this domain&apos;s PHP code can reach. The server
            checks every value.
          </Typography.Paragraph>
          <Row gutter={[16, 16]}>
            {settingItem(
              "php_open_basedir",
              "Allowed folders (open_basedir)",
              <AutoComplete
                style={CONTROL_STYLE}
                disabled={locked("php_open_basedir")}
                placeholder="Home folder + temp folders (Default)"
                allowClear
                filterOption={false}
                options={OPEN_BASEDIR_OPTIONS}
              />,
              <>
                The folders PHP may open files in, separated by <code>:</code>.
                Use <code>{"{DOCROOT}"}</code> for this domain&apos;s folder,{" "}
                <code>{"{WEBSPACEROOT}"}</code> for the home folder and{" "}
                <code>{"{TMP}"}</code> for the temp folders, or absolute paths.
                Without the temp folders, uploads fail in WordPress and most
                apps.
              </>,
            )}
            {settingItem(
              "php_allow_url_fopen",
              "Remote file access (allow_url_fopen)",
              <Select
                style={CONTROL_STYLE}
                disabled={locked("php_allow_url_fopen")}
                placeholder={inheritPlaceholder("allow_url_fopen", flagFmt)}
                allowClear
                options={withDefault(FLAG_OPTIONS, "allow_url_fopen", flagFmt)}
              />,
              "Lets file functions such as file_get_contents() read http:// and ftp:// URLs. cURL works either way.",
            )}
          </Row>
        </>
      ),
    },
  ];

  // The count of unsaved changes, Discard and Save.
  const saveBar = (
    <Flex
      align="center"
      gap={12}
      wrap
      style={{
        marginTop: 16,
        padding: "12px 0",
        background: token.colorBgContainer,
        borderTop: `1px solid ${token.colorBorderSecondary}`,
      }}
    >
      <Typography.Text
        role="status"
        type={dirty ? "warning" : "secondary"}
        style={{ flex: "1 1 auto", whiteSpace: "nowrap" }}
      >
        {dirty ? `Unsaved changes (${changed.length})` : "No unsaved changes"}
      </Typography.Text>
      <Space wrap>
        {dirty && <Button onClick={onDiscard}>Discard</Button>}
        <Button type="primary" htmlType="submit" loading={submitting} disabled={!dirty}>
          Save Changes
        </Button>
      </Space>
    </Flex>
  );

  return (
    <Form<PHPSettingsFormData>
      form={form}
      layout="vertical"
      onFinish={onSave}
      onValuesChange={() => rerender()}
    >
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

              {/* forceRender: a section's fields stay in the form while it is
                  collapsed. Without it a never-opened section's fields would
                  be missing from the save and read as cleared. */}
              <Collapse
                activeKey={openSections}
                onChange={(keys) => setOpenSections(Array.isArray(keys) ? keys : [keys])}
                items={sections.map((s) => ({
                  ...s,
                  forceRender: true,
                  extra: sectionExtra(s.key),
                }))}
              />

              {/* GH #1701: with unsaved changes, Save stays in view at the
                  bottom of a long page. Affix, not CSS sticky: the shell's
                  content column clips overflow-x, which makes it the sticky
                  element's scroll container, and it never scrolls (the window
                  does). */}
              {dirty ? <Affix offsetBottom={0}>{saveBar}</Affix> : saveBar}
            </>
          )}
      </Spin>
      {/* Portal-rendered; holds no form fields. */}
      <LogStreamModal {...logStreams.modalProps} />
    </Form>
  );
}
