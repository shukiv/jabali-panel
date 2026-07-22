// UserPHPPerformanceCard — GH #339 phase 2, Steps 5+6. The SAFE user-facing FPM
// tuning: a "PHP Performance Mode" dropdown (L1) and, when the package allows,
// clamped advanced pm.* knobs (L2). Driven entirely by GET /me/php-fpm-policy —
// if the package doesn't allow editing, the whole card renders a hint instead.
// This replaces the old raw UserPHPPoolCard (removed in PR #34) with the
// package-gated, preset-based model.
import { useTranslation } from "react-i18next";
import {
  Alert,
  Button,
  Card,
  Collapse,
  Form,
  InputNumber,
  Select,
  Space,
  Spin,
  Typography,
  message,
} from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiClient } from "../../../apiClient";

type Mode = { mode: string; label: string };
type Policy = {
  can_edit: boolean;
  advanced: boolean;
  max_children_cap: number;
  worker_mem_mb: number;
  modes: Mode[];
};

export const UserPHPPerformanceCard = ({
  versions,
}: {
  versions: string[];
}) => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [version, setVersion] = useState<string>(versions[0] ?? "");
  const [mode, setMode] = useState<string>("balanced");
  const [saving, setSaving] = useState(false);
  const [advForm] = Form.useForm();

  const { data: policy, isLoading } = useQuery<Policy>({
    queryKey: ["me-fpm-policy"],
    queryFn: async () => (await apiClient.get<Policy>("/me/php-fpm-policy")).data,
  });

  if (isLoading) return <Spin />;
  if (!policy || !policy.can_edit) {
    return (
      <Alert
        type="info"
        showIcon
        message={t("userphpperformancecard.php_performance_tuning_isn_t_enabled_on_your")}
        description={t("userphpperformancecard.ask_your_provider_to_enable_it_on_your_hosti")}
      />
    );
  }

  const applyMode = async () => {
    setSaving(true);
    try {
      await apiClient.put("/me/php-performance-mode", { php_version: version, mode });
      message.success(`Performance mode set to ${mode}`);
      qc.invalidateQueries({ queryKey: ["me-fpm-policy"] });
    } catch (e) {
      const err = e as { response?: { data?: { error?: string; detail?: string } } };
      message.error(err.response?.data?.detail ?? err.response?.data?.error ?? "Failed");
    } finally {
      setSaving(false);
    }
  };

  const applyAdvanced = async () => {
    const vals = await advForm.validateFields();
    setSaving(true);
    try {
      await apiClient.put("/me/php-pool-tuning", { php_version: version, ...vals });
      message.success("Advanced FPM settings applied (clamped to your plan)");
      qc.invalidateQueries({ queryKey: ["me-fpm-policy"] });
    } catch (e) {
      const err = e as { response?: { data?: { error?: string; detail?: string } } };
      message.error(err.response?.data?.detail ?? err.response?.data?.error ?? "Failed");
    } finally {
      setSaving(false);
    }
  };

  const cap = policy.max_children_cap;

  return (
    <Card title={t("userphpperformancecard.php_performance")}>
      <Space direction="vertical" size="large" style={{ width: "100%", maxWidth: 560 }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          Pick how your PHP workers scale. Values are capped for your plan (max{" "}
          {cap} workers, ~{policy.worker_mem_mb} MB each).
        </Typography.Paragraph>

        {versions.length > 1 && (
          <div>
            <Typography.Text>PHP version</Typography.Text>
            <Select
              style={{ width: 200, display: "block", marginTop: 4 }}
              value={version}
              onChange={setVersion}
              options={versions.map((v) => ({ value: v, label: `PHP ${v}` }))}
            />
          </div>
        )}

        <div>
          <Typography.Text strong>Performance Mode</Typography.Text>
          <Select
            style={{ width: "100%", marginTop: 4 }}
            value={mode}
            onChange={setMode}
            options={policy.modes.map((m) => ({ value: m.mode, label: m.label || m.mode }))}
          />
          <Button type="primary" loading={saving} onClick={applyMode} style={{ marginTop: 12 }}>
            Apply mode
          </Button>
        </div>

        {policy.advanced && (
          <Collapse
            items={[
              {
                key: "adv",
                label: "Advanced (raw pm.* — clamped to your plan)",
                children: (
                  <Form form={advForm} layout="vertical" initialValues={{ pm_mode: "dynamic", pm_max_children: Math.min(10, cap) }}>
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
