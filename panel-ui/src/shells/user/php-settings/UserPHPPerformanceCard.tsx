// UserPHPPerformanceCard — GH #339 phase 2, Steps 5+6. The SAFE user-facing FPM
// tuning: a "PHP Performance Mode" dropdown (L1) and, when the package allows,
// clamped advanced pm.* knobs (L2).
//
// GH #1332: each PHP version a user's domains run has its OWN pool (GH #329) with
// its own pm_* + performance_mode. The card loads GET /me/php-pool-tuning (policy
// + the caller's per-version pools) and, on the version selector, shows THAT
// version's stored values instead of static defaults — so switching version
// reflects the real per-version configuration.
import { useTranslation } from "react-i18next";
import { Alert, Button, Card, Collapse, Form, InputNumber, Select, Space, Spin, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { apiClient } from "../../../apiClient";

// GH #1332 item 5: presets carry their full pm_* values (the backend already
// returns them), so selecting a preset can populate + preview the Advanced form.
type Mode = {
  mode: string;
  label: string;
  pm_mode: string;
  pm_max_children: number;
  pm_start_servers: number;
  pm_min_spare_servers: number;
  pm_max_spare_servers: number;
  pm_max_requests: number;
};
type PoolRow = {
  php_version: string;
  pm_mode: string;
  pm_max_children: number;
  pm_start_servers: number;
  pm_min_spare_servers: number;
  pm_max_spare_servers: number;
  pm_max_requests: number;
  request_terminate_timeout_seconds: number;
  process_idle_timeout_seconds: number;
  slowlog_timeout_seconds: number;
  performance_mode: string;
  is_default: boolean;
};
type Tuning = {
  can_edit: boolean;
  advanced: boolean;
  max_children_cap: number;
  worker_mem_mb: number;
  modes: Mode[];
  pools: PoolRow[];
};

export const UserPHPPerformanceCard = () => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [version, setVersion] = useState<string>("");
  const [mode, setMode] = useState<string>("balanced");
  const [saving, setSaving] = useState(false);
  const [advOpen, setAdvOpen] = useState<string[]>([]); // Advanced collapse (item 5)
  const [advForm] = Form.useForm();

  const { data, isLoading } = useQuery<Tuning>({
    queryKey: ["me-php-pool-tuning"],
    queryFn: async () => (await apiClient.get<Tuning>("/me/php-pool-tuning")).data,
  });

  const pools = data?.pools ?? [];
  const selectedPool =
    pools.find((p) => p.php_version === version) ??
    pools.find((p) => p.is_default) ??
    pools[0];

  // Once data arrives (or after a refetch that created a new version pool),
  // default the selector to the user's default pool version.
  useEffect(() => {
    if (!version && pools.length > 0) {
      setVersion((pools.find((p) => p.is_default) ?? pools[0]).php_version);
    }
  }, [pools, version]);

  // Load the selected version pool's own values into the mode + Advanced form,
  // so switching version shows THAT version's stored tuning (GH #1332).
  useEffect(() => {
    if (!selectedPool) return;
    setMode(selectedPool.performance_mode || "balanced");
    advForm.setFieldsValue({
      pm_mode: selectedPool.pm_mode,
      pm_max_children: selectedPool.pm_max_children,
      pm_start_servers: selectedPool.pm_start_servers,
      pm_min_spare_servers: selectedPool.pm_min_spare_servers,
      pm_max_spare_servers: selectedPool.pm_max_spare_servers,
      pm_max_requests: selectedPool.pm_max_requests,
      slowlog_timeout_seconds: selectedPool.slowlog_timeout_seconds,
    });
    // selectedPool identity changes with version/data; fields depend only on it.
  }, [selectedPool, advForm]);

  if (isLoading) return <Spin />;
  if (!data || !data.can_edit) {
    return (
      <Alert
        type="info"
        showIcon
        message={t("userphpperformancecard.php_performance_tuning_isn_t_enabled_on_your")}
        description={t("userphpperformancecard.ask_your_provider_to_enable_it_on_your_hosti")}
      />
    );
  }

  const refresh = () => qc.invalidateQueries({ queryKey: ["me-php-pool-tuning"] });

  const applyMode = async () => {
    setSaving(true);
    try {
      await apiClient.put("/me/php-performance-mode", { php_version: version, mode });
      feedback.message.success(`Performance mode set to ${mode}`);
      refresh();
    } catch (e) {
      const err = e as { response?: { data?: { error?: string; detail?: string } } };
      feedback.message.error(err.response?.data?.detail ?? err.response?.data?.error ?? "Failed");
    } finally {
      setSaving(false);
    }
  };

  const applyAdvanced = async () => {
    const vals = await advForm.validateFields();
    setSaving(true);
    try {
      await apiClient.put("/me/php-pool-tuning", { php_version: version, ...vals });
      feedback.message.success("Advanced FPM settings applied (clamped to your plan)");
      refresh();
    } catch (e) {
      const err = e as { response?: { data?: { error?: string; detail?: string } } };
      feedback.message.error(err.response?.data?.detail ?? err.response?.data?.error ?? "Failed");
    } finally {
      setSaving(false);
    }
  };

  const cap = data.max_children_cap;
  const selectedPreset = data.modes.find((m) => m.mode === mode);

  // One-line human summary of what a preset actually sets (item 5). Built as a
  // single string so it reads naturally and is one text node.
  const presetSummary = (p: Mode): string => {
    const parts = [p.pm_mode, `${Math.min(cap, p.pm_max_children)} max workers`];
    if (p.pm_mode === "dynamic") {
      parts.push(
        `${p.pm_start_servers} start`,
        `${p.pm_min_spare_servers}–${p.pm_max_spare_servers} spare`,
      );
    }
    if (p.pm_max_requests > 0) parts.push(`recycle every ${p.pm_max_requests} requests`);
    return parts.join(", ");
  };

  // GH #1332 item 5 (Option B): picking a preset previews its actual pm_* values
  // in the Advanced form (max children shown clamped to the plan cap, as it would
  // apply) and opens the Advanced panel — so the user sees what the preset does
  // and can still fine-tune before applying.
  const onSelectMode = (m: string) => {
    setMode(m);
    const preset = data.modes.find((p) => p.mode === m);
    if (preset && data.advanced) {
      advForm.setFieldsValue({
        pm_mode: preset.pm_mode,
        pm_max_children: Math.min(cap, preset.pm_max_children),
        pm_start_servers: preset.pm_start_servers,
        pm_min_spare_servers: preset.pm_min_spare_servers,
        pm_max_spare_servers: preset.pm_max_spare_servers,
        pm_max_requests: preset.pm_max_requests,
      });
      setAdvOpen(["adv"]);
    }
  };

  return (
    <Card title={t("userphpperformancecard.php_performance")}>
      <Space direction="vertical" size="large" style={{ width: "100%", maxWidth: 560 }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Pick how your PHP workers scale. Values are capped for your plan (max{" "}
          {cap} workers, ~{data.worker_mem_mb} MB each).
        </Typography.Paragraph>

        {pools.length > 1 && (
          <div>
            <Typography.Text>PHP version</Typography.Text>
            <Select
              style={{ width: 200, display: "block", marginTop: 4 }}
              value={version}
              onChange={setVersion}
              options={pools.map((p) => ({
                value: p.php_version,
                label: `PHP ${p.php_version}${p.is_default ? " (default)" : ""}`,
              }))}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Each PHP version keeps its own tuning.
            </Typography.Text>
          </div>
        )}

        <div>
          <Typography.Text strong>Performance Mode</Typography.Text>
          <Select
            style={{ width: "100%", marginTop: 4 }}
            value={mode}
            onChange={onSelectMode}
            options={data.modes.map((m) => ({ value: m.mode, label: m.label || m.mode }))}
          />
          {selectedPreset && (
            <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: "6px 0 0" }}>
              Sets {presetSummary(selectedPreset)}
              {data.advanced ? " — shown below, tweak before applying." : "."}
            </Typography.Paragraph>
          )}
          <Button type="primary" loading={saving} onClick={applyMode} style={{ marginTop: 12 }}>
            Apply mode
          </Button>
        </div>

        {data.advanced && (
          <Collapse
            activeKey={advOpen}
            onChange={(k) => setAdvOpen(Array.isArray(k) ? k : [k])}
            items={[
              {
                key: "adv",
                label: "Advanced (raw pm.* — clamped to your plan)",
                children: (
                  <Form form={advForm} layout="vertical">
                    <Form.Item name="pm_mode" label={t("userphpperformancecard.process_manager")}>
                      <Select
                        options={[
                          { value: "dynamic", label: "dynamic" },
                          { value: "ondemand", label: "ondemand" },
                          { value: "static", label: "static" },
                        ]}
                      />
                    </Form.Item>
                    <Form.Item name="pm_max_children" label={`Max children (≤ ${cap})`}>
                      <InputNumber min={1} max={cap} style={{ width: 160 }} />
                    </Form.Item>
                    <Form.Item name="pm_start_servers" label={t("userphpperformancecard.start_servers_dynamic")}>
                      <InputNumber min={1} max={cap} style={{ width: 160 }} />
                    </Form.Item>
                    <Form.Item name="pm_min_spare_servers" label={t("userphpperformancecard.min_spare_dynamic")}>
                      <InputNumber min={1} max={cap} style={{ width: 160 }} />
                    </Form.Item>
                    <Form.Item name="pm_max_spare_servers" label={t("userphpperformancecard.max_spare_dynamic")}>
                      <InputNumber min={1} max={cap} style={{ width: 160 }} />
                    </Form.Item>
                    <Form.Item name="pm_max_requests" label={t("userphpperformancecard.max_requests_per_worker_0_never")}>
                      <InputNumber min={0} max={100000} style={{ width: 160 }} />
                    </Form.Item>
                    <Form.Item
                      name="slowlog_timeout_seconds"
                      label="Slow-log threshold (seconds, 0 = off)"
                      extra="Logs a PHP backtrace of any request slower than this to ~/logs/php-slow*.log. Applies to every site on this PHP version."
                    >
                      <InputNumber min={0} max={600} style={{ width: 160 }} />
                    </Form.Item>
                    <Button loading={saving} onClick={applyAdvanced}>
                      Apply advanced
                    </Button>
                  </Form>
                ),
              },
            ]}
          />
        )}
      </Space>
    </Card>
  );
};
