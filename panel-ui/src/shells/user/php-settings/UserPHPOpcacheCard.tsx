// UserPHPOpcacheCard — GH #1332 OPcache / JIT controls. Per-version (per FPM
// pool), like the Performance card: a pool's OPcache SHM is shared by every
// domain on that PHP version. Each control is tri-state — "Pool default" leaves
// no override (server default), On/Off (or a size) writes one. Saving restarts
// the version's FPM master so the change takes effect (a graceful reload keeps
// the old SHM), so Reset lives here too.
import { Alert, Button, Card, Col, Row, Select, Space, Spin, Typography } from "antd";
import { feedback } from "../../../lib/feedback";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { apiClient } from "../../../apiClient";

type PoolRow = { php_version: string; is_default: boolean };
type Tuning = { pools: PoolRow[] };

type Opcache = {
  php_version: string;
  enable: boolean | null;
  validate_timestamps: boolean | null;
  memory_consumption_mb: number | null;
  max_accelerated_files: number | null;
  revalidate_freq: number | null;
  jit_enabled: boolean | null;
  jit_buffer_size_mb: number | null;
};

// Tri-state bool select: "Pool default" (null) / On / Off.
const boolOpts = [
  { label: "Pool default", value: "" },
  { label: "On", value: "on" },
  { label: "Off", value: "off" },
];
const toBool = (v: string): boolean | null => (v === "on" ? true : v === "off" ? false : null);
const fromBool = (b: boolean | null): string => (b === true ? "on" : b === false ? "off" : "");

const numOpts = (vals: number[], suffix = "") => [
  { label: "Pool default", value: -1 },
  ...vals.map((v) => ({ label: `${v}${suffix}`, value: v })),
];
const toNum = (v: number): number | null => (v < 0 ? null : v);
const fromNum = (n: number | null): number => (n == null ? -1 : n);

export const UserPHPOpcacheCard = () => {
  const qc = useQueryClient();
  const [version, setVersion] = useState<string>("");
  const [saving, setSaving] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [form, setForm] = useState<Opcache | null>(null);

  const { data: tuning } = useQuery<Tuning>({
    queryKey: ["me-php-pool-tuning"],
    queryFn: async () => (await apiClient.get<Tuning>("/me/php-pool-tuning")).data,
  });
  const pools = tuning?.pools ?? [];
  useEffect(() => {
    if (!version && pools.length > 0) {
      setVersion((pools.find((p) => p.is_default) ?? pools[0]).php_version);
    }
  }, [pools, version]);

  const { data, isLoading } = useQuery<Opcache>({
    queryKey: ["me-php-opcache", version],
    enabled: !!version,
    queryFn: async () =>
      (await apiClient.get<Opcache>(`/me/php-opcache?php_version=${encodeURIComponent(version)}`)).data,
  });
  useEffect(() => {
    if (data) setForm(data);
  }, [data]);

  const patch = (p: Partial<Opcache>) => setForm((f) => (f ? { ...f, ...p } : f));

  const save = async () => {
    if (!form) return;
    setSaving(true);
    try {
      await apiClient.put("/me/php-opcache", { ...form, php_version: version });
      feedback.message.success(`OPcache/JIT saved — FPM restarted for PHP ${version}`);
      qc.invalidateQueries({ queryKey: ["me-php-opcache", version] });
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to save OPcache settings");
    } finally {
      setSaving(false);
    }
  };

  const reset = async () => {
    setResetting(true);
    try {
      await apiClient.post("/me/php-opcache/reset", { php_version: version });
      feedback.message.success(`OPcache reset (FPM restarted for PHP ${version})`);
    } catch (err) {
      const e = err as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(e.response?.data?.detail ?? e.response?.data?.error ?? "Failed to reset OPcache");
    } finally {
      setResetting(false);
    }
  };

  // GH #1332 (lxsdevcode): lay the controls out in a responsive grid like the
  // Resource Limits section — 3 per row on desktop, collapsing to 2 / 1 columns
  // on narrower screens — instead of one scattered wrapping flex row.
  const Field = ({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) => (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <Typography.Text strong>{label}</Typography.Text>
      {children}
      {hint && <Typography.Text type="secondary" style={{ fontSize: 12 }}>{hint}</Typography.Text>}
    </div>
  );
  const col = { xs: 24, sm: 12, lg: 8 };
  const sel = { width: "100%" as const };

  return (
    <Card title="OPcache & JIT" size="small">
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Per PHP version — these apply to every site you run on the selected
          version (OPcache is shared per FPM pool). Saving restarts that
          version&apos;s FPM master so the change takes effect.
        </Typography.Paragraph>

        <div>
          <Typography.Text strong>PHP version: </Typography.Text>
          <Select
            style={{ minWidth: 200 }}
            value={version || undefined}
            onChange={setVersion}
            options={pools.map((p) => ({
              value: p.php_version,
              label: p.is_default ? `${p.php_version} (default)` : p.php_version,
            }))}
          />
        </div>

        {isLoading || !form ? (
          <Spin />
        ) : (
          <>
            <Row gutter={[16, 16]}>
              <Col {...col}>
                <Field label="OPcache">
                  <Select style={sel} value={fromBool(form.enable)} onChange={(v) => patch({ enable: toBool(v) })} options={boolOpts} />
                </Field>
              </Col>
              <Col {...col}>
                <Field label="JIT">
                  <Select style={sel} value={fromBool(form.jit_enabled)} onChange={(v) => patch({ jit_enabled: toBool(v) })} options={boolOpts} />
                </Field>
              </Col>
              <Col {...col}>
                <Field label="JIT buffer size">
                  <Select style={sel} value={fromNum(form.jit_buffer_size_mb)} onChange={(v) => patch({ jit_buffer_size_mb: toNum(v) })} options={numOpts([8, 16, 32, 64, 128, 256], " MB")} />
                </Field>
              </Col>
              <Col {...col}>
                <Field label="Memory">
                  <Select style={sel} value={fromNum(form.memory_consumption_mb)} onChange={(v) => patch({ memory_consumption_mb: toNum(v) })} options={numOpts([64, 128, 192, 256, 512], " MB")} />
                </Field>
              </Col>
              <Col {...col}>
                <Field label="Max accelerated files">
                  <Select style={sel} value={fromNum(form.max_accelerated_files)} onChange={(v) => patch({ max_accelerated_files: toNum(v) })} options={numOpts([10000, 20000, 50000, 100000])} />
                </Field>
              </Col>
              <Col {...col}>
                <Field label="Validate timestamps">
                  <Select style={sel} value={fromBool(form.validate_timestamps)} onChange={(v) => patch({ validate_timestamps: toBool(v) })} options={boolOpts} />
                </Field>
              </Col>
              <Col {...col}>
                <Field label="Revalidate freq" hint="How often OPcache checks files for changes (with timestamp validation on).">
                  <Select style={sel} value={fromNum(form.revalidate_freq)} onChange={(v) => patch({ revalidate_freq: toNum(v) })} options={numOpts([0, 2, 5, 30, 60], " s")} />
                </Field>
              </Col>
            </Row>

            {form.validate_timestamps === false && (
              <Alert
                type="warning"
                showIcon
                message="Timestamp validation is off"
                description="Changes to your PHP files won't take effect until OPcache is reset. Use the Reset button after each deploy."
              />
            )}

            <Space>
              <Button type="primary" loading={saving} onClick={save}>Save</Button>
              <Button loading={resetting} onClick={reset}>Reset OPcache</Button>
            </Space>
          </>
        )}
      </Space>
    </Card>
  );
};
