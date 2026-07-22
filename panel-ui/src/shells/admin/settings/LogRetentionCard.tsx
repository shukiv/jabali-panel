// LogRetentionCard — Server Settings → Logs. Operator-configurable retention
// (in days) for each log/report table the retention sweep prunes. 90d baseline,
// 365d for forensic security tables, 0 = keep forever. Saving PATCHes
// /admin/settings with a { category: days } log_retention map; the daily
// jabali-retention-sweep reads it (an absent category uses the code default).
import { useTranslation } from "react-i18next";
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  InputNumber,
  Space,
  Spin,
  Tooltip,
  Typography,
} from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";

type RetentionMap = Record<string, number>;

interface SettingsResponse {
  log_retention?: RetentionMap | null;
}

// Mirrors models.DefaultLogRetentionDays. An unset category shows its default.
const CATEGORIES: {
  key: string;
  label: string;
  def: number;
  help: string;
  warn?: boolean;
}[] = [
  { key: "notifications", label: "Notifications", def: 90, help: "Delivered-notification log (bell / inbox rows)." },
  { key: "updates", label: "Update history", def: 90, help: "Update-center run history + stored log excerpts." },
  { key: "mail_reports", label: "Mail reports", def: 90, help: "DMARC, TLS-RPT, and ARF aggregate/failure reports." },
  { key: "security_events", label: "Security events", def: 365, help: "Malware events + Snuffleupagus WAF incidents. Forensic — longer default." },
  { key: "db_admin_audit", label: "DB-admin audit", def: 365, help: "Legacy database-admin audit log. Forensic — longer default." },
  { key: "sessions", label: "Sessions & log streams", def: 90, help: "Root-terminal session rows + expired log-view stream keys." },
  { key: "migrations", label: "Migration jobs", def: 90, help: "Terminal migration job + stage rows (secrets/staging are reaped separately)." },
  { key: "bandwidth", label: "Bandwidth ledger", def: 90, help: "Per-domain daily bandwidth/request totals (bw_daily)." },
  {
    key: "audit",
    label: "Audit log (tamper-evident)",
    def: 0,
    warn: true,
    help: "Hash-chained security audit. Default: keep forever. Setting a window prunes with a chain re-anchor — shorter forensic history.",
  },
];

export function LogRetentionCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [form] = Form.useForm<RetentionMap>();
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    void (async () => {
      try {
        const r = await apiClient.get<SettingsResponse>("/admin/settings");
        const saved = r.data.log_retention ?? {};
        const values: RetentionMap = {};
        for (const c of CATEGORIES) {
          values[c.key] = saved[c.key] ?? c.def;
        }
        form.setFieldsValue(values);
        setLoaded(true);
      } catch {
        message.error("Failed to load log-retention settings");
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const save = async () => {
    const vals = await form.validateFields();
    const payload: RetentionMap = {};
    for (const c of CATEGORIES) {
      payload[c.key] = typeof vals[c.key] === "number" ? vals[c.key] : c.def;
    }
    setSaving(true);
    try {
      await apiClient.patch("/admin/settings", { log_retention: payload });
      message.success("Log-retention settings saved. Applied on the next daily sweep.");
    } catch {
      message.error("Failed to save log-retention settings");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("logretentioncard.logs_retention")} style={{ marginBottom: 16 }}>
      <Typography.Paragraph type="secondary">
        How long each log/report table is kept before the daily retention sweep
        prunes it. <b>0 = keep forever.</b> Changes take effect on the next sweep.
      </Typography.Paragraph>
      {!loaded ? (
        <Spin />
      ) : (
        <Form form={form} layout="vertical">
          <Space direction="vertical" size={0} style={{ width: "100%" }}>
            {CATEGORIES.map((c) => (
              <Form.Item
                key={c.key}
                name={c.key}
                label={
                  <Tooltip title={c.help}>
                    <span>
                      {c.label}
                      {c.warn ? " ⚠" : ""}
                    </span>
                  </Tooltip>
                }
                style={{ marginBottom: 12, maxWidth: 420 }}
              >
                <InputNumber
                  min={0}
                  max={3650}
                  step={1}
                  style={{ width: 180 }}
                  addonAfter="days (0 = forever)"
                />
              </Form.Item>
            ))}
          </Space>
          <Alert
            type="info"
            showIcon
            style={{ marginTop: 8, marginBottom: 16, maxWidth: 640 }}
            message={t("logretentioncard.the_audit_log_defaults_to_keep_forever")}
            description={t("logretentioncard.it_s_hash_chained_and_tamper_evident_set_a_w")}
          />
          <Button type="primary" loading={saving} onClick={() => void save()}>
            Save retention
          </Button>
        </Form>
      )}
    </Card>
  );
}
