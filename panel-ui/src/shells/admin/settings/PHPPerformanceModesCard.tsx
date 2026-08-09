// PHPPerformanceModesCard — GH #339 phase 2, Step 2 UI. Admin editor for the 4
// PHP Performance Mode presets that L1 users select from. Edits are the ceiling
// templates; per-package clamping happens at apply.
import { useTranslation } from "react-i18next";
import { Button, Card, Collapse, Form, InputNumber, Select, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiClient } from "../../../apiClient";

type Mode = {
  mode: string;
  label: string;
  pm_mode: string;
  pm_max_children: number;
  pm_start_servers: number;
  pm_min_spare_servers: number;
  pm_max_spare_servers: number;
  pm_max_requests: number;
  request_terminate_timeout_seconds: number;
  process_idle_timeout_seconds: number;
};

const ModeForm = ({ mode }: { mode: Mode }) => {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form] = Form.useForm();
  const [saving, setSaving] = useState(false);
  const save = async () => {
    const vals = await form.validateFields();
    setSaving(true);
    try {
      await apiClient.put(`/php-performance-modes/${mode.mode}`, vals);
      feedback.message.success(`${mode.mode} preset saved`);
      qc.invalidateQueries({ queryKey: ["php-performance-modes"] });
    } catch (e) {
      const err = e as { response?: { data?: { detail?: string; error?: string } } };
      feedback.message.error(err.response?.data?.detail ?? err.response?.data?.error ?? "Failed");
    } finally {
      setSaving(false);
    }
  };
  return (
    <Form form={form} layout="vertical" initialValues={mode} style={{ maxWidth: 420 }}>
      <Form.Item name="pm_mode" label={t("phpperformancemodescard.process_manager")}>
        <Select options={["dynamic", "ondemand", "static"].map((v) => ({ value: v, label: v }))} />
      </Form.Item>
      <Form.Item name="pm_max_children" label={t("phpperformancemodescard.max_children")}><InputNumber min={1} max={2000} /></Form.Item>
      <Form.Item name="pm_start_servers" label={t("phpperformancemodescard.start_servers")}><InputNumber min={1} max={2000} /></Form.Item>
      <Form.Item name="pm_min_spare_servers" label={t("phpperformancemodescard.min_spare")}><InputNumber min={1} max={2000} /></Form.Item>
      <Form.Item name="pm_max_spare_servers" label={t("phpperformancemodescard.max_spare")}><InputNumber min={1} max={2000} /></Form.Item>
      <Form.Item name="pm_max_requests" label={t("phpperformancemodescard.max_requests_0_never")}><InputNumber min={0} max={100000} /></Form.Item>
      <Form.Item name="process_idle_timeout_seconds" label={t("phpperformancemodescard.idle_timeout_s")}><InputNumber min={1} max={86400} /></Form.Item>
      <Button type="primary" loading={saving} onClick={save}>Save {mode.mode}</Button>
    </Form>
  );
};

export const PHPPerformanceModesCard = () => {
  const { t } = useTranslation();
  const { data } = useQuery<Mode[]>({
    queryKey: ["php-performance-modes"],
    queryFn: async () => (await apiClient.get<{ data: Mode[] }>("/php-performance-modes")).data.data ?? [],
  });
  return (
    <Card title={t("phpperformancemodescard.php_performance_modes")} style={{ marginBottom: 16 }}>
      <Typography.Paragraph type="secondary">
        The presets users pick from (when enabled per hosting package). Values are
        re-clamped to each package&apos;s cap at apply time.
      </Typography.Paragraph>
      <Collapse
        items={(data ?? []).map((m) => ({
          key: m.mode,
          label: m.label || m.mode,
          children: <ModeForm mode={m} />,
        }))}
      />
    </Card>
  );
};
